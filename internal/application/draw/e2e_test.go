package draw_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/siro33950/knowbrew/internal/adapters/config"
	dialogueadapter "github.com/siro33950/knowbrew/internal/adapters/dialogue"
	"github.com/siro33950/knowbrew/internal/adapters/llm"
	persistenceadapter "github.com/siro33950/knowbrew/internal/adapters/persistence"
	"github.com/siro33950/knowbrew/internal/adapters/persistence/markdownstore"
	"github.com/siro33950/knowbrew/internal/adapters/progress"
	"github.com/siro33950/knowbrew/internal/adapters/runlock"
	sourceadapter "github.com/siro33950/knowbrew/internal/adapters/source"
	"github.com/siro33950/knowbrew/internal/application/brew"
	"github.com/siro33950/knowbrew/internal/application/draw"
)

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
			Backend: backend, DrawModel: os.Getenv("KNOWBREW_E2E_MODEL"),
			BrewModel: os.Getenv("KNOWBREW_E2E_MODEL"),
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
			Model: cfg.LLM.DrawModel, ConfigPath: cfg.Path, Sources: configuredSources,
		},
		Repository: &persistenceadapter.Markdown{Store: dataStore},
		Sources:    sourceadapter.New(configuredSources), Runner: runner,
		Progress: progress.From(os.Stderr),
		RunLock: runlock.FileLock{
			Path: filepath.Join(root, ".knowbrew", "state", "draw.lock"), Name: "draw",
		},
	}
	drawSummary, err := drawService.Run(ctx, []string{logPath})
	if err != nil {
		t.Fatal(err)
	}
	if drawSummary.FeedstocksAnnotated == 0 {
		t.Fatalf("draw summary = %#v", drawSummary)
	}
	if drawSummary.MastersAdded == 0 {
		t.Fatalf("test source did not expose a repository subject: %#v", drawSummary)
	}
	brewService := brew.Service{
		Settings: brew.Settings{
			ContextTurns: cfg.Draw.ContextTurns,
			Backend:      cfg.LLM.Backend,
			Model:        cfg.LLM.BrewModel,
		},
		Repository: &persistenceadapter.Markdown{Store: dataStore},
		Lifecycle:  &persistenceadapter.Markdown{Store: dataStore},
		Dialogue: dialogueadapter.Query{
			Store: dataStore, Source: sourceadapter.New(configuredSources),
		},
		Runner:   runner,
		Progress: progress.From(os.Stderr),
		RunLock: runlock.FileLock{
			Path: filepath.Join(root, ".knowbrew", "state", "brew.lock"), Name: "brew",
		},
	}
	brewSummary, err := brewService.Run(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if brewSummary.FeedstocksProcessed != drawSummary.FeedstocksAnnotated {
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
		if feedstock.BrewedAt == nil {
			t.Fatalf("feedstock %s was not marked brewed", feedstock.ID)
		}
	}
}
