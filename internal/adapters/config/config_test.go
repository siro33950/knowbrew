package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSaveAndLoadUsesRootLocalConfigAndGlobalLocator(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv(ConfigEnvironment, "")
	root := filepath.Join(t.TempDir(), "vault")
	cfg := Config{
		LLM: LLM{
			Backend: "claude-cli", DrawDraftModel: "fast-model",
			DrawExtractModel: "extract-model", BrewModel: "quality-model",
			DistillModel: "document-model", DrawDraftEffort: "low",
			DrawExtractEffort: "high", BrewEffort: "max", DistillEffort: "high",
		},
		Draw:    Draw{ContextTurns: DefaultDrawContextTurns},
		Sources: []Source{{Agent: "claude", Parser: "claude", Paths: []string{"~/logs"}}},
	}
	path, err := Save(root, cfg)
	if err != nil {
		t.Fatal(err)
	}
	wantPath := filepath.Join(root, ".knowbrew", "config.toml")
	if path != wantPath {
		t.Fatalf("path = %q, want %q", path, wantPath)
	}
	loaded, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	absoluteRoot, _ := filepath.Abs(root)
	if loaded.Root != absoluteRoot {
		t.Fatalf("root = %q, want %q", loaded.Root, absoluteRoot)
	}
	if len(loaded.Sources[0].Paths) != 1 || loaded.Sources[0].Paths[0] != filepath.Join(home, "logs") {
		t.Fatalf("expanded source paths = %#v", loaded.Sources[0].Paths)
	}
	if loaded.LLM.DrawDraftModel != "fast-model" ||
		loaded.LLM.DrawExtractModel != "extract-model" ||
		loaded.LLM.BrewModel != "quality-model" ||
		loaded.LLM.DistillModel != "document-model" {
		t.Fatalf("LLM models = %#v", loaded.LLM)
	}
	if loaded.LLM.DrawDraftEffort != "low" || loaded.LLM.DrawExtractEffort != "high" ||
		loaded.LLM.BrewEffort != "max" || loaded.LLM.DistillEffort != "high" {
		t.Fatalf("LLM efforts = %#v", loaded.LLM)
	}
	if loaded.Draw.Concurrency != DefaultDrawConcurrency {
		t.Fatalf("draw concurrency = %d, want %d", loaded.Draw.Concurrency, DefaultDrawConcurrency)
	}
	if loaded.Draw.ContextTurns != DefaultDrawContextTurns {
		t.Fatalf("draw context turns = %d, want %d", loaded.Draw.ContextTurns, DefaultDrawContextTurns)
	}
	if loaded.Draw.MaxContextTurns != DefaultDrawMaxContextTurns {
		t.Fatalf("draw max context turns = %d, want %d", loaded.Draw.MaxContextTurns, DefaultDrawMaxContextTurns)
	}
	if _, err := os.Stat(filepath.Join(home, ".config", "knowbrew", "location.toml")); err != nil {
		t.Fatal(err)
	}
}

func TestEnvironmentOverridesLocator(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	root := t.TempDir()
	path := DefaultConfigPath(root)
	cfg := Config{Root: "..", LLM: LLM{Backend: "codex-cli"}}
	if _, err := Save(root, cfg); err != nil {
		t.Fatal(err)
	}
	t.Setenv(ConfigEnvironment, path)
	loaded, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Path != path {
		t.Fatalf("path = %q, want %q", loaded.Path, path)
	}
}

func TestLoadMigratesLegacySourcePathToPaths(t *testing.T) {
	root := t.TempDir()
	path := DefaultConfigPath(root)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	legacyPath := filepath.Join(root, "legacy-sessions")
	contents := fmt.Sprintf(`root = ".."

[llm]
backend = "codex-cli"
draw_draft_model = ""
draw_draft_effort = ""
draw_extract_model = ""
draw_extract_effort = ""

[[sources]]
agent = "codex"
parser = "codex"
path = %q
`, legacyPath)
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadPath(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Sources) != 1 || len(loaded.Sources[0].Paths) != 1 ||
		loaded.Sources[0].Paths[0] != legacyPath || loaded.Sources[0].LegacyPath != "" {
		t.Fatalf("migrated sources = %#v", loaded.Sources)
	}
}

func TestLLMTimeoutDuration(t *testing.T) {
	tests := []struct {
		name    string
		value   string
		want    time.Duration
		wantErr string
	}{
		{name: "default", want: DefaultLLMTimeout},
		{name: "custom", value: "45s", want: 45 * time.Second},
		{name: "invalid", value: "later", wantErr: "invalid LLM timeout"},
		{name: "non-positive", value: "0s", wantErr: "greater than zero"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := (LLM{Timeout: test.value}).TimeoutDuration()
			if test.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantErr) {
					t.Fatalf("error = %v, want %q", err, test.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("timeout = %s, want %s", got, test.want)
			}
		})
	}
}

func TestNormalizePopulatesDefaultLLMTimeout(t *testing.T) {
	cfg := Config{
		Path: filepath.Join(t.TempDir(), ".knowbrew", "config.toml"),
		LLM:  LLM{Backend: "claude-cli"},
	}
	if err := cfg.Normalize(); err != nil {
		t.Fatal(err)
	}
	if cfg.LLM.Timeout != DefaultLLMTimeout.String() {
		t.Fatalf("timeout = %q, want %q", cfg.LLM.Timeout, DefaultLLMTimeout.String())
	}
	if cfg.Draw.Concurrency != DefaultDrawConcurrency {
		t.Fatalf("draw concurrency = %d, want %d", cfg.Draw.Concurrency, DefaultDrawConcurrency)
	}
	if cfg.Draw.ContextTurns != DefaultDrawContextTurns {
		t.Fatalf("draw context turns = %d, want %d", cfg.Draw.ContextTurns, DefaultDrawContextTurns)
	}
	if cfg.Draw.MaxContextTurns != DefaultDrawMaxContextTurns {
		t.Fatalf("draw max context turns = %d, want %d", cfg.Draw.MaxContextTurns, DefaultDrawMaxContextTurns)
	}
}

func TestNormalizeDoesNotValidateEffortVocabulary(t *testing.T) {
	cfg := Config{
		Path: filepath.Join(t.TempDir(), ".knowbrew", "config.toml"),
		LLM: LLM{
			Backend: "claude-cli", DrawDraftEffort: "accepted-by-a-future-cli",
			DrawExtractEffort: "also-backend-specific", BrewEffort: "backend-specific",
		},
	}
	if err := cfg.Normalize(); err != nil {
		t.Fatalf("effort vocabulary should be delegated to the backend: %v", err)
	}
}

func TestLoadRejectsLegacyModelWithMigrationGuidance(t *testing.T) {
	root := t.TempDir()
	path := DefaultConfigPath(root)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	data := []byte("root = \"..\"\n\n[llm]\nbackend = \"claude-cli\"\nmodel = \"legacy\"\n")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(ConfigEnvironment, path)
	_, err := Load()
	if err == nil || !strings.Contains(
		err.Error(),
		"migrate it to draw_draft_model, draw_extract_model, brew_model, and distill_model",
	) {
		t.Fatalf("error = %v", err)
	}
}

// drawStageConfig writes the minimum configuration Load accepts. Both Draw
// stages must name their own model and effort; extra tables follow them.
func drawStageConfig(extra string) string {
	return "root = \"..\"\n\n[llm]\nbackend = \"claude-cli\"\n" +
		"draw_draft_model = \"\"\ndraw_draft_effort = \"\"\n" +
		"draw_extract_model = \"\"\ndraw_extract_effort = \"\"\n" + extra
}

func TestLoadRejectsRetiredDrawKeys(t *testing.T) {
	root := t.TempDir()
	path := DefaultConfigPath(root)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	data := []byte("root = \"..\"\n\n[llm]\nbackend = \"claude-cli\"\n" +
		"draw_model = \"fast\"\ndraw_effort = \"low\"\n")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(ConfigEnvironment, path)
	_, err := Load()
	if err == nil ||
		!strings.Contains(err.Error(), "draw_model and draw_effort are no longer supported") ||
		!strings.Contains(err.Error(), "draw_draft_model") {
		t.Fatalf("error = %v", err)
	}
}

func TestLoadRequiresBothDrawStagesToNameTheirOwnKeys(t *testing.T) {
	root := t.TempDir()
	path := DefaultConfigPath(root)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	data := []byte("root = \"..\"\n\n[llm]\nbackend = \"claude-cli\"\n" +
		"draw_draft_model = \"fast\"\ndraw_draft_effort = \"low\"\nbrew_model = \"quality\"\n")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(ConfigEnvironment, path)
	_, err := Load()
	if err == nil ||
		!strings.Contains(err.Error(), "draw_extract_model, draw_extract_effort must be set") {
		t.Fatalf("error = %v", err)
	}
}

func TestLoadPathForSetupMigratesRetiredDrawKeys(t *testing.T) {
	root := t.TempDir()
	path := DefaultConfigPath(root)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	data := []byte("root = \"..\"\n\n[llm]\nbackend = \"claude-cli\"\n" +
		"draw_model = \"fast\"\ndraw_effort = \"low\"\n" +
		"brew_model = \"quality\"\nbrew_effort = \"max\"\n")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadPathForSetup(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.LLM.DrawDraftModel != "fast" || loaded.LLM.DrawDraftEffort != "low" {
		t.Fatalf("retired draw keys did not seed the draft stage: %#v", loaded.LLM)
	}
	if loaded.LLM.DrawExtractModel != "quality" || loaded.LLM.DrawExtractEffort != "max" {
		t.Fatalf("extract stage did not keep the previous behaviour: %#v", loaded.LLM)
	}
}

func TestLoadAllowsExistingConfigWithoutBrewAndDistillKeys(t *testing.T) {
	root := t.TempDir()
	path := DefaultConfigPath(root)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(drawStageConfig("")), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(ConfigEnvironment, path)
	loaded, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.LLM.DrawDraftEffort != "" || loaded.LLM.DrawExtractEffort != "" ||
		loaded.LLM.BrewEffort != "" {
		t.Fatalf("empty effort keys should remain empty: %#v", loaded.LLM)
	}
	if loaded.LLM.DistillModel != "" || loaded.LLM.DistillEffort != DefaultDistillEffort {
		t.Fatalf("missing distill keys should use migration defaults: %#v", loaded.LLM)
	}
	if loaded.Embedding.Model != "" {
		t.Fatalf("missing embedding config should keep full-text compatibility: %#v", loaded.Embedding)
	}
}

func TestFillInitDefaultsSelectsRecommendedEmbeddingModel(t *testing.T) {
	cfg := Config{}
	cfg.FillInitDefaults()
	if cfg.Embedding.Model != EmbeddingRuri {
		t.Fatalf("embedding model = %q, want %q", cfg.Embedding.Model, EmbeddingRuri)
	}
}

func TestNormalizeEmbeddingConfiguration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "custom-model")
	cfg := Config{Path: filepath.Join(t.TempDir(), "config.toml"), LLM: LLM{Backend: "claude-cli"}, Embedding: Embedding{
		Model: EmbeddingCustom, Path: path,
	}}
	if err := cfg.Normalize(); err != nil {
		t.Fatal(err)
	}
	if cfg.Embedding.Path != path {
		t.Fatalf("custom path = %q, want %q", cfg.Embedding.Path, path)
	}
	cfg = Config{Path: filepath.Join(t.TempDir(), "config.toml"), LLM: LLM{Backend: "claude-cli"}, Embedding: Embedding{Model: EmbeddingCustom}}
	if err := cfg.Normalize(); err == nil || !strings.Contains(err.Error(), "requires path") {
		t.Fatalf("missing custom path error = %v", err)
	}
	cfg = Config{Path: filepath.Join(t.TempDir(), "config.toml"), LLM: LLM{Backend: "claude-cli"}, Embedding: Embedding{Model: "unknown"}}
	if err := cfg.Normalize(); err == nil || !strings.Contains(err.Error(), "unsupported embedding model") {
		t.Fatalf("unknown model error = %v", err)
	}
}

func TestAPILLMRequiresBothTaskModels(t *testing.T) {
	cfg := Config{
		Path: filepath.Join(t.TempDir(), ".knowbrew", "config.toml"),
		LLM:  LLM{Backend: "api", DrawDraftModel: "fast"},
	}
	if err := cfg.Normalize(); err == nil || !strings.Contains(
		err.Error(),
		"draw_draft_model, draw_extract_model, brew_model, and distill_model",
	) {
		t.Fatalf("error = %v", err)
	}
}

func TestLoadRejectsExplicitNonPositiveDrawConcurrency(t *testing.T) {
	root := t.TempDir()
	path := DefaultConfigPath(root)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	data := []byte(drawStageConfig("\n[draw]\nconcurrency = 0\n"))
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(ConfigEnvironment, path)
	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "draw concurrency must be at least 1") {
		t.Fatalf("error = %v", err)
	}
}

func TestLoadContextMaxTokensDefaultsAndValidation(t *testing.T) {
	for _, test := range []struct {
		name    string
		section string
		want    int
		wantErr bool
	}{
		{name: "default", section: "", want: DefaultContextMaxTokens},
		{name: "explicit", section: "\n[context]\nmax_tokens = 500\n", want: 500},
		{name: "zero", section: "\n[context]\nmax_tokens = 0\n", wantErr: true},
		{name: "negative", section: "\n[context]\nmax_tokens = -1\n", wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			path := DefaultConfigPath(root)
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			data := []byte(drawStageConfig(test.section))
			if err := os.WriteFile(path, data, 0o600); err != nil {
				t.Fatal(err)
			}
			t.Setenv(ConfigEnvironment, path)
			loaded, err := Load()
			if test.wantErr {
				if err == nil || !strings.Contains(err.Error(), "context max_tokens must be at least 1") {
					t.Fatalf("error = %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if loaded.Context.MaxTokens != test.want {
				t.Fatalf("max_tokens = %d, want %d", loaded.Context.MaxTokens, test.want)
			}
		})
	}
}

func TestLoadAcceptsZeroAndRejectsNegativeDrawContextTurns(t *testing.T) {
	for _, test := range []struct {
		name    string
		value   int
		wantErr bool
	}{
		{name: "disabled", value: 0},
		{name: "negative", value: -1, wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			path := DefaultConfigPath(root)
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			data := fmt.Appendf(
				nil,
				drawStageConfig("\n[draw]\nconcurrency = 1\ncontext_turns = %d\n"),
				test.value,
			)
			if err := os.WriteFile(path, data, 0o600); err != nil {
				t.Fatal(err)
			}
			t.Setenv(ConfigEnvironment, path)
			loaded, err := Load()
			if test.wantErr {
				if err == nil || !strings.Contains(err.Error(), "context_turns must be at least 0") {
					t.Fatalf("error = %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if loaded.Draw.ContextTurns != 0 {
				t.Fatalf("context turns = %d, want 0", loaded.Draw.ContextTurns)
			}
		})
	}
}

func TestLoadRejectsMaxContextBelowInitialContext(t *testing.T) {
	root := t.TempDir()
	path := DefaultConfigPath(root)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	data := []byte(drawStageConfig("\n[draw]\nconcurrency = 1\ncontext_turns = 3\nmax_context_turns = 2\n"))
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(ConfigEnvironment, path)
	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "max_context_turns must be at least context_turns") {
		t.Fatalf("error = %v", err)
	}
}
