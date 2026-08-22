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
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gofrs/flock"
	"github.com/siro33950/knowbrew/internal/adapters/config"
	"github.com/siro33950/knowbrew/internal/adapters/llm"
	persistenceadapter "github.com/siro33950/knowbrew/internal/adapters/persistence"
	"github.com/siro33950/knowbrew/internal/adapters/persistence/markdownstore"
	"github.com/siro33950/knowbrew/internal/adapters/query"
	"github.com/siro33950/knowbrew/internal/adapters/source/parser"
	"github.com/siro33950/knowbrew/internal/domain"
)

type annotatingRunner struct {
	store *store.Store
	usage llm.Usage
}

type twoStageRunner struct {
	mu              sync.Mutex
	drawCalls       int
	extractionCalls int
	prompts         []string
}

func (runner *twoStageRunner) Run(
	_ context.Context,
	task llm.Task,
	_, prompt string,
) (llm.RunResult, error) {
	runner.mu.Lock()
	defer runner.mu.Unlock()
	switch task {
	case llm.TaskDraw:
		runner.drawCalls++
		return llm.RunResult{Output: json.RawMessage(
			`{"summary":"A durable property was established.","types":["property"]}`,
		)}, nil
	case llm.TaskExtract:
		runner.extractionCalls++
		runner.prompts = append(runner.prompts, prompt)
		return llm.RunResult{Output: json.RawMessage(
			`{"knowledge":[{"type":"property","subject":"knowbrew","statement":"The durable property applies.","rationale":""}]}`,
		)}, nil
	default:
		return llm.RunResult{}, fmt.Errorf("unexpected task %s", task)
	}
}

func TestB001B003DrawCompletesBothStagesWithoutKnowledgeCatalog(t *testing.T) {
	root := t.TempDir()
	dataStore, err := store.New(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := dataStore.EnsureMaster("subjects", domain.MasterEntry{Name: "knowbrew"}); err != nil {
		t.Fatal(err)
	}
	sourceDir := t.TempDir()
	logPath := filepath.Join(sourceDir, "session.jsonl")
	log := `{"type":"user","uuid":"turn-1","sessionId":"session-id","timestamp":"2026-07-30T01:02:03Z","cwd":"/repo","message":{"role":"user","content":"remember this property"}}
{"type":"assistant","sessionId":"session-id","timestamp":"2026-07-30T01:02:04Z","message":{"role":"assistant","content":"Understood."}}
{"type":"user","sessionId":"session-id","timestamp":"2026-07-30T01:02:05Z","message":{"role":"user","content":"[Request interrupted by user]"}}
`
	if err := os.WriteFile(logPath, []byte(log), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{
		Root: root, Path: filepath.Join(root, ".knowbrew", "config.toml"),
		LLM:     config.LLM{Backend: "claude-cli"},
		Sources: []config.Source{{Agent: "claude", Parser: "claude", Paths: []string{sourceDir}}},
	}
	runner := &twoStageRunner{}
	summary, err := Run(context.Background(), cfg, []string{logPath}, runner, nil)
	if err != nil {
		t.Fatal(err)
	}
	if summary.FeedstocksAcquired != 1 || summary.FeedstocksDrawn != 1 ||
		summary.FeedstocksExtracted != 1 || summary.KnowledgeCreated != 1 {
		t.Fatalf("summary = %#v", summary)
	}
	files, warnings, err := dataStore.ListAllKnowledge()
	if err != nil || len(warnings) != 0 {
		t.Fatalf("knowledge error = %v, warnings = %#v", err, warnings)
	}
	if len(files) != 1 || files[0].Knowledge.OrganizedAt != nil ||
		len(files[0].Knowledge.Supersedes) != 0 {
		t.Fatalf("knowledge = %#v", files)
	}
	if len(runner.prompts) != 1 || strings.Contains(runner.prompts[0], "organized_heads") ||
		strings.Contains(runner.prompts[0], "knowledge catalog") {
		t.Fatalf("extraction prompt = %q", runner.prompts)
	}
}

type concurrentExtractionRunner struct {
	active  atomic.Int32
	maximum atomic.Int32
	barrier chan struct{}
}

func (runner *concurrentExtractionRunner) Run(
	_ context.Context,
	task llm.Task,
	_, _ string,
) (llm.RunResult, error) {
	if task != llm.TaskExtract {
		return llm.RunResult{}, fmt.Errorf("unexpected task %s", task)
	}
	current := runner.active.Add(1)
	defer runner.active.Add(-1)
	for {
		maximum := runner.maximum.Load()
		if current <= maximum || runner.maximum.CompareAndSwap(maximum, current) {
			break
		}
	}
	<-runner.barrier
	return llm.RunResult{Output: json.RawMessage(`{"knowledge":[]}`)}, nil
}

func TestB002DrawExtractionUsesConfiguredConcurrency(t *testing.T) {
	root := t.TempDir()
	dataStore, err := store.New(root)
	if err != nil {
		t.Fatal(err)
	}
	repository := &persistenceadapter.Markdown{Store: dataStore}
	base := time.Now().UTC()
	var feedstocks []domain.Feedstock
	snapshots := make(map[string][]domain.FeedstockCandidate)
	for index := range 2 {
		id := fmt.Sprintf("fs-concurrent-%d", index)
		draftedAt := base
		feedstock := domain.Feedstock{
			Schema: domain.SchemaVersion, ID: id, TurnID: "turn-" + id,
			Session: domain.SessionRef{ID: "session-" + id}, Timestamp: base.Add(time.Duration(index) * time.Minute),
			Agent: "codex", Types: []domain.KnowledgeType{"property"}, Summary: "Durable fact.",
			DraftedAt: &draftedAt,
		}
		if err := dataStore.WriteFeedstock(feedstock); err != nil {
			t.Fatal(err)
		}
		feedstocks = append(feedstocks, feedstock)
		snapshots[id] = []domain.FeedstockCandidate{{
			ID: id, TurnID: feedstock.TurnID, Session: feedstock.Session,
			Timestamp: feedstock.Timestamp, Agent: feedstock.Agent,
			Dialogue: []domain.DialogueMessage{{Role: "user", Content: "Remember this."}},
		}}
	}
	runner := &concurrentExtractionRunner{barrier: make(chan struct{})}
	done := make(chan drawPipelineResult, 1)
	go func() {
		done <- runDrawPipeline(context.Background(), Service{
			Settings: Settings{Concurrency: 2}, Repository: repository, Runner: runner,
		}, feedstocks, snapshots, feedstocks, 2)
	}()
	deadline := time.After(5 * time.Second)
	for runner.maximum.Load() < 2 {
		select {
		case <-deadline:
			t.Fatal("extraction did not run concurrently")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	close(runner.barrier)
	result := <-done
	if result.extracted != 2 || result.failed != 0 {
		t.Fatalf("result = %#v", result)
	}
}

func TestB004B005B006HookDefersOnlyTheLatestUnfinishedTurn(t *testing.T) {
	base := time.Now().UTC()
	candidates := []domain.FeedstockCandidate{
		{ID: "fs-first", Agent: "codex", Session: domain.SessionRef{ID: "session"}, SourceSequence: 1, Timestamp: base},
		{ID: "fs-second", Agent: "codex", Session: domain.SessionRef{ID: "session"}, SourceSequence: 2, Timestamp: base.Add(time.Minute)},
		{ID: "fs-third", Agent: "codex", Session: domain.SessionRef{ID: "session"}, SourceSequence: 3, Timestamp: base.Add(2 * time.Minute)},
	}
	existing := map[string]domain.Feedstock{
		"fs-first":  {ID: "fs-first"},
		"fs-second": {ID: "fs-second"},
	}
	selected := selectUnfinishedCandidates(candidates, existing, 0, OrderOldest, true)
	if got := candidateIDs(selected); !slices.Equal(got, []string{"fs-first", "fs-second"}) {
		t.Fatalf("first hook = %#v", got)
	}
	done := base
	first := existing["fs-first"]
	first.ExtractedAt = &done
	existing["fs-first"] = first
	second := existing["fs-second"]
	second.ExtractedAt = &done
	existing["fs-second"] = second
	candidates = append(candidates, domain.FeedstockCandidate{
		ID: "fs-fourth", Agent: "codex", Session: domain.SessionRef{ID: "session"},
		SourceSequence: 4, Timestamp: base.Add(3 * time.Minute),
	})
	selected = selectUnfinishedCandidates(candidates, existing, 0, OrderOldest, true)
	if got := candidateIDs(selected); !slices.Equal(got, []string{"fs-third"}) {
		t.Fatalf("second hook = %#v", got)
	}
	selected = selectUnfinishedCandidates(candidates, existing, 0, OrderOldest, false)
	if got := candidateIDs(selected); !slices.Equal(got, []string{"fs-third", "fs-fourth"}) {
		t.Fatalf("batch = %#v", got)
	}
}

func TestB008B009B011B016ExtractionPersistenceRules(t *testing.T) {
	root := t.TempDir()
	dataStore, err := store.New(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := dataStore.EnsureMaster("subjects", domain.MasterEntry{Name: "knowbrew"}); err != nil {
		t.Fatal(err)
	}
	repository := &persistenceadapter.Markdown{Store: dataStore}
	base := time.Now().UTC()
	writeSource := func(id string, at time.Time) domain.Feedstock {
		draftedAt := at
		feedstock := domain.Feedstock{
			Schema: domain.SchemaVersion, ID: id, TurnID: "turn-" + id,
			Session: domain.SessionRef{ID: "session-" + id}, Timestamp: at, Agent: "codex",
			Types: []domain.KnowledgeType{"property"}, Summary: "Durable fact.", DraftedAt: &draftedAt,
		}
		if err := dataStore.WriteFeedstock(feedstock); err != nil {
			t.Fatal(err)
		}
		return feedstock
	}
	first := writeSource("fs-first", base)
	second := writeSource("fs-second", base.Add(time.Minute))
	for _, source := range []domain.Feedstock{first, second} {
		created, err := ApplyExtraction(context.Background(), repository, source.ID, []domain.KnowledgeDraft{{
			Type: "property", Subject: "knowbrew", Statement: "The same fact applies.",
		}})
		if err != nil || created != 1 {
			t.Fatalf("extract %s: created = %d, error = %v", source.ID, created, err)
		}
	}
	files, _, err := dataStore.ListAllKnowledge()
	if err != nil || len(files) != 2 {
		t.Fatalf("duplicate files = %#v, error = %v", files, err)
	}
	many := writeSource("fs-many", base.Add(2*time.Minute))
	drafts := make([]domain.KnowledgeDraft, 20)
	for index := range drafts {
		drafts[index] = domain.KnowledgeDraft{
			Type: "property", Statement: fmt.Sprintf("Subjectless fact %d.", index),
		}
	}
	created, err := ApplyExtraction(context.Background(), repository, many.ID, drafts)
	if err != nil || created != len(drafts) {
		t.Fatalf("many: created = %d, error = %v", created, err)
	}
	rejected := writeSource("fs-rejected", base.Add(3*time.Minute))
	if _, err := ApplyExtraction(context.Background(), repository, rejected.ID, []domain.KnowledgeDraft{{
		Type: "unknown-type", Statement: "An unusable draft.",
	}}); err == nil {
		t.Fatal("a draft with an unknown type was accepted")
	}
	stored, _, err := dataStore.FindFeedstock(rejected.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.ExtractedAt != nil {
		t.Fatal("failed extraction advanced the feedstock")
	}
	additional := writeSource("fs-additional-subjectless", base.Add(4*time.Minute))
	if created, err := ApplyExtraction(context.Background(), repository, additional.ID, []domain.KnowledgeDraft{{
		Type: "property", Statement: "An additional subjectless fact.",
	}}); err != nil || created != 1 {
		t.Fatalf("additional subjectless: created = %d, error = %v", created, err)
	}
	masters, warnings, err := dataStore.LoadMasters("subjects")
	if err != nil || len(warnings) != 0 || len(masters) != 1 || masters[0].Name != "knowbrew" {
		t.Fatalf("subjects = %#v, warnings = %#v, error = %v", masters, warnings, err)
	}
}

func candidateIDs(candidates []domain.FeedstockCandidate) []string {
	result := make([]string, len(candidates))
	for index, candidate := range candidates {
		result[index] = candidate.ID
	}
	return result
}

type drawResultRunner struct {
	output json.RawMessage
}

func (runner drawResultRunner) Run(
	_ context.Context,
	task llm.Task,
	_, _ string,
) (llm.RunResult, error) {
	if task != llm.TaskDraw {
		return llm.RunResult{Output: json.RawMessage(`{"knowledge":[]}`)}, nil
	}
	return llm.RunResult{Output: runner.output}, nil
}

func (runner annotatingRunner) Run(_ context.Context, task llm.Task, _ string, _ string) (llm.RunResult, error) {
	if task != llm.TaskDraw {
		return llm.RunResult{}, nil
	}
	return llm.RunResult{
		Output: json.RawMessage(`{"summary":"The user requested a tested change.","types":[]}`),
		Usage:  runner.usage,
	}, nil
}

func TestB027DrawIsIdempotentWithoutPersistentSessionState(t *testing.T) {
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
	if first.FeedstocksAcquired != 1 || first.FeedstocksDrawn != 1 {
		t.Fatalf("first summary = %#v", first)
	}
	second, err := Run(context.Background(), cfg, []string{logPath}, runner, nil)
	if err != nil {
		t.Fatal(err)
	}
	if second.FeedstocksAcquired != 0 || second.FeedstocksDrawn != 0 {
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
	if moved.FeedstocksAcquired != 0 || moved.FeedstocksDrawn != 0 {
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

func TestDrawValidatesAndPersistsDraftResults(t *testing.T) {
	tests := []struct {
		name        string
		output      string
		wantDrafted bool
		wantTypes   []domain.KnowledgeType
		wantPending bool
		wantFailed  int
	}{
		{name: "missing types", output: `{"summary":"A durable property was established."}`, wantFailed: 1},
		{
			name:        "empty types",
			output:      `{"summary":"A durable property was established.","types":[]}`,
			wantDrafted: true,
		},
		{
			name:        "valid type",
			output:      `{"summary":"A durable property was established.","types":["property"]}`,
			wantDrafted: true, wantTypes: []domain.KnowledgeType{"property"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			dataStore, err := store.New(root)
			if err != nil {
				t.Fatal(err)
			}
			sourceDir := t.TempDir()
			logPath := filepath.Join(sourceDir, "session.jsonl")
			log := `{"type":"user","uuid":"turn-1","sessionId":"session-id","timestamp":"2026-07-30T01:02:03Z","cwd":"/repo","message":{"role":"user","content":"remember this property"}}
{"type":"user","sessionId":"session-id","timestamp":"2026-07-30T01:02:04Z","message":{"role":"user","content":"[Request interrupted by user]"}}
`
			if err := os.WriteFile(logPath, []byte(log), 0o600); err != nil {
				t.Fatal(err)
			}
			cfg := config.Config{
				Root: root, Path: filepath.Join(root, ".knowbrew", "config.toml"),
				LLM: config.LLM{Backend: "claude-cli"},
				Sources: []config.Source{{
					Agent: "claude", Parser: "claude", Paths: []string{sourceDir},
				}},
			}
			summary, err := Run(
				context.Background(),
				cfg,
				[]string{logPath},
				drawResultRunner{output: json.RawMessage(test.output)},
				nil,
			)
			if err != nil {
				t.Fatal(err)
			}
			if summary.FeedstocksFailed != test.wantFailed {
				t.Fatalf("summary = %#v", summary)
			}
			if test.wantFailed != 0 {
				if len(summary.Failures) != 1 ||
					summary.Failures[0].Phase != "draw" ||
					!strings.Contains(summary.Failures[0].Reason, "draw result types are required") {
					t.Fatalf("failures = %#v", summary.Failures)
				}
			} else if len(summary.Failures) != 0 {
				t.Fatalf("failures = %#v", summary.Failures)
			}
			feedstocks, warnings, err := dataStore.ListFeedstocks()
			if err != nil {
				t.Fatal(err)
			}
			if len(warnings) != 0 || len(feedstocks) != 1 {
				t.Fatalf("feedstocks = %#v, warnings = %#v", feedstocks, warnings)
			}
			feedstock := feedstocks[0]
			if (feedstock.DraftedAt != nil) != test.wantDrafted {
				t.Fatalf("DraftedAt = %v, want drafted %t", feedstock.DraftedAt, test.wantDrafted)
			}
			if !slices.Equal(feedstock.Types, test.wantTypes) {
				t.Fatalf("types = %#v, want %#v", feedstock.Types, test.wantTypes)
			}
			if feedstock.PendingExtraction() != test.wantPending {
				t.Fatalf("PendingExtraction() = %t, want %t", feedstock.PendingExtraction(), test.wantPending)
			}
		})
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
	if first.FeedstocksAcquired != 1 || first.FeedstocksDrawn != 1 {
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
	if second.FeedstocksAcquired != 1 || second.FeedstocksDrawn != 1 {
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
		first.FeedstocksAcquired != 2 || first.FeedstocksDrawn != 2 {
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
		second.FeedstocksAcquired != 1 || second.FeedstocksDrawn != 1 {
		t.Fatalf("second summary = %#v", second)
	}
}

func TestOldestOrderProcessesOldestUnfinishedTurnsFirst(t *testing.T) {
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

	summary, err := RunWithOptions(
		context.Background(), cfg, Options{MaxTurns: 2, Order: OrderOldest},
		annotatingRunner{store: dataStore}, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if summary.TurnsSelected != 2 || summary.TurnsPending != 1 ||
		summary.FeedstocksAcquired != 2 || summary.FeedstocksDrawn != 2 {
		t.Fatalf("summary = %#v", summary)
	}
	for _, turnID := range []string{"turn-1", "turn-2"} {
		id := parser.FeedstockID("claude", "backfill", turnID)
		if _, _, err := dataStore.FindFeedstock(id); err != nil {
			t.Fatalf("oldest selected turn %s was not processed: %v", turnID, err)
		}
	}
	newestID := parser.FeedstockID("claude", "backfill", "turn-3")
	if _, _, err := dataStore.FindFeedstock(newestID); err == nil {
		t.Fatal("newest turn exceeded --max under the oldest order")
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
		summary.FeedstocksAcquired != 0 || summary.FeedstocksDrawn != 1 {
		t.Fatalf("summary = %#v", summary)
	}
	completed, _, err := dataStore.FindFeedstock(incompleteID)
	if err != nil {
		t.Fatal(err)
	}
	if completed.DraftedAt == nil {
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
	if summary.FeedstocksDrawn != 1 {
		t.Fatalf("summary = %#v", summary)
	}
	unchanged, _, err := dataStore.FindFeedstock(unrelated.ID)
	if err != nil {
		t.Fatal(err)
	}
	if unchanged.DraftedAt != nil {
		t.Fatal("draw classified an undrafted feedstock outside the selected sessions")
	}
}

func TestB007ConcurrentDrawsWaitAndRemainIdempotent(t *testing.T) {
	root := t.TempDir()
	dataStore, _ := store.New(root)
	sourceDir := t.TempDir()
	logPath := filepath.Join(sourceDir, "session.jsonl")
	if err := os.WriteFile(logPath, []byte(
		`{"type":"user","uuid":"turn-1","sessionId":"session","timestamp":"2026-07-30T01:02:03Z","message":{"role":"user","content":"first concurrent turn"}}`+"\n"+
			`{"type":"assistant","sessionId":"session","timestamp":"2026-07-30T01:02:04Z","message":{"role":"assistant","content":"first response"}}`+"\n"+
			`{"type":"user","uuid":"turn-2","sessionId":"session","timestamp":"2026-07-30T01:02:05Z","message":{"role":"user","content":"second concurrent turn"}}`+"\n"+
			`{"type":"assistant","sessionId":"session","timestamp":"2026-07-30T01:02:06Z","message":{"role":"assistant","content":"second response"}}`+"\n"+
			`{"type":"user","uuid":"turn-3","sessionId":"session","timestamp":"2026-07-30T01:02:07Z","message":{"role":"user","content":"third concurrent turn"}}`+"\n"+
			`{"type":"assistant","sessionId":"session","timestamp":"2026-07-30T01:02:08Z","message":{"role":"assistant","content":"third response"}}`+"\n"+
			`{"type":"user","sessionId":"session","timestamp":"2026-07-30T01:02:09Z","message":{"role":"user","content":"[Request interrupted by user]"}}`+"\n",
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
	options := []Options{{Paths: []string{logPath}, Hook: true}, {Paths: []string{logPath}}}
	for _, option := range options {
		go func(option Options) {
			<-start
			summary, err := RunWithOptions(
				context.Background(), cfg, option, annotatingRunner{store: dataStore}, nil,
			)
			results <- summary
			errors <- err
		}(option)
	}
	close(start)
	var acquired, drafted, extracted int
	for range 2 {
		if err := <-errors; err != nil {
			t.Fatal(err)
		}
		summary := <-results
		acquired += summary.FeedstocksAcquired
		drafted += summary.FeedstocksDrawn
		extracted += summary.FeedstocksExtracted
	}
	if acquired != 3 || drafted != 3 || extracted != 3 {
		t.Fatalf(
			"combined summaries acquired = %d, drafted = %d, extracted = %d",
			acquired, drafted, extracted,
		)
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

func TestDrawPromptIncludesFilteredDialogueWithoutReadInstructions(t *testing.T) {
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
		Includes:   []string{"verified runtime behavior"},
		Excludes:   []string{"temporary task progress"},
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
	promptText, warnings, err := drawPromptForTest(cfg, dataStore, target.ID, feedstocks)
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 0 {
		t.Fatalf("warnings = %#v", warnings)
	}
	for _, required := range []string{
		target.ID,
		`"user_input": "FULL USER REQUEST"`,
		`"agent_response": "VISIBLE ASSISTANT RESPONSE"`,
		`"prior_turns": []`,
		`"cwd": "/vault"`, `"repo": "https://github.com/example/knowbrew.git"`,
		`run "knowbrew feedstock context ` + target.ID + `" exactly once`,
		`Return exactly one JSON object containing only {"summary": ..., "types": [...]}`,
		"only the supplied user_input", "supplied agent_response action and result",
		"treat knowledge_type_master as the sole authority",
		"First state to yourself, in one sentence, the durable meaning this turn establishes",
		"the turn has no durable meaning and types must be an empty array",
		"The later extraction stage re-checks the excludes entries",
		"Do not decide statement wording, meaning boundaries, subject ownership, or final type assignment",
		`"includes": [`, `"verified runtime behavior"`,
		`"excludes": [`, `"temporary task progress"`,
		`"name": "observation"`,
	} {
		if !strings.Contains(promptText, required) {
			t.Fatalf("draw prompt does not contain %q:\n%s", required, promptText)
		}
	}
	for _, forbidden := range []string{
		"SECRET THINKING", "SECRET TOOL CALL", "SECRET TOOL OUTPUT",
		`"subject_master"`, `"name": "agent-model"`,
		`"target_offset"`, `"offset": 0`, `"offset": 1`,
		"feedstock draft",
	} {
		if strings.Contains(promptText, forbidden) {
			t.Fatalf("draw prompt contains %q:\n%s", forbidden, promptText)
		}
	}
}

func TestWritingGuidesDoNotApplyToDraw(t *testing.T) {
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
	typeCandidateText, _, err := drawPromptForTest(cfg, dataStore, target.ID, feedstocks)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"COMMON WRITING RULE", "KNOWLEDGE WRITING RULE", "DOCUMENT WRITING RULE"} {
		if strings.Contains(typeCandidateText, forbidden) {
			t.Fatalf("draw prompt contains writing guide %q:\n%s", forbidden, typeCandidateText)
		}
	}
}

func TestDrawPromptMarksMissingAssistantResponse(t *testing.T) {
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
	feedstocks, _, err := dataStore.ListFeedstocks()
	if err != nil {
		t.Fatal(err)
	}
	prompt, _, err := drawPromptForTest(
		config.Config{Path: "/configured/config.toml"},
		dataStore,
		target.ID,
		feedstocks,
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
		SourcesFailed: 1,
		MastersAdded:  2,
		SourceFailures: []SourceFailure{{
			Path: "/logs/broken.jsonl", Reason: "unknown format",
		}},
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
		!strings.Contains(string(data), `"sources_failed":1`) ||
		!strings.Contains(string(data), `"source_failures":[{"path":"/logs/broken.jsonl","reason":"unknown format"}]`) ||
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

func TestDrawPromptLimitsOnlyAssistantResponse(t *testing.T) {
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
	feedstocks, _, err := dataStore.ListFeedstocks()
	if err != nil {
		t.Fatal(err)
	}
	prompt, _, err := drawPromptForTest(
		config.Config{Path: "/configured/config.toml", Draw: config.Draw{ContextTurns: 0}},
		dataStore,
		target.ID,
		feedstocks,
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

func TestDrawPromptIncludesOnlyThreePriorTurnsWithinSession(t *testing.T) {
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
	prompt, _, err := drawPromptForTest(cfg, dataStore, target.ID, feedstocks, snapshots)
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{
		"BEFORE QUOTE 1", "BEFORE QUOTE 2", "BEFORE QUOTE 3",
		"BEFORE RESPONSE 1", "BEFORE RESPONSE 2", "BEFORE RESPONSE 3",
		"TARGET USER", "TARGET ASSISTANT",
		`"offset": -3`, `"offset": -1`, `"prior_turns"`,
	} {
		if !strings.Contains(prompt, required) {
			t.Fatalf("prompt does not contain %q:\n%s", required, prompt)
		}
	}
	for _, forbidden := range []string{
		"BEFORE QUOTE 4",
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
	prompt, _, err = drawPromptForTest(withoutContext, dataStore, target.ID, feedstocks, snapshots)
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

func TestAnnotationContextUsesForkAncestorTurnsWithoutCrossingOtherSources(t *testing.T) {
	base := time.Date(2026, 7, 30, 1, 0, 0, 0, time.UTC)
	candidates := []domain.FeedstockCandidate{
		{
			ID: "parent", Agent: "codex", Session: domain.SessionRef{ID: "parent-session"},
			SourceOwnerSessionID: "child-session", Timestamp: base,
			Dialogue: []domain.DialogueMessage{{Role: "user", Content: "parent context"}},
		},
		{
			ID: "target", Agent: "codex", Session: domain.SessionRef{ID: "child-session"},
			SourceOwnerSessionID: "child-session", Timestamp: base.Add(time.Minute),
			Dialogue: []domain.DialogueMessage{{Role: "user", Content: "child request"}},
		},
		{
			ID: "unrelated", Agent: "codex", Session: domain.SessionRef{ID: "other-session"},
			SourceOwnerSessionID: "other-session", Timestamp: base.Add(2 * time.Minute),
			Dialogue: []domain.DialogueMessage{{Role: "user", Content: "unrelated"}},
		},
	}
	context, err := annotationContextFromCandidates(candidates, "target", 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(context.PriorTurns) != 1 || context.PriorTurns[0].UserInput != "parent context" {
		t.Fatalf("context = %#v", context)
	}
}

func TestDrawPromptIncludesBoundedPriorDialogueOnly(t *testing.T) {
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
	prompt, _, err := drawPromptForTest(
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
		`"user_input": "OK"`,
		`"prior_turns"`,
		`"agent_response"`,
		`"offset": -1`,
		"Applied option A",
		strings.TrimSpace(annotationContextAssistantTruncatedMarker),
		"PREVIOUS ASSISTANT BEGIN",
		"PREVIOUS ASSISTANT TAIL",
	} {
		if !strings.Contains(prompt, required) {
			t.Fatalf("prompt does not contain %q:\n%s", required, prompt)
		}
	}
	for _, forbidden := range []string{"What next?", afterAssistant, `"offset": 0`, `"offset": 1`} {
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
	if task != llm.TaskDraw {
		return llm.RunResult{}, nil
	}
	if feedstockID == runner.failFeedstockID && runner.failuresLeft > 0 {
		runner.failuresLeft--
		return llm.RunResult{}, errors.New("temporary annotation failure")
	}
	return llm.RunResult{
		Output: json.RawMessage(`{"summary":"The user requested a tested change.","types":[]}`),
	}, nil
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
	if first.FeedstocksFailed != 1 || first.FeedstocksDrawn != 1 || len(first.Failures) != 1 {
		t.Fatalf("first summary = %#v", first)
	}
	if first.Failures[0].FeedstockID != failedID ||
		!strings.Contains(first.Failures[0].Reason, "temporary annotation failure") {
		t.Fatalf("failure = %#v", first.Failures[0])
	}
	if !strings.Contains(progress.String(), "Draw failed · "+failedID) {
		t.Fatalf("draw failure was not printed:\n%s", progress.String())
	}
	failed, _, err := dataStore.FindFeedstock(failedID)
	if err != nil {
		t.Fatal(err)
	}
	if failed.DraftedAt != nil {
		t.Fatalf("failed feedstock was not left undrafted: %#v", failed)
	}

	second, err := Run(context.Background(), cfg, []string{logPath}, runner, nil)
	if err != nil {
		t.Fatal(err)
	}
	if second.FeedstocksFailed != 0 || second.FeedstocksDrawn != 1 || second.FeedstocksAcquired != 0 {
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
	if !strings.Contains(reason, "apply draw result") || !strings.Contains(reason, "unknown field") {
		t.Fatalf("failure reason = %q", reason)
	}
	if strings.Contains(reason, "did not finalize") {
		t.Fatalf("actual verification error was hidden: %q", reason)
	}
}

func TestDrawDoesNotUseTheRetiredGlobalLock(t *testing.T) {
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
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("draw waited for the retired global lock")
	}
	if err := lock.Unlock(); err != nil {
		t.Fatal(err)
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

func TestAcquisitionPersistsUndraftedFeedstockAndResumeClassifiesIt(t *testing.T) {
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
	if first.FeedstocksAcquired != 1 || first.FeedstocksDrawn != 0 {
		t.Fatalf("first summary = %#v", first)
	}
	id := parser.FeedstockID("claude", "phase-one", "turn-1")
	undrafted, _, err := dataStore.FindFeedstock(id)
	if err != nil {
		t.Fatal(err)
	}
	if undrafted.DraftedAt != nil || undrafted.Summary != "" || len(undrafted.Types) != 0 {
		t.Fatalf("phase-one feedstock = %#v", undrafted)
	}
	found, err := query.Search(context.Background(), dataStore, query.SearchOptions{
		Target: query.TargetFeedstock, Keywords: []string{"rawphasekeyword"},
		Limit: 10, MaxTokens: 1000,
	})
	if err != nil {
		t.Fatal(err)
	}
	if found.Total != 0 {
		t.Fatalf("undrafted raw text must not be indexed without a summary: %#v", found)
	}

	second, err := Run(context.Background(), cfg, []string{logPath}, annotatingRunner{store: dataStore}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if second.FeedstocksAcquired != 0 || second.FeedstocksDrawn != 1 {
		t.Fatalf("resume summary = %#v", second)
	}
	classified, _, err := dataStore.FindFeedstock(id)
	if err != nil {
		t.Fatal(err)
	}
	if classified.DraftedAt == nil {
		t.Fatal("resume did not annotate the pending feedstock")
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
	if task != llm.TaskDraw {
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
	return llm.RunResult{
		Output: json.RawMessage(`{"summary":"The user requested concurrent classification.","types":[]}`),
		Usage:  runner.usage,
	}, nil
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
	if summary.FeedstocksAcquired != 8 || summary.FeedstocksDrawn != 8 ||
		summary.FeedstocksFailed != 0 {
		t.Fatalf("summary = %#v", summary)
	}
	if runner.maxActive.Load() < 2 {
		t.Fatalf("maximum active workers = %d, want concurrent annotation", runner.maxActive.Load())
	}
	if !strings.Contains(
		progress.String(),
		"Draw complete · 8/8 feedstocks · in 8.0k tokens / out 800 tokens",
	) {
		t.Fatalf("draw progress did not reach all feedstocks:\n%s", progress.String())
	}
	if summary.Usage != (llm.UsageReport{
		Backend: "claude-cli", TotalInputTokens: 8000,
		StandardInputTokens: 3200, CacheReadInputTokens: 4800,
		OutputTokens: 800, TotalTokens: 8800,
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
		if feedstock.DraftedAt == nil {
			t.Fatalf("feedstock %s remained undrafted", feedstock.ID)
		}
	}
}

func TestDrawContinuesAfterSourceParseFailure(t *testing.T) {
	root := t.TempDir()
	dataStore, _ := store.New(root)
	sourceDir := t.TempDir()
	validPath := filepath.Join(sourceDir, "valid.jsonl")
	invalidPath := filepath.Join(sourceDir, "invalid.jsonl")
	valid := `{"type":"user","uuid":"turn-1","sessionId":"valid-session","timestamp":"2026-07-30T01:02:03Z","message":{"role":"user","content":"valid"}}
{"type":"assistant","sessionId":"valid-session","timestamp":"2026-07-30T01:02:04Z","message":{"role":"assistant","stop_reason":"end_turn","content":[{"type":"text","text":"done"}]}}
`
	if err := os.WriteFile(validPath, []byte(valid), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		invalidPath,
		[]byte("{\"type\":\"user\",\"message\":\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{
		Root: root, Path: filepath.Join(root, ".knowbrew", "config.toml"),
		LLM: config.LLM{Backend: "claude-cli"}, Draw: config.Draw{Concurrency: 1},
		Sources: []config.Source{{
			Agent: "claude", Parser: "claude", Paths: []string{sourceDir},
		}},
	}
	var progress bytes.Buffer
	summary, err := Run(
		context.Background(), cfg, nil,
		annotatingRunner{store: dataStore}, &progress,
	)
	var acquisitionErr AcquisitionFailuresError
	if !errors.As(err, &acquisitionErr) || acquisitionErr.Count != 1 {
		t.Fatalf("error = %v", err)
	}
	if summary.SourcesFailed != 1 || len(summary.SourceFailures) != 1 ||
		summary.SourceFailures[0].Path != invalidPath ||
		summary.FeedstocksAcquired != 1 || summary.FeedstocksDrawn != 1 {
		t.Fatalf("summary = %#v", summary)
	}
	if !strings.Contains(progress.String(), "Acquisition failed · "+invalidPath) {
		t.Fatalf("progress = %s", progress.String())
	}
	feedstocks, warnings, listErr := dataStore.ListFeedstocks()
	if listErr != nil || len(warnings) != 0 || len(feedstocks) != 1 ||
		feedstocks[0].Session.ID != "valid-session" {
		t.Fatalf("feedstocks = %#v, warnings = %#v, error = %v", feedstocks, warnings, listErr)
	}
}

func TestDrawRejectsEveryFileInAConflictingLogicalSession(t *testing.T) {
	root := t.TempDir()
	dataStore, _ := store.New(root)
	sourceDir := t.TempDir()
	for name, quote := range map[string]string{"first.jsonl": "first", "second.jsonl": "second"} {
		log := fmt.Sprintf(
			"{\"type\":\"user\",\"uuid\":\"turn-1\",\"sessionId\":\"shared-session\",\"timestamp\":\"2026-07-30T01:02:03Z\",\"message\":{\"role\":\"user\",\"content\":%q}}\n"+
				"{\"type\":\"assistant\",\"sessionId\":\"shared-session\",\"timestamp\":\"2026-07-30T01:02:04Z\",\"message\":{\"role\":\"assistant\",\"stop_reason\":\"end_turn\",\"content\":[]}}\n",
			quote,
		)
		if err := os.WriteFile(filepath.Join(sourceDir, name), []byte(log), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cfg := config.Config{
		Root: root, Path: filepath.Join(root, ".knowbrew", "config.toml"),
		LLM: config.LLM{Backend: "claude-cli"}, Draw: config.Draw{Concurrency: 1},
		Sources: []config.Source{{
			Agent: "claude", Parser: "claude", Paths: []string{sourceDir},
		}},
	}
	summary, err := Run(
		context.Background(), cfg, nil,
		annotatingRunner{store: dataStore}, nil,
	)
	var acquisitionErr AcquisitionFailuresError
	if !errors.As(err, &acquisitionErr) || acquisitionErr.Count != 2 {
		t.Fatalf("error = %v", err)
	}
	if summary.SourcesFailed != 2 || len(summary.SourceFailures) != 2 ||
		summary.FeedstocksAcquired != 0 {
		t.Fatalf("summary = %#v", summary)
	}
	feedstocks, warnings, listErr := dataStore.ListFeedstocks()
	if listErr != nil || len(warnings) != 0 || len(feedstocks) != 0 {
		t.Fatalf("feedstocks = %#v, warnings = %#v, error = %v", feedstocks, warnings, listErr)
	}
}

func TestSelectUnfinishedCandidatesUsesSourceSequenceWhenTimestampsMatch(t *testing.T) {
	timestamp := time.Date(2025, 8, 8, 10, 39, 10, 0, time.UTC)
	candidates := []domain.FeedstockCandidate{
		{ID: "first", Session: domain.SessionRef{ID: "session"}, Timestamp: timestamp, SourceSequence: 1},
		{ID: "second", Session: domain.SessionRef{ID: "session"}, Timestamp: timestamp, SourceSequence: 2},
		{ID: "third", Session: domain.SessionRef{ID: "session"}, Timestamp: timestamp, SourceSequence: 3},
	}
	selected := selectUnfinishedCandidates(candidates, nil, 2, OrderNewest, false)
	if len(selected) != 2 || selected[0].ID != "third" || selected[1].ID != "second" {
		t.Fatalf("selected = %#v", selected)
	}
	oldest := selectUnfinishedCandidates(candidates, nil, 2, OrderOldest, false)
	if len(oldest) != 2 || oldest[0].ID != "first" || oldest[1].ID != "second" {
		t.Fatalf("oldest = %#v", oldest)
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
	if summary.FeedstocksAcquired != 2 || summary.FeedstocksDrawn != 2 {
		t.Fatalf("summary = %#v", summary)
	}
	text := output.String()
	for _, required := range []string{
		"Acquiring · 0/1 sources · 0 feedstocks",
		"Acquisition complete · 2 feedstocks from 1 sources",
		"Drawing · 0/2 · 2 workers · in 0 tokens / out 0 tokens",
		"Draw complete · 2/2 feedstocks · in 2.0k tokens / out 200 tokens",
	} {
		if !strings.Contains(text, required) {
			t.Fatalf("progress does not contain %q:\n%s", required, text)
		}
	}
	for _, forbidden := range []string{
		"Acquiring " + logPath,
		"Drawing 1/2 complete:",
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
		if err != nil || second.FeedstocksDrawn != 300 {
			b.Fatalf("classification summary = %#v, error = %v", second, err)
		}
	}
	b.ReportMetric(float64(acquisitionTotal.Microseconds())/1000/float64(b.N), "acquisition_ms/op")
	b.ReportMetric(float64(classificationTotal.Microseconds())/1000/float64(b.N), "classification_mock_ms/op")
}
