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
		Root: root, Backend: "claude-cli", DrawDraftModel: "fast-model",
		DrawExtractModel: "extract-model", BrewModel: "quality-model",
		DistillModel:   "document-model",
		EmbeddingModel: config.EmbeddingDisabled,
		InstallClaude:  false, InstallCodex: false,
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
	if loaded.LLM.DrawDraftModel != "fast-model" ||
		loaded.LLM.DrawExtractModel != "extract-model" ||
		loaded.LLM.BrewModel != "quality-model" ||
		loaded.LLM.DistillModel != "document-model" {
		t.Fatalf("LLM models = %#v", loaded.LLM)
	}
	if loaded.LLM.DrawDraftEffort != config.DefaultDrawDraftEffort ||
		loaded.LLM.DrawExtractEffort != "" || loaded.LLM.BrewEffort != "" ||
		loaded.LLM.DistillEffort != config.DefaultDistillEffort {
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
	if loaded.Embedding.Model != config.EmbeddingDisabled {
		t.Fatalf("embedding model = %q", loaded.Embedding.Model)
	}
	data, err := os.ReadFile(filepath.Join(root, ".knowbrew", "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{
		`draw_draft_effort = "low"`,
		`draw_extract_effort = ""`,
		`brew_effort = ""`,
		`distill_effort = "high"`,
		`context_turns = 3`,
		`max_context_turns = 20`,
		`model = "disabled"`,
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
	if len(warnings) != 0 || len(types) != 8 {
		t.Fatalf("init type masters = %#v, warnings = %#v", types, warnings)
	}
	templates, err := os.ReadDir(filepath.Join(root, "masters", "templates"))
	if err != nil {
		t.Fatal(err)
	}
	if len(templates) != 4 {
		t.Fatalf("init template masters = %#v", templates)
	}
	wantTemplates := []string{"concept.md", "decisions.md", "glossary.md", "reference.md"}
	for index, entry := range templates {
		if entry.IsDir() || entry.Name() != wantTemplates[index] {
			t.Fatalf("init template master %d = %#v, want %q", index, entry, wantTemplates[index])
		}
	}
	decisionTemplate, err := os.ReadFile(filepath.Join(root, "masters", "templates", "decisions.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{
		"# {{subject decisions title}}",
		"## {{decision area}}",
		"**{{rationale label}}:** {{decision rationale}}",
		"Include this entire line only when rationale is grounded in Knowledge.",
	} {
		if !strings.Contains(string(decisionTemplate), required) {
			t.Fatalf("default decisions template does not contain %q:\n%s", required, decisionTemplate)
		}
	}
	for _, forbidden := range []string{"## Superseded decisions", "{{superseded decision}}"} {
		if strings.Contains(string(decisionTemplate), forbidden) {
			t.Fatalf("default decisions template contains %q:\n%s", forbidden, decisionTemplate)
		}
	}
	writingGuides := map[string][]string{
		"common.md": {
			"# Common Writing Rules",
			"## Accuracy",
			"Assert only what the available evidence establishes.",
			"## Structure",
			"## Reader effort",
			"## Presentation and style",
			"Write in the language and style required by the user's configuration.",
		},
		"knowledge.md": {
			"# Knowledge Writing Rules",
			"## Statement phrasing",
			"## Compound Knowledge statements",
			"## Rationale phrasing",
		},
		"document.md": {
			"# Distilled Document Writing Rules",
			"## Information order",
			"When the Template does not prescribe an order",
			"## Headings",
			"## Presentation formats",
		},
	}
	for name, requiredValues := range writingGuides {
		writingRules, err := os.ReadFile(filepath.Join(root, "masters", "writing", name))
		if err != nil {
			t.Fatal(err)
		}
		for _, required := range requiredValues {
			if !strings.Contains(string(writingRules), required) {
				t.Fatalf("default %s does not contain %q:\n%s", name, required, writingRules)
			}
		}
	}
}

func TestApplyReinitMigratesRetiredDrawKeys(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv(config.ConfigEnvironment, "")
	root := filepath.Join(t.TempDir(), "vault")
	configPath := config.DefaultConfigPath(root)
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		t.Fatal(err)
	}
	retired := "root = \"..\"\n\n[llm]\nbackend = \"codex-cli\"\n" +
		"draw_model = \"old-draw\"\ndraw_effort = \"low\"\n" +
		"brew_model = \"old-brew\"\nbrew_effort = \"high\"\n\n" +
		"[embedding]\nmodel = \"disabled\"\n"
	if err := os.WriteFile(configPath, []byte(retired), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := config.LoadPath(configPath); err == nil {
		t.Fatal("retired keys loaded outside init")
	}

	if err := Apply(Choices{
		Root: root, Backend: "codex-cli", DrawDraftModel: "new-draft",
		DrawExtractModel: "new-extract", BrewModel: "new-brew",
		DistillModel:   "new-distill",
		EmbeddingModel: config.EmbeddingDisabled,
	}); err != nil {
		t.Fatal(err)
	}
	loaded, err := config.LoadPath(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.LLM.DrawDraftModel != "new-draft" || loaded.LLM.DrawExtractModel != "new-extract" {
		t.Fatalf("migrated models = %#v", loaded.LLM)
	}
	if loaded.LLM.DrawDraftEffort != "low" || loaded.LLM.DrawExtractEffort != "high" {
		t.Fatalf("migrated efforts = %#v", loaded.LLM)
	}
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "draw_model") || strings.Contains(string(data), "draw_effort =") {
		t.Fatalf("retired keys survived init:\n%s", data)
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
		Root: root, Backend: "codex-cli", DrawDraftModel: "old-draft",
		DrawExtractModel: "old-extract", BrewModel: "old-brew",
		DistillModel:   "old-distill",
		EmbeddingModel: config.EmbeddingDisabled,
		SourceNames:    []string{"claude", "codex"}, InstallClaude: false, InstallCodex: false,
	}); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadPath(config.DefaultConfigPath(root))
	if err != nil {
		t.Fatal(err)
	}
	cfg.LLM.DrawDraftEffort = "medium"
	cfg.LLM.BrewEffort = "high"
	cfg.LLM.Timeout = "9m"
	cfg.Draw.Concurrency = 1
	cfg.Draw.ContextTurns = 0
	cfg.Draw.MaxContextTurns = 12
	customPath := filepath.Join(home, "archived-sessions")
	custom := config.Source{Agent: "codex", Parser: "codex", Paths: []string{customPath}}
	cfg.Sources = append(cfg.Sources, custom)
	if _, err := config.Save(root, cfg); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(root, "knowledge", "keep.md")
	if err := os.WriteFile(sentinel, []byte("keep exactly"), 0o644); err != nil {
		t.Fatal(err)
	}
	writingRulesPath := filepath.Join(root, "masters", "writing", "common.md")
	if err := os.WriteFile(writingRulesPath, []byte("# My writing rules\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	knowledgeRulesPath := filepath.Join(root, "masters", "writing", "knowledge.md")
	if err := os.WriteFile(knowledgeRulesPath, []byte("# My Knowledge rules\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	documentRulesPath := filepath.Join(root, "masters", "writing", "document.md")
	if err := os.Remove(documentRulesPath); err != nil {
		t.Fatal(err)
	}

	if err := Apply(Choices{
		Root: root, Backend: "codex-cli", DrawDraftModel: "new-draft",
		DrawExtractModel: "new-extract", BrewModel: "new-brew",
		DistillModel:   "new-distill",
		EmbeddingModel: config.EmbeddingDisabled,
		SourceNames:    []string{"codex"}, InstallClaude: false, InstallCodex: false,
	}); err != nil {
		t.Fatal(err)
	}
	updated, err := config.LoadPath(config.DefaultConfigPath(root))
	if err != nil {
		t.Fatal(err)
	}
	if updated.LLM.DrawDraftModel != "new-draft" ||
		updated.LLM.DrawExtractModel != "new-extract" ||
		updated.LLM.BrewModel != "new-brew" ||
		updated.LLM.DistillModel != "new-distill" ||
		updated.LLM.DrawDraftEffort != "medium" || updated.LLM.BrewEffort != "high" ||
		updated.LLM.Timeout != "9m" {
		t.Fatalf("reinitialized LLM settings = %#v", updated.LLM)
	}
	if updated.Draw.Concurrency != 1 || updated.Draw.ContextTurns != 0 || updated.Draw.MaxContextTurns != 12 {
		t.Fatalf("reinitialized draw settings = %#v", updated.Draw)
	}
	if len(updated.Sources) != 1 || updated.Sources[0].Agent != "codex" ||
		!slices.Contains(updated.Sources[0].Paths, filepath.Join(home, ".codex", "sessions")) ||
		!slices.Contains(updated.Sources[0].Paths, filepath.Join(home, ".codex", "archived_sessions")) ||
		!slices.Contains(updated.Sources[0].Paths, customPath) {
		t.Fatalf("reinitialized sources = %#v", updated.Sources)
	}
	if data, err := os.ReadFile(sentinel); err != nil || string(data) != "keep exactly" {
		t.Fatalf("reinit changed data: %q, %v", data, err)
	}
	if data, err := os.ReadFile(writingRulesPath); err != nil || string(data) != "# My writing rules\n" {
		t.Fatalf("reinit changed common writing rules: %q, %v", data, err)
	}
	if data, err := os.ReadFile(knowledgeRulesPath); err != nil || string(data) != "# My Knowledge rules\n" {
		t.Fatalf("reinit changed Knowledge writing rules: %q, %v", data, err)
	}
	if data, err := os.ReadFile(documentRulesPath); err != nil ||
		!strings.Contains(string(data), "# Distilled Document Writing Rules") {
		t.Fatalf("reinit did not restore default document writing rules: %q, %v", data, err)
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
		Root: root, Backend: "codex-cli", DrawDraftModel: "draft-existing",
		DrawExtractModel: "extract-existing", BrewModel: "brew-existing",
		EmbeddingModel: config.EmbeddingDisabled,
		InstallClaude:  false, InstallCodex: false,
	}); err != nil {
		t.Fatal(err)
	}
	updated, err := config.LoadPath(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if updated.LLM.DrawDraftEffort != config.DefaultDrawDraftEffort ||
		updated.LLM.BrewEffort != "" ||
		updated.Draw.Concurrency != config.DefaultDrawConcurrency ||
		updated.Draw.ContextTurns != config.DefaultDrawContextTurns ||
		updated.Draw.MaxContextTurns != config.DefaultDrawMaxContextTurns {
		t.Fatalf("missing defaults were not filled: llm=%#v draw=%#v", updated.LLM, updated.Draw)
	}
	if len(updated.Sources) != 1 || len(updated.Sources[0].Paths) != 1 ||
		updated.Sources[0].Paths[0] != customPath {
		t.Fatalf("custom source was not preserved: %#v", updated.Sources)
	}
}

func TestApplyReinitPreservesCustomEmbeddingPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv(config.ConfigEnvironment, "")
	root := filepath.Join(t.TempDir(), "vault")
	if err := Apply(Choices{
		Root: root, Backend: "codex-cli", DrawDraftModel: "draft",
		DrawExtractModel: "extract", BrewModel: "brew",
		EmbeddingModel: config.EmbeddingDisabled,
		InstallClaude:  false, InstallCodex: false,
	}); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadPath(config.DefaultConfigPath(root))
	if err != nil {
		t.Fatal(err)
	}
	customPath := filepath.Join(root, "models", "custom")
	cfg.Embedding = config.Embedding{Model: config.EmbeddingCustom, Path: customPath}
	if _, err := config.Save(root, cfg); err != nil {
		t.Fatal(err)
	}

	if err := Apply(Choices{
		Root: root, Backend: "codex-cli", DrawDraftModel: "draft",
		DrawExtractModel: "extract", BrewModel: "brew",
		EmbeddingModel: config.EmbeddingCustom,
		InstallClaude:  false, InstallCodex: false,
	}); err != nil {
		t.Fatal(err)
	}
	updated, err := config.LoadPath(config.DefaultConfigPath(root))
	if err != nil {
		t.Fatal(err)
	}
	if updated.Embedding.Model != config.EmbeddingCustom || updated.Embedding.Path != customPath {
		t.Fatalf("custom embedding after reinit = %#v", updated.Embedding)
	}
}
