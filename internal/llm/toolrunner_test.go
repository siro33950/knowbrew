package llm

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/siro33950/knowbrew/internal/config"
)

func TestDecodeToolArguments(t *testing.T) {
	tests := []struct {
		name string
		raw  string
	}{
		{name: "OpenAI string", raw: `"{\"feedstock_id\":\"feedstock-1\"}"`},
		{name: "Ollama object", raw: `{"feedstock_id":"feedstock-1"}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var values map[string]any
			if err := decodeToolArguments(json.RawMessage(test.raw), &values); err != nil {
				t.Fatal(err)
			}
			if values["feedstock_id"] != "feedstock-1" {
				t.Fatalf("arguments = %#v", values)
			}
		})
	}
}

func TestReadToolsMapToTargetedCLICommands(t *testing.T) {
	tests := []struct {
		name      string
		arguments map[string]any
		want      []string
	}{
		{
			name: "knowledge_search",
			arguments: map[string]any{
				"include_pending": true,
				"subject":         "knowbrew",
				"limit":           float64(7),
				"keywords":        []any{"create", "index"},
			},
			want: []string{
				"/bin/knowbrew", "knowledge", "--include-pending",
				"--subject", "knowbrew", "--limit", "7", "--", "create", "index",
			},
		},
		{
			name: "feedstock_search",
			arguments: map[string]any{
				"topic":   "testing",
				"session": "session-1",
				"last":    float64(3),
			},
			want: []string{
				"/bin/knowbrew", "feedstock", "--topic", "testing",
				"--session", "session-1", "--last", "3",
			},
		},
		{
			name:      "show",
			arguments: map[string]any{"feedstock_ids": []any{"feedstock-1", "feedstock-2"}},
			want:      []string{"/bin/knowbrew", "show", "feedstock-1", "feedstock-2"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			command, err := commandForTool("/bin/knowbrew", test.name, test.arguments)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(command, test.want) {
				t.Fatalf("command = %#v, want %#v", command, test.want)
			}
		})
	}
}

func TestBrewToolSurfaceIncludesReadsButNotFeedstockMutation(t *testing.T) {
	var names []string
	for _, definition := range toolSchemas(TaskBrew) {
		function := definition["function"].(map[string]any)
		names = append(names, function["name"].(string))
	}
	for _, required := range []string{
		"knowledge_search",
		"feedstock_search",
		"show",
		"knowledge_create",
		"knowledge_add_source",
		"knowledge_invalidate",
	} {
		if !containsString(names, required) {
			t.Fatalf("tool surface %#v does not contain %q", names, required)
		}
	}
	if containsString(names, "feedstock_annotate") {
		t.Fatalf("brew tool surface exposes feedstock mutation: %#v", names)
	}
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func TestCommandForTool(t *testing.T) {
	command, err := commandForTool("/bin/knowbrew", "knowledge_create", map[string]any{
		"slug": "claim", "applies_when": "when testing", "body": "# Claim",
		"sources": []any{"feedstock-1"}, "topics": []any{"testing"},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"/bin/knowbrew", "knowledge", "create", "claim",
		"--applies-when", "when testing", "--body", "# Claim",
		"--source", "feedstock-1", "--topic", "testing",
	}
	if !reflect.DeepEqual(command, want) {
		t.Fatalf("command = %#v, want %#v", command, want)
	}
}

func TestInvocationEnvironmentReplacesInheritedValues(t *testing.T) {
	base := []string{
		"PATH=/bin",
		config.ConfigEnvironment + "=/stale/config.toml",
		config.InvocationFeedstockEnvironment + "=stale-feedstock",
		config.InvocationIDEnvironment + "=stale-invocation",
	}
	environment := invocationEnvironment(base, "/current/config.toml", "feedstock-1", "invocation-1")
	for _, test := range []struct {
		key  string
		want string
	}{
		{config.ConfigEnvironment, "/current/config.toml"},
		{config.InvocationFeedstockEnvironment, "feedstock-1"},
		{config.InvocationIDEnvironment, "invocation-1"},
	} {
		var matches []string
		for _, entry := range environment {
			if strings.HasPrefix(entry, test.key+"=") {
				matches = append(matches, entry)
			}
		}
		if len(matches) != 1 || matches[0] != test.key+"="+test.want {
			t.Fatalf("%s entries = %#v", test.key, matches)
		}
	}
}
