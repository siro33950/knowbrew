package query

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gofrs/flock"
	"github.com/siro33950/knowbrew/internal/diagnostic"
	"github.com/siro33950/knowbrew/internal/domain"
	"github.com/siro33950/knowbrew/internal/store"
	_ "modernc.org/sqlite"
)

const indexSchemaVersion = 3

type Target string

const (
	TargetKnowledge Target = "knowledge"
	TargetFeedstock Target = "feedstock"
)

type SearchOptions struct {
	Target         Target
	Keywords       []string
	Subject        string
	Topic          string
	Since          *time.Time
	Until          *time.Time
	IncludePending bool
	Trigger        string
	Session        string
	Agent          string
	Last           int
	Limit          int
	MaxTokens      int
	Reindex        bool
}

type SearchResult struct {
	ID          string   `json:"id,omitempty"`
	Slug        string   `json:"slug,omitempty"`
	Timestamp   string   `json:"timestamp,omitempty"`
	Summary     string   `json:"summary,omitempty"`
	Subjects    []string `json:"subjects,omitempty"`
	Topics      []string `json:"topics,omitempty"`
	Score       *float64 `json:"score,omitempty"`
	Claim       string   `json:"claim,omitempty"`
	AppliesWhen string   `json:"applies_when,omitempty"`
	Path        string   `json:"path,omitempty"`
}

type SearchResponse struct {
	Results   []SearchResult       `json:"results"`
	Total     int                  `json:"total"`
	Returned  int                  `json:"returned"`
	Truncated bool                 `json:"truncated"`
	Warnings  []diagnostic.Warning `json:"warnings,omitempty"`
}

type ShowResult struct {
	ID        string            `json:"id"`
	Timestamp time.Time         `json:"timestamp"`
	Agent     string            `json:"agent"`
	Session   domain.SessionRef `json:"session"`
	Summary   string            `json:"summary"`
	Subjects  []string          `json:"subjects"`
	Topics    []string          `json:"topics"`
	UserQuote string            `json:"user_quote"`
}

type ShowResponse struct {
	Feedstocks []ShowResult `json:"feedstocks"`
}

func Search(ctx context.Context, dataStore *store.Store, options SearchOptions) (SearchResponse, error) {
	if err := validateOptions(&options); err != nil {
		return SearchResponse{}, err
	}
	if err := dataStore.EnsureLayout(); err != nil {
		return SearchResponse{}, err
	}
	if err := ctx.Err(); err != nil {
		return SearchResponse{}, err
	}

	indexPath := filepath.Join(dataStore.Root, ".state", "index.sqlite")
	indexLock := flock.New(filepath.Join(dataStore.Root, ".state", "index.lock"))
	locked, err := indexLock.TryLock()
	if err != nil {
		return SearchResponse{}, fmt.Errorf("acquire search index lock: %w", err)
	}
	if !locked {
		return SearchResponse{}, errors.New("another knowbrew search process is updating the index")
	}
	defer indexLock.Unlock()

	response, err := searchAttempt(ctx, dataStore, indexPath, options)
	if err == nil {
		return response, nil
	}
	if !isIndexCorruption(err) {
		return SearchResponse{}, err
	}
	if removeErr := removeIndexFiles(indexPath); removeErr != nil {
		return SearchResponse{}, fmt.Errorf("replace corrupt search index: %w", removeErr)
	}
	options.Reindex = true
	response, rebuildErr := searchAttempt(ctx, dataStore, indexPath, options)
	if rebuildErr != nil {
		return SearchResponse{}, fmt.Errorf("rebuild corrupt search index: %w", rebuildErr)
	}
	return response, nil
}

func validateOptions(options *SearchOptions) error {
	if options.Target != TargetKnowledge && options.Target != TargetFeedstock {
		return errors.New("search target must be knowledge or feedstock")
	}
	if options.Limit <= 0 {
		options.Limit = 20
	}
	if options.MaxTokens <= 0 {
		options.MaxTokens = 2000
	}
	if options.Trigger != "" {
		if options.Target != TargetKnowledge {
			return errors.New("--trigger is only valid for knowledge")
		}
		if options.Trigger != "always" {
			return errors.New("--trigger must be always")
		}
		if options.IncludePending {
			return errors.New("--trigger and --include-pending cannot be used together")
		}
	}
	if options.Target == TargetKnowledge {
		if options.Session != "" || options.Agent != "" || options.Last != 0 {
			return errors.New("--session, --agent, and --last are only valid for feedstock")
		}
		return nil
	}
	if options.IncludePending {
		return errors.New("--include-pending is only valid for knowledge")
	}
	if options.Last < 0 {
		return errors.New("--last must be greater than zero")
	}
	if options.Last > 0 && len(keywordTerms(options.Keywords)) > 0 {
		return errors.New("--last cannot be used with keywords")
	}
	return nil
}

func searchAttempt(
	ctx context.Context,
	dataStore *store.Store,
	indexPath string,
	options SearchOptions,
) (SearchResponse, error) {
	database, err := openIndex(ctx, indexPath)
	if err != nil {
		return SearchResponse{}, err
	}
	defer database.Close()

	warnings, err := synchronize(ctx, database, dataStore, options.Reindex)
	if err != nil {
		return SearchResponse{}, err
	}
	response, err := queryIndex(ctx, database, options)
	response.Warnings = warnings
	return response, err
}

func openIndex(ctx context.Context, path string) (*sql.DB, error) {
	database, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	closeOnError := func(err error) (*sql.DB, error) {
		_ = database.Close()
		return nil, err
	}
	if err := database.PingContext(ctx); err != nil {
		return closeOnError(err)
	}
	for _, statement := range []string{
		`PRAGMA journal_mode=WAL`,
		`PRAGMA busy_timeout=5000`,
	} {
		if _, err := database.ExecContext(ctx, statement); err != nil {
			return closeOnError(err)
		}
	}
	return database, nil
}

func synchronize(
	ctx context.Context,
	database *sql.DB,
	dataStore *store.Store,
	reindex bool,
) ([]diagnostic.Warning, error) {
	var version int
	if err := database.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&version); err != nil {
		return nil, err
	}
	if reindex || version != indexSchemaVersion {
		return fullRebuild(ctx, database, dataStore)
	}
	if err := ensureSchema(ctx, database); err != nil {
		return nil, err
	}
	return incrementalSync(ctx, database, dataStore)
}

func ensureSchema(ctx context.Context, database *sql.DB) error {
	for _, statement := range schemaStatements() {
		if _, err := database.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("initialize search index: %w", err)
		}
	}
	return nil
}

func schemaStatements() []string {
	return []string{
		`CREATE TABLE IF NOT EXISTS documents (
			record_key TEXT PRIMARY KEY,
			id TEXT NOT NULL,
			kind TEXT NOT NULL CHECK(kind IN ('feedstock','knowledge')),
			session TEXT NOT NULL,
			agent TEXT NOT NULL,
			timestamp TEXT NOT NULL,
			timestamp_ns INTEGER NOT NULL,
			summary TEXT NOT NULL,
			subjects TEXT NOT NULL,
			topics TEXT NOT NULL,
			claim TEXT NOT NULL,
			applies_when TEXT NOT NULL,
			path TEXT NOT NULL,
			trigger TEXT NOT NULL,
			status TEXT NOT NULL,
			searchable TEXT NOT NULL,
			source_mtime_ns INTEGER NOT NULL,
			source_size INTEGER NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS documents_kind_id ON documents(kind, id)`,
		`CREATE INDEX IF NOT EXISTS documents_kind_path ON documents(kind, path)`,
		`CREATE VIRTUAL TABLE IF NOT EXISTS documents_fts USING fts5(
			summary,
			searchable,
			applies_when,
			content='documents',
			content_rowid='rowid',
			tokenize='trigram'
		)`,
		`CREATE TRIGGER IF NOT EXISTS documents_fts_after_insert
		AFTER INSERT ON documents BEGIN
			INSERT INTO documents_fts(rowid,summary,searchable,applies_when)
			VALUES(new.rowid,new.summary,new.searchable,new.applies_when);
		END`,
		`CREATE TRIGGER IF NOT EXISTS documents_fts_after_delete
		AFTER DELETE ON documents BEGIN
			INSERT INTO documents_fts(documents_fts,rowid,summary,searchable,applies_when)
			VALUES('delete',old.rowid,old.summary,old.searchable,old.applies_when);
		END`,
		`CREATE TRIGGER IF NOT EXISTS documents_fts_after_update
		AFTER UPDATE ON documents BEGIN
			INSERT INTO documents_fts(documents_fts,rowid,summary,searchable,applies_when)
			VALUES('delete',old.rowid,old.summary,old.searchable,old.applies_when);
			INSERT INTO documents_fts(rowid,summary,searchable,applies_when)
			VALUES(new.rowid,new.summary,new.searchable,new.applies_when);
		END`,
	}
}

func fullRebuild(
	ctx context.Context,
	database *sql.DB,
	dataStore *store.Store,
) ([]diagnostic.Warning, error) {
	for _, statement := range []string{
		`PRAGMA user_version=0`,
		`DROP TRIGGER IF EXISTS documents_fts_after_insert`,
		`DROP TRIGGER IF EXISTS documents_fts_after_delete`,
		`DROP TRIGGER IF EXISTS documents_fts_after_update`,
		`DROP TABLE IF EXISTS documents_fts`,
		`DROP TABLE IF EXISTS documents`,
	} {
		if _, err := database.ExecContext(ctx, statement); err != nil {
			return nil, fmt.Errorf("reset search index: %w", err)
		}
	}
	if err := ensureSchema(ctx, database); err != nil {
		return nil, err
	}
	warnings, err := incrementalSync(ctx, database, dataStore)
	if err != nil {
		return warnings, err
	}
	if _, err := database.ExecContext(ctx, fmt.Sprintf(`PRAGMA user_version=%d`, indexSchemaVersion)); err != nil {
		return warnings, err
	}
	return warnings, nil
}

func incrementalSync(
	ctx context.Context,
	database *sql.DB,
	dataStore *store.Store,
) ([]diagnostic.Warning, error) {
	transaction, err := database.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer transaction.Rollback()

	feedstockWarnings, err := syncFeedstocks(ctx, transaction, dataStore)
	if err != nil {
		return feedstockWarnings, err
	}
	knowledgeWarnings, err := syncKnowledge(ctx, transaction, dataStore)
	warnings := append(feedstockWarnings, knowledgeWarnings...)
	if err != nil {
		return warnings, err
	}
	if err := transaction.Commit(); err != nil {
		return warnings, err
	}
	return warnings, nil
}

type fileReference struct {
	ID      string
	Path    string
	ModTime int64
	Size    int64
}

func syncFeedstocks(
	ctx context.Context,
	transaction *sql.Tx,
	dataStore *store.Store,
) ([]diagnostic.Warning, error) {
	indexed := map[string]struct{}{}
	rows, err := transaction.QueryContext(
		ctx,
		`SELECT id FROM documents WHERE kind=?`,
		string(TargetFeedstock),
	)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		indexed[id] = struct{}{}
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}

	files, walkWarnings, err := enumerateMarkdown(filepath.Join(dataStore.Root, "feedstocks"))
	if err != nil {
		return walkWarnings, err
	}
	warnings := append([]diagnostic.Warning(nil), walkWarnings...)
	for _, file := range files {
		if _, exists := indexed[file.ID]; exists {
			continue
		}
		feedstock, err := dataStore.ReadFeedstock(file.Path)
		if err != nil {
			warnings = append(warnings, diagnostic.FromError(file.Path, err))
			continue
		}
		if feedstock.ID != file.ID {
			warnings = append(warnings, diagnostic.FromError(
				file.Path,
				fmt.Errorf("feedstock ID %q does not match filename %q", feedstock.ID, file.ID),
			))
			continue
		}
		subjects, _ := json.Marshal(feedstock.Subjects)
		topics, _ := json.Marshal(feedstock.Topics)
		if err := upsertDocument(ctx, transaction, document{
			ID: feedstock.ID, Kind: TargetFeedstock, Session: feedstock.Session.ID, Agent: feedstock.Agent,
			Timestamp: feedstock.Timestamp.Format(time.RFC3339Nano), TimestampNS: feedstock.Timestamp.UnixNano(),
			Summary: feedstock.Summary, Subjects: string(subjects), Topics: string(topics),
			Searchable: strings.Join([]string{
				feedstock.Summary,
				feedstock.UserQuote,
				strings.Join(feedstock.Topics, " "),
				strings.Join(feedstock.Subjects, " "),
			}, "\n"),
			Path: file.Path, Status: string(domain.StatusActive),
			SourceMtimeNS: file.ModTime, SourceSize: file.Size,
		}); err != nil {
			return warnings, err
		}
	}
	return warnings, nil
}

type indexedKnowledge struct {
	RecordKey string
	ModTime   int64
	Size      int64
}

func syncKnowledge(
	ctx context.Context,
	transaction *sql.Tx,
	dataStore *store.Store,
) ([]diagnostic.Warning, error) {
	indexed := map[string]indexedKnowledge{}
	rows, err := transaction.QueryContext(
		ctx,
		`SELECT record_key,path,source_mtime_ns,source_size FROM documents WHERE kind=?`,
		string(TargetKnowledge),
	)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var path string
		var value indexedKnowledge
		if err := rows.Scan(&value.RecordKey, &path, &value.ModTime, &value.Size); err != nil {
			rows.Close()
			return nil, err
		}
		indexed[path] = value
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}

	files, walkWarnings, err := enumerateMarkdown(filepath.Join(dataStore.Root, "knowledge"))
	if err != nil {
		return walkWarnings, err
	}
	warnings := append([]diagnostic.Warning(nil), walkWarnings...)
	current := make(map[string]struct{}, len(files))
	for _, file := range files {
		current[file.Path] = struct{}{}
		previous, exists := indexed[file.Path]
		if exists && previous.ModTime == file.ModTime && previous.Size == file.Size {
			continue
		}
		if err := domain.ValidateSlug(file.ID); err != nil {
			warnings = append(warnings, diagnostic.FromError(file.Path, err))
			if exists {
				if err := deleteDocument(ctx, transaction, previous.RecordKey); err != nil {
					return warnings, err
				}
			}
			continue
		}
		knowledge, body, err := dataStore.ReadKnowledge(file.Path)
		if err != nil {
			warnings = append(warnings, diagnostic.FromError(file.Path, err))
			if exists {
				if err := deleteDocument(ctx, transaction, previous.RecordKey); err != nil {
					return warnings, err
				}
			}
			continue
		}
		topics, _ := json.Marshal(knowledge.Topics)
		subjects, _ := json.Marshal(domain.UniqueSorted([]string{knowledge.Project}))
		claim := firstClaim(body, file.ID)
		if err := upsertDocument(ctx, transaction, document{
			ID: file.ID, Kind: TargetKnowledge,
			Timestamp: knowledge.Updated.Format(time.RFC3339Nano), TimestampNS: knowledge.Updated.UnixNano(),
			Subjects: string(subjects), Topics: string(topics), Claim: claim,
			AppliesWhen: knowledge.AppliesWhen, Path: file.Path, Trigger: knowledge.Trigger,
			Status: string(knowledge.Status),
			Searchable: strings.Join([]string{
				claim,
				knowledge.AppliesWhen,
				body,
				strings.Join(knowledge.Topics, " "),
				knowledge.Project,
			}, "\n"),
			SourceMtimeNS: file.ModTime, SourceSize: file.Size,
		}); err != nil {
			return warnings, err
		}
	}
	for path, value := range indexed {
		if _, exists := current[path]; exists {
			continue
		}
		if err := deleteDocument(ctx, transaction, value.RecordKey); err != nil {
			return warnings, err
		}
	}
	return warnings, nil
}

func enumerateMarkdown(base string) ([]fileReference, []diagnostic.Warning, error) {
	var files []fileReference
	var warnings []diagnostic.Warning
	err := filepath.WalkDir(base, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			if errors.Is(walkErr, os.ErrNotExist) {
				return nil
			}
			warnings = append(warnings, diagnostic.FromError(path, walkErr))
			return nil
		}
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".md" {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			warnings = append(warnings, diagnostic.FromError(path, err))
			return nil
		}
		files = append(files, fileReference{
			ID:      strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name())),
			Path:    path,
			ModTime: info.ModTime().UnixNano(),
			Size:    info.Size(),
		})
		return nil
	})
	slices.SortFunc(files, func(left, right fileReference) int {
		return strings.Compare(left.Path, right.Path)
	})
	return files, warnings, err
}

type document struct {
	ID, Session, Agent, Timestamp, Summary, Subjects, Topics string
	Claim, AppliesWhen, Path, Trigger, Status, Searchable    string
	Kind                                                     Target
	TimestampNS, SourceMtimeNS, SourceSize                   int64
}

func (value document) recordKey() string {
	return fmt.Sprintf("%s:%s", value.Kind, value.ID)
}

func upsertDocument(ctx context.Context, transaction *sql.Tx, value document) error {
	key := value.recordKey()
	_, err := transaction.ExecContext(ctx, `INSERT INTO documents (
		record_key,id,kind,session,agent,timestamp,timestamp_ns,summary,subjects,topics,
		claim,applies_when,path,trigger,status,searchable,source_mtime_ns,source_size
	) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
	ON CONFLICT(record_key) DO UPDATE SET
		id=excluded.id,
		kind=excluded.kind,
		session=excluded.session,
		agent=excluded.agent,
		timestamp=excluded.timestamp,
		timestamp_ns=excluded.timestamp_ns,
		summary=excluded.summary,
		subjects=excluded.subjects,
		topics=excluded.topics,
		claim=excluded.claim,
		applies_when=excluded.applies_when,
		path=excluded.path,
		trigger=excluded.trigger,
		status=excluded.status,
		searchable=excluded.searchable,
		source_mtime_ns=excluded.source_mtime_ns,
		source_size=excluded.source_size`,
		key, value.ID, string(value.Kind), value.Session, value.Agent, value.Timestamp,
		value.TimestampNS, value.Summary, value.Subjects, value.Topics, value.Claim,
		value.AppliesWhen, value.Path, value.Trigger, value.Status, value.Searchable,
		value.SourceMtimeNS, value.SourceSize,
	)
	return err
}

func deleteDocument(ctx context.Context, transaction *sql.Tx, recordKey string) error {
	_, err := transaction.ExecContext(ctx, `DELETE FROM documents WHERE record_key=?`, recordKey)
	return err
}

func queryIndex(ctx context.Context, database *sql.DB, options SearchOptions) (SearchResponse, error) {
	where, args := filters(options)
	terms := keywordTerms(options.Keywords)
	useFTS := len(terms) > 0 && supportsTrigramQuery(terms)
	if len(terms) > 0 && !useFTS {
		for _, term := range terms {
			where += ` AND d.searchable LIKE ? ESCAPE '\'`
			args = append(args, likePattern(term))
		}
	}

	match := ftsExpression(terms)
	var total int
	if useFTS {
		statement := `SELECT count(*) FROM documents_fts f
			JOIN documents d ON d.rowid=f.rowid
			WHERE documents_fts MATCH ?` + where
		if err := database.QueryRowContext(ctx, statement, append([]any{match}, args...)...).Scan(&total); err != nil {
			return SearchResponse{}, err
		}
	} else {
		if err := database.QueryRowContext(ctx, `SELECT count(*) FROM documents d WHERE 1=1`+where, args...).Scan(&total); err != nil {
			return SearchResponse{}, err
		}
	}

	selectColumns := `d.id,d.timestamp,d.summary,d.subjects,d.topics,d.claim,d.applies_when,d.path`
	var (
		rows *sql.Rows
		err  error
	)
	switch {
	case useFTS:
		statement := `SELECT ` + selectColumns + `,-bm25(documents_fts) AS score
			FROM documents_fts f JOIN documents d ON d.rowid=f.rowid
			WHERE documents_fts MATCH ?` + where + `
			ORDER BY score DESC,d.timestamp_ns DESC LIMIT ?`
		queryArgs := append([]any{match}, args...)
		queryArgs = append(queryArgs, options.Limit)
		rows, err = database.QueryContext(ctx, statement, queryArgs...)
	case options.Last > 0:
		statement := `SELECT id,timestamp,summary,subjects,topics,claim,applies_when,path,NULL
			FROM (
				SELECT ` + selectColumns + `,d.timestamp_ns FROM documents d
				WHERE 1=1` + where + ` ORDER BY d.timestamp_ns DESC LIMIT ?
			) ORDER BY timestamp_ns ASC`
		rows, err = database.QueryContext(ctx, statement, append(args, options.Last)...)
	default:
		statement := `SELECT ` + selectColumns + `,NULL
			FROM documents d WHERE 1=1` + where + `
			ORDER BY d.timestamp_ns DESC LIMIT ?`
		rows, err = database.QueryContext(ctx, statement, append(args, options.Limit)...)
	}
	if err != nil {
		return SearchResponse{}, err
	}
	defer rows.Close()

	response := SearchResponse{Total: total}
	budget := options.MaxTokens * 4
	used := 0
	for rows.Next() {
		var (
			id, timestamp, summary, subjectsJSON, topicsJSON string
			claim, appliesWhen, path                         string
			score                                            sql.NullFloat64
		)
		if err := rows.Scan(
			&id,
			&timestamp,
			&summary,
			&subjectsJSON,
			&topicsJSON,
			&claim,
			&appliesWhen,
			&path,
			&score,
		); err != nil {
			return SearchResponse{}, err
		}
		result := SearchResult{
			Timestamp: timestamp, Summary: summary, Claim: claim,
			AppliesWhen: appliesWhen, Path: path,
		}
		_ = json.Unmarshal([]byte(subjectsJSON), &result.Subjects)
		_ = json.Unmarshal([]byte(topicsJSON), &result.Topics)
		if options.Target == TargetKnowledge {
			result.Slug = id
			result.Timestamp = ""
			result.Summary = ""
			result.Subjects = nil
		} else {
			result.ID = id
			result.Claim = ""
			result.AppliesWhen = ""
			result.Path = ""
		}
		if score.Valid {
			value := score.Float64
			result.Score = &value
		} else if len(terms) > 0 {
			value := float64(0)
			result.Score = &value
		}
		encoded, _ := json.Marshal(result)
		if used+len(encoded) > budget {
			response.Truncated = true
			break
		}
		used += len(encoded)
		response.Results = append(response.Results, result)
	}
	if err := rows.Err(); err != nil {
		return SearchResponse{}, err
	}
	response.Returned = len(response.Results)
	if response.Returned < response.Total {
		response.Truncated = true
	}
	return response, nil
}

func filters(options SearchOptions) (string, []any) {
	var where strings.Builder
	var args []any
	if options.Target == TargetKnowledge {
		where.WriteString(` AND d.kind=?`)
		args = append(args, string(TargetKnowledge))
		if options.IncludePending {
			where.WriteString(` AND d.status IN (?,?)`)
			args = append(args, string(domain.StatusActive), string(domain.StatusPending))
		} else {
			where.WriteString(` AND d.status=?`)
			args = append(args, string(domain.StatusActive))
		}
	} else {
		where.WriteString(` AND d.kind=?`)
		args = append(args, string(TargetFeedstock))
	}
	if options.Subject != "" {
		where.WriteString(` AND EXISTS (SELECT 1 FROM json_each(d.subjects) WHERE value=?)`)
		args = append(args, options.Subject)
	}
	if options.Topic != "" {
		where.WriteString(` AND EXISTS (SELECT 1 FROM json_each(d.topics) WHERE value=?)`)
		args = append(args, options.Topic)
	}
	if options.Since != nil {
		where.WriteString(` AND d.timestamp_ns>=?`)
		args = append(args, options.Since.UnixNano())
	}
	if options.Until != nil {
		where.WriteString(` AND d.timestamp_ns<=?`)
		args = append(args, options.Until.UnixNano())
	}
	if options.Trigger != "" {
		where.WriteString(` AND d.trigger=?`)
		args = append(args, options.Trigger)
	}
	if options.Session != "" {
		where.WriteString(` AND d.session=?`)
		args = append(args, options.Session)
	}
	if options.Agent != "" {
		where.WriteString(` AND d.agent=?`)
		args = append(args, options.Agent)
	}
	return where.String(), args
}

func Show(dataStore *store.Store, ids []string) (ShowResponse, error) {
	if len(ids) == 0 {
		return ShowResponse{}, errors.New("at least one feedstock ID is required")
	}
	response := ShowResponse{}
	for _, id := range ids {
		feedstock, _, err := dataStore.FindFeedstock(id)
		if err != nil {
			return ShowResponse{}, err
		}
		response.Feedstocks = append(response.Feedstocks, ShowResult{
			ID: feedstock.ID, Timestamp: feedstock.Timestamp, Agent: feedstock.Agent,
			Session: feedstock.Session, Summary: feedstock.Summary,
			Subjects: feedstock.Subjects, Topics: feedstock.Topics,
			UserQuote: feedstock.UserQuote,
		})
	}
	return response, nil
}

func removeIndexFiles(path string) error {
	for _, candidate := range []string{path, path + "-wal", path + "-shm"} {
		if err := os.Remove(candidate); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

func isIndexCorruption(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	for _, fragment := range []string{
		"database disk image is malformed",
		"database schema is corrupt",
		"database corruption",
		"file is not a database",
		"malformed database",
		"no such column",
		"has no column named",
		"no such table: documents",
		"no such table: documents_fts",
	} {
		if strings.Contains(message, fragment) {
			return true
		}
	}
	return false
}

func keywordTerms(keywords []string) []string {
	var terms []string
	for _, keyword := range keywords {
		terms = append(terms, strings.Fields(keyword)...)
	}
	return terms
}

func supportsTrigramQuery(terms []string) bool {
	for _, term := range terms {
		if utf8.RuneCountInString(term) < 3 {
			return false
		}
	}
	return true
}

func ftsExpression(terms []string) string {
	parts := make([]string, 0, len(terms))
	for _, term := range terms {
		term = strings.ReplaceAll(term, `"`, `""`)
		parts = append(parts, `"`+term+`"`)
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, " AND ")
}

func likePattern(term string) string {
	replacer := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return "%" + replacer.Replace(term) + "%"
}

func firstClaim(body, fallback string) string {
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(strings.TrimLeft(line, "#"))
		if line != "" {
			return line
		}
	}
	return fallback
}
