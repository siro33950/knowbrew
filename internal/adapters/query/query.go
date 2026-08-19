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
	persistenceadapter "github.com/siro33950/knowbrew/internal/adapters/persistence"
	"github.com/siro33950/knowbrew/internal/adapters/persistence/knowledgefmt"
	"github.com/siro33950/knowbrew/internal/adapters/persistence/markdownstore"
	"github.com/siro33950/knowbrew/internal/application/diagnostic"
	knowledgeapp "github.com/siro33950/knowbrew/internal/application/knowledge"
	"github.com/siro33950/knowbrew/internal/domain"
	_ "modernc.org/sqlite"
)

const indexSchemaVersion = 16
const rawPageSizeBytes = 12_000

type Target string

// The index table named "documents" stores every indexed record regardless of
// kind; TargetDocument marks the rows sourced from distilled Subject documents
// under <root>/documents.
const (
	TargetKnowledge Target = "knowledge"
	TargetFeedstock Target = "feedstock"
	TargetDocument  Target = "document"
)

type SearchOptions struct {
	Target         Target
	Keywords       []string
	Subject        string
	Type           domain.KnowledgeType
	Since          *time.Time
	Until          *time.Time
	IncludePending bool
	Template       string
	Session        string
	Agent          string
	Last           int
	Limit          int
	MaxTokens      int
	Reindex        bool
	IncludeRetired bool
}

type SearchResult struct {
	ID            string                 `json:"id,omitempty"`
	Timestamp     string                 `json:"timestamp,omitempty"`
	EstablishedAt string                 `json:"established_at,omitempty"`
	Summary       string                 `json:"summary,omitempty"`
	Subject       string                 `json:"subject,omitempty"`
	Subjects      []string               `json:"subjects,omitempty"`
	Supersedes    []string               `json:"supersedes,omitempty"`
	Type          domain.KnowledgeType   `json:"type,omitempty"`
	Types         []domain.KnowledgeType `json:"types,omitempty"`
	Score         *float64               `json:"score,omitempty"`
	Claim         string                 `json:"claim,omitempty"`
	Path          string                 `json:"path,omitempty"`
	Status        domain.Status          `json:"status,omitempty"`
}

type SearchResponse struct {
	Results   []SearchResult       `json:"results"`
	Total     int                  `json:"total"`
	Returned  int                  `json:"returned"`
	Truncated bool                 `json:"truncated"`
	Warnings  []diagnostic.Warning `json:"warnings,omitempty"`
}

type ShowResult struct {
	ID         string                 `json:"id"`
	TurnID     string                 `json:"turn_id"`
	Timestamp  time.Time              `json:"timestamp"`
	Agent      string                 `json:"agent"`
	Session    domain.SessionRef      `json:"session"`
	Summary    string                 `json:"summary"`
	Types      []domain.KnowledgeType `json:"types"`
	Subjects   []string               `json:"subjects"`
	Assertions []domain.Assertion     `json:"assertions,omitempty"`
}

type ShowResponse struct {
	Feedstocks []ShowResult `json:"feedstocks"`
}

type RawShowResponse struct {
	FeedstockID string                   `json:"feedstock_id"`
	TurnID      string                   `json:"turn_id"`
	Page        int                      `json:"page"`
	TotalPages  int                      `json:"total_pages"`
	HasMore     bool                     `json:"has_more"`
	Messages    []domain.DialogueMessage `json:"messages"`
}

type RawDialogueReader interface {
	Read(string) ([]domain.DialogueMessage, error)
}

func Search(ctx context.Context, dataStore *store.Store, options SearchOptions) (SearchResponse, error) {
	if err := validateOptions(&options); err != nil {
		return SearchResponse{}, err
	}
	if err := dataStore.EnsureLayout(); err != nil {
		return SearchResponse{}, err
	}
	if options.Type != "" {
		if err := dataStore.ValidateKnowledgeType(options.Type); err != nil {
			return SearchResponse{}, fmt.Errorf("invalid --type: %w", err)
		}
	}
	if err := ctx.Err(); err != nil {
		return SearchResponse{}, err
	}

	indexPath := filepath.Join(dataStore.Root, ".knowbrew", "state", "index.sqlite")
	indexLock := flock.New(filepath.Join(dataStore.Root, ".knowbrew", "state", "index.lock"))
	locked, err := indexLock.TryLock()
	if err != nil {
		return SearchResponse{}, fmt.Errorf("acquire search index lock: %w", err)
	}
	if !locked {
		return SearchResponse{}, errors.New("another knowbrew search process is updating the index")
	}
	defer func() { _ = indexLock.Unlock() }()

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
	if options.Target != TargetKnowledge && options.Target != TargetFeedstock && options.Target != TargetDocument {
		return errors.New("search target must be knowledge, feedstock, or document")
	}
	if options.Limit <= 0 {
		options.Limit = 20
	}
	if options.MaxTokens <= 0 {
		options.MaxTokens = 2000
	}
	if options.Type != "" {
		if options.Target == TargetDocument {
			return errors.New("--type is not valid for document")
		}
		options.Type = domain.KnowledgeType(strings.TrimSpace(string(options.Type)))
		if err := domain.ValidateKnowledgeTypeName(options.Type); err != nil {
			return fmt.Errorf("invalid --type: %w", err)
		}
	}
	options.Template = strings.TrimSpace(options.Template)
	if options.Template != "" && options.Target != TargetDocument {
		return errors.New("--template is only valid for document")
	}
	if options.Target == TargetKnowledge {
		if options.Session != "" || options.Agent != "" || options.Last != 0 {
			return errors.New("--session, --agent, and --last are only valid for feedstock")
		}
		return nil
	}
	if options.Target == TargetDocument {
		if options.Session != "" || options.Agent != "" || options.Last != 0 {
			return errors.New("--session, --agent, and --last are only valid for feedstock")
		}
		if options.IncludePending || options.IncludeRetired {
			return errors.New("--include-pending and --include-retired are only valid for knowledge")
		}
		return nil
	}
	if options.IncludePending || options.IncludeRetired {
		return errors.New("--include-pending and --include-retired are only valid for knowledge")
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
	defer func() { _ = database.Close() }()

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
			kind TEXT NOT NULL CHECK(kind IN ('feedstock','knowledge','document')),
			session TEXT NOT NULL,
			agent TEXT NOT NULL,
			timestamp TEXT NOT NULL,
			timestamp_ns INTEGER NOT NULL,
			summary TEXT NOT NULL,
			subjects TEXT NOT NULL,
			type TEXT NOT NULL,
			supersedes TEXT NOT NULL,
			claim TEXT NOT NULL,
			path TEXT NOT NULL,
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
			content='documents',
			content_rowid='rowid',
			tokenize='trigram'
		)`,
		`CREATE TRIGGER IF NOT EXISTS documents_fts_after_insert
		AFTER INSERT ON documents BEGIN
			INSERT INTO documents_fts(rowid,summary,searchable)
			VALUES(new.rowid,new.summary,new.searchable);
		END`,
		`CREATE TRIGGER IF NOT EXISTS documents_fts_after_delete
		AFTER DELETE ON documents BEGIN
			INSERT INTO documents_fts(documents_fts,rowid,summary,searchable)
			VALUES('delete',old.rowid,old.summary,old.searchable);
		END`,
		`CREATE TRIGGER IF NOT EXISTS documents_fts_after_update
		AFTER UPDATE ON documents BEGIN
			INSERT INTO documents_fts(documents_fts,rowid,summary,searchable)
			VALUES('delete',old.rowid,old.summary,old.searchable);
			INSERT INTO documents_fts(rowid,summary,searchable)
			VALUES(new.rowid,new.summary,new.searchable);
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
	_, lifecycleWarnings, err := knowledgeapp.Reconcile(
		ctx, &persistenceadapter.Markdown{Store: dataStore},
	)
	if err != nil {
		return lifecycleWarnings, err
	}
	transaction, err := database.BeginTx(ctx, nil)
	if err != nil {
		return lifecycleWarnings, err
	}
	defer func() { _ = transaction.Rollback() }()

	feedstockWarnings, err := syncFeedstocks(ctx, transaction, dataStore)
	feedstockWarnings = append(lifecycleWarnings, feedstockWarnings...)
	if err != nil {
		return feedstockWarnings, err
	}
	knowledgeWarnings, err := syncKnowledge(ctx, transaction, dataStore)
	warnings := append(feedstockWarnings, knowledgeWarnings...)
	if err != nil {
		return warnings, err
	}
	documentWarnings, err := syncDocuments(ctx, transaction, dataStore)
	warnings = append(warnings, documentWarnings...)
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
	indexed := map[string]indexedFeedstock{}
	rows, err := transaction.QueryContext(
		ctx,
		`SELECT record_key,path,source_mtime_ns,source_size FROM documents WHERE kind=?`,
		string(TargetFeedstock),
	)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var path string
		var value indexedFeedstock
		if err := rows.Scan(&value.RecordKey, &path, &value.ModTime, &value.Size); err != nil {
			_ = rows.Close()
			return nil, err
		}
		indexed[path] = value
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}

	files, walkWarnings, err := enumerateMarkdown(filepath.Join(dataStore.Root, "feedstocks"))
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
		feedstock, err := dataStore.ReadFeedstock(file.Path)
		if err != nil {
			warnings = append(warnings, diagnostic.FromError(file.Path, err))
			if exists {
				if err := deleteDocument(ctx, transaction, previous.RecordKey); err != nil {
					return warnings, err
				}
			}
			continue
		}
		if feedstock.ID != file.ID {
			warnings = append(warnings, diagnostic.FromError(
				file.Path,
				fmt.Errorf("feedstock ID %q does not match filename %q", feedstock.ID, file.ID),
			))
			if exists {
				if err := deleteDocument(ctx, transaction, previous.RecordKey); err != nil {
					return warnings, err
				}
			}
			continue
		}
		subjects, _ := json.Marshal(feedstock.Subjects)
		types, _ := json.Marshal(feedstock.Types)
		supersedes, _ := json.Marshal([]string{})
		if err := upsertDocument(ctx, transaction, document{
			ID: feedstock.ID, Kind: TargetFeedstock, Session: feedstock.Session.ID, Agent: feedstock.Agent,
			Timestamp: feedstock.Timestamp.Format(time.RFC3339Nano), TimestampNS: feedstock.Timestamp.UnixNano(),
			Summary: feedstock.Summary, Subjects: string(subjects),
			Type: string(types), Supersedes: string(supersedes),
			Searchable: strings.Join([]string{
				feedstock.Summary,
				assertionSearchableText(feedstock.Assertions),
				strings.Join(feedstock.Subjects, " "),
				strings.Join(knowledgeTypeStrings(feedstock.Types), " "),
			}, "\n"),
			Path: file.Path, Status: string(domain.StatusActive),
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

type indexedFeedstock struct {
	RecordKey string
	ModTime   int64
	Size      int64
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
			_ = rows.Close()
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
		subjects, _ := json.Marshal(domain.UniqueSorted([]string{knowledge.Subject}))
		types, _ := json.Marshal([]domain.KnowledgeType{knowledge.Type})
		supersedes, _ := json.Marshal(knowledge.Supersedes)
		claim, _, bodyErr := knowledgefmt.Decode(body)
		if bodyErr != nil {
			warnings = append(warnings, diagnostic.FromError(file.Path, bodyErr))
			if exists {
				if err := deleteDocument(ctx, transaction, previous.RecordKey); err != nil {
					return warnings, err
				}
			}
			continue
		}
		establishedAt, establishedErr := dataStore.KnowledgeEstablishedAt(knowledge)
		if establishedErr != nil {
			warnings = append(warnings, diagnostic.FromError(file.Path, establishedErr))
			establishedAt = knowledge.Updated
		}
		if err := upsertDocument(ctx, transaction, document{
			ID: knowledge.ID, Kind: TargetKnowledge,
			Timestamp: establishedAt.Format(time.RFC3339Nano), TimestampNS: establishedAt.UnixNano(),
			Subjects: string(subjects), Type: string(types),
			Supersedes: string(supersedes), Claim: claim,
			Path:   file.Path,
			Status: string(knowledge.Status),
			Searchable: strings.Join([]string{
				claim,
				body,
				knowledge.Subject,
				string(knowledge.Type),
				strings.Join(knowledge.Supersedes, " "),
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

type indexedDistilled struct {
	RecordKey string
	ModTime   int64
	Size      int64
}

func syncDocuments(
	ctx context.Context,
	transaction *sql.Tx,
	dataStore *store.Store,
) ([]diagnostic.Warning, error) {
	indexed := map[string]indexedDistilled{}
	rows, err := transaction.QueryContext(
		ctx,
		`SELECT record_key,path,source_mtime_ns,source_size FROM documents WHERE kind=?`,
		string(TargetDocument),
	)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var path string
		var value indexedDistilled
		if err := rows.Scan(&value.RecordKey, &path, &value.ModTime, &value.Size); err != nil {
			_ = rows.Close()
			return nil, err
		}
		indexed[path] = value
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}

	files, walkWarnings, err := enumerateMarkdown(filepath.Join(dataStore.Root, "documents"))
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
		distilled, err := dataStore.ReadDistilledDocumentFile(file.Path)
		if err != nil {
			warnings = append(warnings, diagnostic.FromError(file.Path, err))
			if exists {
				if err := deleteDocument(ctx, transaction, previous.RecordKey); err != nil {
					return warnings, err
				}
			}
			continue
		}
		subjects, _ := json.Marshal([]string{distilled.Subject})
		templates, _ := json.Marshal([]string{distilled.Template})
		supersedes, _ := json.Marshal([]string{})
		timestamp := time.Unix(0, file.ModTime).UTC()
		if err := upsertDocument(ctx, transaction, document{
			ID: distilled.Subject + "/" + distilled.Template, Kind: TargetDocument,
			Timestamp: timestamp.Format(time.RFC3339Nano), TimestampNS: file.ModTime,
			Subjects: string(subjects), Type: string(templates),
			Supersedes: string(supersedes), Claim: documentExcerpt(distilled.Body),
			Path: file.Path, Status: string(domain.StatusActive),
			Searchable: strings.Join([]string{
				distilled.Body,
				distilled.Subject,
				distilled.Template,
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

const documentExcerptBytes = 500

// documentExcerpt selects the first non-heading paragraph as the record's
// representative text, mirroring how knowledge uses its claim for embedding.
func documentExcerpt(body string) string {
	for _, block := range strings.Split(body, "\n\n") {
		block = strings.TrimSpace(block)
		if block == "" || strings.HasPrefix(block, "#") {
			continue
		}
		if len(block) <= documentExcerptBytes {
			return block
		}
		end := documentExcerptBytes
		for end > 0 && !utf8.RuneStart(block[end]) {
			end--
		}
		return block[:end]
	}
	return ""
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
	ID, Session, Agent, Timestamp, Summary, Subjects, Type string
	Supersedes                                             string
	Claim, Path, Status, Searchable                        string
	Kind                                                   Target
	TimestampNS, SourceMtimeNS, SourceSize                 int64
}

func (value document) recordKey() string {
	return fmt.Sprintf("%s:%s", value.Kind, value.ID)
}

func upsertDocument(ctx context.Context, transaction *sql.Tx, value document) error {
	key := value.recordKey()
	_, err := transaction.ExecContext(ctx, `INSERT INTO documents (
		record_key,id,kind,session,agent,timestamp,timestamp_ns,summary,subjects,
		type,supersedes,claim,path,status,searchable,source_mtime_ns,source_size
	) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
	ON CONFLICT(record_key) DO UPDATE SET
		id=excluded.id,
		kind=excluded.kind,
		session=excluded.session,
		agent=excluded.agent,
		timestamp=excluded.timestamp,
		timestamp_ns=excluded.timestamp_ns,
		summary=excluded.summary,
		subjects=excluded.subjects,
		type=excluded.type,
		supersedes=excluded.supersedes,
		claim=excluded.claim,
		path=excluded.path,
		status=excluded.status,
		searchable=excluded.searchable,
		source_mtime_ns=excluded.source_mtime_ns,
		source_size=excluded.source_size`,
		key, value.ID, string(value.Kind), value.Session, value.Agent, value.Timestamp,
		value.TimestampNS, value.Summary, value.Subjects, value.Type,
		value.Supersedes, value.Claim, value.Path, value.Status, value.Searchable,
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

	selectColumns := `d.id,d.timestamp,d.summary,d.subjects,d.type,d.supersedes,d.claim,d.path,d.status`
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
		statement := `SELECT id,timestamp,summary,subjects,type,supersedes,claim,path,status,NULL
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
	defer func() { _ = rows.Close() }()

	response := SearchResponse{Total: total}
	budget := options.MaxTokens * 4
	used := 0
	for rows.Next() {
		var (
			id, timestamp, summary, subjectsJSON, typesJSON string
			supersedesJSON, claim, path, status             string
			score                                           sql.NullFloat64
		)
		if err := rows.Scan(
			&id,
			&timestamp,
			&summary,
			&subjectsJSON,
			&typesJSON,
			&supersedesJSON,
			&claim,
			&path,
			&status,
			&score,
		); err != nil {
			return SearchResponse{}, err
		}
		result := SearchResult{
			Timestamp: timestamp, Summary: summary, Claim: claim,
			Path: path,
		}
		_ = json.Unmarshal([]byte(subjectsJSON), &result.Subjects)
		_ = json.Unmarshal([]byte(supersedesJSON), &result.Supersedes)
		var resultTypes []domain.KnowledgeType
		_ = json.Unmarshal([]byte(typesJSON), &resultTypes)
		switch options.Target {
		case TargetKnowledge:
			if len(resultTypes) == 1 {
				result.Type = resultTypes[0]
			}
			result.ID = id
			result.EstablishedAt = timestamp
			if len(result.Subjects) == 1 {
				result.Subject = result.Subjects[0]
			}
			result.Timestamp = ""
			result.Summary = ""
			result.Subjects = nil
			result.Status = domain.Status(status)
		case TargetDocument:
			result.Types = resultTypes
			result.ID = id
			if len(result.Subjects) == 1 {
				result.Subject = result.Subjects[0]
			}
			result.Summary = ""
			result.Subjects = nil
			result.Status = ""
			result.Supersedes = nil
		default:
			result.Types = resultTypes
			result.ID = id
			result.Claim = ""
			result.Path = ""
			result.Status = ""
			result.Supersedes = nil
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
	switch options.Target {
	case TargetKnowledge:
		where.WriteString(` AND d.kind=?`)
		args = append(args, string(TargetKnowledge))
		switch {
		case options.IncludePending && options.IncludeRetired:
			where.WriteString(` AND d.status IN (?,?,?,?)`)
			args = append(
				args,
				string(domain.StatusActive),
				string(domain.StatusPending),
				string(domain.StatusInvalidated),
				string(domain.StatusSuperseded),
			)
		case options.IncludePending:
			where.WriteString(` AND d.status IN (?,?)`)
			args = append(args, string(domain.StatusActive), string(domain.StatusPending))
		case options.IncludeRetired:
			where.WriteString(` AND d.status IN (?,?,?)`)
			args = append(
				args,
				string(domain.StatusActive),
				string(domain.StatusInvalidated),
				string(domain.StatusSuperseded),
			)
		default:
			where.WriteString(` AND d.status=?`)
			args = append(args, string(domain.StatusActive))
		}
	case TargetDocument:
		where.WriteString(` AND d.kind=?`)
		args = append(args, string(TargetDocument))
	default:
		where.WriteString(` AND d.kind=?`)
		args = append(args, string(TargetFeedstock))
	}
	if options.Subject != "" {
		where.WriteString(` AND EXISTS (SELECT 1 FROM json_each(d.subjects) WHERE value=?)`)
		args = append(args, options.Subject)
	}
	if options.Type != "" {
		where.WriteString(` AND EXISTS (SELECT 1 FROM json_each(d.type) WHERE value=?)`)
		args = append(args, string(options.Type))
	}
	if options.Since != nil {
		where.WriteString(` AND d.timestamp_ns>=?`)
		args = append(args, options.Since.UnixNano())
	}
	if options.Until != nil {
		where.WriteString(` AND d.timestamp_ns<=?`)
		args = append(args, options.Until.UnixNano())
	}
	if options.Template != "" {
		where.WriteString(` AND EXISTS (SELECT 1 FROM json_each(d.type) WHERE value=?)`)
		args = append(args, options.Template)
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
			ID: feedstock.ID, TurnID: feedstock.TurnID,
			Timestamp: feedstock.Timestamp, Agent: feedstock.Agent,
			Session: feedstock.Session, Summary: feedstock.Summary,
			Types:      feedstock.Types,
			Subjects:   feedstock.Subjects,
			Assertions: feedstock.Assertions,
		})
	}
	return response, nil
}

func assertionSearchableText(assertions []domain.Assertion) string {
	parts := make([]string, 0, len(assertions)*4)
	for _, assertion := range assertions {
		parts = append(parts,
			assertion.Statement,
			assertion.Rationale,
			assertion.Subject,
		)
	}
	return strings.Join(parts, "\n")
}

func knowledgeTypeStrings(values []domain.KnowledgeType) []string {
	out := make([]string, len(values))
	for index, value := range values {
		out[index] = string(value)
	}
	return out
}

func ShowRaw(
	dataStore *store.Store,
	reader RawDialogueReader,
	id string,
	page int,
) (RawShowResponse, error) {
	if page < 1 {
		return RawShowResponse{}, errors.New("raw show page must be at least 1")
	}
	feedstock, messages, err := extractRawDialogue(dataStore, reader, id)
	if err != nil {
		return RawShowResponse{}, err
	}
	pages := paginateDialogue(messages, rawPageSizeBytes)
	if page > len(pages) {
		return RawShowResponse{}, fmt.Errorf(
			"raw show page %d exceeds total pages %d for feedstock %s",
			page,
			len(pages),
			feedstock.ID,
		)
	}
	return RawShowResponse{
		FeedstockID: feedstock.ID,
		TurnID:      feedstock.TurnID,
		Page:        page,
		TotalPages:  len(pages),
		HasMore:     page < len(pages),
		Messages:    pages[page-1],
	}, nil
}

// ExtractRawDialogue returns the same mechanically filtered dialogue used by
// show --raw, without applying its presentation-oriented pagination.
func ExtractRawDialogue(
	dataStore *store.Store,
	reader RawDialogueReader,
	id string,
) ([]domain.DialogueMessage, error) {
	_, messages, err := extractRawDialogue(dataStore, reader, id)
	return messages, err
}

func extractRawDialogue(
	dataStore *store.Store,
	reader RawDialogueReader,
	id string,
) (domain.Feedstock, []domain.DialogueMessage, error) {
	feedstock, _, err := dataStore.FindFeedstock(id)
	if err != nil {
		return domain.Feedstock{}, nil, err
	}
	messages, err := reader.Read(feedstock.ID)
	if err != nil {
		return domain.Feedstock{}, nil, err
	}
	return feedstock, messages, nil
}

func paginateDialogue(messages []domain.DialogueMessage, pageSize int) [][]domain.DialogueMessage {
	if pageSize < 1 {
		pageSize = rawPageSizeBytes
	}
	var pages [][]domain.DialogueMessage
	current := make([]domain.DialogueMessage, 0, len(messages))
	used := 0
	flush := func() {
		if len(current) == 0 {
			return
		}
		pages = append(pages, current)
		current = nil
		used = 0
	}
	for _, message := range messages {
		remainingContent := message.Content
		if remainingContent == "" {
			continue
		}
		for remainingContent != "" {
			if used >= pageSize {
				flush()
			}
			available := pageSize - used
			end := min(len(remainingContent), available)
			if end < len(remainingContent) {
				for end > 0 && !utf8.RuneStart(remainingContent[end]) {
					end--
				}
			}
			if end == 0 {
				if used > 0 {
					flush()
					continue
				}
				_, end = utf8.DecodeRuneInString(remainingContent)
			}
			chunk := remainingContent[:end]
			current = append(current, domain.DialogueMessage{
				Role: message.Role, Content: chunk,
			})
			used += len(chunk)
			remainingContent = remainingContent[end:]
		}
	}
	flush()
	if len(pages) == 0 {
		pages = append(pages, []domain.DialogueMessage{})
	}
	return pages
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
