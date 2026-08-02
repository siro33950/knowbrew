package draw

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/siro33950/knowbrew/internal/config"
	"github.com/siro33950/knowbrew/internal/domain"
	"github.com/siro33950/knowbrew/internal/store"
)

func TestAnnotateUpdatesOnlyClassificationAndDerivesSubjectsFromAssertions(t *testing.T) {
	dataStore, _ := store.New(t.TempDir())
	for _, name := range []string{"product", "subject"} {
		if _, err := dataStore.EnsureMaster("subjects", domain.MasterEntry{
			Name: name, Definition: "An existing subject.",
		}); err != nil {
			t.Fatal(err)
		}
	}
	feedstock := domain.Feedstock{
		Schema: domain.SchemaVersion, ID: "codex-session-t000001",
		TurnID:    "turn-1",
		Session:   domain.SessionRef{ID: "session", Path: "/log"},
		Timestamp: time.Now().UTC(), Agent: "codex", CWD: "/work/subject",
		Repo: "https://example.com/subject.git", Branch: "main",
		Subjects: []string{"subject"}, Summary: "The user requested table-driven tests.",
	}
	if err := dataStore.WriteFeedstock(feedstock); err != nil {
		t.Fatal(err)
	}
	added, err := Annotate(context.Background(), dataStore, Annotation{
		FeedstockID: feedstock.ID,
		Assertions: []AssertionInput{
			{Type: "principle", Subject: "product", Statement: "Table-driven tests expose cases uniformly."},
			{Type: "constraint", Subject: "", Statement: "Existing behavior must remain compatible."},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if added != 0 {
		t.Fatalf("masters added = %d, want 0", added)
	}
	annotated, _, err := dataStore.FindFeedstock(feedstock.ID)
	if err != nil {
		t.Fatal(err)
	}
	if annotated.AnnotatedAt == nil || annotated.Summary == "" ||
		!reflect.DeepEqual(annotated.Subjects, []string{"product"}) ||
		!reflect.DeepEqual(
			annotated.Types,
			[]domain.KnowledgeType{domain.KnowledgeType("constraint"), domain.KnowledgeType("principle")},
		) {
		t.Fatalf("feedstock = %#v", annotated)
	}
	for name, values := range map[string]any{
		"turn_id": annotated.TurnID, "session": annotated.Session,
		"timestamp": annotated.Timestamp, "agent": annotated.Agent,
		"cwd": annotated.CWD, "repo": annotated.Repo, "branch": annotated.Branch,
	} {
		var want any
		switch name {
		case "turn_id":
			want = feedstock.TurnID
		case "session":
			want = feedstock.Session
		case "timestamp":
			want = feedstock.Timestamp
		case "agent":
			want = feedstock.Agent
		case "cwd":
			want = feedstock.CWD
		case "repo":
			want = feedstock.Repo
		case "branch":
			want = feedstock.Branch
		}
		if !reflect.DeepEqual(values, want) {
			t.Fatalf("%s changed: got %#v, want %#v", name, values, want)
		}
	}
	subjects, warnings, err := dataStore.LoadMasters("subjects")
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 0 || len(subjects) != 2 ||
		subjects[0].Name != "product" ||
		subjects[1].Name != "subject" {
		t.Fatalf("subjects = %#v, warnings = %#v", subjects, warnings)
	}
}

func TestAnnotateAllowsNoSubject(t *testing.T) {
	dataStore, _ := store.New(t.TempDir())
	feedstock := domain.Feedstock{
		Schema: domain.SchemaVersion, ID: "codex-session-t000001",
		TurnID:    "turn-1",
		Session:   domain.SessionRef{ID: "session", Path: "/log"},
		Timestamp: time.Now().UTC(), Agent: "codex", Summary: "The user asked a one-off question.",
	}
	if err := dataStore.WriteFeedstock(feedstock); err != nil {
		t.Fatal(err)
	}
	added, err := Annotate(context.Background(), dataStore, Annotation{
		FeedstockID: feedstock.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if added != 0 {
		t.Fatalf("masters added = %d, want 0", added)
	}
	annotated, _, err := dataStore.FindFeedstock(feedstock.ID)
	if err != nil {
		t.Fatal(err)
	}
	if annotated.AnnotatedAt == nil || len(annotated.Subjects) != 0 {
		t.Fatalf("annotated feedstock = %#v", annotated)
	}
}

func TestAnnotateAllowsSameAtomicStatementForDifferentSubjects(t *testing.T) {
	dataStore, _ := store.New(t.TempDir())
	for _, name := range []string{"agent-prompt", "agent-model"} {
		if _, err := dataStore.EnsureMaster("subjects", domain.MasterEntry{Name: name}); err != nil {
			t.Fatal(err)
		}
	}
	feedstock := domain.Feedstock{
		Schema: domain.SchemaVersion, ID: "fs-multi-subject", TurnID: "turn-multi-subject",
		Session:   domain.SessionRef{ID: "session", Path: "/log"},
		Timestamp: time.Now().UTC(), Agent: "codex", Summary: "Two subjects share one established relation.",
	}
	if err := dataStore.WriteFeedstock(feedstock); err != nil {
		t.Fatal(err)
	}
	statement := "Agent behavior depends on selected instructions and model characteristics."
	if _, err := Annotate(context.Background(), dataStore, Annotation{
		FeedstockID: feedstock.ID,
		Assertions: []AssertionInput{
			{Type: "relation", Subject: "agent-prompt", Statement: statement},
			{Type: "relation", Subject: "agent-model", Statement: statement},
		},
	}); err != nil {
		t.Fatal(err)
	}
	stored, _, err := dataStore.FindFeedstock(feedstock.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(stored.Assertions) != 2 || stored.Assertions[0].ID == stored.Assertions[1].ID ||
		stored.Assertions[0].Subject == stored.Assertions[1].Subject {
		t.Fatalf("assertions = %#v", stored.Assertions)
	}
}

func TestAnnotateRejectsUnknownSubjectWithoutCreatingMaster(t *testing.T) {
	dataStore, _ := store.New(t.TempDir())
	feedstock := domain.Feedstock{
		Schema: domain.SchemaVersion, ID: "codex-session-t000001",
		TurnID:    "turn-1",
		Session:   domain.SessionRef{ID: "session", Path: "/log"},
		Timestamp: time.Now().UTC(), Agent: "codex", Summary: "The user discussed an unknown subject.",
	}
	if err := dataStore.WriteFeedstock(feedstock); err != nil {
		t.Fatal(err)
	}
	_, err := Annotate(context.Background(), dataStore, Annotation{
		FeedstockID: feedstock.ID,
		Assertions: []AssertionInput{{
			Type: "property", Subject: "invented-subject", Statement: "The value is stable.",
		}},
	})
	if err == nil ||
		!strings.Contains(err.Error(), `subject "invented-subject" is not defined in masters/subjects`) {
		t.Fatalf("error = %v", err)
	}
	subjects, warnings, loadErr := dataStore.LoadMasters("subjects")
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if len(warnings) != 0 || len(subjects) != 0 {
		t.Fatalf("subjects = %#v, warnings = %#v", subjects, warnings)
	}
}

func TestAnnotateRejectsAlreadyAnnotatedFeedstock(t *testing.T) {
	dataStore, _ := store.New(t.TempDir())
	feedstock := domain.Feedstock{
		Schema: domain.SchemaVersion, ID: "codex-session-t000001",
		TurnID:    "turn-1",
		Session:   domain.SessionRef{ID: "session", Path: "/log"},
		Timestamp: time.Now().UTC(), Agent: "codex",
		Subjects: []string{"subject"}, Summary: "The user requested tests.",
	}
	if err := dataStore.WriteFeedstock(feedstock); err != nil {
		t.Fatal(err)
	}
	annotation := Annotation{FeedstockID: feedstock.ID}
	if _, err := Annotate(context.Background(), dataStore, annotation); err != nil {
		t.Fatal(err)
	}
	if _, err := Annotate(context.Background(), dataStore, annotation); err == nil ||
		!strings.Contains(err.Error(), "already annotated") {
		t.Fatalf("error = %v", err)
	}
}

func TestAnnotateNormalizesWikilinksBeforeMasterValidation(t *testing.T) {
	dataStore, _ := store.New(t.TempDir())
	feedstock := domain.Feedstock{
		Schema: domain.SchemaVersion, ID: "codex-session-t000001",
		TurnID:    "turn-1",
		Session:   domain.SessionRef{ID: "session", Path: "/log"},
		Timestamp: time.Now().UTC(), Agent: "codex",
		Subjects: []string{"subject"}, Summary: "The existing vocabulary was used.",
	}
	if err := dataStore.WriteFeedstock(feedstock); err != nil {
		t.Fatal(err)
	}
	if _, err := dataStore.EnsureMaster("subjects", domain.MasterEntry{
		Name: "subject", Definition: "Existing vocabulary.",
	}); err != nil {
		t.Fatal(err)
	}
	added, err := Annotate(context.Background(), dataStore, Annotation{
		FeedstockID: feedstock.ID,
		Assertions: []AssertionInput{{
			Type: "property", Subject: "[[subject]]", Statement: "The value is stable.",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if added != 0 {
		t.Fatalf("masters added = %d, want 0", added)
	}
	annotated, _, err := dataStore.FindFeedstock(feedstock.ID)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(annotated.Subjects, ",") != "subject" {
		t.Fatalf("annotation subjects = %#v", annotated.Subjects)
	}
}

func TestAnnotateRejectsUnknownKnowledgeType(t *testing.T) {
	dataStore, _ := store.New(t.TempDir())
	feedstock := domain.Feedstock{
		Schema: domain.SchemaVersion, ID: "codex-session-t000001", TurnID: "turn-1",
		Session:   domain.SessionRef{ID: "session", Path: "/log"},
		Timestamp: time.Now().UTC(), Agent: "codex", Summary: "summary",
	}
	if err := dataStore.WriteFeedstock(feedstock); err != nil {
		t.Fatal(err)
	}
	_, err := Annotate(context.Background(), dataStore, Annotation{
		FeedstockID: feedstock.ID,
		Assertions: []AssertionInput{{
			Type: "other", Subject: "", Statement: "The value is stable.",
		}},
	})
	if err == nil || !strings.Contains(err.Error(), "not defined in masters/types") {
		t.Fatalf("error = %v", err)
	}
}

func TestAnnotateRejectsDifferentInvocationFeedstock(t *testing.T) {
	dataStore, _ := store.New(t.TempDir())
	t.Setenv(config.InvocationFeedstockEnvironment, "codex-session-t000001")
	_, err := Annotate(context.Background(), dataStore, Annotation{
		FeedstockID: "codex-session-t000002",
	})
	if err == nil {
		t.Fatal("expected an annotation for a different invocation feedstock to fail")
	}
}

func TestSummaryAndAnnotationUseSeparateStateTransitions(t *testing.T) {
	dataStore, _ := store.New(t.TempDir())
	feedstock := domain.Feedstock{
		Schema: domain.SchemaVersion, ID: "fs-separated-phases", TurnID: "turn-separated-phases",
		Session:   domain.SessionRef{ID: "session", Path: "/log"},
		Timestamp: time.Now().UTC(), Agent: "codex", CWD: "/work", Repo: "repo",
	}
	if err := dataStore.WriteFeedstock(feedstock); err != nil {
		t.Fatal(err)
	}
	if _, err := Annotate(context.Background(), dataStore, Annotation{FeedstockID: feedstock.ID}); err == nil ||
		!strings.Contains(err.Error(), "must be summarized before annotation") {
		t.Fatalf("annotate before summary error = %v", err)
	}
	if err := Summarize(context.Background(), dataStore, feedstock.ID, "  target summary  "); err != nil {
		t.Fatal(err)
	}
	summarized, _, err := dataStore.FindFeedstock(feedstock.ID)
	if err != nil {
		t.Fatal(err)
	}
	if summarized.Summary != "target summary" || summarized.AnnotatedAt != nil ||
		summarized.CWD != feedstock.CWD || summarized.Repo != feedstock.Repo {
		t.Fatalf("summarized feedstock = %#v", summarized)
	}
	if err := Summarize(context.Background(), dataStore, feedstock.ID, "replacement"); err == nil ||
		!strings.Contains(err.Error(), "already summarized") {
		t.Fatalf("second summarize error = %v", err)
	}
	if _, err := Annotate(context.Background(), dataStore, Annotation{FeedstockID: feedstock.ID}); err != nil {
		t.Fatal(err)
	}
	annotated, _, err := dataStore.FindFeedstock(feedstock.ID)
	if err != nil {
		t.Fatal(err)
	}
	if annotated.AnnotatedAt == nil || annotated.Summary != "target summary" {
		t.Fatalf("annotated feedstock = %#v", annotated)
	}
	if err := Summarize(context.Background(), dataStore, feedstock.ID, "replacement"); err == nil ||
		!strings.Contains(err.Error(), "already annotated") {
		t.Fatalf("summarize annotated error = %v", err)
	}
}
