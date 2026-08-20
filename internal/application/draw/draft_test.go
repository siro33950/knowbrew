package draw

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/siro33950/knowbrew/internal/adapters/config"
	"github.com/siro33950/knowbrew/internal/adapters/persistence/markdownstore"
	"github.com/siro33950/knowbrew/internal/domain"
)

func TestDraftWritesSummaryAndNormalizedTypeCandidates(t *testing.T) {
	dataStore, _ := store.New(t.TempDir())
	feedstock := domain.Feedstock{
		Schema: domain.SchemaVersion, ID: "fs-draft", TurnID: "turn-1",
		Session: domain.SessionRef{ID: "session"}, Timestamp: time.Now().UTC(), Agent: "codex",
		CWD: "/work/subject", Repo: "https://example.com/subject.git", Branch: "main",
	}
	if err := dataStore.WriteFeedstock(feedstock); err != nil {
		t.Fatal(err)
	}
	if err := draftForTest(context.Background(), dataStore, Draft{
		FeedstockID: feedstock.ID,
		Summary:     " The user requested durable behavior. ",
		Types:       []domain.KnowledgeType{"principle", "constraint", "principle"},
	}); err != nil {
		t.Fatal(err)
	}
	drawn, _, err := dataStore.FindFeedstock(feedstock.ID)
	if err != nil {
		t.Fatal(err)
	}
	if drawn.AnnotatedAt == nil || !reflect.DeepEqual(
		drawn.Types,
		[]domain.KnowledgeType{"constraint", "principle"},
	) {
		t.Fatalf("feedstock = %#v", drawn)
	}
	if drawn.Summary != "The user requested durable behavior." {
		t.Fatalf("summary = %q", drawn.Summary)
	}
	if drawn.CWD != feedstock.CWD || drawn.Repo != feedstock.Repo ||
		drawn.Branch != feedstock.Branch {
		t.Fatalf("acquisition fields changed: %#v", drawn)
	}
}

func TestDraftAllowsEmptyTypeCandidates(t *testing.T) {
	dataStore, _ := store.New(t.TempDir())
	feedstock := domain.Feedstock{
		Schema: domain.SchemaVersion, ID: "fs-empty", TurnID: "turn-1",
		Session: domain.SessionRef{ID: "session"}, Timestamp: time.Now().UTC(), Agent: "codex",
	}
	if err := dataStore.WriteFeedstock(feedstock); err != nil {
		t.Fatal(err)
	}
	if err := draftForTest(context.Background(), dataStore, Draft{
		FeedstockID: feedstock.ID,
		Summary:     "The turn contains no durable Knowledge.",
	}); err != nil {
		t.Fatal(err)
	}
	drawn, _, err := dataStore.FindFeedstock(feedstock.ID)
	if err != nil {
		t.Fatal(err)
	}
	if drawn.AnnotatedAt == nil || len(drawn.Types) != 0 || drawn.PendingBrew() {
		t.Fatalf("feedstock = %#v", drawn)
	}
}

func TestDraftRequiresSummary(t *testing.T) {
	dataStore, _ := store.New(t.TempDir())
	feedstock := domain.Feedstock{
		Schema: domain.SchemaVersion, ID: "fs-nosummary", TurnID: "turn-1",
		Session: domain.SessionRef{ID: "session"}, Timestamp: time.Now().UTC(), Agent: "codex",
	}
	if err := dataStore.WriteFeedstock(feedstock); err != nil {
		t.Fatal(err)
	}
	err := draftForTest(context.Background(), dataStore, Draft{
		FeedstockID: feedstock.ID,
		Types:       []domain.KnowledgeType{"property"},
	})
	if err == nil || !strings.Contains(err.Error(), "summary is required") {
		t.Fatalf("error = %v", err)
	}
	unchanged, _, err := dataStore.FindFeedstock(feedstock.ID)
	if err != nil {
		t.Fatal(err)
	}
	if unchanged.AnnotatedAt != nil || len(unchanged.Types) != 0 {
		t.Fatalf("feedstock = %#v", unchanged)
	}
}

func TestDraftRejectsUnknownKnowledgeType(t *testing.T) {
	dataStore, _ := store.New(t.TempDir())
	feedstock := domain.Feedstock{
		Schema: domain.SchemaVersion, ID: "fs-unknown", TurnID: "turn-1",
		Session: domain.SessionRef{ID: "session"}, Timestamp: time.Now().UTC(), Agent: "codex",
	}
	if err := dataStore.WriteFeedstock(feedstock); err != nil {
		t.Fatal(err)
	}
	err := draftForTest(context.Background(), dataStore, Draft{
		FeedstockID: feedstock.ID,
		Summary:     "summary",
		Types:       []domain.KnowledgeType{"other"},
	})
	if err == nil || !strings.Contains(err.Error(), "not defined in masters/types") {
		t.Fatalf("error = %v", err)
	}
}

func TestDraftRejectsDifferentInvocationFeedstock(t *testing.T) {
	dataStore, _ := store.New(t.TempDir())
	t.Setenv(config.InvocationFeedstockEnvironment, "fs-one")
	err := draftForTest(context.Background(), dataStore, Draft{
		FeedstockID: "fs-two", Summary: "summary",
	})
	if err == nil || !strings.Contains(err.Error(), "feedstock fs-two does not match invocation feedstock fs-one") {
		t.Fatalf("error = %v", err)
	}
}

func TestDraftRejectsAlreadyDrawnFeedstock(t *testing.T) {
	dataStore, _ := store.New(t.TempDir())
	feedstock := domain.Feedstock{
		Schema: domain.SchemaVersion, ID: "fs-twice", TurnID: "turn-1",
		Session: domain.SessionRef{ID: "session"}, Timestamp: time.Now().UTC(), Agent: "codex",
	}
	if err := dataStore.WriteFeedstock(feedstock); err != nil {
		t.Fatal(err)
	}
	draft := Draft{
		FeedstockID: feedstock.ID, Summary: "first summary",
		Types: []domain.KnowledgeType{"property"},
	}
	if err := draftForTest(context.Background(), dataStore, draft); err != nil {
		t.Fatal(err)
	}
	draft.Summary = "second summary"
	err := draftForTest(context.Background(), dataStore, draft)
	if err == nil || !strings.Contains(err.Error(), "already drawn") {
		t.Fatalf("error = %v", err)
	}
	drawn, _, err := dataStore.FindFeedstock(feedstock.ID)
	if err != nil {
		t.Fatal(err)
	}
	if drawn.Summary != "first summary" {
		t.Fatalf("summary = %q", drawn.Summary)
	}
}
