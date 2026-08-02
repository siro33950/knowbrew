package store

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/siro33950/knowbrew/internal/adapters/fsutil"
)

func TestTransactionRecoveryRollsForwardEveryFile(t *testing.T) {
	dataStore, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := dataStore.EnsureLayout(); err != nil {
		t.Fatal(err)
	}
	first := filepath.Join(dataStore.Root, "knowledge", "first.md")
	second := filepath.Join(dataStore.Root, "knowledge", "second.md")
	if err := os.WriteFile(first, []byte("before-one"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(second, []byte("before-two"), 0o644); err != nil {
		t.Fatal(err)
	}
	journal := transactionJournal{Created: time.Now().UTC(), Mutations: []transactionMutation{
		{Path: "knowledge/first.md", BeforeDigest: digestBytes([]byte("before-one")), AfterDigest: digestBytes([]byte("after-one")), Mode: 0o644, Data: []byte("after-one")},
		{Path: "knowledge/second.md", BeforeDigest: digestBytes([]byte("before-two")), AfterDigest: digestBytes([]byte("after-two")), Mode: 0o644, Data: []byte("after-two")},
	}}
	encoded, err := json.Marshal(journal)
	if err != nil {
		t.Fatal(err)
	}
	journalPath := filepath.Join(dataStore.Root, ".knowbrew", "state", "transactions", "interrupted.json")
	if err := fsutil.AtomicWrite(journalPath, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := fsutil.AtomicWrite(first, []byte("after-one"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := dataStore.WithLock(context.Background(), func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]string{first: "after-one", second: "after-two"} {
		data, err := os.ReadFile(path)
		if err != nil || string(data) != want {
			t.Fatalf("%s = %q, err = %v", path, data, err)
		}
	}
	if _, err := os.Stat(journalPath); !os.IsNotExist(err) {
		t.Fatalf("journal still exists: %v", err)
	}
}

func TestTransactionRecoveryDoesNotOverwriteExternalEdit(t *testing.T) {
	dataStore, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := dataStore.EnsureLayout(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dataStore.Root, "knowledge", "edited.md")
	if err := os.WriteFile(path, []byte("human edit"), 0o644); err != nil {
		t.Fatal(err)
	}
	journal := transactionJournal{Created: time.Now().UTC(), Mutations: []transactionMutation{{
		Path: "knowledge/edited.md", BeforeDigest: digestBytes([]byte("before")),
		AfterDigest: digestBytes([]byte("after")), Mode: 0o644, Data: []byte("after"),
	}}}
	encoded, err := json.Marshal(journal)
	if err != nil {
		t.Fatal(err)
	}
	journalPath := filepath.Join(dataStore.Root, ".knowbrew", "state", "transactions", "conflict.json")
	if err := fsutil.AtomicWrite(journalPath, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	err = dataStore.WithLock(context.Background(), func() error { return nil })
	if err == nil || !strings.Contains(err.Error(), "changed outside the transaction") {
		t.Fatalf("recovery error = %v", err)
	}
	data, readErr := os.ReadFile(path)
	if readErr != nil || string(data) != "human edit" {
		t.Fatalf("external edit = %q, err = %v", data, readErr)
	}
}
