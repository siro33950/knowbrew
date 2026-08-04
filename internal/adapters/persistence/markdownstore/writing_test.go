package store

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadWritingGuideAcceptsExistingAndMissingFiles(t *testing.T) {
	dataStore, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := dataStore.EnsureLayout(); err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(dataStore.Root, "masters", "writing")
	if err := os.WriteFile(filepath.Join(directory, "common.md"), []byte("common rules\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	content, exists, err := dataStore.ReadWritingGuide("common")
	if err != nil {
		t.Fatal(err)
	}
	if !exists || content != "common rules\n" {
		t.Fatalf("common guide = %q, %t", content, exists)
	}
	content, exists, err = dataStore.ReadWritingGuide("knowledge")
	if err != nil {
		t.Fatal(err)
	}
	if exists || content != "" {
		t.Fatalf("missing guide = %q, %t", content, exists)
	}
}

func TestReadWritingGuideRejectsInvalidNameAndUnreadablePath(t *testing.T) {
	dataStore, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := dataStore.EnsureLayout(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := dataStore.ReadWritingGuide("../common"); err == nil {
		t.Fatal("expected invalid guide name to fail")
	}
	path := filepath.Join(dataStore.Root, "masters", "writing", "document.md")
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, _, err := dataStore.ReadWritingGuide("document"); err == nil {
		t.Fatal("expected a non-file guide path to fail")
	}
}

func TestEnsureDefaultWritingGuidesCreatesMissingWithoutOverwritingExisting(t *testing.T) {
	dataStore, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := dataStore.EnsureLayout(); err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(dataStore.Root, "masters", "writing")
	commonPath := filepath.Join(directory, "common.md")
	if err := os.WriteFile(commonPath, []byte("custom common\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := dataStore.EnsureDefaultWritingGuides(); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(commonPath); err != nil || string(data) != "custom common\n" {
		t.Fatalf("common guide was overwritten: %q, %v", data, err)
	}
	for _, name := range []string{"knowledge.md", "document.md"} {
		if data, err := os.ReadFile(filepath.Join(directory, name)); err != nil || len(data) == 0 {
			t.Fatalf("missing default %s: %q, %v", name, data, err)
		}
	}
}
