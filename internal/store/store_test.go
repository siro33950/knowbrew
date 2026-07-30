package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gofrs/flock"
	"github.com/siro33950/knowbrew/internal/domain"
)

func TestFeedstockIsImmutableExceptBrewedAt(t *testing.T) {
	dataStore, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	feedstock := validFeedstock()
	if err := dataStore.WriteFeedstock(feedstock); err != nil {
		t.Fatal(err)
	}
	changed := feedstock
	changed.Summary = "A changed summary."
	if err := dataStore.WriteFeedstock(changed); err == nil {
		t.Fatal("expected immutable feedstock update to fail")
	}
	brewedAt := time.Now().UTC()
	if err := dataStore.MarkBrewed(feedstock.ID, brewedAt); err != nil {
		t.Fatal(err)
	}
	stored, _, err := dataStore.FindFeedstock(feedstock.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.BrewedAt == nil || !stored.BrewedAt.Equal(brewedAt) {
		t.Fatalf("brewed_at = %v", stored.BrewedAt)
	}
	later := brewedAt.Add(time.Hour)
	if err := dataStore.MarkBrewed(feedstock.ID, later); err != nil {
		t.Fatal(err)
	}
	stored, _, _ = dataStore.FindFeedstock(feedstock.ID)
	if !stored.BrewedAt.Equal(brewedAt) {
		t.Fatal("brewed_at was changed after first processing")
	}
}

func TestKnowledgeLifecycleAndSources(t *testing.T) {
	dataStore, _ := New(t.TempDir())
	feedstock := validFeedstock()
	if err := dataStore.WriteFeedstock(feedstock); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	knowledge := domain.Knowledge{
		Created: now, Updated: now, AppliesWhen: "When testing",
		Sources: []string{feedstock.ID}, Status: domain.StatusActive,
	}
	if err := dataStore.WriteNewKnowledge("testing-rule", knowledge, "# Testing rule"); err != nil {
		t.Fatal(err)
	}
	path, _ := dataStore.KnowledgePath("testing-rule")
	stored, _, err := dataStore.ReadKnowledge(path)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != domain.StatusPending {
		t.Fatalf("new status = %q, want pending", stored.Status)
	}
	if err := dataStore.InvalidateKnowledge("testing-rule", []string{feedstock.ID}, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	stored, _, err = dataStore.ReadKnowledge(path)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != domain.StatusInvalidated || stored.InvalidatedAt == nil {
		t.Fatalf("invalidated knowledge = %#v", stored)
	}
	if err := dataStore.AddKnowledgeSources("testing-rule", []string{feedstock.ID}, now); err == nil {
		t.Fatal("expected adding a source to invalidated knowledge to fail")
	}
}

func TestCandidateRoundTripUnderLock(t *testing.T) {
	dataStore, _ := New(t.TempDir())
	candidate := domain.FeedstockCandidate{
		ID: "claude-session-t000001", Session: domain.SessionRef{ID: "session", Path: "/log"},
		Timestamp: time.Now().UTC(), Agent: "claude", UserQuote: "hello",
	}
	if err := dataStore.WithLock(context.Background(), func() error {
		return dataStore.WriteCandidate(candidate)
	}); err != nil {
		t.Fatal(err)
	}
	loaded, err := dataStore.ReadCandidate(candidate.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.UserQuote != candidate.UserQuote {
		t.Fatalf("quote = %q", loaded.UserQuote)
	}
	path, _ := dataStore.PendingPath(candidate.ID)
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
}

func TestWithLockFailsImmediatelyWhenHeld(t *testing.T) {
	dataStore, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := dataStore.EnsureLayout(); err != nil {
		t.Fatal(err)
	}
	lock := flock.New(filepath.Join(dataStore.Root, ".state", "knowbrew.lock"))
	if err := lock.Lock(); err != nil {
		t.Fatal(err)
	}
	defer lock.Unlock()

	started := time.Now()
	err = dataStore.WithLock(context.Background(), func() error {
		t.Fatal("lock callback must not run")
		return nil
	})
	if err == nil || err.Error() != "another knowbrew process holds the store lock" {
		t.Fatalf("error = %v", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("lock conflict took %s", elapsed)
	}
}

func TestListingsSkipBrokenMarkdownAndCollectWarnings(t *testing.T) {
	dataStore, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := dataStore.EnsureLayout(); err != nil {
		t.Fatal(err)
	}
	feedstock := validFeedstock()
	if err := dataStore.WriteFeedstock(feedstock); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := dataStore.WriteNewKnowledge("valid-knowledge", domain.Knowledge{
		Created: now, Updated: now, AppliesWhen: "When testing",
		Sources: []string{feedstock.ID}, Status: domain.StatusPending,
	}, "# Valid knowledge"); err != nil {
		t.Fatal(err)
	}
	if _, err := dataStore.EnsureMaster("topics", domain.MasterEntry{
		Name: "testing", Definition: "Automated verification.", Status: domain.StatusPending,
		Created: now, Updated: now,
	}); err != nil {
		t.Fatal(err)
	}
	brokenPaths := []string{
		filepath.Join(dataStore.Root, "feedstocks", "broken.md"),
		filepath.Join(dataStore.Root, "knowledge", "broken-knowledge.md"),
		filepath.Join(dataStore.Root, "masters", "topics", "broken-master.md"),
	}
	for _, path := range brokenPaths {
		if err := os.WriteFile(path, []byte("---\nunknown_field: true\n---\nbroken\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	feedstocks, feedstockWarnings, err := dataStore.ListFeedstocks()
	if err != nil {
		t.Fatal(err)
	}
	if len(feedstocks) != 1 || len(feedstockWarnings) != 1 {
		t.Fatalf("feedstocks = %#v, warnings = %#v", feedstocks, feedstockWarnings)
	}
	knowledge, knowledgeWarnings, err := dataStore.ListKnowledge(true)
	if err != nil {
		t.Fatal(err)
	}
	if len(knowledge) != 1 || len(knowledgeWarnings) != 1 {
		t.Fatalf("knowledge = %#v, warnings = %#v", knowledge, knowledgeWarnings)
	}
	masters, masterWarnings, err := dataStore.LoadMasters("topics")
	if err != nil {
		t.Fatal(err)
	}
	if len(masters) != 1 || len(masterWarnings) != 1 {
		t.Fatalf("masters = %#v, warnings = %#v", masters, masterWarnings)
	}
	for index, warnings := range [][]string{
		{feedstockWarnings[0].Path, feedstockWarnings[0].String()},
		{knowledgeWarnings[0].Path, knowledgeWarnings[0].String()},
		{masterWarnings[0].Path, masterWarnings[0].String()},
	} {
		if warnings[0] != brokenPaths[index] || !strings.HasPrefix(warnings[1], "skipped: ") {
			t.Fatalf("warning %d = %#v", index, warnings)
		}
	}
}

func TestFindFeedstockUsesSentinelForMissingFeedstock(t *testing.T) {
	dataStore, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = dataStore.FindFeedstock("claude-missing-t000001")
	if !errors.Is(err, ErrFeedstockNotFound) {
		t.Fatalf("error = %v, want ErrFeedstockNotFound", err)
	}
}

func validFeedstock() domain.Feedstock {
	return domain.Feedstock{
		Schema: domain.SchemaVersion, ID: "claude-session-t000001",
		Session:   domain.SessionRef{ID: "session", Path: "/logs/session.jsonl"},
		Timestamp: time.Date(2026, 7, 30, 1, 0, 0, 0, time.UTC),
		Agent:     "claude", UserQuote: "Please test it.",
		SpeechActs: []string{"request"}, Subjects: []string{"project"},
		Summary: "The user requested testing.",
	}
}
