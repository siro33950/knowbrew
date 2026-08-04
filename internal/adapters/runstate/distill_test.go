package runstate

import (
	"path/filepath"
	"testing"

	"github.com/siro33950/knowbrew/internal/application/distill"
)

func TestDistillCursorPersistsPositionAtomically(t *testing.T) {
	cursor := DistillCursor{Path: filepath.Join(t.TempDir(), "state", "distill-cursor.json")}
	if _, exists, err := cursor.Load(); err != nil || exists {
		t.Fatalf("missing cursor exists = %v, err = %v", exists, err)
	}
	want := distill.CursorPosition{Subject: "knowbrew", Template: "concept"}
	if err := cursor.Save(want); err != nil {
		t.Fatal(err)
	}
	got, exists, err := cursor.Load()
	if err != nil {
		t.Fatal(err)
	}
	if !exists || got != want {
		t.Fatalf("cursor = %#v, exists = %v", got, exists)
	}
}
