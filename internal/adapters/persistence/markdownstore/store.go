package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/gofrs/flock"
	"github.com/siro33950/knowbrew/internal/adapters/fsutil"
	"github.com/siro33950/knowbrew/internal/adapters/markdown/frontmatter"
	"github.com/siro33950/knowbrew/internal/application/diagnostic"
	"github.com/siro33950/knowbrew/internal/domain"
	"gopkg.in/yaml.v3"
)

type Store struct {
	Root string
}

var ErrFeedstockNotFound = errors.New("feedstock not found")

type masterFrontmatter struct {
	Definition string   `yaml:"definition,omitempty"`
	Example    string   `yaml:"example,omitempty"`
	Includes   []string `yaml:"includes,omitempty"`
	Excludes   []string `yaml:"excludes,omitempty"`
	Aliases    []string `yaml:"aliases,omitempty"`
	Documents  []string `yaml:"documents,omitempty"`
}

type legacyMasterFrontmatter struct {
	Name       string   `yaml:"name,omitempty"`
	Definition string   `yaml:"definition"`
	Example    string   `yaml:"example,omitempty"`
	Includes   []string `yaml:"includes,omitempty"`
	Excludes   []string `yaml:"excludes,omitempty"`
	Status     any      `yaml:"status,omitempty"`
	Aliases    []string `yaml:"aliases,omitempty"`
	Documents  []string `yaml:"documents,omitempty"`
	Created    any      `yaml:"created,omitempty"`
	Updated    any      `yaml:"updated,omitempty"`
}

type knowledgeFrontmatter struct {
	ID            string               `yaml:"id,omitempty"`
	Created       time.Time            `yaml:"created"`
	Updated       time.Time            `yaml:"updated"`
	EstablishedBy string               `yaml:"established_by,omitempty"`
	Type          domain.KnowledgeType `yaml:"type"`
	Subject       string               `yaml:"subject,omitempty"`
	Feedstocks    []string             `yaml:"feedstocks"`
	Approved      *bool                `yaml:"approved,omitempty"`
	Supersedes    []string             `yaml:"supersedes,omitempty"`
	SupersededBy  string               `yaml:"superseded_by,omitempty"`
	SupersededAt  *time.Time           `yaml:"superseded_at,omitempty"`
	InvalidatedAt *time.Time           `yaml:"invalidated_at,omitempty"`
	OrganizedAt   *time.Time           `yaml:"organized_at,omitempty"`
	// DeprecatedTrigger absorbs the retired trigger key from existing files;
	// the strict frontmatter decoder would otherwise reject them. The value
	// is discarded.
	DeprecatedTrigger string        `yaml:"trigger,omitempty"`
	Status            domain.Status `yaml:"status,omitempty"`
}

type readableFeedstockFrontmatter struct {
	Schema      int                    `yaml:"schema"`
	ID          string                 `yaml:"id"`
	TurnID      string                 `yaml:"turn_id"`
	Session     readableSessionRef     `yaml:"session"`
	Timestamp   time.Time              `yaml:"timestamp"`
	Agent       string                 `yaml:"agent"`
	CWD         string                 `yaml:"cwd,omitempty"`
	Repo        string                 `yaml:"repo,omitempty"`
	Branch      string                 `yaml:"branch,omitempty"`
	Types       []domain.KnowledgeType `yaml:"types"`
	Summary     string                 `yaml:"summary"`
	AnnotatedAt *time.Time             `yaml:"annotated_at,omitempty"`
	ExtractedAt *time.Time             `yaml:"extracted_at,omitempty"`
}

type readableSessionRef struct {
	ID         string `yaml:"id"`
	LegacyPath string `yaml:"path,omitempty"`
}

func (header readableFeedstockFrontmatter) domainFeedstock() domain.Feedstock {
	return domain.Feedstock{
		Schema: header.Schema, ID: header.ID, TurnID: header.TurnID,
		Session:   domain.SessionRef{ID: header.Session.ID},
		Timestamp: header.Timestamp, Agent: header.Agent, CWD: header.CWD,
		Repo: header.Repo, Branch: header.Branch, Types: header.Types,
		Summary: header.Summary, AnnotatedAt: header.AnnotatedAt, ExtractedAt: header.ExtractedAt,
	}
}

type writableKnowledgeFrontmatter struct {
	ID            string               `yaml:"id"`
	Created       time.Time            `yaml:"created"`
	Updated       time.Time            `yaml:"updated"`
	EstablishedBy string               `yaml:"established_by,omitempty"`
	Type          domain.KnowledgeType `yaml:"type"`
	Subject       string               `yaml:"subject,omitempty"`
	Feedstocks    []string             `yaml:"feedstocks"`
	Approved      bool                 `yaml:"approved"`
	Supersedes    []string             `yaml:"supersedes,omitempty"`
	SupersededBy  string               `yaml:"superseded_by,omitempty"`
	SupersededAt  *time.Time           `yaml:"superseded_at,omitempty"`
	InvalidatedAt *time.Time           `yaml:"invalidated_at,omitempty"`
	OrganizedAt   *time.Time           `yaml:"organized_at,omitempty"`
}

func New(root string) (*Store, error) {
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	return &Store{Root: filepath.Clean(absolute)}, nil
}

func (s *Store) EnsureLayout() error {
	for _, dir := range []string{
		"feedstocks",
		"knowledge",
		"documents",
		filepath.Join("masters", "subjects"),
		filepath.Join("masters", "templates"),
		filepath.Join("masters", "types"),
		filepath.Join("masters", "writing"),
		filepath.Join(".knowbrew", "state", "runs"),
		filepath.Join(".knowbrew", "state", "transactions"),
	} {
		path, err := fsutil.ResolveWithin(s.Root, dir)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(path, 0o755); err != nil {
			return fmt.Errorf("create %s: %w", path, err)
		}
	}
	return s.ensureDefaultTypes()
}

func (s *Store) ReadWritingGuide(name string) (string, bool, error) {
	if err := domain.ValidateIdentifier(name, "writing guide name"); err != nil {
		return "", false, err
	}
	path, err := fsutil.ResolveWithin(s.Root, "masters", "writing", name+".md")
	if err != nil {
		return "", false, err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("read writing guide %s: %w", path, err)
	}
	return string(data), true, nil
}

func (s *Store) WithLock(ctx context.Context, fn func() error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.EnsureLayout(); err != nil {
		return err
	}
	lockPath, err := fsutil.ResolveWithin(s.Root, ".knowbrew", "state", "knowbrew.lock")
	if err != nil {
		return err
	}
	fileLock := flock.New(lockPath)
	locked, err := fileLock.TryLockContext(ctx, 25*time.Millisecond)
	if err != nil {
		return fmt.Errorf("acquire store lock: %w", err)
	}
	if !locked {
		return errors.New("store lock wait ended without acquiring the lock")
	}
	defer func() { _ = fileLock.Unlock() }()
	if err := s.recoverTransactionsLocked(); err != nil {
		return err
	}
	return fn()
}

func (s *Store) FeedstockPath(feedstock domain.Feedstock) (string, error) {
	return fsutil.ResolveWithin(s.Root, "feedstocks", feedstock.Agent, feedstock.Timestamp.Format("2006"), feedstock.Timestamp.Format("01"), feedstock.ID+".md")
}

func (s *Store) WriteFeedstock(feedstock domain.Feedstock) error {
	types, err := s.NormalizeKnowledgeTypes(feedstock.Types)
	if err != nil {
		return fmt.Errorf("feedstock types: %w", err)
	}
	feedstock.Types = types
	if err := domain.ValidateFeedstock(feedstock); err != nil {
		return err
	}
	path, err := s.FeedstockPath(feedstock)
	if err != nil {
		return err
	}
	if _, err := os.Stat(path); err == nil {
		existing, readErr := s.ReadFeedstock(path)
		if readErr != nil {
			return readErr
		}
		if equalFeedstockExceptExtracted(existing, feedstock) {
			return nil
		}
		return fmt.Errorf("feedstock %s is immutable and already exists", feedstock.ID)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	data, err := encodeWithWikilinks(feedstock, "", "types")
	if err != nil {
		return err
	}
	return fsutil.AtomicWrite(path, data, 0o644)
}

func (s *Store) ReadFeedstock(path string) (domain.Feedstock, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return domain.Feedstock{}, err
	}
	var header readableFeedstockFrontmatter
	_, err = frontmatter.Decode(data, &header)
	if err != nil {
		return domain.Feedstock{}, fmt.Errorf("read feedstock %s: %w", path, err)
	}
	feedstock := header.domainFeedstock()
	feedstock.Types, err = s.NormalizeKnowledgeTypes(feedstock.Types)
	if err != nil {
		return domain.Feedstock{}, fmt.Errorf("validate feedstock %s: feedstock types: %w", path, err)
	}
	if err := domain.ValidateFeedstock(feedstock); err != nil {
		return domain.Feedstock{}, fmt.Errorf("validate feedstock %s: %w", path, err)
	}
	return feedstock, nil
}

func (s *Store) FindFeedstock(id string) (domain.Feedstock, string, error) {
	if err := domain.ValidateIdentifier(id, "feedstock ID"); err != nil {
		return domain.Feedstock{}, "", err
	}
	var foundPath string
	err := filepath.WalkDir(filepath.Join(s.Root, "feedstocks"), func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			if errors.Is(walkErr, os.ErrNotExist) {
				return nil
			}
			return walkErr
		}
		if !entry.IsDir() && entry.Name() == id+".md" {
			foundPath = path
			return fs.SkipAll
		}
		return nil
	})
	if err != nil {
		return domain.Feedstock{}, "", err
	}
	if foundPath == "" {
		return domain.Feedstock{}, "", fmt.Errorf("%w: %s", ErrFeedstockNotFound, id)
	}
	feedstock, err := s.ReadFeedstock(foundPath)
	return feedstock, foundPath, err
}

func (s *Store) ListFeedstocks() ([]domain.Feedstock, []diagnostic.Warning, error) {
	var feedstocks []domain.Feedstock
	var warnings []diagnostic.Warning
	base := filepath.Join(s.Root, "feedstocks")
	err := filepath.WalkDir(base, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			if errors.Is(walkErr, os.ErrNotExist) {
				return nil
			}
			warnings = append(warnings, diagnostic.FromError(path, walkErr))
			return nil
		}
		if entry.IsDir() || filepath.Ext(path) != ".md" {
			return nil
		}
		feedstock, err := s.ReadFeedstock(path)
		if err != nil {
			warnings = append(warnings, diagnostic.FromError(path, err))
			return nil
		}
		feedstocks = append(feedstocks, feedstock)
		return nil
	})
	return feedstocks, warnings, err
}

func (s *Store) WriteExtractedFeedstock(feedstock domain.Feedstock, when time.Time) error {
	current, path, err := s.FindFeedstock(feedstock.ID)
	if err != nil {
		return err
	}
	if current.AnnotatedAt == nil {
		return fmt.Errorf("feedstock %s is not annotated", feedstock.ID)
	}
	if err := current.ApplyExtractionProgress(when); err != nil {
		return err
	}
	data, err := encodeWithWikilinks(current, "", "types")
	if err != nil {
		return err
	}
	return fsutil.AtomicWrite(path, data, 0o644)
}

func (s *Store) DraftFeedstock(
	id string,
	summary string,
	types []domain.KnowledgeType,
	when time.Time,
) error {
	feedstock, path, err := s.FindFeedstock(id)
	if err != nil {
		return err
	}
	if feedstock.AnnotatedAt != nil {
		return fmt.Errorf("feedstock %s is already drawn", id)
	}
	types, err = s.NormalizeKnowledgeTypes(types)
	if err != nil {
		return fmt.Errorf("feedstock types: %w", err)
	}
	if err := feedstock.ApplyDraft(summary, types, when); err != nil {
		return err
	}
	data, err := encodeWithWikilinks(feedstock, "", "types")
	if err != nil {
		return err
	}
	return fsutil.AtomicWrite(path, data, 0o644)
}

func (s *Store) KnowledgePath(id string) (string, error) {
	if err := domain.ValidateKnowledgeID(id); err != nil {
		return "", err
	}
	return fsutil.ResolveWithin(s.Root, "knowledge", id+".md")
}

func (s *Store) WriteNewKnowledge(id string, knowledge domain.Knowledge, body string) error {
	if err := domain.ValidateKnowledgeID(id); err != nil {
		return err
	}
	types, err := s.NormalizeKnowledgeTypes([]domain.KnowledgeType{knowledge.Type})
	if err != nil {
		return fmt.Errorf("knowledge type: %w", err)
	}
	knowledge.Type = types[0]
	if strings.TrimSpace(knowledge.ID) == "" {
		knowledge.ID = id
	}
	if knowledge.ID != id {
		return errors.New("new knowledge ID must match its filename")
	}
	knowledge.Approved = false
	knowledge.Status = domain.StatusPending
	if knowledge.OrganizedAt == nil {
		knowledge.InvalidatedAt = nil
		knowledge.Supersedes = nil
		knowledge.SupersededBy = ""
		knowledge.SupersededAt = nil
	}
	knowledge.Subject = domain.MasterName(knowledge.Subject)
	knowledge.EstablishedBy = domain.MasterName(knowledge.EstablishedBy)
	knowledge.Feedstocks = normalizeFeedstockLinks(knowledge.Feedstocks)
	knowledge.Supersedes = normalizeKnowledgeLinks(knowledge.Supersedes)
	if slices.Contains(knowledge.Supersedes, id) {
		return errors.New("knowledge cannot supersede itself")
	}
	if err := domain.ValidateKnowledge(knowledge); err != nil {
		return err
	}
	if strings.TrimSpace(body) == "" {
		return errors.New("knowledge body is required")
	}
	for _, feedstock := range knowledge.Feedstocks {
		if _, _, err := s.FindFeedstock(feedstock); err != nil {
			return fmt.Errorf("invalid feedstock %s: %w", feedstock, err)
		}
	}
	path, err := s.KnowledgePath(id)
	if err != nil {
		return err
	}
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("knowledge %s already exists", id)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	data, err := encodeKnowledge(knowledge, body)
	if err != nil {
		return err
	}
	return fsutil.AtomicWrite(path, data, 0o644)
}

func (s *Store) ReadKnowledge(path string) (domain.Knowledge, string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return domain.Knowledge{}, "", err
	}
	var header knowledgeFrontmatter
	body, err := frontmatter.Decode(data, &header)
	if err != nil {
		return domain.Knowledge{}, "", fmt.Errorf("read knowledge %s: %w", path, err)
	}
	knowledge, err := knowledgeFromFrontmatter(header)
	if err != nil {
		return domain.Knowledge{}, "", fmt.Errorf("read knowledge %s: %w", path, err)
	}
	knowledge.Subject = domain.MasterName(knowledge.Subject)
	if knowledge.ID == "" {
		knowledge.ID = strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	}
	knowledge.Feedstocks = normalizeFeedstockLinks(knowledge.Feedstocks)
	knowledge.Supersedes = normalizeKnowledgeLinks(knowledge.Supersedes)
	knowledge.SupersededBy = domain.MasterName(knowledge.SupersededBy)
	types, err := s.NormalizeKnowledgeTypes([]domain.KnowledgeType{knowledge.Type})
	if err != nil {
		return domain.Knowledge{}, "", fmt.Errorf("validate knowledge %s: knowledge type: %w", path, err)
	}
	knowledge.Type = types[0]
	knowledge.Status = domain.EffectiveKnowledgeStatus(knowledge)
	if err := domain.ValidateKnowledge(knowledge); err != nil {
		return domain.Knowledge{}, "", fmt.Errorf("validate knowledge %s: %w", path, err)
	}
	return knowledge, body, nil
}

func (s *Store) ListKnowledge(includePending bool) ([]KnowledgeFile, []diagnostic.Warning, error) {
	all, warnings, err := s.ListAllKnowledge()
	if err != nil {
		return nil, warnings, err
	}
	var files []KnowledgeFile
	for _, file := range all {
		if file.Knowledge.OrganizedAt == nil {
			continue
		}
		status := file.Knowledge.Status
		if status == domain.StatusInvalidated ||
			status == domain.StatusSuperseded ||
			(!includePending && status != domain.StatusActive) {
			continue
		}
		files = append(files, file)
	}
	return files, warnings, nil
}

func (s *Store) ListAllKnowledge() ([]KnowledgeFile, []diagnostic.Warning, error) {
	var files []KnowledgeFile
	var warnings []diagnostic.Warning
	base := filepath.Join(s.Root, "knowledge")
	err := filepath.WalkDir(base, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			if errors.Is(walkErr, os.ErrNotExist) {
				return nil
			}
			warnings = append(warnings, diagnostic.FromError(path, walkErr))
			return nil
		}
		if entry.IsDir() || filepath.Ext(path) != ".md" {
			return nil
		}
		record, body, err := s.ReadKnowledge(path)
		if err != nil {
			warnings = append(warnings, diagnostic.FromError(path, err))
			return nil
		}
		files = append(files, KnowledgeFile{
			Path:      path,
			Knowledge: record,
			Body:      body,
		})
		return nil
	})
	return files, warnings, err
}

type KnowledgeFile struct {
	Path      string
	Knowledge domain.Knowledge
	Body      string
}

func (s *Store) FindKnowledge(id string) (KnowledgeFile, error) {
	if err := domain.ValidateIdentifier(id, "knowledge ID"); err != nil {
		return KnowledgeFile{}, err
	}
	files, _, err := s.ListAllKnowledge()
	if err != nil {
		return KnowledgeFile{}, err
	}
	for _, file := range files {
		if file.Knowledge.ID == id {
			return file, nil
		}
	}
	return KnowledgeFile{}, fmt.Errorf("knowledge %q was not found", id)
}

func (s *Store) AddKnowledgeFeedstocks(id string, feedstocks []string, when time.Time) error {
	return s.addKnowledgeFeedstocks(id, feedstocks, when, false)
}

// AddKnowledgeEvidence attaches provenance without reactivating or changing
// the semantic head of a retired knowledge version. It is used when backfilled
// evidence matches a historical version.
func (s *Store) AddKnowledgeEvidence(id string, feedstocks []string, when time.Time) error {
	return s.addKnowledgeFeedstocks(id, feedstocks, when, true)
}

func (s *Store) addKnowledgeFeedstocks(
	id string,
	feedstocks []string,
	when time.Time,
	allowRetired bool,
) error {
	path, err := s.KnowledgePath(id)
	if err != nil {
		return err
	}
	knowledge, body, err := s.ReadKnowledge(path)
	if err != nil {
		return err
	}
	if !allowRetired &&
		(knowledge.Status == domain.StatusInvalidated || knowledge.Status == domain.StatusSuperseded) {
		return fmt.Errorf("cannot add feedstocks to %s knowledge", knowledge.Status)
	}
	feedstocks = normalizeFeedstockLinks(feedstocks)
	for _, feedstock := range feedstocks {
		if _, _, err := s.FindFeedstock(feedstock); err != nil {
			return fmt.Errorf("invalid feedstock %s: %w", feedstock, err)
		}
	}
	knowledge.Feedstocks = domain.UniqueSorted(append(knowledge.Feedstocks, feedstocks...))
	knowledge.Updated = when
	data, err := encodeKnowledge(knowledge, body)
	if err != nil {
		return err
	}
	return fsutil.AtomicWrite(path, data, 0o644)
}

// KnowledgeEstablishedAt returns source-event time, not file mutation time.
// Existing records without established_by conservatively use their newest
// cited feedstock so that late historical imports cannot regress them.
func (s *Store) KnowledgeEstablishedAt(knowledge domain.Knowledge) (time.Time, error) {
	var latest time.Time
	for _, feedstockID := range normalizeFeedstockLinks(knowledge.Feedstocks) {
		feedstock, _, err := s.FindFeedstock(feedstockID)
		if err != nil {
			return time.Time{}, fmt.Errorf("read knowledge feedstock %s: %w", feedstockID, err)
		}
		if feedstock.Timestamp.After(latest) {
			latest = feedstock.Timestamp
		}
	}
	if latest.IsZero() {
		return time.Time{}, errors.New("knowledge has no source timestamp")
	}
	return latest, nil
}

func (s *Store) InvalidateKnowledge(id string, feedstocks []string, when time.Time) error {
	if len(feedstocks) == 0 {
		return errors.New("invalidation requires at least one feedstock")
	}
	path, err := s.KnowledgePath(id)
	if err != nil {
		return err
	}
	knowledge, body, err := s.ReadKnowledge(path)
	if err != nil {
		return err
	}
	if knowledge.OrganizedAt == nil {
		return errors.New("cannot invalidate unorganized knowledge")
	}
	if knowledge.Status == domain.StatusInvalidated {
		return nil
	}
	if knowledge.Status == domain.StatusSuperseded {
		return errors.New("cannot invalidate superseded knowledge")
	}
	feedstocks = normalizeFeedstockLinks(feedstocks)
	for _, feedstock := range feedstocks {
		if _, _, err := s.FindFeedstock(feedstock); err != nil {
			return fmt.Errorf("invalid feedstock %s: %w", feedstock, err)
		}
	}
	knowledge.Feedstocks = domain.UniqueSorted(append(knowledge.Feedstocks, feedstocks...))
	knowledge.InvalidatedAt = &when
	knowledge.Status = domain.StatusInvalidated
	knowledge.Updated = when
	data, err := encodeKnowledge(knowledge, body)
	if err != nil {
		return err
	}
	return fsutil.AtomicWrite(path, data, 0o644)
}

func (s *Store) EnsureMaster(kind string, entry domain.MasterEntry) (bool, error) {
	if kind != "subjects" && kind != "types" {
		return false, fmt.Errorf("unsupported master kind %q", kind)
	}
	entry.Name = domain.MasterName(entry.Name)
	entry.Aliases = domain.UniqueSorted(entry.Aliases)
	entry.Includes = normalizeScopeEntries(entry.Includes)
	entry.Excludes = normalizeScopeEntries(entry.Excludes)
	entry.Documents = domain.NormalizeMasterNames(entry.Documents)
	var validateErr error
	if kind == "types" {
		validateErr = domain.ValidateTypeMaster(entry)
	} else {
		validateErr = domain.ValidateMaster(entry)
	}
	if validateErr != nil {
		return false, validateErr
	}
	path, err := fsutil.ResolveWithin(s.Root, "masters", kind, entry.Name+".md")
	if err != nil {
		return false, err
	}
	if _, err := os.Stat(path); err == nil {
		if kind == "subjects" && len(entry.Aliases) > 0 {
			if _, err := s.AddSubjectAliases(entry.Name, entry.Aliases); err != nil {
				return false, err
			}
		}
		return false, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	header := masterFrontmatter{
		Definition: entry.Definition,
		Example:    entry.Example,
		Includes:   entry.Includes,
		Excludes:   entry.Excludes,
		Documents:  entry.Documents,
	}
	if kind == "subjects" {
		header.Aliases = entry.Aliases
	}
	data, err := encodeWithWikilinks(header, "", "documents")
	if err != nil {
		return false, err
	}
	return true, fsutil.AtomicWrite(path, data, 0o644)
}

// AddSubjectAliases enriches an existing subject master without changing its
// filename identity, definition, example, or body. The caller is responsible
// for holding the store lock when coordinating this with other writes.
func (s *Store) AddSubjectAliases(name string, aliases []string) (bool, error) {
	name = domain.MasterName(name)
	path, err := fsutil.ResolveWithin(s.Root, "masters", "subjects", name+".md")
	if err != nil {
		return false, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	var header legacyMasterFrontmatter
	body, err := frontmatter.Decode(data, &header)
	if err != nil {
		return false, fmt.Errorf("read subject master %s: %w", path, err)
	}
	merged := domain.UniqueSorted(append(header.Aliases, aliases...))
	if slices.Equal(merged, domain.UniqueSorted(header.Aliases)) {
		return false, nil
	}
	entry := domain.MasterEntry{
		Name:       name,
		Definition: header.Definition,
		Example:    header.Example,
		Includes:   normalizeScopeEntries(header.Includes),
		Excludes:   normalizeScopeEntries(header.Excludes),
		Aliases:    merged,
		Documents:  domain.NormalizeMasterNames(header.Documents),
	}
	if err := domain.ValidateMaster(entry); err != nil {
		return false, err
	}
	encoded, err := encodeWithWikilinks(masterFrontmatter{
		Definition: entry.Definition,
		Example:    entry.Example,
		Includes:   entry.Includes,
		Excludes:   entry.Excludes,
		Aliases:    entry.Aliases,
		Documents:  entry.Documents,
	}, body, "documents")
	if err != nil {
		return false, err
	}
	if err := fsutil.AtomicWrite(path, encoded, 0o644); err != nil {
		return false, err
	}
	return true, nil
}

func (s *Store) LoadMasters(kind string) ([]domain.MasterEntry, []diagnostic.Warning, error) {
	if kind != "subjects" && kind != "types" {
		return nil, nil, fmt.Errorf("unsupported master kind %q", kind)
	}
	base := filepath.Join(s.Root, "masters", kind)
	var entries []domain.MasterEntry
	var warnings []diagnostic.Warning
	err := filepath.WalkDir(base, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			if errors.Is(walkErr, os.ErrNotExist) {
				return nil
			}
			warnings = append(warnings, diagnostic.FromError(path, walkErr))
			return nil
		}
		if entry.IsDir() || filepath.Ext(path) != ".md" {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			warnings = append(warnings, diagnostic.FromError(path, err))
			return nil
		}
		var header legacyMasterFrontmatter
		if _, err := frontmatter.Decode(data, &header); err != nil {
			warnings = append(warnings, diagnostic.FromError(path, err))
			return nil
		}
		master := domain.MasterEntry{
			Name:       strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name())),
			Definition: header.Definition,
			Example:    header.Example,
			Includes:   normalizeScopeEntries(header.Includes),
			Excludes:   normalizeScopeEntries(header.Excludes),
			Documents:  domain.NormalizeMasterNames(header.Documents),
		}
		if kind == "subjects" {
			master.Aliases = domain.UniqueSorted(header.Aliases)
		}
		var validateErr error
		if kind == "types" {
			validateErr = domain.ValidateTypeMaster(master)
		} else {
			validateErr = domain.ValidateMaster(master)
		}
		if validateErr != nil {
			warnings = append(warnings, diagnostic.FromError(path, validateErr))
			return nil
		}
		entries = append(entries, master)
		return nil
	})
	return entries, warnings, err
}

func (s *Store) KnowledgeTypes() ([]domain.MasterEntry, error) {
	entries, warnings, err := s.LoadMasters("types")
	if err != nil {
		return nil, err
	}
	if len(entries) == 0 && len(warnings) == 0 {
		if err := s.EnsureLayout(); err != nil {
			return nil, err
		}
		entries, warnings, err = s.LoadMasters("types")
		if err != nil {
			return nil, err
		}
	}
	if len(warnings) > 0 {
		return nil, fmt.Errorf("invalid type master: %s", warnings[0].Message)
	}
	if len(entries) == 0 {
		return nil, errors.New("masters/types does not contain any valid type masters")
	}
	return entries, nil
}

func (s *Store) NormalizeKnowledgeTypes(values []domain.KnowledgeType) ([]domain.KnowledgeType, error) {
	unlinked := make([]domain.KnowledgeType, len(values))
	for index, value := range values {
		unlinked[index] = domain.KnowledgeType(domain.MasterName(string(value)))
	}
	normalized, err := domain.NormalizeKnowledgeTypes(unlinked)
	if err != nil {
		return nil, err
	}
	if len(normalized) == 0 {
		return normalized, nil
	}
	entries, err := s.KnowledgeTypes()
	if err != nil {
		return nil, err
	}
	known := make(map[domain.KnowledgeType]struct{}, len(entries))
	for _, entry := range entries {
		known[domain.KnowledgeType(entry.Name)] = struct{}{}
	}
	for _, value := range normalized {
		if _, exists := known[value]; !exists {
			return nil, fmt.Errorf("knowledge type %q is not defined in masters/types", value)
		}
	}
	return normalized, nil
}

func (s *Store) ValidateKnowledgeType(value domain.KnowledgeType) error {
	_, err := s.NormalizeKnowledgeTypes([]domain.KnowledgeType{value})
	return err
}

func encodeWithWikilinks(
	header any,
	body string,
	fields ...string,
) ([]byte, error) {
	var document yaml.Node
	if err := document.Encode(header); err != nil {
		return nil, fmt.Errorf("encode linked frontmatter: %w", err)
	}
	root := &document
	if document.Kind == yaml.DocumentNode && len(document.Content) == 1 {
		root = document.Content[0]
	}
	if root.Kind != yaml.MappingNode {
		return nil, errors.New("linked frontmatter header must encode as a mapping")
	}
	fieldSet := make(map[string]struct{}, len(fields))
	for _, field := range fields {
		fieldSet[field] = struct{}{}
	}
	for index := 0; index+1 < len(root.Content); index += 2 {
		if _, linked := fieldSet[root.Content[index].Value]; !linked {
			continue
		}
		linkWikilinkNode(root.Content[index+1])
	}
	return frontmatter.Encode(&document, body)
}

func knowledgeFromFrontmatter(header knowledgeFrontmatter) (domain.Knowledge, error) {
	approved := false
	if header.Approved != nil {
		approved = *header.Approved
	} else {
		switch header.Status {
		case "", domain.StatusPending:
		case domain.StatusActive:
			approved = true
		case domain.StatusInvalidated:
			if header.InvalidatedAt == nil {
				return domain.Knowledge{}, errors.New("legacy invalidated knowledge requires invalidated_at")
			}
		default:
			return domain.Knowledge{}, fmt.Errorf("invalid legacy status %q", header.Status)
		}
	}
	knowledge := domain.Knowledge{
		ID:      header.ID,
		Created: header.Created, Updated: header.Updated,
		EstablishedBy: domain.MasterName(header.EstablishedBy), Type: header.Type,
		Subject: header.Subject, Feedstocks: header.Feedstocks,
		Approved: approved, Supersedes: header.Supersedes,
		SupersededBy: header.SupersededBy, SupersededAt: header.SupersededAt,
		InvalidatedAt: header.InvalidatedAt, OrganizedAt: header.OrganizedAt,
	}
	knowledge.Status = domain.EffectiveKnowledgeStatus(knowledge)
	return knowledge, nil
}

func encodeKnowledge(knowledge domain.Knowledge, body string) ([]byte, error) {
	header := writableKnowledgeFrontmatter{
		ID:      knowledge.ID,
		Created: knowledge.Created, Updated: knowledge.Updated,
		EstablishedBy: knowledge.EstablishedBy, Type: knowledge.Type,
		Subject: knowledge.Subject, Feedstocks: knowledge.Feedstocks,
		Approved:   knowledge.Approved,
		Supersedes: knowledge.Supersedes, SupersededBy: knowledge.SupersededBy,
		SupersededAt: knowledge.SupersededAt, InvalidatedAt: knowledge.InvalidatedAt,
		OrganizedAt: knowledge.OrganizedAt,
	}
	return encodeWithWikilinks(
		header,
		body,
		"established_by",
		"type",
		"subject",
		"feedstocks",
		"supersedes",
		"superseded_by",
	)
}

func normalizeFeedstockLinks(values []string) []string {
	normalized := make([]string, 0, len(values))
	for _, value := range values {
		normalized = append(normalized, domain.MasterName(value))
	}
	return domain.UniqueSorted(normalized)
}

func normalizeKnowledgeLinks(values []string) []string {
	normalized := make([]string, 0, len(values))
	for _, value := range values {
		normalized = append(normalized, domain.MasterName(value))
	}
	return domain.UniqueSorted(normalized)
}

func normalizeScopeEntries(values []string) []string {
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func linkWikilinkNode(node *yaml.Node) {
	switch node.Kind {
	case yaml.ScalarNode:
		name := domain.MasterName(node.Value)
		if name == "" {
			return
		}
		node.Value = "[[" + name + "]]"
		node.Style = yaml.DoubleQuotedStyle
	case yaml.SequenceNode:
		for _, child := range node.Content {
			linkWikilinkNode(child)
		}
	}
}

func equalFeedstockExceptExtracted(left, right domain.Feedstock) bool {
	left.ExtractedAt = nil
	right.ExtractedAt = nil
	leftJSON, _ := json.Marshal(left)
	rightJSON, _ := json.Marshal(right)
	return string(leftJSON) == string(rightJSON)
}
