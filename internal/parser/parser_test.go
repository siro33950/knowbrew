package parser

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestParsers(t *testing.T) {
	tests := []struct {
		name         string
		parser       Parser
		content      string
		wantAgent    string
		wantQuote    string
		wantCommands int
		wantFiles    int
		wantExit     int
		checkExit    bool
	}{
		{
			name:   "claude string content and tool records",
			parser: Claude{},
			content: `{"type":"user","sessionId":"abc","timestamp":"2026-07-30T01:02:03Z","cwd":"/repo","gitBranch":"main","message":{"role":"user","content":"fix the test"}}
{"type":"assistant","sessionId":"abc","timestamp":"2026-07-30T01:02:04Z","message":{"role":"assistant","content":[{"type":"tool_use","id":"tool-1","name":"Bash","input":{"command":"go test ./..."}},{"type":"tool_use","id":"tool-2","name":"Edit","input":{"file_path":"/repo/a.go","old_string":"a","new_string":"b"}}]}}
{"type":"user","sessionId":"abc","timestamp":"2026-07-30T01:02:05Z","toolUseResult":{"exitCode":1},"message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"tool-1","is_error":true,"content":"failed"}]}}
`,
			wantAgent:    "claude",
			wantQuote:    "fix the test",
			wantCommands: 1,
			wantFiles:    1,
			wantExit:     1,
			checkExit:    true,
		},
		{
			name:   "claude ignores meta and tool result users",
			parser: Claude{},
			content: `{"type":"user","sessionId":"abc","timestamp":"2026-07-30T01:02:03Z","isMeta":true,"message":{"role":"user","content":"hidden"}}
{"type":"user","sessionId":"abc","timestamp":"2026-07-30T01:02:04Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"x","content":"output"}]}}
{"type":"user","sessionId":"abc","timestamp":"2026-07-30T01:02:05Z","message":{"role":"user","content":[{"type":"text","text":"visible"}]}}
`,
			wantAgent: "claude",
			wantQuote: "visible",
		},
		{
			name:   "claude extracts string-form nonzero exit code",
			parser: Claude{},
			content: `{"type":"user","sessionId":"abc","timestamp":"2026-07-30T01:02:03Z","message":{"role":"user","content":"run it"}}
{"type":"assistant","sessionId":"abc","timestamp":"2026-07-30T01:02:04Z","message":{"role":"assistant","content":[{"type":"tool_use","id":"tool-1","name":"Bash","input":{"command":"false"}}]}}
{"type":"user","sessionId":"abc","timestamp":"2026-07-30T01:02:05Z","toolUseResult":"Error: Exit code 2\nfailed","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"tool-1","is_error":true,"content":"Exit code 2\nfailed"}]}}
`,
			wantAgent:    "claude",
			wantQuote:    "run it",
			wantCommands: 1,
			wantExit:     2,
			checkExit:    true,
		},
		{
			name:   "codex explicit user event and function calls",
			parser: Codex{},
			content: `{"timestamp":"2026-07-30T01:00:00Z","type":"session_meta","payload":{"id":"session-1","cwd":"/repo","git":{"branch":"main","repository_url":"https://example/repo.git"}}}
{"timestamp":"2026-07-30T01:00:01Z","type":"event_msg","payload":{"type":"user_message","message":"implement it"}}
{"timestamp":"2026-07-30T01:00:02Z","type":"response_item","payload":{"type":"function_call","name":"exec_command","call_id":"c1","arguments":"{\"cmd\":\"go test ./...\"}"}}
{"timestamp":"2026-07-30T01:00:03Z","type":"response_item","payload":{"type":"function_call_output","call_id":"c1","output":"{\"exit_code\":0,\"output\":\"ok\"}"}}
{"timestamp":"2026-07-30T01:00:04Z","type":"response_item","payload":{"type":"custom_tool_call","name":"apply_patch","call_id":"c2","input":"*** Begin Patch\n*** Update File: a.go\n*** End Patch"}}
`,
			wantAgent:    "codex",
			wantQuote:    "implement it",
			wantCommands: 1,
			wantFiles:    1,
			wantExit:     0,
			checkExit:    true,
		},
		{
			name:   "codex ignores duplicated response user message",
			parser: Codex{},
			content: `{"timestamp":"2026-07-30T01:00:00Z","type":"session_meta","payload":{"id":"session-1","cwd":"/repo"}}
{"timestamp":"2026-07-30T01:00:01Z","type":"event_msg","payload":{"type":"user_message","message":"one feedstock"}}
{"timestamp":"2026-07-30T01:00:01Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"one feedstock"}]}}
`,
			wantAgent: "codex",
			wantQuote: "one feedstock",
		},
		{
			name:   "codex accepts object function arguments",
			parser: Codex{},
			content: `{"timestamp":"2026-07-30T01:00:00Z","type":"session_meta","payload":{"id":"session-1","cwd":"/repo"}}
{"timestamp":"2026-07-30T01:00:01Z","type":"event_msg","payload":{"type":"user_message","message":"run it"}}
{"timestamp":"2026-07-30T01:00:02Z","type":"response_item","payload":{"type":"function_call","name":"exec_command","call_id":"c1","arguments":{"cmd":"go test ./..."}}}
`,
			wantAgent:    "codex",
			wantQuote:    "run it",
			wantCommands: 1,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "session.jsonl")
			if err := os.WriteFile(path, []byte(test.content), 0o600); err != nil {
				t.Fatal(err)
			}
			feedstocks, warnings, err := test.parser.Parse(path)
			if err != nil {
				t.Fatal(err)
			}
			if len(warnings) != 0 {
				t.Fatalf("warnings = %#v", warnings)
			}
			if len(feedstocks) != 1 {
				t.Fatalf("got %d feedstocks, want 1", len(feedstocks))
			}
			if feedstocks[0].Agent != test.wantAgent {
				t.Errorf("agent = %q, want %q", feedstocks[0].Agent, test.wantAgent)
			}
			if feedstocks[0].UserQuote != test.wantQuote {
				t.Errorf("quote = %q, want %q", feedstocks[0].UserQuote, test.wantQuote)
			}
			if len(feedstocks[0].Commands) != test.wantCommands {
				t.Errorf("commands = %d, want %d", len(feedstocks[0].Commands), test.wantCommands)
			}
			if len(feedstocks[0].FilesChanged) != test.wantFiles {
				t.Errorf("files = %d, want %d", len(feedstocks[0].FilesChanged), test.wantFiles)
			}
			if test.checkExit {
				if len(feedstocks[0].Commands) == 0 || feedstocks[0].Commands[0].ExitCode == nil {
					t.Fatal("missing command exit code")
				}
				if *feedstocks[0].Commands[0].ExitCode != test.wantExit {
					t.Errorf("exit code = %d, want %d", *feedstocks[0].Commands[0].ExitCode, test.wantExit)
				}
			}
		})
	}
}

func TestRealLogWhenConfigured(t *testing.T) {
	tests := []struct {
		environment string
		parser      Parser
	}{
		{environment: "KNOWBREW_TEST_CLAUDE_LOG", parser: Claude{}},
		{environment: "KNOWBREW_TEST_CODEX_LOG", parser: Codex{}},
	}
	for _, test := range tests {
		path := os.Getenv(test.environment)
		if path == "" {
			continue
		}
		feedstocks, warnings, err := test.parser.Parse(path)
		if err != nil {
			t.Fatalf("%s: %v", test.environment, err)
		}
		for _, warning := range warnings {
			t.Log(warning.String())
		}
		if len(feedstocks) == 0 {
			t.Fatalf("%s: parsed no user feedstocks", test.environment)
		}
		for _, feedstock := range feedstocks {
			if feedstock.ID == "" || feedstock.Session.ID == "" || feedstock.UserQuote == "" || feedstock.Timestamp.IsZero() {
				t.Fatalf("%s: incomplete feedstock metadata: %#v", test.environment, feedstock)
			}
		}
		t.Logf("%s: parsed %d feedstocks", test.environment, len(feedstocks))
	}
}

func TestParsersSkipMalformedJSONLines(t *testing.T) {
	tests := []struct {
		name    string
		parser  Parser
		content string
	}{
		{
			name:   "Claude",
			parser: Claude{},
			content: `{"type":"user","sessionId":"session","timestamp":"2026-07-30T01:00:00Z","message":{"role":"user","content":"first"}}
{definitely not JSON}
{"type":"user","sessionId":"session","timestamp":"2026-07-30T01:00:01Z","message":{"role":"user","content":"second"}}
`,
		},
		{
			name:   "Codex",
			parser: Codex{},
			content: `{"timestamp":"2026-07-30T01:00:00Z","type":"session_meta","payload":{"id":"session","cwd":"/repo"}}
{definitely not JSON}
{"timestamp":"2026-07-30T01:00:01Z","type":"event_msg","payload":{"type":"user_message","message":"first"}}
{"timestamp":"2026-07-30T01:00:02Z","type":"event_msg","payload":{"type":"user_message","message":"second"}}
`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "session.jsonl")
			if err := os.WriteFile(path, []byte(test.content), 0o600); err != nil {
				t.Fatal(err)
			}
			feedstocks, warnings, err := test.parser.Parse(path)
			if err != nil {
				t.Fatal(err)
			}
			if len(feedstocks) != 2 {
				t.Fatalf("feedstocks = %#v", feedstocks)
			}
			if len(warnings) != 1 {
				t.Fatalf("warnings = %#v", warnings)
			}
			if warnings[0].Path != path+":2" || !strings.Contains(warnings[0].Reason, "decode "+test.name+" record") {
				t.Fatalf("warning = %#v", warnings[0])
			}
			if !strings.HasPrefix(warnings[0].String(), "skipped: "+path+":2:") {
				t.Fatalf("warning string = %q", warnings[0].String())
			}
		})
	}
}

func TestClippedPreservesUTF8(t *testing.T) {
	input := strings.Repeat("日本語", 1000)
	output := clipped(input)
	if !utf8.ValidString(output) {
		t.Fatalf("clipped string is invalid UTF-8: %q", output[len(output)-20:])
	}
	if !strings.HasSuffix(output, "…") {
		t.Fatalf("clipped output has no ellipsis")
	}
	if len(output) > 4000+len("…") {
		t.Fatalf("clipped output is %d bytes", len(output))
	}
}
