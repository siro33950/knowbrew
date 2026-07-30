package setup

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/siro33950/knowbrew/internal/config"
)

func TestApplyCreatesRootLocalConfigAndUserLocator(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv(config.ConfigEnvironment, "")
	root := filepath.Join(t.TempDir(), "vault")
	if err := Apply(Choices{
		Root: root, Backend: "claude-cli", InstallClaude: false, InstallCodex: false,
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
}
