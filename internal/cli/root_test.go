package cli

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCommandSurfaceDoesNotExposeDistill(t *testing.T) {
	root := newRootCommand()
	got := map[string]bool{}
	for _, command := range root.Commands() {
		got[command.Name()] = true
	}
	for _, name := range []string{"init", "draw", "brew", "show", "feedstock", "knowledge"} {
		if !got[name] {
			t.Errorf("missing command %q", name)
		}
	}
	if got["search"] {
		t.Fatal("legacy mixed search command must not be exposed")
	}
	if got["distill"] {
		t.Fatal("reserved distill command must not be implemented")
	}
	if got["completion"] {
		t.Fatal("unexpected default completion command")
	}
}

func TestKnowledgeSearchEscapesSubcommandNamesAfterDoubleDash(t *testing.T) {
	rootDir := t.TempDir()
	configPath := filepath.Join(t.TempDir(), "config.toml")
	configData := "root = " + quoteTOML(rootDir) + "\n\n[llm]\nbackend = \"claude-cli\"\n"
	if err := os.WriteFile(configPath, []byte(configData), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KNOWBREW_CONFIG", configPath)

	for _, invocation := range [][]string{
		{"knowledge", "--", "create"},
		{"kn", "--", "create"},
	} {
		command := newRootCommand()
		command.SetArgs(invocation)
		if err := command.Execute(); err != nil {
			t.Fatalf("%v routed to a subcommand: %v", invocation, err)
		}
	}
}

func TestFeedstockRejectsExplicitZeroLast(t *testing.T) {
	command := newRootCommand()
	command.SetArgs([]string{"feedstock", "--last", "0"})
	err := command.Execute()
	if err == nil || err.Error() != "--last must be greater than zero" {
		t.Fatalf("error = %v", err)
	}
}

func quoteTOML(value string) string {
	return `"` + value + `"`
}
