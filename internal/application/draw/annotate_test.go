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

func TestAnnotateWritesOnlyNormalizedTypeCandidates(t *testing.T) {
	dataStore, _ := store.New(t.TempDir())
	feedstock := domain.Feedstock{
		Schema: domain.SchemaVersion, ID: "fs-annotate", TurnID: "turn-1",
		Session: domain.SessionRef{ID: "session"}, Timestamp: time.Now().UTC(), Agent: "codex",
		CWD: "/work/subject", Repo: "https://example.com/subject.git", Branch: "main",
		Summary: "The user requested durable behavior.",
	}
	if err := dataStore.WriteFeedstock(feedstock); err != nil {
		t.Fatal(err)
	}
	added, err := annotateForTest(context.Background(), dataStore, Annotation{
		FeedstockID: feedstock.ID,
		Types:       []domain.KnowledgeType{"principle", "constraint", "principle"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if added != 0 {
		t.Fatalf("masters added = %d", added)
	}
	annotated, _, err := dataStore.FindFeedstock(feedstock.ID)
	if err != nil {
		t.Fatal(err)
	}
	if annotated.AnnotatedAt == nil || !reflect.DeepEqual(
		annotated.Types,
		[]domain.KnowledgeType{"constraint", "principle"},
	) {
		t.Fatalf("feedstock = %#v", annotated)
	}
	if annotated.Summary != feedstock.Summary || annotated.CWD != feedstock.CWD ||
		annotated.Repo != feedstock.Repo || annotated.Branch != feedstock.Branch {
		t.Fatalf("non-annotation fields changed: %#v", annotated)
	}
}

func TestAnnotateAllowsEmptyTypeCandidates(t *testing.T) {
	dataStore, _ := store.New(t.TempDir())
	feedstock := domain.Feedstock{
		Schema: domain.SchemaVersion, ID: "fs-empty", TurnID: "turn-1",
		Session: domain.SessionRef{ID: "session"}, Timestamp: time.Now().UTC(), Agent: "codex",
		Summary: "The turn contains no durable Knowledge.",
	}
	if err := dataStore.WriteFeedstock(feedstock); err != nil {
		t.Fatal(err)
	}
	if _, err := annotateForTest(context.Background(), dataStore, Annotation{FeedstockID: feedstock.ID}); err != nil {
		t.Fatal(err)
	}
	annotated, _, err := dataStore.FindFeedstock(feedstock.ID)
	if err != nil {
		t.Fatal(err)
	}
	if annotated.AnnotatedAt == nil || len(annotated.Types) != 0 || annotated.PendingBrew() {
		t.Fatalf("feedstock = %#v", annotated)
	}
}

func TestAnnotateRejectsUnknownKnowledgeType(t *testing.T) {
	dataStore, _ := store.New(t.TempDir())
	feedstock := domain.Feedstock{
		Schema: domain.SchemaVersion, ID: "fs-unknown", TurnID: "turn-1",
		Session: domain.SessionRef{ID: "session"}, Timestamp: time.Now().UTC(), Agent: "codex",
		Summary: "summary",
	}
	if err := dataStore.WriteFeedstock(feedstock); err != nil {
		t.Fatal(err)
	}
	_, err := annotateForTest(context.Background(), dataStore, Annotation{
		FeedstockID: feedstock.ID,
		Types:       []domain.KnowledgeType{"other"},
	})
	if err == nil || !strings.Contains(err.Error(), "not defined in masters/types") {
		t.Fatalf("error = %v", err)
	}
}

func TestAnnotateRejectsDifferentInvocationFeedstock(t *testing.T) {
	dataStore, _ := store.New(t.TempDir())
	t.Setenv(config.InvocationFeedstockEnvironment, "fs-one")
	_, err := annotateForTest(context.Background(), dataStore, Annotation{FeedstockID: "fs-two"})
	if err == nil || !strings.Contains(err.Error(), "feedstock fs-two does not match invocation feedstock fs-one") {
		t.Fatalf("error = %v", err)
	}
}

func TestSummaryAndAnnotationUseSeparateStateTransitions(t *testing.T) {
	dataStore, _ := store.New(t.TempDir())
	feedstock := domain.Feedstock{
		Schema: domain.SchemaVersion, ID: "fs-separated", TurnID: "turn-separated",
		Session: domain.SessionRef{ID: "session"}, Timestamp: time.Now().UTC(), Agent: "codex",
	}
	if err := dataStore.WriteFeedstock(feedstock); err != nil {
		t.Fatal(err)
	}
	if _, err := annotateForTest(context.Background(), dataStore, Annotation{FeedstockID: feedstock.ID}); err == nil ||
		!strings.Contains(err.Error(), "must be summarized") {
		t.Fatalf("error = %v", err)
	}
	if err := summarizeForTest(context.Background(), dataStore, feedstock.ID, " target summary "); err != nil {
		t.Fatal(err)
	}
	if _, err := annotateForTest(context.Background(), dataStore, Annotation{
		FeedstockID: feedstock.ID, Types: []domain.KnowledgeType{"property"},
	}); err != nil {
		t.Fatal(err)
	}
	annotated, _, err := dataStore.FindFeedstock(feedstock.ID)
	if err != nil {
		t.Fatal(err)
	}
	if annotated.Summary != "target summary" || annotated.AnnotatedAt == nil {
		t.Fatalf("feedstock = %#v", annotated)
	}
}
