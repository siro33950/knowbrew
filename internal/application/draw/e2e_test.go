package draw_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/siro33950/knowbrew/internal/adapters/config"
	"github.com/siro33950/knowbrew/internal/adapters/llm"
	persistenceadapter "github.com/siro33950/knowbrew/internal/adapters/persistence"
	"github.com/siro33950/knowbrew/internal/adapters/persistence/markdownstore"
	"github.com/siro33950/knowbrew/internal/adapters/progress"
	"github.com/siro33950/knowbrew/internal/adapters/runlock"
	sourceadapter "github.com/siro33950/knowbrew/internal/adapters/source"
	"github.com/siro33950/knowbrew/internal/application/agent"
	"github.com/siro33950/knowbrew/internal/application/brew"
	"github.com/siro33950/knowbrew/internal/application/distill"
	"github.com/siro33950/knowbrew/internal/application/draw"
	"github.com/siro33950/knowbrew/internal/domain"
)

type fixedPipelineRunner struct {
	brewKnowledgeID string
}

func (runner *fixedPipelineRunner) Run(
	_ context.Context,
	task agent.Task,
	_, _ string,
) (agent.RunResult, error) {
	switch task {
	case agent.TaskDraw:
		return agent.RunResult{Output: json.RawMessage(
			`{"summary":"A durable property was established.","types":["property"]}`,
		)}, nil
	case agent.TaskExtract:
		return agent.RunResult{Output: json.RawMessage(
			`{"knowledge":[{"type":"property","subject":"knowbrew","statement":"Knowbrew retains durable meaning after its source log is removed.","rationale":""}]}`,
		)}, nil
	case agent.TaskBrew:
		if runner.brewKnowledgeID == "" {
			return agent.RunResult{}, fmt.Errorf("brew Knowledge ID is not configured")
		}
		return agent.RunResult{Output: json.RawMessage(fmt.Sprintf(
			`{"actions":[{"knowledge_id":%q,"resolution":{"kind":"new","knowledge_ids":[],"draft":null}}]}`,
			runner.brewKnowledgeID,
		))}, nil
	case agent.TaskDistillSelect:
		return agent.RunResult{Output: json.RawMessage(`{"knowledge_references":["K001"]}`)}, nil
	case agent.TaskDistillGenerate:
		return agent.RunResult{Output: json.RawMessage(
			`{"body":"# knowbrew\n\nKnowbrew retains durable meaning after its source log is removed.","knowledge_references":["K001"]}`,
		)}, nil
	default:
		return agent.RunResult{}, fmt.Errorf("unexpected task %s", task)
	}
}

func TestB019PipelineAfterExtractionDoesNotDependOnSessionLog(t *testing.T) {
	root := t.TempDir()
	dataStore, err := store.New(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := dataStore.EnsureLayout(); err != nil {
		t.Fatal(err)
	}
	if err := dataStore.EnsureDefaultTemplates(); err != nil {
		t.Fatal(err)
	}
	if _, err := dataStore.EnsureMaster("subjects", domain.MasterEntry{
		Name: "knowbrew", Documents: []string{"concept"},
	}); err != nil {
		t.Fatal(err)
	}
	sourceDir := t.TempDir()
	logPath := filepath.Join(sourceDir, "session.jsonl")
	log := `{"type":"user","uuid":"turn-1","sessionId":"session-id","timestamp":"2026-07-30T01:02:03Z","cwd":"/work","message":{"role":"user","content":"Remember this durable property."}}
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
	runner := &fixedPipelineRunner{}
	drawSummary, err := draw.Run(context.Background(), cfg, []string{logPath}, runner, nil)
	if err != nil {
		t.Fatal(err)
	}
	if drawSummary.FeedstocksExtracted != 1 || drawSummary.KnowledgeCreated != 1 {
		t.Fatalf("draw summary = %#v", drawSummary)
	}
	knowledge, warnings, err := dataStore.ListAllKnowledge()
	if err != nil || len(warnings) != 0 || len(knowledge) != 1 {
		t.Fatalf("Knowledge = %#v, warnings = %#v, error = %v", knowledge, warnings, err)
	}
	runner.brewKnowledgeID = knowledge[0].Knowledge.ID
	if err := os.Remove(logPath); err != nil {
		t.Fatal(err)
	}
	repository := &persistenceadapter.Markdown{Store: dataStore}
	brewSummary, err := (brew.Service{
		Repository: repository, Lifecycle: repository, Runner: runner,
		Claimer: runlock.FileClaimer{Root: root, Namespace: "subject-claims"},
	}).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if brewSummary.SubjectsProcessed != 1 || len(brewSummary.ChangedSubjects) != 1 ||
		brewSummary.ChangedSubjects[0] != "knowbrew" {
		t.Fatalf("brew summary = %#v", brewSummary)
	}
	organized, err := dataStore.FindKnowledge(runner.brewKnowledgeID)
	if err != nil || organized.Knowledge.OrganizedAt == nil {
		t.Fatalf("organized Knowledge = %#v, error = %v", organized.Knowledge, err)
	}
	data, err := os.ReadFile(organized.Path)
	if err != nil {
		t.Fatal(err)
	}
	approved := strings.Replace(string(data), "approved: false", "approved: true", 1)
	if approved == string(data) {
		t.Fatal("Knowledge approval field was not found")
	}
	if err := os.WriteFile(organized.Path, []byte(approved), 0o644); err != nil {
		t.Fatal(err)
	}
	distillSummary, err := (distill.Service{
		Repository: repository, Lifecycle: repository, Runner: runner,
		RunLock: runlock.FileLock{Path: filepath.Join(root, ".knowbrew", "state", "distill.lock")},
	}).Run(context.Background(), distill.Options{Subject: "knowbrew", Template: "concept"})
	if err != nil {
		t.Fatal(err)
	}
	if distillSummary.DocumentsCreated != 1 || distillSummary.KnowledgeUsed != 1 {
		t.Fatalf("distill summary = %#v", distillSummary)
	}
	templates, warnings, err := dataStore.LoadTemplates()
	if err != nil || len(warnings) != 0 {
		t.Fatalf("templates warnings = %#v, error = %v", warnings, err)
	}
	var concept domain.DocumentTemplate
	for _, template := range templates {
		if template.Name == "concept" {
			concept = template
			break
		}
	}
	document, exists, err := dataStore.ReadDistilledDocument(concept, "knowbrew")
	if err != nil || !exists || len(document.KnowledgeIDs) != 1 ||
		document.KnowledgeIDs[0] != runner.brewKnowledgeID {
		t.Fatalf("document = %#v, exists = %v, error = %v", document, exists, err)
	}
}

func TestRealLLMEndToEndWhenConfigured(t *testing.T) {
	logPath := os.Getenv("KNOWBREW_E2E_LOG")
	executable := os.Getenv("KNOWBREW_E2E_BINARY")
	if logPath == "" || executable == "" {
		t.Skip("set KNOWBREW_E2E_LOG and KNOWBREW_E2E_BINARY to run")
	}
	backend := os.Getenv("KNOWBREW_E2E_BACKEND")
	if backend == "" {
		backend = "claude-cli"
	}
	agent := os.Getenv("KNOWBREW_E2E_AGENT")
	if agent == "" {
		agent = "claude"
	}
	configuredSources := []draw.ConfiguredSource{{
		Agent: agent, Parser: agent, Paths: []string{filepath.Dir(logPath)},
	}}
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), ".config"))
	cfg := config.Config{
		LLM: config.LLM{
			Backend: backend, DrawDraftModel: os.Getenv("KNOWBREW_E2E_MODEL"),
			DrawExtractModel: os.Getenv("KNOWBREW_E2E_MODEL"),
			BrewModel:        os.Getenv("KNOWBREW_E2E_MODEL"),
		},
		Draw: config.Draw{Concurrency: config.DefaultDrawConcurrency},
		Sources: []config.Source{{
			Agent: agent, Parser: agent, Paths: []string{filepath.Dir(logPath)},
		}},
	}
	path, err := config.Save(root, cfg)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Root = root
	cfg.Path = path
	runner, err := llm.New(cfg, executable, root, os.Stderr)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	dataStore, err := store.New(root)
	if err != nil {
		t.Fatal(err)
	}
	drawService := draw.Service{
		Settings: draw.Settings{
			Concurrency: cfg.Draw.Concurrency, ContextTurns: cfg.Draw.ContextTurns,
			MaxContextTurns: cfg.Draw.MaxContextTurns, Backend: cfg.LLM.Backend,
			Model: cfg.LLM.DrawDraftModel, ConfigPath: cfg.Path, Sources: configuredSources,
		},
		Repository: &persistenceadapter.Markdown{Store: dataStore},
		Sources:    sourceadapter.New(configuredSources), Runner: runner,
		Progress: progress.From(os.Stderr),
		Claimer: runlock.FileClaimer{
			Root: root, Namespace: "feedstock-claims",
		},
	}
	drawSummary, err := drawService.Run(ctx, []string{logPath})
	if err != nil {
		t.Fatal(err)
	}
	if drawSummary.FeedstocksDrawn == 0 {
		t.Fatalf("draw summary = %#v", drawSummary)
	}
	if drawSummary.MastersAdded == 0 {
		t.Fatalf("test source did not expose a repository subject: %#v", drawSummary)
	}
	brewService := brew.Service{
		Settings: brew.Settings{
			Concurrency: cfg.Draw.Concurrency,
			Backend:     cfg.LLM.Backend,
			Model:       cfg.LLM.BrewModel,
		},
		Repository: &persistenceadapter.Markdown{Store: dataStore},
		Lifecycle:  &persistenceadapter.Markdown{Store: dataStore},
		Runner:     runner,
		Progress:   progress.From(os.Stderr),
		Claimer: runlock.FileClaimer{
			Root: root, Namespace: "subject-claims",
		},
	}
	brewSummary, err := brewService.Run(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if brewSummary.SubjectsProcessed == 0 && drawSummary.KnowledgeCreated > 0 {
		t.Fatalf("brew summary = %#v, draw summary = %#v", brewSummary, drawSummary)
	}
	feedstocks, warnings, err := dataStore.ListFeedstocks()
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 0 {
		t.Fatalf("feedstock warnings = %#v", warnings)
	}
	for _, feedstock := range feedstocks {
		if feedstock.ExtractedAt == nil {
			t.Fatalf("feedstock %s was not marked extracted", feedstock.ID)
		}
	}
}
