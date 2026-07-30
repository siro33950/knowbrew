package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gofrs/flock"
	"github.com/siro33950/knowbrew/internal/diagnostic"
	"github.com/siro33950/knowbrew/internal/domain"
	"github.com/siro33950/knowbrew/internal/frontmatter"
	"github.com/siro33950/knowbrew/internal/fsutil"
)

type Store struct {
	Root string
}

var ErrFeedstockNotFound = errors.New("feedstock not found")

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
		filepath.Join("masters", "topics"),
		filepath.Join("masters", "subjects"),
		filepath.Join(".state", "pending"),
		filepath.Join(".state", "runs"),
	} {
		path, err := fsutil.ResolveWithin(s.Root, dir)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(path, 0o755); err != nil {
			return fmt.Errorf("create %s: %w", path, err)
		}
	}
	return nil
}

func (s *Store) WithLock(ctx context.Context, fn func() error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.EnsureLayout(); err != nil {
		return err
	}
	lockPath, err := fsutil.ResolveWithin(s.Root, ".state", "knowbrew.lock")
	if err != nil {
		return err
	}
	fileLock := flock.New(lockPath)
	locked, err := fileLock.TryLock()
	if err != nil {
		return fmt.Errorf("acquire store lock: %w", err)
	}
	if !locked {
		return errors.New("another knowbrew process holds the store lock")
	}
	defer fileLock.Unlock()
	return fn()
}

func (s *Store) FeedstockPath(feedstock domain.Feedstock) (string, error) {
	return fsutil.ResolveWithin(s.Root, "feedstocks", feedstock.Agent, feedstock.Timestamp.Format("2006"), feedstock.Timestamp.Format("01"), feedstock.ID+".md")
}

func (s *Store) WriteFeedstock(feedstock domain.Feedstock) error {
	feedstock.Topics = domain.UniqueSorted(feedstock.Topics)
	feedstock.Subjects = domain.UniqueSorted(feedstock.Subjects)
	feedstock.SpeechActs = domain.UniqueSorted(feedstock.SpeechActs)
	feedstock.FilesChanged = domain.UniqueSorted(feedstock.FilesChanged)
	feedstock.Errors = domain.UniqueSorted(feedstock.Errors)
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
		if equalFeedstockExceptBrewed(existing, feedstock) {
			return nil
		}
		return fmt.Errorf("feedstock %s is immutable and already exists", feedstock.ID)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	data, err := frontmatter.Encode(feedstock, "")
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
	var feedstock domain.Feedstock
	if _, err := frontmatter.Decode(data, &feedstock); err != nil {
		return domain.Feedstock{}, fmt.Errorf("read feedstock %s: %w", path, err)
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

func (s *Store) MarkBrewed(id string, when time.Time) error {
	feedstock, path, err := s.FindFeedstock(id)
	if err != nil {
		return err
	}
	if feedstock.BrewedAt != nil {
		return nil
	}
	feedstock.BrewedAt = &when
	data, err := frontmatter.Encode(feedstock, "")
	if err != nil {
		return err
	}
	return fsutil.AtomicWrite(path, data, 0o644)
}

func (s *Store) KnowledgePath(slug string) (string, error) {
	if err := domain.ValidateSlug(slug); err != nil {
		return "", err
	}
	return fsutil.ResolveWithin(s.Root, "knowledge", slug+".md")
}

func (s *Store) WriteNewKnowledge(slug string, knowledge domain.Knowledge, body string) error {
	if err := domain.ValidateSlug(slug); err != nil {
		return err
	}
	knowledge.Status = domain.StatusPending
	knowledge.InvalidatedAt = nil
	knowledge.Topics = domain.UniqueSorted(knowledge.Topics)
	knowledge.Sources = domain.UniqueSorted(knowledge.Sources)
	if err := domain.ValidateKnowledge(knowledge); err != nil {
		return err
	}
	if strings.TrimSpace(body) == "" {
		return errors.New("knowledge body is required")
	}
	for _, source := range knowledge.Sources {
		if _, _, err := s.FindFeedstock(source); err != nil {
			return fmt.Errorf("invalid source %s: %w", source, err)
		}
	}
	path, err := s.KnowledgePath(slug)
	if err != nil {
		return err
	}
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("knowledge %s already exists", slug)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	data, err := frontmatter.Encode(knowledge, body)
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
	var knowledge domain.Knowledge
	body, err := frontmatter.Decode(data, &knowledge)
	if err != nil {
		return domain.Knowledge{}, "", fmt.Errorf("read knowledge %s: %w", path, err)
	}
	if err := domain.ValidateKnowledge(knowledge); err != nil {
		return domain.Knowledge{}, "", fmt.Errorf("validate knowledge %s: %w", path, err)
	}
	return knowledge, body, nil
}

func (s *Store) ListKnowledge(includePending bool) ([]KnowledgeFile, []diagnostic.Warning, error) {
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
		if record.Status == domain.StatusInvalidated || (!includePending && record.Status != domain.StatusActive) {
			return nil
		}
		files = append(files, KnowledgeFile{
			Slug:      strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name())),
			Path:      path,
			Knowledge: record,
			Body:      body,
		})
		return nil
	})
	return files, warnings, err
}

type KnowledgeFile struct {
	Slug      string
	Path      string
	Knowledge domain.Knowledge
	Body      string
}

func (s *Store) AddKnowledgeSources(slug string, sources []string, when time.Time) error {
	path, err := s.KnowledgePath(slug)
	if err != nil {
		return err
	}
	knowledge, body, err := s.ReadKnowledge(path)
	if err != nil {
		return err
	}
	if knowledge.Status == domain.StatusInvalidated {
		return errors.New("cannot add sources to an invalidated knowledge")
	}
	for _, source := range sources {
		if _, _, err := s.FindFeedstock(source); err != nil {
			return fmt.Errorf("invalid source %s: %w", source, err)
		}
	}
	knowledge.Sources = domain.UniqueSorted(append(knowledge.Sources, sources...))
	knowledge.Updated = when
	data, err := frontmatter.Encode(knowledge, body)
	if err != nil {
		return err
	}
	return fsutil.AtomicWrite(path, data, 0o644)
}

func (s *Store) InvalidateKnowledge(slug string, sources []string, when time.Time) error {
	if len(sources) == 0 {
		return errors.New("invalidation requires at least one source")
	}
	path, err := s.KnowledgePath(slug)
	if err != nil {
		return err
	}
	knowledge, body, err := s.ReadKnowledge(path)
	if err != nil {
		return err
	}
	if knowledge.Status == domain.StatusInvalidated {
		return nil
	}
	for _, source := range sources {
		if _, _, err := s.FindFeedstock(source); err != nil {
			return fmt.Errorf("invalid source %s: %w", source, err)
		}
	}
	knowledge.Sources = domain.UniqueSorted(append(knowledge.Sources, sources...))
	knowledge.Status = domain.StatusInvalidated
	knowledge.InvalidatedAt = &when
	knowledge.Updated = when
	data, err := frontmatter.Encode(knowledge, body)
	if err != nil {
		return err
	}
	return fsutil.AtomicWrite(path, data, 0o644)
}

func (s *Store) EnsureMaster(kind string, entry domain.MasterEntry) (bool, error) {
	if kind != "topics" && kind != "subjects" {
		return false, fmt.Errorf("unsupported master kind %q", kind)
	}
	if err := domain.ValidateMaster(entry); err != nil {
		return false, err
	}
	path, err := fsutil.ResolveWithin(s.Root, "masters", kind, entry.Name+".md")
	if err != nil {
		return false, err
	}
	if _, err := os.Stat(path); err == nil {
		return false, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	entry.Status = domain.StatusPending
	data, err := frontmatter.Encode(entry, "")
	if err != nil {
		return false, err
	}
	return true, fsutil.AtomicWrite(path, data, 0o644)
}

func (s *Store) LoadMasters(kind string) ([]domain.MasterEntry, []diagnostic.Warning, error) {
	if kind != "topics" && kind != "subjects" {
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
		var master domain.MasterEntry
		if _, err := frontmatter.Decode(data, &master); err != nil {
			warnings = append(warnings, diagnostic.FromError(path, err))
			return nil
		}
		if err := domain.ValidateMaster(master); err != nil {
			warnings = append(warnings, diagnostic.FromError(path, err))
			return nil
		}
		entries = append(entries, master)
		return nil
	})
	return entries, warnings, err
}

func (s *Store) PendingPath(id string) (string, error) {
	if err := domain.ValidateIdentifier(id, "feedstock ID"); err != nil {
		return "", err
	}
	return fsutil.ResolveWithin(s.Root, ".state", "pending", id+".json")
}

func (s *Store) WriteCandidate(candidate domain.FeedstockCandidate) error {
	path, err := s.PendingPath(candidate.ID)
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(candidate, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return fsutil.AtomicWrite(path, data, 0o600)
}

func (s *Store) ReadCandidate(id string) (domain.FeedstockCandidate, error) {
	path, err := s.PendingPath(id)
	if err != nil {
		return domain.FeedstockCandidate{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return domain.FeedstockCandidate{}, err
	}
	var candidate domain.FeedstockCandidate
	if err := json.Unmarshal(data, &candidate); err != nil {
		return domain.FeedstockCandidate{}, err
	}
	return candidate, nil
}

func (s *Store) RemoveCandidate(id string) error {
	path, err := s.PendingPath(id)
	if err != nil {
		return err
	}
	err = os.Remove(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func equalFeedstockExceptBrewed(left, right domain.Feedstock) bool {
	left.BrewedAt = nil
	right.BrewedAt = nil
	leftJSON, _ := json.Marshal(left)
	rightJSON, _ := json.Marshal(right)
	return string(leftJSON) == string(rightJSON)
}
