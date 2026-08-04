package draw

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gofrs/flock"
	"github.com/siro33950/knowbrew/internal/adapters/config"
	"github.com/siro33950/knowbrew/internal/adapters/llm"
	"github.com/siro33950/knowbrew/internal/adapters/persistence/markdownstore"
	"github.com/siro33950/knowbrew/internal/adapters/query"
	"github.com/siro33950/knowbrew/internal/adapters/source/parser"
	"github.com/siro33950/knowbrew/internal/domain"
)

type annotatingRunner struct {
	store *store.Store
	usage llm.Usage
}

func (runner annotatingRunner) Run(_ context.Context, task llm.Task, _ string, _ string) (llm.RunResult, error) {
	switch task {
	case llm.TaskSummarize:
		return llm.RunResult{
			Output: json.RawMessage(`{"summary":"The user requested a tested change."}`),
			Usage:  runner.usage,
		}, nil
	case llm.TaskAnnotate:
		return llm.RunResult{Output: json.RawMessage(`{"assertions":[]}`), Usage: runner.usage}, nil
	default:
		return llm.RunResult{}, nil
	}
}

func TestDrawIsIdempotentWithoutPersistentSessionState(t *testing.T) {
	root := t.TempDir()
	dataStore, _ := store.New(root)
	sourceDir := t.TempDir()
	movedDir := filepath.Join(t.TempDir(), "backup")
	logPath := filepath.Join(sourceDir, "session.jsonl")
	log := `{"type":"user","sessionId":"session-id","timestamp":"2026-07-30T01:02:03Z","cwd":"/repo","gitBranch":"main","message":{"role":"user","content":"test this"}}
{"type":"user","sessionId":"session-id","timestamp":"2026-07-30T01:02:04Z","message":{"role":"user","content":"[Request interrupted by user]"}}
`
	if err := os.WriteFile(logPath, []byte(log), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{
		Root: root, Path: filepath.Join(root, ".knowbrew", "config.toml"),
		LLM: config.LLM{Backend: "claude-cli"}, Sources: []config.Source{{
			Agent: "claude", Parser: "claude", Paths: []string{sourceDir, movedDir},
		}},
	}
	runner := annotatingRunner{store: dataStore}
	first, err := Run(context.Background(), cfg, []string{logPath}, runner, nil)
	if err != nil {
		t.Fatal(err)
	}
	if first.FeedstocksAcquired != 1 || first.FeedstocksAnnotated != 1 {
		t.Fatalf("first summary = %#v", first)
	}
	second, err := Run(context.Background(), cfg, []string{logPath}, runner, nil)
	if err != nil {
		t.Fatal(err)
	}
	if second.FeedstocksAcquired != 0 || second.FeedstocksAnnotated != 0 {
		t.Fatalf("second summary = %#v", second)
	}
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
	if moved.FeedstocksAcquired != 0 || moved.FeedstocksAnnotated != 0 {
		t.Fatalf("moved summary = %#v", moved)
	}
	if _, err := os.Stat(filepath.Join(root, ".knowbrew", "state", "draw-state.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("draw persisted session state: %v", err)
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

func TestDrawSynchronizesSearchIndexAfterCompletionAndWarnsOnFailure(t *testing.T) {
	root := t.TempDir()
	cfg := config.Config{
		Root: root, Path: filepath.Join(root, ".knowbrew", "config.toml"),
		LLM: config.LLM{Backend: "claude-cli"},
	}
	index := &recordingSearchIndex{failOn: map[int]error{1: errors.New("index unavailable")}}
	summary, err := RunWithOptions(context.Background(), cfg, Options{}, nil, nil, index)
	if err != nil {
		t.Fatal(err)
	}
	if index.calls != 1 {
		t.Fatalf("search index sync calls = %d, want 1", index.calls)
	}
	if len(summary.Warnings) != 1 || !strings.Contains(summary.Warnings[0].Message, "index unavailable") {
		t.Fatalf("summary warnings = %#v", summary.Warnings)
	}
}

func TestExplicitPathsUseTheirConfiguredSources(t *testing.T) {
	claudeDir := t.TempDir()
	codexDir := t.TempDir()
	claudePath := filepath.Join(claudeDir, "01234567-89ab-cdef-0123-456789abcdef.jsonl")
	codexPath := filepath.Join(codexDir, "rollout-2026-07-30T01-00-00-019fb136-74f8-7283-8907-eb33a3cc74fd.jsonl")
	for _, path := range []string{claudePath, codexPath} {
		if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cfg := config.Config{Sources: []config.Source{
		{Agent: "claude", Parser: "claude", Paths: []string{claudeDir}},
		{Agent: "codex", Parser: "codex", Paths: []string{codexDir}},
	}}
	files, err := collectFiles(cfg, Options{Paths: []string{claudePath, codexPath}}, time.Now())
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

func TestDefaultDrawUsesOnlyLogsModifiedInLast24Hours(t *testing.T) {
	root := t.TempDir()
	dataStore, _ := store.New(root)
	sourceDir := t.TempDir()
	recentPath := filepath.Join(sourceDir, "recent.jsonl")
	oldPath := filepath.Join(sourceDir, "old.jsonl")
	for path, value := range map[string]string{
		recentPath: `{"type":"user","uuid":"recent-turn","sessionId":"recent-session","timestamp":"2026-07-30T01:02:03Z","message":{"role":"user","content":"recent"}}` + "\n" + `{"type":"user","sessionId":"recent-session","timestamp":"2026-07-30T01:02:04Z","message":{"role":"user","content":"[Request interrupted by user]"}}` + "\n",
		oldPath:    `{"type":"user","uuid":"old-turn","sessionId":"old-session","timestamp":"2026-07-29T01:02:03Z","message":{"role":"user","content":"old"}}` + "\n" + `{"type":"user","sessionId":"old-session","timestamp":"2026-07-29T01:02:04Z","message":{"role":"user","content":"[Request interrupted by user]"}}` + "\n",
	} {
		if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	oldTime := time.Now().Add(-DefaultLookback - time.Hour)
	if err := os.Chtimes(oldPath, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{
		Root: root, Path: filepath.Join(root, ".knowbrew", "config.toml"),
		LLM:     config.LLM{Backend: "claude-cli"},
		Sources: []config.Source{{Agent: "claude", Parser: "claude", Paths: []string{sourceDir}}},
	}

	first, err := Run(context.Background(), cfg, nil, annotatingRunner{store: dataStore}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if first.FeedstocksAcquired != 1 || first.FeedstocksAnnotated != 1 {
		t.Fatalf("default summary = %#v", first)
	}
	if _, _, err := dataStore.FindFeedstock(parser.FeedstockID("claude", "old-session", "old-turn")); err == nil {
		t.Fatal("default draw acquired a session older than 24 hours")
	}

	second, err := RunWithOptions(
		context.Background(),
		cfg,
		Options{MaxTurns: 1},
		annotatingRunner{store: dataStore},
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if second.FeedstocksAcquired != 1 || second.FeedstocksAnnotated != 1 {
		t.Fatalf("--max summary = %#v", second)
	}
}

func TestMaxTurnsProcessesNewestUnfinishedTurnsAndReportsBacklog(t *testing.T) {
	root := t.TempDir()
	dataStore, _ := store.New(root)
	sourceDir := t.TempDir()
	logPath := filepath.Join(sourceDir, "session.jsonl")
	log := `{"type":"user","uuid":"turn-1","sessionId":"backfill","timestamp":"2026-07-28T01:02:03Z","message":{"role":"user","content":"oldest"}}
{"type":"user","uuid":"turn-2","sessionId":"backfill","timestamp":"2026-07-29T01:02:03Z","message":{"role":"user","content":"middle"}}
{"type":"user","uuid":"turn-3","sessionId":"backfill","timestamp":"2026-07-30T01:02:03Z","message":{"role":"user","content":"newest"}}
{"type":"user","sessionId":"backfill","timestamp":"2026-07-30T01:02:04Z","message":{"role":"user","content":"[Request interrupted by user]"}}
`
	if err := os.WriteFile(logPath, []byte(log), 0o600); err != nil {
		t.Fatal(err)
	}
	oldTime := time.Now().Add(-DefaultLookback - time.Hour)
	if err := os.Chtimes(logPath, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{
		Root: root, Path: filepath.Join(root, ".knowbrew", "config.toml"),
		LLM:     config.LLM{Backend: "claude-cli"},
		Sources: []config.Source{{Agent: "claude", Parser: "claude", Paths: []string{sourceDir}}},
	}

	first, err := RunWithOptions(
		context.Background(), cfg, Options{MaxTurns: 2},
		annotatingRunner{store: dataStore}, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if first.TurnsSelected != 2 || first.TurnsPending != 1 ||
		first.FeedstocksAcquired != 2 || first.FeedstocksAnnotated != 2 {
		t.Fatalf("first summary = %#v", first)
	}
	for _, turnID := range []string{"turn-2", "turn-3"} {
		id := parser.FeedstockID("claude", "backfill", turnID)
		if _, _, err := dataStore.FindFeedstock(id); err != nil {
			t.Fatalf("newest selected turn %s was not processed: %v", turnID, err)
		}
	}
	oldestID := parser.FeedstockID("claude", "backfill", "turn-1")
	if _, _, err := dataStore.FindFeedstock(oldestID); err == nil {
		t.Fatal("oldest turn exceeded --max")
	}

	second, err := RunWithOptions(
		context.Background(), cfg, Options{MaxTurns: 2},
		annotatingRunner{store: dataStore}, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if second.TurnsSelected != 1 || second.TurnsPending != 0 ||
		second.FeedstocksAcquired != 1 || second.FeedstocksAnnotated != 1 {
		t.Fatalf("second summary = %#v", second)
	}
}

func TestMaxTurnsPrioritizesPreviouslyAcquiredIncompleteFeedstock(t *testing.T) {
	root := t.TempDir()
	dataStore, _ := store.New(root)
	sourceDir := t.TempDir()
	logPath := filepath.Join(sourceDir, "session.jsonl")
	log := `{"type":"user","uuid":"turn-1","sessionId":"resume","timestamp":"2026-07-29T01:02:03Z","message":{"role":"user","content":"incomplete"}}
{"type":"user","uuid":"turn-2","sessionId":"resume","timestamp":"2026-07-30T01:02:03Z","message":{"role":"user","content":"new"}}
{"type":"user","sessionId":"resume","timestamp":"2026-07-30T01:02:04Z","message":{"role":"user","content":"[Request interrupted by user]"}}
`
	if err := os.WriteFile(logPath, []byte(log), 0o600); err != nil {
		t.Fatal(err)
	}
	incompleteID := parser.FeedstockID("claude", "resume", "turn-1")
	if err := dataStore.WriteFeedstock(domain.Feedstock{
		Schema: domain.SchemaVersion, ID: incompleteID, TurnID: "turn-1",
		Session:   domain.SessionRef{ID: "resume"},
		Timestamp: time.Date(2026, 7, 29, 1, 2, 3, 0, time.UTC), Agent: "claude",
	}); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{
		Root: root, Path: filepath.Join(root, ".knowbrew", "config.toml"),
		LLM:     config.LLM{Backend: "claude-cli"},
		Sources: []config.Source{{Agent: "claude", Parser: "claude", Paths: []string{sourceDir}}},
	}

	summary, err := RunWithOptions(
		context.Background(), cfg, Options{Paths: []string{logPath}, MaxTurns: 1},
		annotatingRunner{store: dataStore}, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if summary.TurnsSelected != 1 || summary.TurnsPending != 1 ||
		summary.FeedstocksAcquired != 0 || summary.FeedstocksAnnotated != 1 {
		t.Fatalf("summary = %#v", summary)
	}
	completed, _, err := dataStore.FindFeedstock(incompleteID)
	if err != nil {
		t.Fatal(err)
	}
	if completed.AnnotatedAt == nil {
		t.Fatal("previously acquired incomplete feedstock was not resumed first")
	}
	newID := parser.FeedstockID("claude", "resume", "turn-2")
	if _, _, err := dataStore.FindFeedstock(newID); err == nil {
		t.Fatal("new turn displaced a previously acquired incomplete feedstock")
	}
}

func TestCollectFilesCanLimitConfiguredAgentSources(t *testing.T) {
	claudeDir := t.TempDir()
	codexDir := t.TempDir()
	claudePath := filepath.Join(claudeDir, "session.jsonl")
	codexPath := filepath.Join(codexDir, "rollout-session.jsonl")
	for _, path := range []string{claudePath, codexPath} {
		if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cfg := config.Config{Sources: []config.Source{
		{Agent: "claude", Parser: "claude", Paths: []string{claudeDir}},
		{Agent: "codex", Parser: "codex", Paths: []string{codexDir}},
	}}
	files, err := collectFiles(cfg, Options{Sources: []string{"codex"}}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files[0].Agent != "codex" || files[0].Path != codexPath {
		t.Fatalf("files = %#v", files)
	}
}

func TestCollectFilesRejectsConflictingOrInvalidScopeOptions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	tests := []Options{
		{Paths: []string{path}, Sources: []string{"claude"}},
		{Sources: []string{"other"}},
		{MaxTurns: -1},
	}
	for _, options := range tests {
		if _, err := collectFiles(config.Config{}, options, now); err == nil {
			t.Errorf("options %#v were accepted", options)
		}
	}
}

func TestDrawClassifiesOnlyFeedstocksFromSelectedSessions(t *testing.T) {
	root := t.TempDir()
	dataStore, _ := store.New(root)
	unrelated := domain.Feedstock{
		Schema:    domain.SchemaVersion,
		ID:        parser.FeedstockID("claude", "unrelated-session", "unrelated-turn"),
		TurnID:    "unrelated-turn",
		Session:   domain.SessionRef{ID: "unrelated-session"},
		Timestamp: time.Now().Add(-48 * time.Hour), Agent: "claude",
	}
	if err := dataStore.WriteFeedstock(unrelated); err != nil {
		t.Fatal(err)
	}
	sourceDir := t.TempDir()
	logPath := filepath.Join(sourceDir, "selected.jsonl")
	if err := os.WriteFile(logPath, []byte(
		`{"type":"user","uuid":"selected-turn","sessionId":"selected-session","timestamp":"2026-07-30T01:02:03Z","message":{"role":"user","content":"selected"}}`+"\n"+`{"type":"user","sessionId":"selected-session","timestamp":"2026-07-30T01:02:04Z","message":{"role":"user","content":"[Request interrupted by user]"}}`+"\n",
	), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{
		Root: root, Path: filepath.Join(root, ".knowbrew", "config.toml"),
		LLM:     config.LLM{Backend: "claude-cli"},
		Sources: []config.Source{{Agent: "claude", Parser: "claude", Paths: []string{sourceDir}}},
	}

	summary, err := Run(
		context.Background(), cfg, []string{logPath}, annotatingRunner{store: dataStore}, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if summary.FeedstocksAnnotated != 1 {
		t.Fatalf("summary = %#v", summary)
	}
	unchanged, _, err := dataStore.FindFeedstock(unrelated.ID)
	if err != nil {
		t.Fatal(err)
	}
	if unchanged.AnnotatedAt != nil {
		t.Fatal("draw classified an unannotated feedstock outside the selected sessions")
	}
}

func TestConcurrentDrawsWaitAndRemainIdempotent(t *testing.T) {
	root := t.TempDir()
	dataStore, _ := store.New(root)
	sourceDir := t.TempDir()
	logPath := filepath.Join(sourceDir, "session.jsonl")
	if err := os.WriteFile(logPath, []byte(
		`{"type":"user","uuid":"turn","sessionId":"session","timestamp":"2026-07-30T01:02:03Z","message":{"role":"user","content":"concurrent"}}`+"\n"+`{"type":"user","sessionId":"session","timestamp":"2026-07-30T01:02:04Z","message":{"role":"user","content":"[Request interrupted by user]"}}`+"\n",
	), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{
		Root: root, Path: filepath.Join(root, ".knowbrew", "config.toml"),
		LLM:     config.LLM{Backend: "claude-cli"},
		Sources: []config.Source{{Agent: "claude", Parser: "claude", Paths: []string{sourceDir}}},
	}
	start := make(chan struct{})
	results := make(chan Summary, 2)
	errors := make(chan error, 2)
	for range 2 {
		go func() {
			<-start
			summary, err := Run(
				context.Background(), cfg, nil, annotatingRunner{store: dataStore}, nil,
			)
			results <- summary
			errors <- err
		}()
	}
	close(start)
	var acquired, annotated int
	for range 2 {
		if err := <-errors; err != nil {
			t.Fatal(err)
		}
		summary := <-results
		acquired += summary.FeedstocksAcquired
		annotated += summary.FeedstocksAnnotated
	}
	if acquired != 1 || annotated != 1 {
		t.Fatalf("combined summaries acquired = %d, annotated = %d", acquired, annotated)
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

func TestEnsureRepositorySubjectDoesNotCreateFromNonRepositoryCWD(t *testing.T) {
	dataStore, err := store.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := dataStore.EnsureLayout(); err != nil {
		t.Fatal(err)
	}
	candidate := domain.FeedstockCandidate{CWD: t.TempDir()}
	added, warnings, err := ensureRepositorySubjectForTest(context.Background(), dataStore, &candidate)
	if err != nil {
		t.Fatal(err)
	}
	if added != 0 || len(warnings) != 0 || candidate.Repo != "" {
		t.Fatalf("candidate = %#v, added = %d, warnings = %#v", candidate, added, warnings)
	}
	masters, warnings, err := dataStore.LoadMasters("subjects")
	if err != nil {
		t.Fatal(err)
	}
	if len(masters) != 0 || len(warnings) != 0 {
		t.Fatalf("masters = %#v, warnings = %#v", masters, warnings)
	}
}

func TestEnsureRepositorySubjectCreatesMasterWithoutAssigningFeedstock(t *testing.T) {
	dataStore, err := store.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	candidate := domain.FeedstockCandidate{
		CWD:  "/workspace/knowbrew",
		Repo: "https://github.com/example/knowbrew.git",
	}
	added, _, err := ensureRepositorySubjectForTest(context.Background(), dataStore, &candidate)
	if err != nil {
		t.Fatal(err)
	}
	if added != 1 {
		t.Fatalf("candidate = %#v, added = %d", candidate, added)
	}
	masters, warnings, err := dataStore.LoadMasters("subjects")
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 0 || len(masters) != 1 ||
		masters[0].Name != "knowbrew" ||
		masters[0].Definition != "" {
		t.Fatalf("masters = %#v, warnings = %#v", masters, warnings)
	}
}

func TestEnsureRepositorySubjectAddsAliasesToSameNamedMasterWithoutAssignment(t *testing.T) {
	dataStore, err := store.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := dataStore.EnsureMaster("subjects", domain.MasterEntry{
		Name:       "knowbrew",
		Definition: "The existing knowbrew subject.",
	}); err != nil {
		t.Fatal(err)
	}
	candidate := domain.FeedstockCandidate{
		CWD:  "/workspace/knowbrew",
		Repo: "ssh://git@github.com/siro33950/knowbrew.git",
	}
	added, warnings, err := ensureRepositorySubjectForTest(context.Background(), dataStore, &candidate)
	if err != nil {
		t.Fatal(err)
	}
	if added != 0 || len(warnings) != 0 {
		t.Fatalf("candidate = %#v, added = %d, warnings = %#v", candidate, added, warnings)
	}
	masters, warnings, err := dataStore.LoadMasters("subjects")
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 0 || len(masters) != 1 ||
		masters[0].Name != "knowbrew" ||
		masters[0].Definition != "The existing knowbrew subject." ||
		strings.Join(masters[0].Aliases, ",") !=
			"/workspace/knowbrew,ssh://git@github.com/siro33950/knowbrew.git" {
		t.Fatalf("masters = %#v, warnings = %#v", masters, warnings)
	}
	hashedPath := filepath.Join(
		dataStore.Root,
		"masters",
		"subjects",
		"knowbrew-20b44a7e.md",
	)
	if _, err := os.Stat(hashedPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("hashed master exists or stat failed: %v", err)
	}
}

func TestEnsureRepositorySubjectHashesOnlyConfirmedRepositoryCollision(t *testing.T) {
	dataStore, err := store.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := dataStore.EnsureMaster("subjects", domain.MasterEntry{
		Name:       "knowbrew",
		Definition: "Another repository with the same basename.",
		Aliases:    []string{"https://github.com/example/knowbrew.git"},
	}); err != nil {
		t.Fatal(err)
	}
	candidate := domain.FeedstockCandidate{
		CWD:  "/workspace/knowbrew",
		Repo: "ssh://git@github.com/siro33950/knowbrew.git",
	}
	added, warnings, err := ensureRepositorySubjectForTest(context.Background(), dataStore, &candidate)
	if err != nil {
		t.Fatal(err)
	}
	if added != 1 || len(warnings) != 0 {
		t.Fatalf("candidate = %#v, added = %d, warnings = %#v", candidate, added, warnings)
	}
	masters, warnings, err := dataStore.LoadMasters("subjects")
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 0 || len(masters) != 2 {
		t.Fatalf("masters = %#v, warnings = %#v", masters, warnings)
	}
}

func TestEnsureRepositorySubjectDoesNotAssignCWDMasterAlias(t *testing.T) {
	dataStore, err := store.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cwd := t.TempDir()
	if _, err := dataStore.EnsureMaster("subjects", domain.MasterEntry{
		Name: "vault", Definition: "The configured knowledge vault.", Aliases: []string{cwd},
	}); err != nil {
		t.Fatal(err)
	}
	candidate := domain.FeedstockCandidate{CWD: cwd}
	added, _, err := ensureRepositorySubjectForTest(context.Background(), dataStore, &candidate)
	if err != nil {
		t.Fatal(err)
	}
	if added != 0 || candidate.Repo != "" {
		t.Fatalf("candidate = %#v, added = %d", candidate, added)
	}
}

func TestEnsureRepositorySubjectDiscoversRepositoryWithoutAssignment(t *testing.T) {
	dataStore, err := store.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	repository := t.TempDir()
	runGit(t, repository, "init")
	runGit(t, repository, "remote", "add", "origin", "git@github.com:example/discovered.git")
	candidate := domain.FeedstockCandidate{CWD: repository}
	added, _, err := ensureRepositorySubjectForTest(context.Background(), dataStore, &candidate)
	if err != nil {
		t.Fatal(err)
	}
	if added != 1 || candidate.Repo != "git@github.com:example/discovered.git" {
		t.Fatalf("candidate = %#v, added = %d", candidate, added)
	}
}

func runGit(t *testing.T, repository string, arguments ...string) {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", repository}, arguments...)...)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", arguments, err, output)
	}
}

func TestAnnotationPromptIncludesFilteredDialogueWithoutReadInstructions(t *testing.T) {
	dataStore, err := store.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := dataStore.EnsureLayout(); err != nil {
		t.Fatal(err)
	}
	typeDir := filepath.Join(dataStore.Root, "masters", "types")
	typeFiles, err := os.ReadDir(typeDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range typeFiles {
		if err := os.Remove(filepath.Join(typeDir, entry.Name())); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := dataStore.EnsureMaster("types", domain.MasterEntry{
		Name:       "observation",
		Definition: "A verified observation from the dialogue.",
		Example:    "The custom parser accepts nested records.",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := dataStore.EnsureMaster("subjects", domain.MasterEntry{
		Name: "agent-model", Definition: "Model-specific agent behavior.",
		Includes: []string{"behavior caused by the selected model"},
		Excludes: []string{"agent prompt architecture"},
		Aliases:  []string{"/private/machine/path"},
	}); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(dataStore.Root, "session.jsonl")
	log := `{"type":"user","uuid":"turn-target","sessionId":"session","timestamp":"2026-07-30T01:00:00Z","message":{"role":"user","content":"FULL USER REQUEST"}}
{"type":"assistant","sessionId":"session","timestamp":"2026-07-30T01:00:01Z","message":{"role":"assistant","content":[{"type":"thinking","text":"SECRET THINKING"},{"type":"text","text":"VISIBLE ASSISTANT RESPONSE"},{"type":"tool_use","id":"tool-1","name":"Bash","input":{"command":"SECRET TOOL CALL"}}]}}
{"type":"user","sessionId":"session","timestamp":"2026-07-30T01:00:02Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"tool-1","content":"SECRET TOOL OUTPUT"}]}}
{"type":"assistant","sessionId":"session","timestamp":"2026-07-30T01:00:03Z","message":{"role":"assistant","stop_reason":"end_turn","content":[]}}
`
	if err := os.WriteFile(logPath, []byte(log), 0o600); err != nil {
		t.Fatal(err)
	}
	target := domain.Feedstock{
		Schema:    domain.SchemaVersion,
		ID:        parser.FeedstockID("claude", "session", "turn-target"),
		TurnID:    "turn-target",
		Session:   domain.SessionRef{ID: "session"},
		Timestamp: time.Date(2026, 7, 30, 1, 0, 0, 0, time.UTC),
		Agent:     "claude", CWD: "/vault", Repo: "https://github.com/example/knowbrew.git",
	}
	if err := dataStore.WriteFeedstock(target); err != nil {
		t.Fatal(err)
	}
	feedstocks, warnings, err := dataStore.ListFeedstocks()
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 0 {
		t.Fatalf("feedstock warnings = %#v", warnings)
	}
	cfg := config.Config{Path: "/configured/config.toml", Draw: config.Draw{ContextTurns: 0}}
	summaryText, warnings, err := summaryPromptForTest(cfg, dataStore, target.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 0 {
		t.Fatalf("warnings = %#v", warnings)
	}
	for _, required := range []string{
		target.ID, `"user_input": "FULL USER REQUEST"`,
		`"agent_response": "VISIBLE ASSISTANT RESPONSE"`,
		"Return one JSON object containing only summary",
		"only the supplied user_input", "supplied agent_response action and result",
	} {
		if !strings.Contains(summaryText, required) {
			t.Fatalf("summary prompt does not contain %q:\n%s", required, summaryText)
		}
	}
	for _, forbidden := range []string{"SECRET THINKING", "SECRET TOOL CALL", "SECRET TOOL OUTPUT", "subject_master", "knowledge_type_master", "prior_turns", "feedstock summarize"} {
		if strings.Contains(summaryText, forbidden) {
			t.Fatalf("summary prompt contains %q:\n%s", forbidden, summaryText)
		}
	}
	assertionText, warnings, err := annotationPromptForTest(cfg, dataStore, target.ID, feedstocks)
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 0 {
		t.Fatalf("warnings = %#v", warnings)
	}
	for _, required := range []string{
		`"target_user_input": "FULL USER REQUEST"`, `"prior_turns": []`,
		`"cwd": "/vault"`, `"repo": "https://github.com/example/knowbrew.git"`,
		`run "knowbrew feedstock context ` + target.ID + `" exactly once`,
		"target agent response, generated summary, and future turns are deliberately absent",
		"Read all of target_user_input before inspecting prior_turns",
		"A user instruction to add, remove, change, preserve, or use persistent subject behavior establishes the requested resulting behavior",
		"Do not let an earlier acknowledgement in the same message replace, hide, or weaken a later direct clause",
		"Do not promote supporting explanation, examples, rationale, implementation mechanics, consequences, or a definition of every named term",
		"Do not turn one broad approval into separate assertions for every explanatory sentence",
		"Re-read target_user_input clause by clause",
		"Treat knowledge_type_master as the sole authority",
		"Choose only existing subjects", `"name": "observation"`, `"name": "agent-model"`,
	} {
		if !strings.Contains(assertionText, required) {
			t.Fatalf("assertion prompt does not contain %q:\n%s", required, assertionText)
		}
	}
	for _, forbidden := range []string{
		"VISIBLE ASSISTANT RESPONSE", "SECRET THINKING", "SECRET TOOL CALL", "SECRET TOOL OUTPUT",
		`"summary"`, `"target_offset"`, `"offset": 0`, `"offset": 1`,
	} {
		if strings.Contains(assertionText, forbidden) {
			t.Fatalf("assertion prompt contains %q:\n%s", forbidden, assertionText)
		}
	}
	stages := []string{
		"1. Target decomposition.",
		"2. Direct target meanings.",
		"3. Bounded reference resolution.",
		"4. Approval scope.",
		"5. Meaning consolidation.",
		"6. Type qualification.",
		"7. Atomic assertions.",
		"8. Subject expansion.",
		"9. Coverage audit and return.",
	}
	previous := -1
	for _, stage := range stages {
		position := strings.Index(assertionText, stage)
		if position <= previous {
			t.Fatalf("assertion prompt stage %q is missing or out of order:\n%s", stage, assertionText)
		}
		previous = position
	}
}

func TestWritingGuidesApplyOnlyToAssertionExtraction(t *testing.T) {
	dataStore, err := store.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := dataStore.EnsureLayout(); err != nil {
		t.Fatal(err)
	}
	writingDirectory := filepath.Join(dataStore.Root, "masters", "writing")
	for name, content := range map[string]string{
		"common.md":    "COMMON WRITING RULE",
		"knowledge.md": "KNOWLEDGE WRITING RULE",
		"document.md":  "DOCUMENT WRITING RULE",
	} {
		if err := os.WriteFile(filepath.Join(writingDirectory, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	target := writePromptTarget(
		t,
		dataStore,
		"session-writing",
		"turn-writing",
		time.Now().UTC(),
		"Use precise terminology.",
		"Done.",
	)
	feedstocks, _, err := dataStore.ListFeedstocks()
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{Path: "/configured/config.toml"}
	assertionText, _, err := annotationPromptForTest(cfg, dataStore, target.ID, feedstocks)
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{"COMMON WRITING RULE", "KNOWLEDGE WRITING RULE"} {
		if !strings.Contains(assertionText, required) {
			t.Fatalf("assertion prompt does not contain %q:\n%s", required, assertionText)
		}
	}
	if strings.Contains(assertionText, "DOCUMENT WRITING RULE") {
		t.Fatalf("assertion prompt contains document-only rules:\n%s", assertionText)
	}
	if strings.Contains(assertionText, "Never prefix it with Absolute: or Default:") {
		t.Fatalf("assertion prompt retained externalized strength wording:\n%s", assertionText)
	}

	summaryText, _, err := summaryPromptForTest(cfg, dataStore, target.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{
		"COMMON WRITING RULE", "KNOWLEDGE WRITING RULE", "DOCUMENT WRITING RULE",
	} {
		if strings.Contains(summaryText, forbidden) {
			t.Fatalf("summary prompt contains writing guide %q:\n%s", forbidden, summaryText)
		}
	}
}

func TestAnnotationPromptMarksMissingAssistantResponse(t *testing.T) {
	dataStore, err := store.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := dataStore.EnsureLayout(); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(dataStore.Root, "session.jsonl")
	log := `{"type":"user","uuid":"turn-without-assistant","sessionId":"session","timestamp":"2026-07-30T01:00:00Z","message":{"role":"user","content":"UNANSWERED USER REQUEST"}}
{"type":"user","sessionId":"session","timestamp":"2026-07-30T01:00:01Z","message":{"role":"user","content":"[Request interrupted by user]"}}
`
	if err := os.WriteFile(logPath, []byte(log), 0o600); err != nil {
		t.Fatal(err)
	}
	target := domain.Feedstock{
		Schema:    domain.SchemaVersion,
		ID:        parser.FeedstockID("claude", "session", "turn-without-assistant"),
		TurnID:    "turn-without-assistant",
		Session:   domain.SessionRef{ID: "session"},
		Timestamp: time.Date(2026, 7, 30, 1, 0, 0, 0, time.UTC),
		Agent:     "claude",
	}
	if err := dataStore.WriteFeedstock(target); err != nil {
		t.Fatal(err)
	}
	prompt, _, err := summaryPromptForTest(
		config.Config{Path: "/configured/config.toml"},
		dataStore,
		target.ID,
	)
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{
		`"user_input": "UNANSWERED USER REQUEST"`,
		"UNANSWERED USER REQUEST",
		"When agent_response is absent",
		"do not invent an action or result",
	} {
		if !strings.Contains(prompt, required) {
			t.Fatalf("prompt does not contain %q:\n%s", required, prompt)
		}
	}
	if strings.Contains(prompt, `"agent_response"`) {
		t.Fatalf("missing response was serialized as agent_response:\n%s", prompt)
	}
}

func TestSummaryUsesMastersAddedJSONName(t *testing.T) {
	data, err := json.Marshal(Summary{
		TurnsSelected: 3,
		TurnsPending:  7,
		MastersAdded:  2,
		Usage: llm.NewUsageReport("api", "priced-model", llm.Usage{
			InputTokens:           100,
			CachedInputTokens:     30,
			CacheWriteInputTokens: 20,
			OutputTokens:          10,
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"turns_selected":3`) ||
		!strings.Contains(string(data), `"turns_pending":7`) ||
		!strings.Contains(string(data), `"masters_added":2`) ||
		strings.Contains(string(data), "masters_pending_added") {
		t.Fatalf("summary JSON = %s", data)
	}
	for _, required := range []string{
		`"backend":"api"`,
		`"model":"priced-model"`,
		`"total_input_tokens":100`,
		`"standard_input_tokens":50`,
		`"cache_read_input_tokens":30`,
		`"cache_write_input_tokens":20`,
		`"output_tokens":10`,
		`"total_tokens":110`,
	} {
		if !strings.Contains(string(data), required) {
			t.Fatalf("summary JSON does not contain %s: %s", required, data)
		}
	}
}

func TestSummaryPromptLimitsOnlyAssistantResponse(t *testing.T) {
	dataStore, err := store.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	userText := strings.Repeat("U", annotationAssistantLimitBytes+2000)
	assistantText := strings.Repeat("A", annotationAssistantLimitBytes+2000)
	target := writePromptTarget(
		t,
		dataStore,
		"session-long",
		"turn-long",
		time.Now().UTC(),
		userText,
		assistantText,
	)
	prompt, _, err := summaryPromptForTest(
		config.Config{Path: "/configured/config.toml", Draw: config.Draw{ContextTurns: 0}},
		dataStore,
		target.ID,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(prompt, userText) {
		t.Fatal("the full user message was truncated")
	}
	if strings.Contains(prompt, assistantText) {
		t.Fatal("the over-limit assistant response was not truncated")
	}
	if !strings.Contains(prompt, strings.TrimSpace(annotationAssistantTruncatedMarker)) {
		t.Fatalf("assistant truncation marker is missing:\n%s", prompt)
	}
	if !strings.Contains(prompt, strings.Repeat("A", annotationAssistantLimitBytes/3)) {
		t.Fatal("assistant response prefix was not retained")
	}
}

func TestAnnotationPromptIncludesOnlyThreePriorTurnsWithinSession(t *testing.T) {
	dataStore, err := store.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	target := writePromptTarget(
		t,
		dataStore,
		"session-context",
		"turn-target",
		base,
		"TARGET USER",
		"TARGET ASSISTANT",
	)
	makeCandidate := func(id, turnID, user, response string) domain.FeedstockCandidate {
		return domain.FeedstockCandidate{
			ID: id, TurnID: turnID, Session: target.Session,
			Timestamp: base, Agent: target.Agent,
			Dialogue: []domain.DialogueMessage{
				{Role: "user", Content: user},
				{Role: "assistant", Content: response},
			},
		}
	}
	candidates := make([]domain.FeedstockCandidate, 0, 11)
	for offset := 4; offset >= 1; offset-- {
		candidates = append(candidates, makeCandidate(
			fmt.Sprintf("fs-before-%d", offset),
			fmt.Sprintf("turn-before-%d", offset),
			fmt.Sprintf("BEFORE QUOTE %d", offset),
			fmt.Sprintf("BEFORE RESPONSE %d", offset),
		))
	}
	candidates = append(candidates,
		domain.FeedstockCandidate{
			ID: "fs-other-session", TurnID: "turn-other",
			Session:   domain.SessionRef{ID: "other-session"},
			Timestamp: base, Agent: "claude",
			Dialogue: []domain.DialogueMessage{{Role: "user", Content: "CROSS SESSION QUOTE"}},
		},
		makeCandidate(
			target.ID, target.TurnID, "TARGET USER", "TARGET ASSISTANT",
		),
		domain.FeedstockCandidate{
			ID: "fs-other-agent", TurnID: "turn-other-agent", Session: target.Session,
			Timestamp: base, Agent: "codex",
			Dialogue: []domain.DialogueMessage{{Role: "user", Content: "CROSS AGENT QUOTE"}},
		},
	)
	for offset := 1; offset <= 4; offset++ {
		candidates = append(candidates, makeCandidate(
			fmt.Sprintf("fs-after-%d", offset),
			fmt.Sprintf("turn-after-%d", offset),
			fmt.Sprintf("AFTER QUOTE %d", offset),
			fmt.Sprintf("AFTER RESPONSE %d", offset),
		))
	}
	feedstocks, _, err := dataStore.ListFeedstocks()
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{
		Path: "/configured/config.toml",
		Draw: config.Draw{ContextTurns: 3},
	}
	snapshots := map[string][]domain.FeedstockCandidate{target.ID: candidates}
	prompt, _, err := annotationPromptForTest(cfg, dataStore, target.ID, feedstocks, snapshots)
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{
		"BEFORE QUOTE 1", "BEFORE QUOTE 2", "BEFORE QUOTE 3",
		"BEFORE RESPONSE 1", "BEFORE RESPONSE 2", "BEFORE RESPONSE 3",
		`"offset": -3`, `"offset": -1`, `"prior_turns"`,
	} {
		if !strings.Contains(prompt, required) {
			t.Fatalf("prompt does not contain %q:\n%s", required, prompt)
		}
	}
	for _, forbidden := range []string{
		"BEFORE QUOTE 4",
		"TARGET ASSISTANT",
		"AFTER QUOTE 1", "AFTER QUOTE 2", "AFTER QUOTE 3", "AFTER QUOTE 4",
		"AFTER RESPONSE 1", "AFTER RESPONSE 2", "AFTER RESPONSE 3", "AFTER RESPONSE 4",
		"CROSS SESSION QUOTE",
		"CROSS AGENT QUOTE",
	} {
		if strings.Contains(prompt, forbidden) {
			t.Fatalf("prompt crossed its context boundary with %q:\n%s", forbidden, prompt)
		}
	}

	withoutContext := cfg
	withoutContext.Draw.ContextTurns = 0
	prompt, _, err = annotationPromptForTest(withoutContext, dataStore, target.ID, feedstocks, snapshots)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{
		`"offset": -1`,
		`"offset": 1`,
		"BEFORE QUOTE 1",
		"AFTER QUOTE 1",
	} {
		if strings.Contains(prompt, forbidden) {
			t.Fatalf("zero-context prompt contains %q:\n%s", forbidden, prompt)
		}
	}
}

func TestAnnotationPromptIncludesBoundedPriorDialogueOnly(t *testing.T) {
	dataStore, err := store.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sessionID := "session-assistant-context"
	base := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	logPath := filepath.Join(dataStore.Root, sessionID+".jsonl")
	previousAssistant := "PREVIOUS ASSISTANT BEGIN\n" +
		strings.Repeat("x", annotationContextAssistantLimitBytes) +
		"\nPREVIOUS ASSISTANT TAIL"
	afterAssistant := "AFTER ASSISTANT MUST NOT BE INCLUDED"
	log := fmt.Sprintf(
		"{\"type\":\"user\",\"uuid\":\"turn-before\",\"sessionId\":%q,\"timestamp\":%q,\"message\":{\"role\":\"user\",\"content\":\"Choose option A or B\"}}\n"+
			"{\"type\":\"assistant\",\"sessionId\":%q,\"timestamp\":%q,\"message\":{\"role\":\"assistant\",\"content\":[{\"type\":\"text\",\"text\":%q}]}}\n"+
			"{\"type\":\"user\",\"uuid\":\"turn-target\",\"sessionId\":%q,\"timestamp\":%q,\"message\":{\"role\":\"user\",\"content\":\"OK\"}}\n"+
			"{\"type\":\"assistant\",\"sessionId\":%q,\"timestamp\":%q,\"message\":{\"role\":\"assistant\",\"content\":[{\"type\":\"text\",\"text\":\"Applied option A\"}]}}\n"+
			"{\"type\":\"user\",\"uuid\":\"turn-after\",\"sessionId\":%q,\"timestamp\":%q,\"message\":{\"role\":\"user\",\"content\":\"What next?\"}}\n"+
			"{\"type\":\"assistant\",\"sessionId\":%q,\"timestamp\":%q,\"message\":{\"role\":\"assistant\",\"stop_reason\":\"end_turn\",\"content\":[{\"type\":\"text\",\"text\":%q}]}}\n",
		sessionID,
		base.Add(-time.Minute).Format(time.RFC3339Nano),
		sessionID,
		base.Add(-time.Minute+time.Second).Format(time.RFC3339Nano),
		previousAssistant,
		sessionID,
		base.Format(time.RFC3339Nano),
		sessionID,
		base.Add(time.Second).Format(time.RFC3339Nano),
		sessionID,
		base.Add(time.Minute).Format(time.RFC3339Nano),
		sessionID,
		base.Add(time.Minute+time.Second).Format(time.RFC3339Nano),
		afterAssistant,
	)
	if err := os.WriteFile(logPath, []byte(log), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, turn := range []struct {
		id        string
		timestamp time.Time
		quote     string
	}{
		{id: "turn-before", timestamp: base.Add(-time.Minute), quote: "Choose option A or B"},
		{id: "turn-target", timestamp: base, quote: "OK"},
		{id: "turn-after", timestamp: base.Add(time.Minute), quote: "What next?"},
	} {
		feedstock := domain.Feedstock{
			Schema:    domain.SchemaVersion,
			ID:        parser.FeedstockID("claude", sessionID, turn.id),
			TurnID:    turn.id,
			Session:   domain.SessionRef{ID: sessionID},
			Timestamp: turn.timestamp,
			Agent:     "claude",
		}
		if err := dataStore.WriteFeedstock(feedstock); err != nil {
			t.Fatal(err)
		}
	}
	feedstocks, _, err := dataStore.ListFeedstocks()
	if err != nil {
		t.Fatal(err)
	}
	targetID := parser.FeedstockID("claude", sessionID, "turn-target")
	prompt, _, err := annotationPromptForTest(
		config.Config{
			Path: "/configured/config.toml",
			Draw: config.Draw{ContextTurns: 1},
		},
		dataStore,
		targetID,
		feedstocks,
	)
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{
		`"target_user_input": "OK"`,
		`"prior_turns"`,
		`"agent_response"`,
		`"offset": -1`,
		strings.TrimSpace(annotationContextAssistantTruncatedMarker),
		"PREVIOUS ASSISTANT BEGIN",
		"PREVIOUS ASSISTANT TAIL",
	} {
		if !strings.Contains(prompt, required) {
			t.Fatalf("prompt does not contain %q:\n%s", required, prompt)
		}
	}
	for _, forbidden := range []string{"Applied option A", "What next?", afterAssistant, `"offset": 0`, `"offset": 1`} {
		if strings.Contains(prompt, forbidden) {
			t.Fatalf("prompt contains target response or future context %q:\n%s", forbidden, prompt)
		}
	}
}

func TestAnnotationContextHandlesSourceEdges(t *testing.T) {
	base := time.Now().UTC()
	candidates := []domain.FeedstockCandidate{
		{ID: "first", Agent: "claude", Session: domain.SessionRef{ID: "session"}, Timestamp: base, Dialogue: []domain.DialogueMessage{{Role: "user", Content: "first"}}},
		{ID: "middle", Agent: "claude", Session: domain.SessionRef{ID: "session"}, Timestamp: base, Dialogue: []domain.DialogueMessage{{Role: "user", Content: "middle"}}},
		{ID: "last", Agent: "claude", Session: domain.SessionRef{ID: "session"}, Timestamp: base, Dialogue: []domain.DialogueMessage{{Role: "user", Content: "last"}}},
	}
	first, err := annotationContextFromCandidates(candidates, "first", 3)
	if err != nil {
		t.Fatal(err)
	}
	if first.TargetUserInput != "first" || len(first.PriorTurns) != 0 {
		t.Fatalf("first context = %#v", first)
	}
	last, err := annotationContextFromCandidates(candidates, "last", 3)
	if err != nil {
		t.Fatal(err)
	}
	if last.TargetUserInput != "last" || len(last.PriorTurns) != 2 ||
		last.PriorTurns[0].Offset != -2 || last.PriorTurns[1].Offset != -1 {
		t.Fatalf("last context = %#v", last)
	}
}

func writePromptTarget(
	t *testing.T,
	dataStore *store.Store,
	sessionID,
	turnID string,
	timestamp time.Time,
	userText,
	assistantText string,
) domain.Feedstock {
	t.Helper()
	logPath := filepath.Join(dataStore.Root, sessionID+".jsonl")
	log := fmt.Sprintf(
		"{\"type\":\"user\",\"uuid\":%q,\"sessionId\":%q,\"timestamp\":%q,\"message\":{\"role\":\"user\",\"content\":%q}}\n"+
			"{\"type\":\"assistant\",\"sessionId\":%q,\"timestamp\":%q,\"message\":{\"role\":\"assistant\",\"stop_reason\":\"end_turn\",\"content\":[{\"type\":\"text\",\"text\":%q}]}}\n",
		turnID,
		sessionID,
		timestamp.Format(time.RFC3339Nano),
		userText,
		sessionID,
		timestamp.Add(time.Second).Format(time.RFC3339Nano),
		assistantText,
	)
	if err := os.WriteFile(logPath, []byte(log), 0o600); err != nil {
		t.Fatal(err)
	}
	feedstock := domain.Feedstock{
		Schema:    domain.SchemaVersion,
		ID:        parser.FeedstockID("claude", sessionID, turnID),
		TurnID:    turnID,
		Session:   domain.SessionRef{ID: sessionID},
		Timestamp: timestamp, Agent: "claude",
	}
	if err := dataStore.WriteFeedstock(feedstock); err != nil {
		t.Fatal(err)
	}
	return feedstock
}

type retryingAnnotatingRunner struct {
	store           *store.Store
	failFeedstockID string
	failuresLeft    int
}

func (runner *retryingAnnotatingRunner) Run(_ context.Context, task llm.Task, feedstockID, _ string) (llm.RunResult, error) {
	if task == llm.TaskSummarize {
		return llm.RunResult{Output: json.RawMessage(`{"summary":"The user requested a tested change."}`)}, nil
	}
	if task != llm.TaskAnnotate {
		return llm.RunResult{}, nil
	}
	if feedstockID == runner.failFeedstockID && runner.failuresLeft > 0 {
		runner.failuresLeft--
		return llm.RunResult{}, errors.New("temporary annotation failure")
	}
	return llm.RunResult{Output: json.RawMessage(`{"assertions":[]}`)}, nil
}

func TestDrawContinuesAfterAnnotationFailureAndRetriesFeedstock(t *testing.T) {
	root := t.TempDir()
	dataStore, _ := store.New(root)
	logPath := filepath.Join(t.TempDir(), "session.jsonl")
	log := `{"type":"user","uuid":"turn-1","sessionId":"session-id","timestamp":"2026-07-30T01:02:03Z","cwd":"/repo","message":{"role":"user","content":"first"}}
{"type":"user","uuid":"turn-2","sessionId":"session-id","timestamp":"2026-07-30T01:02:04Z","cwd":"/repo","message":{"role":"user","content":"second"}}
{"type":"user","sessionId":"session-id","timestamp":"2026-07-30T01:02:05Z","message":{"role":"user","content":"[Request interrupted by user]"}}
`
	if err := os.WriteFile(logPath, []byte(log), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{
		Root: root, Path: filepath.Join(root, ".knowbrew", "config.toml"),
		LLM:     config.LLM{Backend: "claude-cli"},
		Sources: []config.Source{{Agent: "claude", Parser: "claude", Paths: []string{filepath.Dir(logPath)}}},
	}
	failedID := parser.FeedstockID("claude", "session-id", "turn-1")
	runner := &retryingAnnotatingRunner{
		store: dataStore, failFeedstockID: failedID, failuresLeft: 1,
	}

	var progress bytes.Buffer
	first, err := Run(context.Background(), cfg, []string{logPath}, runner, &progress)
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
	if !strings.Contains(progress.String(), "Assertion extraction failed · "+failedID) {
		t.Fatalf("assertion extraction failure was not printed:\n%s", progress.String())
	}
	failed, _, err := dataStore.FindFeedstock(failedID)
	if err != nil {
		t.Fatal(err)
	}
	if failed.AnnotatedAt != nil {
		t.Fatalf("failed feedstock was not left unannotated: %#v", failed)
	}

	second, err := Run(context.Background(), cfg, []string{logPath}, runner, nil)
	if err != nil {
		t.Fatal(err)
	}
	if second.FeedstocksFailed != 0 || second.FeedstocksAnnotated != 1 || second.FeedstocksAcquired != 0 {
		t.Fatalf("second summary = %#v", second)
	}
	if _, _, err := dataStore.FindFeedstock(failedID); err != nil {
		t.Fatalf("retry did not annotate feedstock: %v", err)
	}
}

type corruptingRunner struct {
	root string
}

func (runner corruptingRunner) Run(_ context.Context, _ llm.Task, _ string, _ string) (llm.RunResult, error) {
	return llm.RunResult{Output: json.RawMessage(`{"unknown_field":true}`)}, nil
}

func TestDrawReportsActualVerificationError(t *testing.T) {
	root := t.TempDir()
	logPath := filepath.Join(t.TempDir(), "session.jsonl")
	if err := os.WriteFile(logPath, []byte(
		`{"type":"user","sessionId":"session-id","timestamp":"2026-07-30T01:02:03Z","message":{"role":"user","content":"test this"}}`+"\n"+`{"type":"user","sessionId":"session-id","timestamp":"2026-07-30T01:02:04Z","message":{"role":"user","content":"[Request interrupted by user]"}}`+"\n",
	), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{
		Root: root, Path: filepath.Join(root, ".knowbrew", "config.toml"),
		LLM:     config.LLM{Backend: "claude-cli"},
		Sources: []config.Source{{Agent: "claude", Parser: "claude", Paths: []string{filepath.Dir(logPath)}}},
	}
	summary, err := Run(context.Background(), cfg, []string{logPath}, corruptingRunner{root: root}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if summary.FeedstocksFailed != 1 || len(summary.Failures) != 1 {
		t.Fatalf("summary = %#v", summary)
	}
	reason := summary.Failures[0].Reason
	if !strings.Contains(reason, "apply summarize result") || !strings.Contains(reason, "unknown field") {
		t.Fatalf("failure reason = %q", reason)
	}
	if strings.Contains(reason, "did not finalize") {
		t.Fatalf("actual verification error was hidden: %q", reason)
	}
}

func TestDrawLockWaitsUntilHeldLockIsReleased(t *testing.T) {
	root := t.TempDir()
	dataStore, _ := store.New(root)
	if err := dataStore.EnsureLayout(); err != nil {
		t.Fatal(err)
	}
	lock := flock.New(filepath.Join(root, ".knowbrew", "state", "draw.lock"))
	if err := lock.Lock(); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{
		Root: root, Path: filepath.Join(root, ".knowbrew", "config.toml"),
		LLM: config.LLM{Backend: "claude-cli"},
	}

	done := make(chan error, 1)
	go func() {
		_, err := Run(context.Background(), cfg, nil, nil, nil)
		done <- err
	}()
	select {
	case err := <-done:
		t.Fatalf("draw returned before lock release: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	if err := lock.Unlock(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("draw did not continue after lock release")
	}
}

type cancelOnAcquisitionWriter struct {
	cancel context.CancelFunc
}

func (writer cancelOnAcquisitionWriter) Write(data []byte) (int, error) {
	if strings.Contains(string(data), "Acquisition complete ·") {
		writer.cancel()
	}
	return len(data), nil
}

func TestAcquisitionPersistsUnannotatedFeedstockAndResumeClassifiesIt(t *testing.T) {
	root := t.TempDir()
	dataStore, _ := store.New(root)
	logPath := filepath.Join(t.TempDir(), "session.jsonl")
	log := `{"type":"user","uuid":"turn-1","sessionId":"phase-one","timestamp":"2026-07-30T01:02:03Z","cwd":"/repo","message":{"role":"user","content":"remember rawphasekeyword exactly"}}
{"type":"user","sessionId":"phase-one","timestamp":"2026-07-30T01:02:04Z","message":{"role":"user","content":"[Request interrupted by user]"}}
`
	if err := os.WriteFile(logPath, []byte(log), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{
		Root: root, Path: filepath.Join(root, ".knowbrew", "config.toml"),
		LLM: config.LLM{Backend: "claude-cli"}, Draw: config.Draw{Concurrency: 2},
		Sources: []config.Source{{Agent: "claude", Parser: "claude", Paths: []string{filepath.Dir(logPath)}}},
	}
	ctx, cancel := context.WithCancel(context.Background())
	first, err := Run(ctx, cfg, []string{logPath}, nil, cancelOnAcquisitionWriter{cancel: cancel})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context cancellation", err)
	}
	if first.FeedstocksAcquired != 1 || first.FeedstocksAnnotated != 0 {
		t.Fatalf("first summary = %#v", first)
	}
	id := parser.FeedstockID("claude", "phase-one", "turn-1")
	unannotated, _, err := dataStore.FindFeedstock(id)
	if err != nil {
		t.Fatal(err)
	}
	if unannotated.AnnotatedAt != nil || unannotated.Summary != "" || len(unannotated.Assertions) != 0 {
		t.Fatalf("phase-one feedstock = %#v", unannotated)
	}
	found, err := query.Search(context.Background(), dataStore, query.SearchOptions{
		Target: query.TargetFeedstock, Keywords: []string{"rawphasekeyword"},
		Limit: 10, MaxTokens: 1000,
	})
	if err != nil {
		t.Fatal(err)
	}
	if found.Total != 0 {
		t.Fatalf("unannotated raw text must not be indexed without a summary: %#v", found)
	}

	second, err := Run(context.Background(), cfg, []string{logPath}, annotatingRunner{store: dataStore}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if second.FeedstocksAcquired != 0 || second.FeedstocksAnnotated != 1 {
		t.Fatalf("resume summary = %#v", second)
	}
	classified, _, err := dataStore.FindFeedstock(id)
	if err != nil {
		t.Fatal(err)
	}
	if classified.AnnotatedAt == nil {
		t.Fatal("resume did not annotate the pending feedstock")
	}
}

type phaseOrderRunner struct {
	store          *store.Store
	mu             sync.Mutex
	summarizeCalls int
	annotateCalls  int
	orderViolation bool
}

func (runner *phaseOrderRunner) Run(_ context.Context, task llm.Task, _ string, _ string) (llm.RunResult, error) {
	switch task {
	case llm.TaskSummarize:
		runner.mu.Lock()
		runner.summarizeCalls++
		runner.mu.Unlock()
		return llm.RunResult{Output: json.RawMessage(`{"summary":"target-only summary"}`)}, nil
	case llm.TaskAnnotate:
		feedstocks, _, err := runner.store.ListFeedstocks()
		if err != nil {
			return llm.RunResult{}, err
		}
		for _, feedstock := range feedstocks {
			if strings.TrimSpace(feedstock.Summary) == "" {
				runner.mu.Lock()
				runner.orderViolation = true
				runner.mu.Unlock()
			}
		}
		runner.mu.Lock()
		runner.annotateCalls++
		runner.mu.Unlock()
		return llm.RunResult{Output: json.RawMessage(`{"assertions":[]}`)}, nil
	default:
		return llm.RunResult{}, nil
	}
}

type cancelOnSummaryWriter struct {
	cancel context.CancelFunc
}

func (writer cancelOnSummaryWriter) Write(data []byte) (int, error) {
	if strings.Contains(string(data), "Summarization complete ·") {
		writer.cancel()
	}
	return len(data), nil
}

func TestDrawCompletesAllSummariesBeforeAssertionsAndResumesAtAssertionPhase(t *testing.T) {
	root := t.TempDir()
	dataStore, _ := store.New(root)
	logPath := filepath.Join(t.TempDir(), "session.jsonl")
	log := `{"type":"user","uuid":"turn-1","sessionId":"phase-order","timestamp":"2026-07-30T01:02:01Z","message":{"role":"user","content":"first"}}
{"type":"user","uuid":"turn-2","sessionId":"phase-order","timestamp":"2026-07-30T01:02:02Z","message":{"role":"user","content":"second"}}
{"type":"user","uuid":"turn-3","sessionId":"phase-order","timestamp":"2026-07-30T01:02:03Z","message":{"role":"user","content":"third"}}
{"type":"user","sessionId":"phase-order","timestamp":"2026-07-30T01:02:04Z","message":{"role":"user","content":"[Request interrupted by user]"}}
`
	if err := os.WriteFile(logPath, []byte(log), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{
		Root: root, Path: filepath.Join(root, ".knowbrew", "config.toml"),
		LLM: config.LLM{Backend: "claude-cli"}, Draw: config.Draw{Concurrency: 3},
		Sources: []config.Source{{Agent: "claude", Parser: "claude", Paths: []string{filepath.Dir(logPath)}}},
	}
	firstRunner := &phaseOrderRunner{store: dataStore}
	ctx, cancel := context.WithCancel(context.Background())
	first, err := Run(ctx, cfg, []string{logPath}, firstRunner, cancelOnSummaryWriter{cancel: cancel})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("first error = %v", err)
	}
	if first.FeedstocksSummarized != 3 || first.FeedstocksAnnotated != 0 ||
		firstRunner.summarizeCalls != 3 || firstRunner.annotateCalls != 0 {
		t.Fatalf("first summary = %#v, runner = %#v", first, firstRunner)
	}
	feedstocks, warnings, err := dataStore.ListFeedstocks()
	if err != nil || len(warnings) != 0 {
		t.Fatalf("feedstocks error = %v, warnings = %#v", err, warnings)
	}
	for _, feedstock := range feedstocks {
		if feedstock.Summary == "" || feedstock.AnnotatedAt != nil {
			t.Fatalf("partial feedstock = %#v", feedstock)
		}
	}
	secondRunner := &phaseOrderRunner{store: dataStore}
	second, err := Run(context.Background(), cfg, []string{logPath}, secondRunner, nil)
	if err != nil {
		t.Fatal(err)
	}
	if second.FeedstocksSummarized != 0 || second.FeedstocksAnnotated != 3 ||
		secondRunner.summarizeCalls != 0 || secondRunner.annotateCalls != 3 || secondRunner.orderViolation {
		t.Fatalf("second summary = %#v, runner = %#v", second, secondRunner)
	}
}

type concurrentAnnotatingRunner struct {
	store       *store.Store
	usage       llm.Usage
	active      atomic.Int32
	maxActive   atomic.Int32
	releaseOnce sync.Once
	release     chan struct{}
}

func (runner *concurrentAnnotatingRunner) Run(
	ctx context.Context,
	task llm.Task,
	_ string,
	_ string,
) (llm.RunResult, error) {
	if task != llm.TaskSummarize && task != llm.TaskAnnotate {
		return llm.RunResult{}, nil
	}
	active := runner.active.Add(1)
	defer runner.active.Add(-1)
	for {
		current := runner.maxActive.Load()
		if active <= current || runner.maxActive.CompareAndSwap(current, active) {
			break
		}
	}
	if active >= 3 {
		runner.releaseOnce.Do(func() { close(runner.release) })
	}
	select {
	case <-runner.release:
	case <-ctx.Done():
		return llm.RunResult{}, ctx.Err()
	}
	if task == llm.TaskSummarize {
		return llm.RunResult{
			Output: json.RawMessage(`{"summary":"The user requested concurrent classification."}`),
			Usage:  runner.usage,
		}, nil
	}
	return llm.RunResult{Output: json.RawMessage(`{"assertions":[]}`), Usage: runner.usage}, nil
}

func TestDrawClassifiesAllFeedstocksWithConcurrentWorkers(t *testing.T) {
	root := t.TempDir()
	dataStore, _ := store.New(root)
	logPath := filepath.Join(t.TempDir(), "session.jsonl")
	var log strings.Builder
	for index := 1; index <= 8; index++ {
		fmt.Fprintf(
			&log,
			"{\"type\":\"user\",\"sessionId\":\"parallel\",\"timestamp\":\"2026-07-30T01:02:%02dZ\",\"cwd\":\"/repo\",\"message\":{\"role\":\"user\",\"content\":\"turn %d\"}}\n",
			index,
			index,
		)
	}
	log.WriteString("{\"type\":\"user\",\"sessionId\":\"parallel\",\"timestamp\":\"2026-07-30T01:03:00Z\",\"message\":{\"role\":\"user\",\"content\":\"[Request interrupted by user]\"}}\n")
	if err := os.WriteFile(logPath, []byte(log.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{
		Root: root, Path: filepath.Join(root, ".knowbrew", "config.toml"),
		LLM: config.LLM{Backend: "claude-cli"}, Draw: config.Draw{Concurrency: 3},
		Sources: []config.Source{{Agent: "claude", Parser: "claude", Paths: []string{filepath.Dir(logPath)}}},
	}
	runner := &concurrentAnnotatingRunner{
		store: dataStore,
		usage: llm.Usage{
			InputTokens: 1000, CachedInputTokens: 600, OutputTokens: 100,
		},
		release: make(chan struct{}),
	}
	var progress bytes.Buffer
	summary, err := Run(context.Background(), cfg, []string{logPath}, runner, &progress)
	if err != nil {
		t.Fatal(err)
	}
	if summary.FeedstocksAcquired != 8 || summary.FeedstocksAnnotated != 8 ||
		summary.FeedstocksFailed != 0 {
		t.Fatalf("summary = %#v", summary)
	}
	if runner.maxActive.Load() < 2 {
		t.Fatalf("maximum active workers = %d, want concurrent annotation", runner.maxActive.Load())
	}
	if !strings.Contains(
		progress.String(),
		"Summarization complete · 8/8 feedstocks · in 8.0k tokens / out 800 tokens",
	) || !strings.Contains(
		progress.String(),
		"Assertion extraction complete · 8/8 feedstocks · in 8.0k tokens / out 800 tokens",
	) {
		t.Fatalf("draw phase progress did not reach all feedstocks:\n%s", progress.String())
	}
	if summary.Usage != (llm.UsageReport{
		Backend: "claude-cli", TotalInputTokens: 16000,
		StandardInputTokens: 6400, CacheReadInputTokens: 9600,
		OutputTokens: 1600, TotalTokens: 17600,
	}) {
		t.Fatalf("usage report = %#v", summary.Usage)
	}
	feedstocks, warnings, err := dataStore.ListFeedstocks()
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 0 || len(feedstocks) != 8 {
		t.Fatalf("feedstocks = %#v, warnings = %#v", feedstocks, warnings)
	}
	for _, feedstock := range feedstocks {
		if feedstock.AnnotatedAt == nil {
			t.Fatalf("feedstock %s remained unannotated", feedstock.ID)
		}
	}
}

func TestDrawNonTTYProgressUsesPhaseLinesOnly(t *testing.T) {
	root := t.TempDir()
	dataStore, _ := store.New(root)
	logPath := filepath.Join(t.TempDir(), "session.jsonl")
	log := `{"type":"user","uuid":"turn-1","sessionId":"progress","timestamp":"2026-07-30T01:02:03Z","cwd":"/repo","message":{"role":"user","content":"first"}}
{"type":"user","uuid":"turn-2","sessionId":"progress","timestamp":"2026-07-30T01:02:04Z","cwd":"/repo","message":{"role":"user","content":"second"}}
{"type":"user","sessionId":"progress","timestamp":"2026-07-30T01:02:05Z","message":{"role":"user","content":"[Request interrupted by user]"}}
`
	if err := os.WriteFile(logPath, []byte(log), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{
		Root: root, Path: filepath.Join(root, ".knowbrew", "config.toml"),
		LLM: config.LLM{Backend: "claude-cli"}, Draw: config.Draw{Concurrency: 2},
		Sources: []config.Source{{Agent: "claude", Parser: "claude", Paths: []string{filepath.Dir(logPath)}}},
	}
	var output bytes.Buffer
	summary, err := Run(
		context.Background(),
		cfg,
		[]string{logPath},
		annotatingRunner{
			store: dataStore,
			usage: llm.Usage{
				InputTokens: 1000, CachedInputTokens: 600, OutputTokens: 100,
			},
		},
		&output,
	)
	if err != nil {
		t.Fatal(err)
	}
	if summary.FeedstocksAcquired != 2 || summary.FeedstocksAnnotated != 2 {
		t.Fatalf("summary = %#v", summary)
	}
	text := output.String()
	for _, required := range []string{
		"Acquiring · 0/1 sources · 0 feedstocks",
		"Acquisition complete · 2 feedstocks from 1 sources",
		"Summarizing · 0/2 · 2 workers · in 0 tokens / out 0 tokens",
		"Summarization complete · 2/2 feedstocks · in 2.0k tokens / out 200 tokens",
		"Extracting assertions · 0/2 · 2 workers · in 0 tokens / out 0 tokens",
		"Assertion extraction complete · 2/2 feedstocks · in 2.0k tokens / out 200 tokens",
	} {
		if !strings.Contains(text, required) {
			t.Fatalf("progress does not contain %q:\n%s", required, text)
		}
	}
	for _, forbidden := range []string{
		"Acquiring " + logPath,
		"Summarizing 1/2 complete:",
		"Extracting assertions 1/2 complete:",
	} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("non-TTY progress contains per-record line %q:\n%s", forbidden, text)
		}
	}
}

func BenchmarkDraw300TurnsByPhase(b *testing.B) {
	var acquisitionTotal, classificationTotal time.Duration
	for range b.N {
		root := b.TempDir()
		dataStore, err := store.New(root)
		if err != nil {
			b.Fatal(err)
		}
		logPath := filepath.Join(b.TempDir(), "session.jsonl")
		var log strings.Builder
		for index := 1; index <= 300; index++ {
			fmt.Fprintf(
				&log,
				"{\"type\":\"user\",\"sessionId\":\"benchmark\",\"timestamp\":\"2026-07-30T01:%02d:%02dZ\",\"cwd\":\"/repo\",\"message\":{\"role\":\"user\",\"content\":\"benchmark turn %d\"}}\n",
				(index/60)%60,
				index%60,
				index,
			)
		}
		if err := os.WriteFile(logPath, []byte(log.String()), 0o600); err != nil {
			b.Fatal(err)
		}
		cfg := config.Config{
			Root: root, Path: filepath.Join(root, ".knowbrew", "config.toml"),
			LLM: config.LLM{Backend: "claude-cli"}, Draw: config.Draw{Concurrency: 5},
		}
		ctx, cancel := context.WithCancel(context.Background())
		started := time.Now()
		first, err := Run(ctx, cfg, []string{logPath}, nil, cancelOnAcquisitionWriter{cancel: cancel})
		acquisitionTotal += time.Since(started)
		if !errors.Is(err, context.Canceled) || first.FeedstocksAcquired != 300 {
			b.Fatalf("acquisition summary = %#v, error = %v", first, err)
		}
		started = time.Now()
		second, err := Run(context.Background(), cfg, []string{logPath}, annotatingRunner{store: dataStore}, nil)
		classificationTotal += time.Since(started)
		if err != nil || second.FeedstocksAnnotated != 300 {
			b.Fatalf("classification summary = %#v, error = %v", second, err)
		}
	}
	b.ReportMetric(float64(acquisitionTotal.Microseconds())/1000/float64(b.N), "acquisition_ms/op")
	b.ReportMetric(float64(classificationTotal.Microseconds())/1000/float64(b.N), "classification_mock_ms/op")
}
