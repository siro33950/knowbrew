package draw_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/siro33950/knowbrew/internal/brew"
	"github.com/siro33950/knowbrew/internal/config"
	"github.com/siro33950/knowbrew/internal/draw"
	"github.com/siro33950/knowbrew/internal/llm"
	"github.com/siro33950/knowbrew/internal/store"
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
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), ".config"))
	cfg := config.Config{
		LLM: config.LLM{
			Backend: backend, DrawModel: os.Getenv("KNOWBREW_E2E_MODEL"),
			BrewModel: os.Getenv("KNOWBREW_E2E_MODEL"),
		},
		Draw: config.Draw{Concurrency: config.DefaultDrawConcurrency},
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
	drawSummary, err := draw.Run(ctx, cfg, []string{logPath}, runner, os.Stderr)
	if err != nil {
		t.Fatal(err)
	}
	if drawSummary.FeedstocksAnnotated == 0 {
		t.Fatalf("draw summary = %#v", drawSummary)
	}
	if drawSummary.MastersAdded == 0 {
		t.Fatalf("test source did not expose a repository subject: %#v", drawSummary)
	}
	brewSummary, err := brew.Run(ctx, cfg, runner, os.Stderr)
	if err != nil {
		t.Fatal(err)
	}
	if brewSummary.FeedstocksProcessed != drawSummary.FeedstocksAnnotated {
		t.Fatalf("brew summary = %#v, draw summary = %#v", brewSummary, drawSummary)
	}
	dataStore, _ := store.New(root)
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
