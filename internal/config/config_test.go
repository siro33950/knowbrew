package config

import (
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
		LLM:     LLM{Backend: "claude-cli", Model: "test-model"},
		Sources: []Source{{Agent: "claude", Parser: "claude", Path: "~/logs"}},
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
	if loaded.Sources[0].Path != filepath.Join(home, "logs") {
		t.Fatalf("expanded source = %q", loaded.Sources[0].Path)
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
}
