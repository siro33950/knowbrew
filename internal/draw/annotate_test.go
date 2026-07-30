package draw

import (
	"context"
	"testing"
	"time"

	"github.com/siro33950/knowbrew/internal/config"
	"github.com/siro33950/knowbrew/internal/domain"
	"github.com/siro33950/knowbrew/internal/store"
)

func TestAnnotateFinalizesCandidateAndCreatesPendingMasters(t *testing.T) {
	dataStore, _ := store.New(t.TempDir())
	candidate := domain.FeedstockCandidate{
		ID: "codex-session-t000001", Session: domain.SessionRef{ID: "session", Path: "/log"},
		Timestamp: time.Now().UTC(), Agent: "codex", UserQuote: "Use table tests.",
		Subjects: []string{"project"},
	}
	if err := dataStore.WriteCandidate(candidate); err != nil {
		t.Fatal(err)
	}
	added, err := Annotate(context.Background(), dataStore, Annotation{
		FeedstockID: candidate.ID, Summary: "The user requested table-driven tests.",
		SpeechActs:  []string{"instruction"},
		NewTopics:   []string{"testing=Software verification with automated tests."},
		NewSubjects: []string{"project=The current project."},
	})
	if err != nil {
		t.Fatal(err)
	}
	if added != 2 {
		t.Fatalf("masters added = %d, want 2", added)
	}
	feedstock, _, err := dataStore.FindFeedstock(candidate.ID)
	if err != nil {
		t.Fatal(err)
	}
	if feedstock.Summary == "" || len(feedstock.Subjects) != 1 {
		t.Fatalf("feedstock = %#v", feedstock)
	}
	if _, err := dataStore.ReadCandidate(candidate.ID); err == nil {
		t.Fatal("candidate was not removed")
	}
	masters, warnings, err := dataStore.LoadMasters("topics")
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 0 {
		t.Fatalf("master warnings = %#v", warnings)
	}
	if masters[0].Status != domain.StatusPending {
		t.Fatalf("master status = %q", masters[0].Status)
	}
}

func TestAnnotateRejectsOpenSpeechAct(t *testing.T) {
	dataStore, _ := store.New(t.TempDir())
	_, err := Annotate(context.Background(), dataStore, Annotation{
		FeedstockID: "missing", Summary: "summary", SpeechActs: []string{"invented"},
	})
	if err == nil {
		t.Fatal("expected unsupported speech act to fail")
	}
}

func TestAnnotateRejectsDifferentInvocationFeedstock(t *testing.T) {
	dataStore, _ := store.New(t.TempDir())
	t.Setenv(config.InvocationFeedstockEnvironment, "codex-session-t000001")
	_, err := Annotate(context.Background(), dataStore, Annotation{
		FeedstockID: "codex-session-t000002", Summary: "The user requested tests.",
		SpeechActs: []string{"request"},
	})
	if err == nil {
		t.Fatal("expected an annotation for a different invocation feedstock to fail")
	}
}
