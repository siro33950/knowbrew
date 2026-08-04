package store

import (
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/siro33950/knowbrew/internal/domain"
)

func TestFeedstockAssertionsRoundTripAsGeneratedMarkdown(t *testing.T) {
	dataStore, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	annotatedAt := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	assertions := []domain.Assertion{
		{
			ID: "as-property", Type: "property", Subject: "knowbrew",
			Trigger:   "always",
			Statement: "Feedstock assertions are searchable.",
			Rationale: "They are stored as generated Markdown in the feedstock body.",
		},
		{
			ID: "as-decision", Type: "decision",
			Statement: "Each assertion has exactly one type.",
		},
	}
	feedstock := domain.Feedstock{
		Schema: domain.SchemaVersion, ID: "fs-assertions", TurnID: "turn-assertions",
		Session:   domain.SessionRef{ID: "session"},
		Timestamp: time.Date(2026, 8, 1, 11, 0, 0, 0, time.UTC), Agent: "codex",
		Summary: "The turn established two assertions.", AnnotatedAt: &annotatedAt,
		Assertions: assertions,
	}
	if err := dataStore.WriteFeedstock(feedstock); err != nil {
		t.Fatal(err)
	}
	path, err := dataStore.FeedstockPath(feedstock)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, required := range []string{
		"## Assertions",
		"### as-property",
		`- Type: [[property]]`,
		`- Subject: [[knowbrew]]`,
		"- Trigger: always",
		"Feedstock assertions are searchable.",
		"#### Rationale",
		"### as-decision",
		`- Type: [[decision]]`,
	} {
		if !strings.Contains(text, required) {
			t.Fatalf("feedstock Markdown does not contain %q:\n%s", required, text)
		}
	}
	for _, removed := range []string{"user_quote:", "speech_acts:", "commands:", "files_changed:", "errors:", "Applies when:"} {
		if strings.Contains(text, removed) {
			t.Fatalf("feedstock contains removed field %q:\n%s", removed, text)
		}
	}
	loaded, err := dataStore.ReadFeedstock(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(loaded.Assertions, assertions) {
		t.Fatalf("assertions = %#v, want %#v", loaded.Assertions, assertions)
	}
	wantTypes := []domain.KnowledgeType{"decision", "property"}
	if !reflect.DeepEqual(loaded.Types, wantTypes) {
		t.Fatalf("derived types = %#v, want %#v", loaded.Types, wantTypes)
	}
}
