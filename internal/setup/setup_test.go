package setup

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

func TestMergeClaudeSettingsPreservesExistingContentAndIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	existing := `{"permissions":{"allow":["Read"]},"hooks":{"SessionStart":[{"matcher":"startup","hooks":[{"type":"command","command":"echo existing"}]},{"matcher":"startup","hooks":[{"type":"command","command":"knowbrew search --trigger always"}]}]}}`
	if err := os.WriteFile(path, []byte(existing), 0o600); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := MergeClaudeSettings(path, "/opt/bin/knowbrew"); err != nil {
			t.Fatal(err)
		}
	}
	data, _ := os.ReadFile(path)
	var root map[string]any
	if err := json.Unmarshal(data, &root); err != nil {
		t.Fatal(err)
	}
	if root["permissions"] == nil {
		t.Fatal("existing permissions were lost")
	}
	if count := strings.Count(string(data), "Loading approved knowbrew rules"); count != 1 {
		t.Fatalf("knowbrew hook count = %d", count)
	}
	if !strings.Contains(string(data), "echo existing") {
		t.Fatal("existing hook was lost")
	}
	if strings.Contains(string(data), "search --trigger always") {
		t.Fatalf("legacy hook was not removed:\n%s", data)
	}
	if !strings.Contains(string(data), "/opt/bin/knowbrew knowledge --trigger always") {
		t.Fatalf("new knowledge hook was not installed:\n%s", data)
	}
}

func TestMergeCodexConfigAndInstructionsAreIdempotent(t *testing.T) {
	root := t.TempDir()
	configPath := filepath.Join(root, "config.toml")
	if err := os.WriteFile(configPath, []byte("model = \"existing\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := MergeCodexConfig(configPath, "/opt/bin/knowbrew"); err != nil {
			t.Fatal(err)
		}
	}
	data, _ := os.ReadFile(configPath)
	if strings.Count(string(data), tomlStart) != 1 || !strings.Contains(string(data), `model = "existing"`) {
		t.Fatalf("unexpected config:\n%s", data)
	}
	var parsed map[string]any
	if _, err := toml.Decode(string(data), &parsed); err != nil {
		t.Fatal(err)
	}

	instructionsPath := filepath.Join(root, "AGENTS.md")
	if err := os.WriteFile(instructionsPath, []byte("# Existing\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := MergeInstructions(instructionsPath); err != nil {
			t.Fatal(err)
		}
	}
	instructions, _ := os.ReadFile(instructionsPath)
	if strings.Count(string(instructions), startMarker) != 1 || !strings.Contains(string(instructions), "# Existing") {
		t.Fatalf("unexpected instructions:\n%s", instructions)
	}
	for _, expected := range []string{
		"knowbrew knowledge [filters] -- <keywords>",
		"knowbrew feedstock --subject <name> --topic <name>",
		"Always place search keywords after `--`",
		"status: active",
		"trigger: always",
	} {
		if !strings.Contains(string(instructions), expected) {
			t.Fatalf("instructions do not contain %q:\n%s", expected, instructions)
		}
	}
}
