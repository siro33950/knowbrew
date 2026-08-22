package source

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/siro33950/knowbrew/internal/adapters/source/parser"
	"github.com/siro33950/knowbrew/internal/application/diagnostic"
	applicationsource "github.com/siro33950/knowbrew/internal/application/source"
	"github.com/siro33950/knowbrew/internal/domain"
	_ "modernc.org/sqlite"
)

const (
	sourceCacheSchemaVersion = 4
	boundaryFingerprintBytes = 64 << 10
)

type sourceCache struct {
	path string
	once sync.Once
	db   *sql.DB
	err  error
}

type sourceCacheEntry struct {
	Path            string
	Agent           string
	Parser          string
	FirstRecordHash string
	BoundaryHash    string
	Size            int64
	ModifiedNS      int64
	Checkpoint      parser.Checkpoint
	Candidates      []domain.FeedstockCandidate
}

type storedCandidate struct {
	ID                   string                   `json:"id"`
	TurnID               string                   `json:"turn_id"`
	Session              domain.SessionRef        `json:"session"`
	Timestamp            time.Time                `json:"timestamp"`
	Agent                string                   `json:"agent"`
	CWD                  string                   `json:"cwd,omitempty"`
	Repo                 string                   `json:"repo,omitempty"`
	Branch               string                   `json:"branch,omitempty"`
	Dialogue             []domain.DialogueMessage `json:"dialogue"`
	SourceSequence       int64                    `json:"source_sequence"`
	SourceOwnerSessionID string                   `json:"source_owner_session_id"`
}

func newSourceCache(root string) *sourceCache {
	if root == "" {
		return nil
	}
	return &sourceCache{path: filepath.Join(root, ".knowbrew", "state", "source-index.sqlite")}
}

func (cache *sourceCache) parse(
	file applicationsource.File,
	logParser parser.Parser,
) ([]domain.FeedstockCandidate, []diagnostic.Warning, error) {
	info, err := os.Stat(file.Path)
	if err != nil {
		return nil, nil, err
	}
	database, err := cache.database()
	if err != nil {
		return nil, nil, err
	}
	entry, found, err := cache.loadByPath(database, file.Path)
	if err != nil {
		return nil, nil, err
	}
	if found && entry.Agent == file.Agent && entry.Parser == file.Parser &&
		entry.Size == info.Size() && entry.ModifiedNS == info.ModTime().UnixNano() {
		return entry.Candidates, nil, nil
	}
	firstHash, err := parser.SourceFingerprint(file.Path)
	if err != nil {
		return nil, nil, err
	}
	relocated := false
	if !found {
		entry, found, err = cache.relocateMissingEntry(database, file, firstHash)
		if err != nil {
			return nil, nil, err
		}
		relocated = found
	}
	if relocated && entry.Agent == file.Agent && entry.Parser == file.Parser &&
		entry.FirstRecordHash == firstHash && entry.Size == info.Size() {
		boundaryHash, hashErr := fingerprintRange(
			file.Path,
			max(int64(0), entry.Size-boundaryFingerprintBytes),
			entry.Size,
		)
		if hashErr != nil {
			return nil, nil, hashErr
		}
		if boundaryHash == entry.BoundaryHash {
			entry.ModifiedNS = info.ModTime().UnixNano()
			if err := cache.save(database, entry); err != nil {
				return nil, nil, err
			}
			return entry.Candidates, nil, nil
		}
	}
	canResume := found && entry.Agent == file.Agent && entry.Parser == file.Parser &&
		entry.FirstRecordHash == firstHash && info.Size() > entry.Size &&
		entry.Checkpoint.Offset <= entry.Size
	if canResume {
		boundaryHash, hashErr := fingerprintRange(
			file.Path,
			max(int64(0), entry.Size-boundaryFingerprintBytes),
			entry.Size,
		)
		if hashErr != nil {
			return nil, nil, hashErr
		}
		canResume = boundaryHash == entry.BoundaryHash
	}
	var checkpoint *parser.Checkpoint
	candidates := []domain.FeedstockCandidate(nil)
	if canResume {
		checkpoint = &entry.Checkpoint
		candidates = append(candidates, entry.Candidates...)
	}
	result, warnings, err := logParser.ParseIncremental(file.Path, checkpoint)
	if err != nil {
		return nil, warnings, err
	}
	candidates = append(candidates, result.Candidates...)
	cachedSize := result.Checkpoint.SnapshotSize
	if cachedSize == 0 {
		cachedSize = info.Size()
	}
	boundaryHash, err := fingerprintRange(
		file.Path,
		max(int64(0), cachedSize-boundaryFingerprintBytes),
		cachedSize,
	)
	if err != nil {
		return nil, warnings, err
	}
	modifiedNS := int64(0)
	if currentInfo, statErr := os.Stat(file.Path); statErr != nil {
		return nil, warnings, statErr
	} else if currentInfo.Size() == cachedSize {
		modifiedNS = currentInfo.ModTime().UnixNano()
	}
	updated := sourceCacheEntry{
		Path: file.Path, Agent: file.Agent, Parser: file.Parser,
		FirstRecordHash: firstHash, BoundaryHash: boundaryHash,
		Size: cachedSize, ModifiedNS: modifiedNS,
		Checkpoint: result.Checkpoint, Candidates: candidates,
	}
	if err := cache.save(database, updated); err != nil {
		return nil, warnings, err
	}
	return candidates, warnings, nil
}

func (cache *sourceCache) database() (*sql.DB, error) {
	cache.once.Do(func() {
		cache.err = os.MkdirAll(filepath.Dir(cache.path), 0o700)
		if cache.err != nil {
			return
		}
		cache.db, cache.err = sql.Open("sqlite", cache.path)
		if cache.err != nil {
			return
		}
		cache.db.SetMaxOpenConns(1)
		ctx := context.Background()
		for _, statement := range []string{
			`PRAGMA busy_timeout = 30000`,
			`PRAGMA journal_mode = WAL`,
		} {
			if _, cache.err = cache.db.ExecContext(ctx, statement); cache.err != nil {
				return
			}
		}
		var version int
		if cache.err = cache.db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&version); cache.err != nil {
			return
		}
		if version != sourceCacheSchemaVersion {
			if _, cache.err = cache.db.ExecContext(ctx, `DROP TABLE IF EXISTS source_cache`); cache.err != nil {
				return
			}
		}
		_, cache.err = cache.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS source_cache (
			path TEXT PRIMARY KEY,
			agent TEXT NOT NULL,
			parser TEXT NOT NULL,
			first_record_hash TEXT NOT NULL,
			boundary_hash TEXT NOT NULL,
			size INTEGER NOT NULL,
			modified_ns INTEGER NOT NULL,
			checkpoint BLOB NOT NULL,
			candidates BLOB NOT NULL
		)`)
		if cache.err != nil {
			return
		}
		_, cache.err = cache.db.ExecContext(ctx, fmt.Sprintf(
			`PRAGMA user_version = %d`, sourceCacheSchemaVersion,
		))
	})
	if cache.err != nil {
		return nil, fmt.Errorf("open source index %s: %w", cache.path, cache.err)
	}
	return cache.db, nil
}

func (cache *sourceCache) loadByPath(
	database *sql.DB,
	path string,
) (sourceCacheEntry, bool, error) {
	row := database.QueryRow(`SELECT path,agent,parser,first_record_hash,boundary_hash,
		size,modified_ns,checkpoint,candidates FROM source_cache WHERE path = ?`, path)
	entry, err := scanSourceCacheEntry(row)
	if errors.Is(err, sql.ErrNoRows) {
		return sourceCacheEntry{}, false, nil
	}
	if err != nil {
		return sourceCacheEntry{}, false, fmt.Errorf("read source cache %s: %w", path, err)
	}
	return entry, true, nil
}

func (cache *sourceCache) relocateMissingEntry(
	database *sql.DB,
	file applicationsource.File,
	firstHash string,
) (sourceCacheEntry, bool, error) {
	rows, err := database.Query(`SELECT path,agent,parser,first_record_hash,boundary_hash,
		size,modified_ns,checkpoint,candidates FROM source_cache
		WHERE agent = ? AND parser = ? AND first_record_hash = ? AND path <> ?`,
		file.Agent, file.Parser, firstHash, file.Path)
	if err != nil {
		return sourceCacheEntry{}, false, err
	}
	defer func() { _ = rows.Close() }()
	var missing []sourceCacheEntry
	for rows.Next() {
		entry, err := scanSourceCacheEntry(rows)
		if err != nil {
			return sourceCacheEntry{}, false, err
		}
		if _, err := os.Stat(entry.Path); errors.Is(err, os.ErrNotExist) {
			missing = append(missing, entry)
		}
	}
	if err := rows.Err(); err != nil {
		return sourceCacheEntry{}, false, err
	}
	if err := rows.Close(); err != nil {
		return sourceCacheEntry{}, false, err
	}
	if len(missing) != 1 {
		return sourceCacheEntry{}, false, nil
	}
	entry := missing[0]
	if _, err := database.Exec(`UPDATE source_cache SET path = ? WHERE path = ?`, file.Path, entry.Path); err != nil {
		return sourceCacheEntry{}, false, err
	}
	entry.Path = file.Path
	return entry, true, nil
}

type rowScanner interface {
	Scan(...any) error
}

func scanSourceCacheEntry(row rowScanner) (sourceCacheEntry, error) {
	var entry sourceCacheEntry
	var checkpointData, candidatesData []byte
	err := row.Scan(
		&entry.Path, &entry.Agent, &entry.Parser,
		&entry.FirstRecordHash, &entry.BoundaryHash,
		&entry.Size, &entry.ModifiedNS, &checkpointData, &candidatesData,
	)
	if err != nil {
		return sourceCacheEntry{}, err
	}
	if err := json.Unmarshal(checkpointData, &entry.Checkpoint); err != nil {
		return sourceCacheEntry{}, err
	}
	entry.Candidates, err = decodeStoredCandidates(candidatesData)
	return entry, err
}

func (cache *sourceCache) save(database *sql.DB, entry sourceCacheEntry) error {
	checkpointData, err := json.Marshal(entry.Checkpoint)
	if err != nil {
		return err
	}
	candidatesData, err := encodeStoredCandidates(entry.Candidates)
	if err != nil {
		return err
	}
	_, err = database.Exec(`INSERT INTO source_cache (
		path,agent,parser,first_record_hash,boundary_hash,size,modified_ns,checkpoint,candidates
	) VALUES (?,?,?,?,?,?,?,?,?) ON CONFLICT(path) DO UPDATE SET
		agent=excluded.agent,parser=excluded.parser,first_record_hash=excluded.first_record_hash,
		boundary_hash=excluded.boundary_hash,size=excluded.size,modified_ns=excluded.modified_ns,
		checkpoint=excluded.checkpoint,candidates=excluded.candidates`,
		entry.Path, entry.Agent, entry.Parser, entry.FirstRecordHash, entry.BoundaryHash,
		entry.Size, entry.ModifiedNS, checkpointData, candidatesData,
	)
	if err != nil {
		return fmt.Errorf("write source cache %s: %w", entry.Path, err)
	}
	return nil
}

func encodeStoredCandidates(candidates []domain.FeedstockCandidate) ([]byte, error) {
	stored := make([]storedCandidate, len(candidates))
	for index, candidate := range candidates {
		stored[index] = storedCandidate{
			ID: candidate.ID, TurnID: candidate.TurnID, Session: candidate.Session,
			Timestamp: candidate.Timestamp, Agent: candidate.Agent,
			CWD: candidate.CWD, Repo: candidate.Repo, Branch: candidate.Branch,
			Dialogue: candidate.Dialogue, SourceSequence: candidate.SourceSequence,
			SourceOwnerSessionID: candidate.SourceOwnerSessionID,
		}
	}
	return json.Marshal(stored)
}

func decodeStoredCandidates(data []byte) ([]domain.FeedstockCandidate, error) {
	var stored []storedCandidate
	if err := json.Unmarshal(data, &stored); err != nil {
		return nil, err
	}
	candidates := make([]domain.FeedstockCandidate, len(stored))
	for index, candidate := range stored {
		candidates[index] = domain.FeedstockCandidate{
			ID: candidate.ID, TurnID: candidate.TurnID, Session: candidate.Session,
			Timestamp: candidate.Timestamp, Agent: candidate.Agent,
			CWD: candidate.CWD, Repo: candidate.Repo, Branch: candidate.Branch,
			Dialogue: candidate.Dialogue, SourceSequence: candidate.SourceSequence,
			SourceOwnerSessionID: candidate.SourceOwnerSessionID,
		}
	}
	return candidates, nil
}

func fingerprintRange(path string, start, end int64) (string, error) {
	if start < 0 || end < start {
		return "", fmt.Errorf("invalid fingerprint range %d:%d", start, end)
	}
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = file.Close() }()
	digest := sha256.New()
	if _, err := io.Copy(digest, io.NewSectionReader(file, start, end-start)); err != nil {
		return "", err
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}
