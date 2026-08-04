package query

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	sqlitevec "github.com/asg017/sqlite-vec-go-bindings/cgo"
	"github.com/gofrs/flock"
	_ "github.com/mattn/go-sqlite3"
	"github.com/siro33950/knowbrew/internal/adapters/embedding"
	"github.com/siro33950/knowbrew/internal/adapters/fsutil"
	"github.com/siro33950/knowbrew/internal/adapters/persistence/markdownstore"
	"github.com/siro33950/knowbrew/internal/application/diagnostic"
	searchapp "github.com/siro33950/knowbrew/internal/application/search"
	"github.com/siro33950/knowbrew/internal/domain"
)

const vectorIndexSchemaVersion = 1
const vectorEmbeddingBatchSize = 64

func init() {
	sqlitevec.Auto()
}

type Gateway struct {
	Store   *store.Store
	Encoder embedding.Encoder
}

type vectorSource struct {
	key, id, kind, text string
	mtime, size         int64
}

type indexSyncState struct {
	LastAttemptedAt    time.Time `json:"last_attempted_at"`
	LastSynchronizedAt time.Time `json:"last_synchronized_at,omitempty"`
	LastError          string    `json:"last_error,omitempty"`
}

func (gateway Gateway) SemanticEnabled() bool {
	return gateway.Encoder != nil
}

func (gateway Gateway) ValidateType(value domain.KnowledgeType) error {
	if gateway.Store == nil {
		return errors.New("search store is required")
	}
	return gateway.Store.ValidateKnowledgeType(value)
}

func (gateway Gateway) Synchronize(
	ctx context.Context,
	rebuild bool,
) (searchapp.SyncReport, []diagnostic.Warning, error) {
	if gateway.Store == nil {
		return searchapp.SyncReport{}, nil, errors.New("search store is required")
	}
	if err := gateway.Store.EnsureLayout(); err != nil {
		return searchapp.SyncReport{}, nil, err
	}
	stateDirectory := filepath.Join(gateway.Store.Root, ".knowbrew", "state")
	indexPath := filepath.Join(stateDirectory, "index.sqlite")
	lock := flock.New(filepath.Join(stateDirectory, "index.lock"))
	locked, err := lock.TryLock()
	if err != nil {
		return searchapp.SyncReport{}, nil, fmt.Errorf("acquire search index lock: %w", err)
	}
	if !locked {
		return searchapp.SyncReport{}, nil, errors.New("another knowbrew process is updating the search index")
	}
	defer func() { _ = lock.Unlock() }()
	report, warnings, syncErr := gateway.synchronizeLocked(ctx, indexPath, rebuild)
	stateErr := gateway.writeSyncState(stateDirectory, report, syncErr)
	if syncErr != nil {
		return report, warnings, errors.Join(syncErr, stateErr)
	}
	if stateErr != nil {
		return report, warnings, fmt.Errorf("record search index state: %w", stateErr)
	}
	return report, warnings, nil
}

func (gateway Gateway) synchronizeLocked(
	ctx context.Context,
	indexPath string,
	rebuild bool,
) (searchapp.SyncReport, []diagnostic.Warning, error) {
	database, err := openIndex(ctx, indexPath)
	if err != nil {
		return searchapp.SyncReport{}, nil, err
	}
	warnings, syncErr := synchronize(ctx, database, gateway.Store, rebuild)
	if syncErr != nil && isIndexCorruption(syncErr) {
		_ = database.Close()
		if removeErr := removeIndexFiles(indexPath); removeErr != nil {
			return searchapp.SyncReport{}, warnings, fmt.Errorf("replace corrupt search index: %w", removeErr)
		}
		database, err = openIndex(ctx, indexPath)
		if err != nil {
			return searchapp.SyncReport{}, warnings, err
		}
		warnings, syncErr = synchronize(ctx, database, gateway.Store, true)
	}
	if syncErr != nil {
		_ = database.Close()
		return searchapp.SyncReport{}, warnings, syncErr
	}
	var documents int
	if err := database.QueryRowContext(ctx, `SELECT count(*) FROM documents`).Scan(&documents); err != nil {
		_ = database.Close()
		return searchapp.SyncReport{}, warnings, err
	}
	report := searchapp.SyncReport{
		Documents: documents, IndexVersion: indexSchemaVersion,
	}
	if gateway.Encoder != nil {
		embedded, deleted, vectorErr := gateway.syncVectors(ctx, database, rebuild)
		if vectorErr != nil {
			_ = database.Close()
			return report, warnings, vectorErr
		}
		report.Embedded = embedded
		report.Deleted = deleted
		report.Model = gateway.Encoder.ID()
	}
	if _, err := database.ExecContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		_ = database.Close()
		return report, warnings, fmt.Errorf("checkpoint search index: %w", err)
	}
	if err := database.Close(); err != nil {
		return report, warnings, err
	}
	report.SynchronizedAt = time.Now().UTC()
	return report, warnings, nil
}

func (gateway Gateway) writeSyncState(
	stateDirectory string,
	report searchapp.SyncReport,
	syncErr error,
) error {
	path := filepath.Join(stateDirectory, "index-status.json")
	state := indexSyncState{LastAttemptedAt: time.Now().UTC()}
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, &state)
		state.LastAttemptedAt = time.Now().UTC()
	}
	if syncErr != nil {
		state.LastError = syncErr.Error()
	} else {
		state.LastError = ""
		state.LastSynchronizedAt = report.SynchronizedAt
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	return fsutil.AtomicWrite(path, append(data, '\n'), 0o600)
}

func (gateway Gateway) Text(
	ctx context.Context,
	options searchapp.Options,
	limit int,
) ([]searchapp.RankedID, error) {
	database, err := gateway.openTextIndex(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = database.Close() }()
	where, args, terms, useFTS := textQueryParts(options)
	var rows *sql.Rows
	if useFTS {
		statement := `SELECT d.id FROM documents_fts f
			JOIN documents d ON d.rowid=f.rowid
			WHERE documents_fts MATCH ?` + where + `
			ORDER BY bm25(documents_fts),d.timestamp_ns DESC,d.id LIMIT ?`
		queryArgs := append([]any{ftsExpression(terms)}, args...)
		rows, err = database.QueryContext(ctx, statement, append(queryArgs, limit)...)
	} else {
		rows, err = database.QueryContext(ctx,
			`SELECT d.id FROM documents d WHERE 1=1`+where+`
			ORDER BY d.timestamp_ns DESC,d.id LIMIT ?`, append(args, limit)...,
		)
	}
	if err != nil {
		return nil, err
	}
	return rankedRows(rows)
}

func (gateway Gateway) Vector(
	ctx context.Context,
	options searchapp.Options,
	limit int,
) ([]searchapp.RankedID, error) {
	if gateway.Encoder == nil {
		return nil, errors.New("vector search is disabled")
	}
	query := strings.Join(keywordTerms(options.Keywords), " ")
	vector, err := gateway.Encoder.EncodeQuery(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("embed search query: %w", err)
	}
	eligible, err := gateway.eligibleIDs(ctx, options)
	if err != nil {
		return nil, err
	}
	database, err := openVectorIndexReadOnly(ctx, gateway.vectorPath())
	if err != nil {
		return nil, err
	}
	defer func() { _ = database.Close() }()
	table, err := vectorTable(options.Target)
	if err != nil {
		return nil, err
	}
	var count int
	if err := database.QueryRowContext(ctx,
		`SELECT count(*) FROM vector_documents WHERE kind=?`, string(options.Target),
	).Scan(&count); err != nil {
		return nil, err
	}
	if count == 0 {
		return nil, nil
	}
	serialized, err := sqlitevec.SerializeFloat32(vector)
	if err != nil {
		return nil, err
	}
	k := min(count, max(1, limit))
	for {
		ranked, err := vectorPrefix(ctx, database, table, serialized, eligible, k, limit)
		if err != nil {
			return nil, err
		}
		if len(ranked) >= limit || k == count {
			return ranked, nil
		}
		k = min(count, k*2)
	}
}

func vectorPrefix(
	ctx context.Context,
	database *sql.DB,
	table string,
	serialized []byte,
	eligible map[string]struct{},
	k, limit int,
) ([]searchapp.RankedID, error) {
	rows, err := database.QueryContext(ctx,
		`SELECT v.rowid,v.distance,d.id FROM `+table+` v
		LEFT JOIN vector_documents d ON d.vector_rowid=v.rowid
		WHERE v.embedding MATCH ? AND k=? ORDER BY v.distance`, serialized, k,
	)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var ranked []searchapp.RankedID
	for rows.Next() {
		var rowID int64
		var distance float64
		var id sql.NullString
		if err := rows.Scan(&rowID, &distance, &id); err != nil {
			return nil, err
		}
		if !id.Valid {
			return nil, fmt.Errorf("vector row %d has no document metadata", rowID)
		}
		if _, exists := eligible[id.String]; !exists {
			continue
		}
		ranked = append(ranked, searchapp.RankedID{ID: id.String, Rank: len(ranked) + 1})
		if len(ranked) == limit {
			break
		}
	}
	return ranked, rows.Err()
}

func (gateway Gateway) Chronological(
	ctx context.Context,
	options searchapp.Options,
	limit int,
) ([]searchapp.RankedID, error) {
	database, err := gateway.openTextIndex(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = database.Close() }()
	where, args := filters(legacyOptions(options))
	direction := "DESC"
	if options.Last > 0 {
		direction = "ASC"
	}
	statement := `SELECT id FROM (
		SELECT d.id,d.timestamp_ns FROM documents d WHERE 1=1` + where + `
		ORDER BY d.timestamp_ns DESC,d.id LIMIT ?
	) ORDER BY timestamp_ns ` + direction
	rows, err := database.QueryContext(ctx, statement, append(args, limit)...)
	if err != nil {
		return nil, err
	}
	return rankedRows(rows)
}

func (gateway Gateway) Count(
	ctx context.Context,
	options searchapp.Options,
	mode searchapp.Mode,
) (int, error) {
	if len(keywordTerms(options.Keywords)) == 0 || mode == searchapp.ModeText {
		return gateway.textCount(ctx, options)
	}
	if gateway.Encoder == nil {
		return 0, errors.New("vector search is disabled")
	}
	vectorIDs, err := gateway.vectorEligibleIDs(ctx, options)
	if err != nil {
		return 0, err
	}
	if mode == searchapp.ModeVector {
		return len(vectorIDs), nil
	}
	if mode != searchapp.ModeHybrid {
		return 0, fmt.Errorf("unsupported search mode %q", mode)
	}
	textIDs, err := gateway.textMatchingIDs(ctx, options)
	if err != nil {
		return 0, err
	}
	for id := range textIDs {
		vectorIDs[id] = struct{}{}
	}
	return len(vectorIDs), nil
}

func (gateway Gateway) textCount(ctx context.Context, options searchapp.Options) (int, error) {
	database, err := gateway.openTextIndex(ctx)
	if err != nil {
		return 0, err
	}
	defer func() { _ = database.Close() }()
	where, args, terms, useFTS := textQueryParts(options)
	var count int
	if useFTS {
		statement := `SELECT count(*) FROM documents_fts f
			JOIN documents d ON d.rowid=f.rowid
			WHERE documents_fts MATCH ?` + where
		queryArgs := append([]any{ftsExpression(terms)}, args...)
		err = database.QueryRowContext(ctx, statement, queryArgs...).Scan(&count)
	} else {
		err = database.QueryRowContext(ctx,
			`SELECT count(*) FROM documents d WHERE 1=1`+where, args...,
		).Scan(&count)
	}
	return count, err
}

func (gateway Gateway) textMatchingIDs(
	ctx context.Context,
	options searchapp.Options,
) (map[string]struct{}, error) {
	database, err := gateway.openTextIndex(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = database.Close() }()
	where, args, terms, useFTS := textQueryParts(options)
	var rows *sql.Rows
	if useFTS {
		statement := `SELECT d.id FROM documents_fts f
			JOIN documents d ON d.rowid=f.rowid
			WHERE documents_fts MATCH ?` + where
		queryArgs := append([]any{ftsExpression(terms)}, args...)
		rows, err = database.QueryContext(ctx, statement, queryArgs...)
	} else {
		rows, err = database.QueryContext(ctx,
			`SELECT d.id FROM documents d WHERE 1=1`+where, args...,
		)
	}
	if err != nil {
		return nil, err
	}
	return idSet(rows)
}

func (gateway Gateway) vectorEligibleIDs(
	ctx context.Context,
	options searchapp.Options,
) (map[string]struct{}, error) {
	eligible, err := gateway.eligibleIDs(ctx, options)
	if err != nil {
		return nil, err
	}
	database, err := openVectorIndexReadOnly(ctx, gateway.vectorPath())
	if err != nil {
		return nil, err
	}
	defer func() { _ = database.Close() }()
	rows, err := database.QueryContext(ctx,
		`SELECT id FROM vector_documents WHERE kind=?`, string(options.Target),
	)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	result := map[string]struct{}{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		if _, exists := eligible[id]; exists {
			result[id] = struct{}{}
		}
	}
	return result, rows.Err()
}

func textQueryParts(options searchapp.Options) (string, []any, []string, bool) {
	where, args := filters(legacyOptions(options))
	terms := keywordTerms(options.Keywords)
	useFTS := len(terms) > 0 && supportsTrigramQuery(terms)
	if len(terms) > 0 && !useFTS {
		for _, term := range terms {
			where += ` AND d.searchable LIKE ? ESCAPE '\'`
			args = append(args, likePattern(term))
		}
	}
	return where, args, terms, useFTS
}

func idSet(rows *sql.Rows) (map[string]struct{}, error) {
	defer func() { _ = rows.Close() }()
	result := map[string]struct{}{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		result[id] = struct{}{}
	}
	return result, rows.Err()
}

func (gateway Gateway) Load(
	ctx context.Context,
	target searchapp.Target,
	ids []string,
) ([]searchapp.Result, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	database, err := gateway.openTextIndex(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = database.Close() }()
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
	args := make([]any, 0, len(ids)+1)
	args = append(args, string(target))
	for _, id := range ids {
		args = append(args, id)
	}
	rows, err := database.QueryContext(ctx, `SELECT
		id,timestamp,agent,session,summary,subjects,type,claim
		FROM documents WHERE kind=? AND id IN (`+placeholders+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var results []searchapp.Result
	for rows.Next() {
		var id, timestamp, agent, session, summary, subjectsJSON, typesJSON, claim string
		if err := rows.Scan(
			&id, &timestamp, &agent, &session, &summary, &subjectsJSON, &typesJSON, &claim,
		); err != nil {
			return nil, err
		}
		result := searchapp.Result{ID: id}
		_ = json.Unmarshal([]byte(subjectsJSON), &result.Subjects)
		var types []domain.KnowledgeType
		_ = json.Unmarshal([]byte(typesJSON), &types)
		if target == searchapp.TargetKnowledge {
			result.Claim = claim
			if len(result.Subjects) == 1 {
				result.Subject = result.Subjects[0]
			}
			if len(types) == 1 {
				result.Type = types[0]
			}
			result.Subjects = nil
		} else {
			result.Timestamp = timestamp
			result.Agent = agent
			result.Session = session
			result.Summary = summary
			result.Types = types
		}
		results = append(results, result)
	}
	return results, rows.Err()
}

func (gateway Gateway) Status(
	ctx context.Context,
) (searchapp.Status, []diagnostic.Warning, error) {
	if gateway.Store == nil {
		return searchapp.Status{}, nil, errors.New("search store is required")
	}
	status := searchapp.Status{
		ExpectedVersion: indexSchemaVersion,
		SemanticEnabled: gateway.Encoder != nil,
	}
	if gateway.Encoder != nil {
		status.Model = gateway.Encoder.ID()
	}
	if state, err := gateway.readSyncState(); err == nil {
		status.LastSynchronizedAt = state.LastSynchronizedAt
		status.LastError = state.LastError
	} else if !errors.Is(err, os.ErrNotExist) {
		status.LastError = fmt.Sprintf("read search index state: %v", err)
	}
	database, err := gateway.openTextIndex(ctx)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			count, warnings, countErr := gateway.unindexedSourceFiles()
			status.Unsynchronized = count
			return status, warnings, countErr
		}
		return status, nil, err
	}
	defer func() { _ = database.Close() }()
	if err := database.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&status.IndexVersion); err != nil {
		return status, nil, err
	}
	if err := database.QueryRowContext(ctx, `SELECT count(*) FROM documents`).Scan(&status.Documents); err != nil {
		return status, nil, err
	}
	unsynchronized, warnings, err := gateway.unsynchronized(database)
	if err != nil {
		return status, warnings, err
	}
	status.Unsynchronized = unsynchronized
	if gateway.Encoder != nil {
		vectors, vectorUnsynchronized, lastSync, vectorErr := gateway.vectorStatus(ctx, database)
		if vectorErr != nil {
			status.LastError = vectorErr.Error()
			return status, warnings, nil
		}
		status.Vectors = vectors
		if status.LastSynchronizedAt.IsZero() {
			status.LastSynchronizedAt = lastSync
		}
		status.Unsynchronized += vectorUnsynchronized
	}
	return status, warnings, nil
}

func (gateway Gateway) readSyncState() (indexSyncState, error) {
	data, err := os.ReadFile(filepath.Join(
		gateway.Store.Root, ".knowbrew", "state", "index-status.json",
	))
	if err != nil {
		return indexSyncState{}, err
	}
	var state indexSyncState
	if err := json.Unmarshal(data, &state); err != nil {
		return indexSyncState{}, err
	}
	return state, nil
}

func (gateway Gateway) openTextIndex(ctx context.Context) (*sql.DB, error) {
	if gateway.Store == nil {
		return nil, errors.New("search store is required")
	}
	path := filepath.Join(gateway.Store.Root, ".knowbrew", "state", "index.sqlite")
	if _, err := os.Stat(path); err != nil {
		return nil, err
	}
	uri := (&url.URL{Scheme: "file", Path: path}).String() + "?mode=ro&_pragma=busy_timeout(5000)"
	database, err := sql.Open("sqlite", uri)
	if err != nil {
		return nil, err
	}
	if err := database.PingContext(ctx); err != nil {
		_ = database.Close()
		return nil, err
	}
	return database, nil
}

func (gateway Gateway) vectorPath() string {
	return filepath.Join(gateway.Store.Root, ".knowbrew", "state", "vectors.sqlite")
}

func (gateway Gateway) syncVectors(
	ctx context.Context,
	textIndex *sql.DB,
	rebuild bool,
) (int, int, error) {
	path := gateway.vectorPath()
	if rebuild {
		if err := removeIndexFiles(path); err != nil {
			return 0, 0, fmt.Errorf("replace vector index: %w", err)
		}
	}
	vectorIndex, err := openVectorIndex(ctx, path)
	if err != nil {
		return 0, 0, err
	}
	defer func() { _ = vectorIndex.Close() }()
	if err := ensureVectorSchema(ctx, vectorIndex, gateway.Encoder, rebuild); err != nil {
		return 0, 0, err
	}
	rows, err := textIndex.QueryContext(ctx, `SELECT
		record_key,id,kind,summary,claim,source_mtime_ns,source_size FROM documents ORDER BY record_key`)
	if err != nil {
		return 0, 0, err
	}
	var sources []vectorSource
	for rows.Next() {
		var source vectorSource
		var summary, claim string
		if err := rows.Scan(&source.key, &source.id, &source.kind, &summary, &claim, &source.mtime, &source.size); err != nil {
			_ = rows.Close()
			return 0, 0, err
		}
		if source.kind == string(searchapp.TargetKnowledge) {
			source.text = strings.TrimSpace(claim)
		} else {
			source.text = strings.TrimSpace(summary)
		}
		sources = append(sources, source)
	}
	if err := rows.Close(); err != nil {
		return 0, 0, err
	}
	indexed := map[string]struct {
		rowID       int64
		mtime, size int64
		model       string
	}{}
	indexedRows, err := vectorIndex.QueryContext(ctx,
		`SELECT record_key,vector_rowid,source_mtime_ns,source_size,model_id FROM vector_documents`,
	)
	if err != nil {
		return 0, 0, err
	}
	for indexedRows.Next() {
		var key string
		var value struct {
			rowID       int64
			mtime, size int64
			model       string
		}
		if err := indexedRows.Scan(&key, &value.rowID, &value.mtime, &value.size, &value.model); err != nil {
			_ = indexedRows.Close()
			return 0, 0, err
		}
		indexed[key] = value
	}
	if err := indexedRows.Close(); err != nil {
		return 0, 0, err
	}
	current := make(map[string]struct{}, len(sources))
	var changed []vectorSource
	var emptied []string
	for _, source := range sources {
		current[source.key] = struct{}{}
		previous, exists := indexed[source.key]
		if source.text == "" {
			if exists {
				emptied = append(emptied, source.key)
			}
			continue
		}
		if exists && previous.mtime == source.mtime && previous.size == source.size && previous.model == gateway.Encoder.ID() {
			continue
		}
		changed = append(changed, source)
	}
	deleted, err := deleteStaleVectors(ctx, vectorIndex, indexed, current, emptied)
	if err != nil {
		return 0, deleted, err
	}
	embedded := 0
	for start := 0; start < len(changed); start += vectorEmbeddingBatchSize {
		end := min(start+vectorEmbeddingBatchSize, len(changed))
		batch := changed[start:end]
		vectors, err := gateway.Encoder.EncodeDocuments(ctx, sourceTexts(batch))
		if err != nil {
			return embedded, deleted, err
		}
		if len(vectors) != len(batch) {
			return embedded, deleted, fmt.Errorf(
				"embedding backend returned %d vectors for %d documents", len(vectors), len(batch),
			)
		}
		if err := upsertVectorBatch(ctx, vectorIndex, gateway.Encoder, indexed, batch, vectors); err != nil {
			return embedded, deleted, err
		}
		embedded += len(batch)
	}
	if _, err := vectorIndex.ExecContext(ctx,
		`INSERT INTO vector_metadata(key,value) VALUES('last_synchronized_at',?)
		ON CONFLICT(key) DO UPDATE SET value=excluded.value`, time.Now().UTC().Format(time.RFC3339Nano),
	); err != nil {
		return embedded, deleted, err
	}
	return embedded, deleted, nil
}

func deleteStaleVectors(
	ctx context.Context,
	database *sql.DB,
	indexed map[string]struct {
		rowID       int64
		mtime, size int64
		model       string
	},
	current map[string]struct{},
	emptied []string,
) (int, error) {
	transaction, err := database.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = transaction.Rollback() }()
	deleted := 0
	for _, key := range emptied {
		previous := indexed[key]
		if err := deleteVector(ctx, transaction, previous.rowID, key); err != nil {
			return deleted, err
		}
		deleted++
	}
	for key, previous := range indexed {
		if _, exists := current[key]; exists {
			continue
		}
		if err := deleteVector(ctx, transaction, previous.rowID, key); err != nil {
			return deleted, err
		}
		deleted++
	}
	return deleted, transaction.Commit()
}

func upsertVectorBatch(
	ctx context.Context,
	database *sql.DB,
	encoder embedding.Encoder,
	indexed map[string]struct {
		rowID       int64
		mtime, size int64
		model       string
	},
	sources []vectorSource,
	vectors [][]float32,
) error {
	transaction, err := database.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = transaction.Rollback() }()
	for index, source := range sources {
		if previous, exists := indexed[source.key]; exists {
			if err := deleteVector(ctx, transaction, previous.rowID, source.key); err != nil {
				return err
			}
		}
		serialized, err := sqlitevec.SerializeFloat32(vectors[index])
		if err != nil {
			return err
		}
		result, err := transaction.ExecContext(ctx, `INSERT INTO vector_documents
			(record_key,id,kind,source_mtime_ns,source_size,model_id)
			VALUES(?,?,?,?,?,?)`, source.key, source.id, source.kind, source.mtime, source.size, encoder.ID())
		if err != nil {
			return err
		}
		rowID, err := result.LastInsertId()
		if err != nil {
			return err
		}
		table, err := vectorTable(searchapp.Target(source.kind))
		if err != nil {
			return err
		}
		if _, err := transaction.ExecContext(ctx,
			`INSERT INTO `+table+`(rowid,embedding) VALUES(?,?)`, rowID, serialized,
		); err != nil {
			return err
		}
	}
	return transaction.Commit()
}

func sourceTexts(values []vectorSource) []string {
	result := make([]string, len(values))
	for index, value := range values {
		result[index] = value.text
	}
	return result
}

func openVectorIndex(ctx context.Context, path string) (*sql.DB, error) {
	uri := (&url.URL{Scheme: "file", Path: path}).String() + "?_busy_timeout=5000&_journal_mode=WAL"
	database, err := sql.Open("sqlite3", uri)
	if err != nil {
		return nil, err
	}
	database.SetMaxOpenConns(1)
	if err := database.PingContext(ctx); err != nil {
		_ = database.Close()
		return nil, err
	}
	return database, nil
}

func openVectorIndexReadOnly(ctx context.Context, path string) (*sql.DB, error) {
	uri := (&url.URL{Scheme: "file", Path: path}).String() + "?mode=ro&_busy_timeout=5000&_query_only=1"
	database, err := sql.Open("sqlite3", uri)
	if err != nil {
		return nil, err
	}
	database.SetMaxOpenConns(1)
	if err := database.PingContext(ctx); err != nil {
		_ = database.Close()
		return nil, err
	}
	return database, nil
}

func ensureVectorSchema(
	ctx context.Context,
	database *sql.DB,
	encoder embedding.Encoder,
	rebuild bool,
) error {
	var version int
	if err := database.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&version); err != nil {
		return err
	}
	var existingModel string
	_ = database.QueryRowContext(ctx,
		`SELECT value FROM vector_metadata WHERE key='model_id'`,
	).Scan(&existingModel)
	if rebuild || version != vectorIndexSchemaVersion || existingModel != encoder.ID() {
		for _, statement := range []string{
			`DROP TABLE IF EXISTS knowledge_vectors`,
			`DROP TABLE IF EXISTS feedstock_vectors`,
			`DROP TABLE IF EXISTS vector_documents`,
			`DROP TABLE IF EXISTS vector_metadata`,
		} {
			if _, err := database.ExecContext(ctx, statement); err != nil {
				return err
			}
		}
	}
	statements := []string{
		`CREATE TABLE IF NOT EXISTS vector_metadata(key TEXT PRIMARY KEY,value TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS vector_documents(
			vector_rowid INTEGER PRIMARY KEY AUTOINCREMENT,
			record_key TEXT NOT NULL UNIQUE,
			id TEXT NOT NULL,
			kind TEXT NOT NULL,
			source_mtime_ns INTEGER NOT NULL,
			source_size INTEGER NOT NULL,
			model_id TEXT NOT NULL
		)`,
		fmt.Sprintf(`CREATE VIRTUAL TABLE IF NOT EXISTS knowledge_vectors USING vec0(
			embedding float[%d] distance_metric=cosine
		)`, encoder.Dimension()),
		fmt.Sprintf(`CREATE VIRTUAL TABLE IF NOT EXISTS feedstock_vectors USING vec0(
			embedding float[%d] distance_metric=cosine
		)`, encoder.Dimension()),
		`INSERT INTO vector_metadata(key,value) VALUES('model_id',?)
		ON CONFLICT(key) DO UPDATE SET value=excluded.value`,
		fmt.Sprintf(`PRAGMA user_version=%d`, vectorIndexSchemaVersion),
	}
	for index, statement := range statements {
		var err error
		if index == len(statements)-2 {
			_, err = database.ExecContext(ctx, statement, encoder.ID())
		} else {
			_, err = database.ExecContext(ctx, statement)
		}
		if err != nil {
			return fmt.Errorf("initialize vector index: %w", err)
		}
	}
	return nil
}

func deleteVector(ctx context.Context, transaction *sql.Tx, rowID int64, recordKey string) error {
	var kind string
	if err := transaction.QueryRowContext(ctx,
		`SELECT kind FROM vector_documents WHERE record_key=?`, recordKey,
	).Scan(&kind); err != nil {
		return err
	}
	table, err := vectorTable(searchapp.Target(kind))
	if err != nil {
		return err
	}
	if _, err := transaction.ExecContext(ctx, `DELETE FROM `+table+` WHERE rowid=?`, rowID); err != nil {
		return err
	}
	_, err = transaction.ExecContext(ctx, `DELETE FROM vector_documents WHERE record_key=?`, recordKey)
	return err
}

func vectorTable(target searchapp.Target) (string, error) {
	switch target {
	case searchapp.TargetKnowledge:
		return "knowledge_vectors", nil
	case searchapp.TargetFeedstock:
		return "feedstock_vectors", nil
	default:
		return "", fmt.Errorf("unsupported vector target %q", target)
	}
}

func (gateway Gateway) eligibleIDs(
	ctx context.Context,
	options searchapp.Options,
) (map[string]struct{}, error) {
	database, err := gateway.openTextIndex(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = database.Close() }()
	where, args := filters(legacyOptions(options))
	rows, err := database.QueryContext(ctx, `SELECT d.id FROM documents d WHERE 1=1`+where, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	result := map[string]struct{}{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		result[id] = struct{}{}
	}
	return result, rows.Err()
}

func (gateway Gateway) vectorStatus(
	ctx context.Context,
	textIndex *sql.DB,
) (int, int, time.Time, error) {
	path := gateway.vectorPath()
	if _, err := os.Stat(path); err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return 0, 0, time.Time{}, err
		}
		var count int
		err = textIndex.QueryRowContext(ctx, `SELECT count(*) FROM documents WHERE
			(kind=? AND trim(claim)<>'') OR (kind=? AND trim(summary)<>'')`,
			string(searchapp.TargetKnowledge), string(searchapp.TargetFeedstock),
		).Scan(&count)
		return 0, count, time.Time{}, err
	}
	database, err := openVectorIndexReadOnly(ctx, path)
	if err != nil {
		return 0, 0, time.Time{}, err
	}
	defer func() { _ = database.Close() }()
	type vectorState struct {
		mtime, size int64
		model       string
	}
	indexed := map[string]vectorState{}
	rows, err := database.QueryContext(ctx,
		`SELECT record_key,source_mtime_ns,source_size,model_id FROM vector_documents`,
	)
	if err != nil {
		return 0, 0, time.Time{}, err
	}
	for rows.Next() {
		var key string
		var value vectorState
		if err := rows.Scan(&key, &value.mtime, &value.size, &value.model); err != nil {
			_ = rows.Close()
			return 0, 0, time.Time{}, err
		}
		indexed[key] = value
	}
	if err := rows.Close(); err != nil {
		return 0, 0, time.Time{}, err
	}
	current := map[string]struct{}{}
	unsynchronized := 0
	textRows, err := textIndex.QueryContext(ctx, `SELECT
		record_key,source_mtime_ns,source_size,kind,claim,summary FROM documents`)
	if err != nil {
		return 0, 0, time.Time{}, err
	}
	for textRows.Next() {
		var key, kind, claim, summary string
		var mtime, size int64
		if err := textRows.Scan(&key, &mtime, &size, &kind, &claim, &summary); err != nil {
			_ = textRows.Close()
			return 0, 0, time.Time{}, err
		}
		text := summary
		if kind == string(searchapp.TargetKnowledge) {
			text = claim
		}
		if strings.TrimSpace(text) == "" {
			continue
		}
		current[key] = struct{}{}
		value, exists := indexed[key]
		if !exists || value.mtime != mtime || value.size != size || value.model != gateway.Encoder.ID() {
			unsynchronized++
		}
	}
	if err := textRows.Close(); err != nil {
		return 0, 0, time.Time{}, err
	}
	for key := range indexed {
		if _, exists := current[key]; !exists {
			unsynchronized++
		}
	}
	var value string
	_ = database.QueryRowContext(ctx,
		`SELECT value FROM vector_metadata WHERE key='last_synchronized_at'`,
	).Scan(&value)
	lastSync, _ := time.Parse(time.RFC3339Nano, value)
	return len(indexed), unsynchronized, lastSync, nil
}

func (gateway Gateway) unindexedSourceFiles() (int, []diagnostic.Warning, error) {
	count := 0
	var warnings []diagnostic.Warning
	for _, directory := range []string{"feedstocks", "knowledge"} {
		files, foundWarnings, err := enumerateMarkdown(filepath.Join(gateway.Store.Root, directory))
		warnings = append(warnings, foundWarnings...)
		if err != nil {
			return count, warnings, err
		}
		count += len(files)
	}
	return count, warnings, nil
}

func (gateway Gateway) unsynchronized(database *sql.DB) (int, []diagnostic.Warning, error) {
	indexed := map[string][2]int64{}
	rows, err := database.Query(`SELECT path,source_mtime_ns,source_size FROM documents`)
	if err != nil {
		return 0, nil, err
	}
	for rows.Next() {
		var path string
		var mtime, size int64
		if err := rows.Scan(&path, &mtime, &size); err != nil {
			_ = rows.Close()
			return 0, nil, err
		}
		indexed[path] = [2]int64{mtime, size}
	}
	if err := rows.Close(); err != nil {
		return 0, nil, err
	}
	var files []fileReference
	var warnings []diagnostic.Warning
	for _, directory := range []string{"feedstocks", "knowledge"} {
		values, foundWarnings, err := enumerateMarkdown(filepath.Join(gateway.Store.Root, directory))
		warnings = append(warnings, foundWarnings...)
		if err != nil {
			return 0, warnings, err
		}
		files = append(files, values...)
	}
	current := make(map[string]struct{}, len(files))
	unsynchronized := 0
	for _, file := range files {
		current[file.Path] = struct{}{}
		value, exists := indexed[file.Path]
		if !exists || value[0] != file.ModTime || value[1] != file.Size {
			unsynchronized++
		}
	}
	for path := range indexed {
		if _, exists := current[path]; !exists {
			unsynchronized++
		}
	}
	return unsynchronized, warnings, nil
}

func legacyOptions(options searchapp.Options) SearchOptions {
	return SearchOptions{
		Target: Target(options.Target), Keywords: options.Keywords,
		Subject: options.Subject, Type: options.Type, Since: options.Since, Until: options.Until,
		IncludePending: options.IncludePending, Trigger: options.Trigger,
		Session: options.Session, Agent: options.Agent, Last: options.Last,
		Limit: options.Limit, MaxTokens: options.MaxTokens, Reindex: options.Reindex,
		IncludeRetired: options.IncludeRetired,
	}
}

func rankedRows(rows *sql.Rows) ([]searchapp.RankedID, error) {
	defer func() { _ = rows.Close() }()
	var result []searchapp.RankedID
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		result = append(result, searchapp.RankedID{ID: id, Rank: len(result) + 1})
	}
	return result, rows.Err()
}
