package draw

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gofrs/flock"
	"github.com/siro33950/knowbrew/internal/config"
	"github.com/siro33950/knowbrew/internal/domain"
	"github.com/siro33950/knowbrew/internal/llm"
	"github.com/siro33950/knowbrew/internal/store"
)

type annotatingRunner struct {
	store *store.Store
}

func (runner annotatingRunner) Run(ctx context.Context, task llm.Task, _ string, prompt string) error {
	if task != llm.TaskAnnotate {
		return nil
	}
	dir := filepath.Join(runner.store.Root, ".state", "pending")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		id := strings.TrimSuffix(entry.Name(), ".json")
		if !strings.Contains(prompt, id) {
			continue
		}
		_, err := Annotate(ctx, runner.store, Annotation{
			FeedstockID: id, Summary: "The user requested a tested change.",
			SpeechActs: []string{"request"}, Topics: []string{"testing"},
			NewTopics: []string{"testing=Automated software verification."},
		})
		return err
	}
	return nil
}

func TestDrawIsIdempotentAndStateUsesSessionIdentity(t *testing.T) {
	root := t.TempDir()
	dataStore, _ := store.New(root)
	logPath := filepath.Join(t.TempDir(), "session.jsonl")
	log := `{"type":"user","sessionId":"session-id","timestamp":"2026-07-30T01:02:03Z","cwd":"/repo","gitBranch":"main","message":{"role":"user","content":"test this"}}
`
	if err := os.WriteFile(logPath, []byte(log), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{
		Root: root, Path: filepath.Join(root, ".knowbrew", "config.toml"),
		LLM: config.LLM{Backend: "claude-cli"},
	}
	runner := annotatingRunner{store: dataStore}
	first, err := Run(context.Background(), cfg, []string{logPath}, runner, nil)
	if err != nil {
		t.Fatal(err)
	}
	if first.FeedstocksAnnotated != 1 {
		t.Fatalf("annotated = %d", first.FeedstocksAnnotated)
	}
	second, err := Run(context.Background(), cfg, []string{logPath}, runner, nil)
	if err != nil {
		t.Fatal(err)
	}
	if second.FeedstocksSkipped != 1 || second.FeedstocksAnnotated != 0 {
		t.Fatalf("second summary = %#v", second)
	}
	movedDir := filepath.Join(t.TempDir(), "backup")
	if err := os.MkdirAll(movedDir, 0o755); err != nil {
		t.Fatal(err)
	}
	movedPath := filepath.Join(movedDir, filepath.Base(logPath))
	if err := os.Rename(logPath, movedPath); err != nil {
		t.Fatal(err)
	}
	moved, err := Run(context.Background(), cfg, []string{movedPath}, runner, nil)
	if err != nil {
		t.Fatal(err)
	}
	if moved.FeedstocksSkipped != 1 || moved.FeedstocksAnnotated != 0 {
		t.Fatalf("moved summary = %#v", moved)
	}
	state, err := loadState(root)
	if err != nil {
		t.Fatal(err)
	}
	key := stateKey("claude", "session-id")
	session, ok := state.Sessions[key]
	if !ok {
		t.Fatalf("state keys = %#v, want %q", state.Sessions, key)
	}
	if session.Path != movedPath || len(session.FeedstockIDs) != 1 {
		t.Fatalf("session state = %#v", session)
	}
	if len(state.Sessions) != 1 {
		t.Fatalf("state should be keyed by identity, got %#v", state.Sessions)
	}
	feedstocks, warnings, err := dataStore.ListFeedstocks()
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 0 {
		t.Fatalf("feedstock warnings = %#v", warnings)
	}
	if len(feedstocks) != 1 || feedstocks[0].Schema != domain.SchemaVersion {
		t.Fatalf("feedstocks = %#v", feedstocks)
	}
}

func TestManualDirectoryInfersParserPerFile(t *testing.T) {
	dir := t.TempDir()
	claudePath := filepath.Join(dir, "01234567-89ab-cdef-0123-456789abcdef.jsonl")
	codexPath := filepath.Join(dir, "rollout-2026-07-30T01-00-00-019fb136-74f8-7283-8907-eb33a3cc74fd.jsonl")
	for _, path := range []string{claudePath, codexPath} {
		if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cfg := config.Config{}
	files, err := collectFiles(cfg, []string{dir})
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 {
		t.Fatalf("files = %#v", files)
	}
	agents := map[string]string{}
	for _, file := range files {
		agents[filepath.Base(file.Path)] = file.Agent
	}
	if agents[filepath.Base(claudePath)] != "claude" || agents[filepath.Base(codexPath)] != "codex" {
		t.Fatalf("agents = %#v", agents)
	}
}

func TestSubjectNameUsesRepositoryBasename(t *testing.T) {
	tests := map[string]string{
		"ssh://git@github.com/example/knowbrew.git": "knowbrew",
		"git@github.com:example/knowbrew.git":       "knowbrew",
		"/workspace/knowbrew.worktrees/feature":     "feature",
	}
	for input, want := range tests {
		if got := subjectName(input); got != want {
			t.Errorf("subjectName(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestAliasMatchNormalizesRepositoryURLs(t *testing.T) {
	if !aliasMatch("git@github.com:example/knowbrew.git", "https://github.com/example/knowbrew.git") {
		t.Fatal("equivalent repository URLs did not match")
	}
	if aliasMatch("git@github.com:first/knowbrew.git", "https://github.com/second/knowbrew.git") {
		t.Fatal("different repository owners were conflated")
	}
}

type retryingAnnotatingRunner struct {
	store           *store.Store
	failFeedstockID string
	failuresLeft    int
}

func (runner *retryingAnnotatingRunner) Run(ctx context.Context, task llm.Task, feedstockID, _ string) error {
	if task != llm.TaskAnnotate {
		return nil
	}
	if feedstockID == runner.failFeedstockID && runner.failuresLeft > 0 {
		runner.failuresLeft--
		return errors.New("temporary annotation failure")
	}
	_, err := Annotate(ctx, runner.store, Annotation{
		FeedstockID: feedstockID, Summary: "The user requested a tested change.",
		SpeechActs: []string{"request"}, Topics: []string{"testing"},
		NewTopics: []string{"testing=Automated software verification."},
	})
	return err
}

func TestDrawContinuesAfterAnnotationFailureAndRetriesFeedstock(t *testing.T) {
	root := t.TempDir()
	dataStore, _ := store.New(root)
	logPath := filepath.Join(t.TempDir(), "session.jsonl")
	log := `{"type":"user","sessionId":"session-id","timestamp":"2026-07-30T01:02:03Z","cwd":"/repo","message":{"role":"user","content":"first"}}
{"type":"user","sessionId":"session-id","timestamp":"2026-07-30T01:02:04Z","cwd":"/repo","message":{"role":"user","content":"second"}}
`
	if err := os.WriteFile(logPath, []byte(log), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{
		Root: root, Path: filepath.Join(root, ".knowbrew", "config.toml"),
		LLM: config.LLM{Backend: "claude-cli"},
	}
	failedID := "claude-session-id-t000001"
	runner := &retryingAnnotatingRunner{
		store: dataStore, failFeedstockID: failedID, failuresLeft: 1,
	}

	first, err := Run(context.Background(), cfg, []string{logPath}, runner, nil)
	if err != nil {
		t.Fatal(err)
	}
	if first.FeedstocksFailed != 1 || first.FeedstocksAnnotated != 1 || len(first.Failures) != 1 {
		t.Fatalf("first summary = %#v", first)
	}
	if first.Failures[0].FeedstockID != failedID ||
		!strings.Contains(first.Failures[0].Reason, "temporary annotation failure") {
		t.Fatalf("failure = %#v", first.Failures[0])
	}
	if _, err := dataStore.ReadCandidate(failedID); err != nil {
		t.Fatalf("failed feedstock was not left pending for retry: %v", err)
	}

	second, err := Run(context.Background(), cfg, []string{logPath}, runner, nil)
	if err != nil {
		t.Fatal(err)
	}
	if second.FeedstocksFailed != 0 || second.FeedstocksAnnotated != 1 || second.FeedstocksSkipped != 1 {
		t.Fatalf("second summary = %#v", second)
	}
	if _, _, err := dataStore.FindFeedstock(failedID); err != nil {
		t.Fatalf("retry did not annotate feedstock: %v", err)
	}
}

type corruptingRunner struct {
	root string
}

func (runner corruptingRunner) Run(_ context.Context, _ llm.Task, feedstockID, _ string) error {
	path := filepath.Join(runner.root, "feedstocks", "claude", "2026", "07", feedstockID+".md")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte("---\nunknown_field: true\n---\n"), 0o644)
}

func TestDrawReportsActualVerificationError(t *testing.T) {
	root := t.TempDir()
	logPath := filepath.Join(t.TempDir(), "session.jsonl")
	if err := os.WriteFile(logPath, []byte(
		`{"type":"user","sessionId":"session-id","timestamp":"2026-07-30T01:02:03Z","message":{"role":"user","content":"test this"}}`+"\n",
	), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{
		Root: root, Path: filepath.Join(root, ".knowbrew", "config.toml"),
		LLM: config.LLM{Backend: "claude-cli"},
	}
	summary, err := Run(context.Background(), cfg, []string{logPath}, corruptingRunner{root: root}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if summary.FeedstocksFailed != 1 || len(summary.Failures) != 1 {
		t.Fatalf("summary = %#v", summary)
	}
	reason := summary.Failures[0].Reason
	if !strings.Contains(reason, "verify annotation") || !strings.Contains(reason, "unknown_field") {
		t.Fatalf("failure reason = %q", reason)
	}
	if strings.Contains(reason, "did not finalize") {
		t.Fatalf("actual verification error was hidden: %q", reason)
	}
}

func TestDrawLockFailsImmediatelyWhenHeld(t *testing.T) {
	root := t.TempDir()
	dataStore, _ := store.New(root)
	if err := dataStore.EnsureLayout(); err != nil {
		t.Fatal(err)
	}
	lock := flock.New(filepath.Join(root, ".state", "draw.lock"))
	if err := lock.Lock(); err != nil {
		t.Fatal(err)
	}
	defer lock.Unlock()
	cfg := config.Config{
		Root: root, Path: filepath.Join(root, ".knowbrew", "config.toml"),
		LLM: config.LLM{Backend: "claude-cli"},
	}

	started := time.Now()
	_, err := Run(context.Background(), cfg, nil, nil, nil)
	if err == nil || err.Error() != "another knowbrew draw process is running" {
		t.Fatalf("error = %v", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("lock conflict took %s", elapsed)
	}
}
