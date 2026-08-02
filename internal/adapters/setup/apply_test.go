package setup

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/siro33950/knowbrew/internal/adapters/config"
	"github.com/siro33950/knowbrew/internal/adapters/persistence/markdownstore"
)

func TestApplyCreatesRootLocalConfigAndUserLocator(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv(config.ConfigEnvironment, "")
	root := filepath.Join(t.TempDir(), "vault")
	if err := Apply(Choices{
		Root: root, Backend: "claude-cli", DrawModel: "fast-model", BrewModel: "quality-model",
		InstallClaude: false, InstallCodex: false,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, ".knowbrew", "config.toml")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(home, ".config", "knowbrew", "location.toml")); err != nil {
		t.Fatal(err)
	}
	loaded, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	absoluteRoot, _ := filepath.Abs(root)
	if loaded.Root != absoluteRoot {
		t.Fatalf("root = %q, want %q", loaded.Root, absoluteRoot)
	}
	if loaded.LLM.DrawModel != "fast-model" || loaded.LLM.BrewModel != "quality-model" {
		t.Fatalf("LLM models = %#v", loaded.LLM)
	}
	if loaded.LLM.DrawEffort != config.DefaultDrawEffort || loaded.LLM.BrewEffort != "" {
		t.Fatalf("LLM efforts = %#v", loaded.LLM)
	}
	if loaded.Draw.Concurrency != config.DefaultDrawConcurrency {
		t.Fatalf("draw concurrency = %d", loaded.Draw.Concurrency)
	}
	if loaded.Draw.ContextTurns != config.DefaultDrawContextTurns {
		t.Fatalf("draw context turns = %d", loaded.Draw.ContextTurns)
	}
	if loaded.Draw.MaxContextTurns != config.DefaultDrawMaxContextTurns {
		t.Fatalf("draw max context turns = %d", loaded.Draw.MaxContextTurns)
	}
	data, err := os.ReadFile(filepath.Join(root, ".knowbrew", "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{
		`draw_effort = "low"`,
		`brew_effort = ""`,
		`context_turns = 3`,
		`max_context_turns = 20`,
	} {
		if !strings.Contains(string(data), required) {
			t.Fatalf("config does not contain %q:\n%s", required, data)
		}
	}
	dataStore, err := store.New(root)
	if err != nil {
		t.Fatal(err)
	}
	types, warnings, err := dataStore.LoadMasters("types")
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 0 || len(types) != 7 {
		t.Fatalf("init type masters = %#v, warnings = %#v", types, warnings)
	}
}

func TestApplyReinitPreservesUnaskedSettingsCustomSourcesAndData(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv(config.ConfigEnvironment, "")
	for _, path := range []string{
		filepath.Join(home, ".claude", "projects"),
		filepath.Join(home, ".codex", "sessions"),
	} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	root := filepath.Join(t.TempDir(), "vault")
	if err := Apply(Choices{
		Root: root, Backend: "codex-cli", DrawModel: "old-draw", BrewModel: "old-brew",
		SourceNames: []string{"claude", "codex"}, InstallClaude: false, InstallCodex: false,
	}); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadPath(config.DefaultConfigPath(root))
	if err != nil {
		t.Fatal(err)
	}
	cfg.LLM.DrawEffort = "medium"
	cfg.LLM.BrewEffort = "high"
	cfg.LLM.Timeout = "9m"
	cfg.Draw.Concurrency = 1
	cfg.Draw.ContextTurns = 0
	cfg.Draw.MaxContextTurns = 12
	custom := config.Source{Agent: "codex", Parser: "codex", Path: filepath.Join(home, "archived-sessions")}
	cfg.Sources = append(cfg.Sources, custom)
	if _, err := config.Save(root, cfg); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(root, "knowledge", "keep.md")
	if err := os.WriteFile(sentinel, []byte("keep exactly"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := Apply(Choices{
		Root: root, Backend: "codex-cli", DrawModel: "new-draw", BrewModel: "new-brew",
		SourceNames: []string{"codex"}, InstallClaude: false, InstallCodex: false,
	}); err != nil {
		t.Fatal(err)
	}
	updated, err := config.LoadPath(config.DefaultConfigPath(root))
	if err != nil {
		t.Fatal(err)
	}
	if updated.LLM.DrawModel != "new-draw" || updated.LLM.BrewModel != "new-brew" ||
		updated.LLM.DrawEffort != "medium" || updated.LLM.BrewEffort != "high" ||
		updated.LLM.Timeout != "9m" {
		t.Fatalf("reinitialized LLM settings = %#v", updated.LLM)
	}
	if updated.Draw.Concurrency != 1 || updated.Draw.ContextTurns != 0 || updated.Draw.MaxContextTurns != 12 {
		t.Fatalf("reinitialized draw settings = %#v", updated.Draw)
	}
	if len(updated.Sources) != 2 ||
		!slices.ContainsFunc(updated.Sources, func(source config.Source) bool {
			return source.Agent == "codex" && source.Path == filepath.Join(home, ".codex", "sessions")
		}) ||
		!slices.ContainsFunc(updated.Sources, func(source config.Source) bool {
			return source.Path == custom.Path
		}) {
		t.Fatalf("reinitialized sources = %#v", updated.Sources)
	}
	if data, err := os.ReadFile(sentinel); err != nil || string(data) != "keep exactly" {
		t.Fatalf("reinit changed data: %q, %v", data, err)
	}
}

func TestApplyReinitFillsOnlyMissingInitDefaults(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv(config.ConfigEnvironment, "")
	root := filepath.Join(t.TempDir(), "vault")
	configPath := config.DefaultConfigPath(root)
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		t.Fatal(err)
	}
	customPath := filepath.Join(home, "custom-sessions")
	legacy := `root = ".."

[llm]
backend = "codex-cli"
draw_model = "draw-existing"
brew_model = "brew-existing"

[[sources]]
agent = "codex"
parser = "codex"
path = "` + customPath + `"
`
	if err := os.WriteFile(configPath, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Apply(Choices{
		Root: root, Backend: "codex-cli", DrawModel: "draw-existing", BrewModel: "brew-existing",
		InstallClaude: false, InstallCodex: false,
	}); err != nil {
		t.Fatal(err)
	}
	updated, err := config.LoadPath(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if updated.LLM.DrawEffort != config.DefaultDrawEffort || updated.LLM.BrewEffort != "" ||
		updated.Draw.Concurrency != config.DefaultDrawConcurrency ||
		updated.Draw.ContextTurns != config.DefaultDrawContextTurns ||
		updated.Draw.MaxContextTurns != config.DefaultDrawMaxContextTurns {
		t.Fatalf("missing defaults were not filled: llm=%#v draw=%#v", updated.LLM, updated.Draw)
	}
	if len(updated.Sources) != 1 || updated.Sources[0].Path != customPath {
		t.Fatalf("custom source was not preserved: %#v", updated.Sources)
	}
}
