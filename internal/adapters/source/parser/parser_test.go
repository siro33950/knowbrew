package parser

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParsers(t *testing.T) {
	tests := []struct {
		name      string
		parser    Parser
		content   string
		wantAgent string
		wantQuote string
	}{
		{
			name:   "claude string content and tool records",
			parser: Claude{},
			content: `{"type":"user","sessionId":"abc","timestamp":"2026-07-30T01:02:03Z","cwd":"/repo","gitBranch":"main","message":{"role":"user","content":"fix the test"}}
{"type":"assistant","sessionId":"abc","timestamp":"2026-07-30T01:02:04Z","message":{"role":"assistant","content":[{"type":"tool_use","id":"tool-1","name":"Bash","input":{"command":"go test ./..."}},{"type":"tool_use","id":"tool-2","name":"Edit","input":{"file_path":"/repo/a.go","old_string":"a","new_string":"b"}}]}}
{"type":"user","sessionId":"abc","timestamp":"2026-07-30T01:02:05Z","toolUseResult":{"exitCode":1},"message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"tool-1","is_error":true,"content":"failed"}]}}
`,
			wantAgent: "claude",
			wantQuote: "fix the test",
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
			wantAgent: "claude",
			wantQuote: "run it",
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
			wantAgent: "codex",
			wantQuote: "implement it",
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
			wantAgent: "codex",
			wantQuote: "run it",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "session.jsonl")
			content := test.content
			switch test.parser.(type) {
			case Claude:
				content += `{"type":"user","uuid":"next-turn","sessionId":"abc","timestamp":"2026-07-30T01:02:59Z","message":{"role":"user","content":"next"}}` + "\n"
			case Codex:
				content += `{"timestamp":"2026-07-30T01:02:59Z","type":"event_msg","payload":{"type":"user_message","message":"next"}}` + "\n"
			}
			if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
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
			if len(feedstocks[0].Dialogue) == 0 || feedstocks[0].Dialogue[0].Role != "user" ||
				feedstocks[0].Dialogue[0].Content != test.wantQuote {
				t.Errorf("dialogue = %#v, want user quote %q", feedstocks[0].Dialogue, test.wantQuote)
			}
		})
	}
}

func TestClaudeUsesUUIDAndSkipsSyntheticUserRecords(t *testing.T) {
	const humanRecord = `{"type":"user","uuid":"human-turn-id","sessionId":"session-id","timestamp":"2026-07-30T01:02:03Z","message":{"role":"user","content":"actual human request"}}`
	tests := []struct {
		name   string
		prefix string
	}{
		{
			name: "compact summary",
			prefix: `{"type":"user","uuid":"summary-id","sessionId":"session-id","timestamp":"2026-07-30T01:02:02Z","isCompactSummary":true,"message":{"role":"user","content":"synthetic compact summary"}}
`,
		},
		{
			name: "transcript-only message",
			prefix: `{"type":"user","uuid":"transcript-id","sessionId":"session-id","timestamp":"2026-07-30T01:02:02Z","isVisibleInTranscriptOnly":true,"message":{"role":"user","content":"synthetic transcript record"}}
`,
		},
		{
			name: "interruption marker",
			prefix: `{"type":"user","uuid":"interruption-id","sessionId":"session-id","timestamp":"2026-07-30T01:02:02Z","message":{"role":"user","content":[{"type":"text","text":"[Request interrupted by user]"}]}}
`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "session.jsonl")
			completed := test.prefix + humanRecord + "\n" +
				`{"type":"assistant","sessionId":"session-id","timestamp":"2026-07-30T01:02:04Z","message":{"role":"assistant","stop_reason":"end_turn","content":[]}}` + "\n"
			if err := os.WriteFile(path, []byte(completed), 0o600); err != nil {
				t.Fatal(err)
			}
			feedstocks, warnings, err := (Claude{}).Parse(path)
			if err != nil {
				t.Fatal(err)
			}
			if len(warnings) != 0 || len(feedstocks) != 1 {
				t.Fatalf("feedstocks = %#v, warnings = %#v", feedstocks, warnings)
			}
			if feedstocks[0].TurnID != "human-turn-id" {
				t.Fatalf("turn_id = %q", feedstocks[0].TurnID)
			}
			wantID := FeedstockID("claude", "session-id", "human-turn-id")
			if feedstocks[0].ID != wantID || len(feedstocks[0].Dialogue) == 0 ||
				feedstocks[0].Dialogue[0].Content != "actual human request" {
				t.Fatalf("feedstock = %#v, want ID %q", feedstocks[0], wantID)
			}
		})
	}
}

func TestCodexUsesTurnContextTurnID(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	content := `{"timestamp":"2026-07-30T01:00:00Z","type":"session_meta","payload":{"id":"session-id","cwd":"/repo"}}
{"timestamp":"2026-07-30T01:00:01Z","type":"turn_context","payload":{"turn_id":"codex-turn-id","cwd":"/repo"}}
{"timestamp":"2026-07-30T01:00:02Z","type":"event_msg","payload":{"type":"user_message","message":"actual human request"}}
{"timestamp":"2026-07-30T01:00:03Z","type":"response_item","payload":{"type":"function_call","name":"exec_command","call_id":"c1","arguments":{"cmd":"before resume"}}}
{"timestamp":"2026-07-30T01:00:04Z","type":"turn_context","payload":{"turn_id":"codex-turn-id","cwd":"/resumed-repo"}}
{"timestamp":"2026-07-30T01:00:05Z","type":"response_item","payload":{"type":"function_call","name":"exec_command","call_id":"c2","arguments":{"cmd":"after resume"}}}
{"timestamp":"2026-07-30T01:00:06Z","type":"event_msg","payload":{"type":"task_complete","turn_id":"codex-turn-id"}}
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	feedstocks, warnings, err := (Codex{}).Parse(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 0 || len(feedstocks) != 1 {
		t.Fatalf("feedstocks = %#v, warnings = %#v", feedstocks, warnings)
	}
	if feedstocks[0].TurnID != "codex-turn-id" {
		t.Fatalf("turn_id = %q", feedstocks[0].TurnID)
	}
	wantID := FeedstockID("codex", "session-id", "codex-turn-id")
	if feedstocks[0].ID != wantID {
		t.Fatalf("ID = %q, want %q", feedstocks[0].ID, wantID)
	}
	if feedstocks[0].CWD != "/resumed-repo" || len(feedstocks[0].Dialogue) == 0 ||
		feedstocks[0].Dialogue[0].Content != "actual human request" {
		t.Fatalf("repeated turn context split the feedstock: %#v", feedstocks[0])
	}
}

func TestCodexDoesNotParseNestedUserEventsFromToolOutput(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	content := `{"timestamp":"2026-07-30T01:00:00Z","type":"session_meta","payload":{"id":"session-id","cwd":"/repo"}}
{"timestamp":"2026-07-30T01:00:01Z","type":"turn_context","payload":{"turn_id":"real-turn","cwd":"/repo"}}
{"timestamp":"2026-07-30T01:00:02Z","type":"event_msg","payload":{"type":"user_message","message":"inspect the audit output"}}
{"timestamp":"2026-07-30T01:00:03Z","type":"response_item","payload":{"type":"function_call_output","call_id":"call-1","output":"{\"timestamp\":\"2026-07-30T00:00:00Z\",\"type\":\"event_msg\",\"payload\":{\"type\":\"user_message\",\"turn_id\":\"fake-turn\",\"message\":\"nested tool output\"}}"}}
{"timestamp":"2026-07-30T01:00:04Z","type":"event_msg","payload":{"type":"task_complete","turn_id":"real-turn"}}
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	feedstocks, warnings, err := (Codex{}).Parse(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 0 || len(feedstocks) != 1 {
		t.Fatalf("feedstocks = %#v, warnings = %#v", feedstocks, warnings)
	}
	if feedstocks[0].TurnID != "real-turn" ||
		feedstocks[0].Dialogue[0].Content != "inspect the audit output" {
		t.Fatalf("feedstock = %#v", feedstocks[0])
	}
	if _, err := (Codex{}).ExtractTurn(path, "fake-turn"); err == nil {
		t.Fatal("nested tool output was addressable as a source turn")
	}
}

func TestParsersOmitOpenLastTurnUntilTerminalRecord(t *testing.T) {
	tests := []struct {
		name     string
		parser   Parser
		open     string
		terminal string
	}{
		{
			name: "Claude", parser: Claude{},
			open:     `{"type":"user","uuid":"turn-open","sessionId":"session","timestamp":"2026-07-30T01:00:00Z","message":{"role":"user","content":"open"}}` + "\n",
			terminal: `{"type":"assistant","sessionId":"session","timestamp":"2026-07-30T01:00:01Z","message":{"role":"assistant","stop_reason":"end_turn","content":[{"type":"text","text":"done"}]}}` + "\n",
		},
		{
			name: "Codex", parser: Codex{},
			open: `{"timestamp":"2026-07-30T01:00:00Z","type":"session_meta","payload":{"id":"session"}}` + "\n" +
				`{"timestamp":"2026-07-30T01:00:01Z","type":"turn_context","payload":{"turn_id":"turn-open"}}` + "\n" +
				`{"timestamp":"2026-07-30T01:00:02Z","type":"event_msg","payload":{"type":"user_message","message":"open"}}` + "\n",
			terminal: `{"timestamp":"2026-07-30T01:00:03Z","type":"event_msg","payload":{"type":"task_complete","turn_id":"turn-open"}}` + "\n",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "session.jsonl")
			if err := os.WriteFile(path, []byte(test.open), 0o600); err != nil {
				t.Fatal(err)
			}
			feedstocks, _, err := test.parser.Parse(path)
			if err != nil {
				t.Fatal(err)
			}
			if len(feedstocks) != 0 {
				t.Fatalf("open turn was acquired: %#v", feedstocks)
			}
			if err := os.WriteFile(path, []byte(test.open+test.terminal), 0o600); err != nil {
				t.Fatal(err)
			}
			feedstocks, _, err = test.parser.Parse(path)
			if err != nil {
				t.Fatal(err)
			}
			if len(feedstocks) != 1 || feedstocks[0].TurnID != "turn-open" {
				t.Fatalf("completed turn = %#v", feedstocks)
			}
		})
	}
}

func TestClaudeExtractTurnReturnsUserAndFinalAssistantTextOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	content := `{"type":"user","uuid":"turn-1","sessionId":"session-id","timestamp":"2026-07-30T01:02:03Z","message":{"role":"user","content":"actual user text"}}
{"type":"assistant","sessionId":"session-id","timestamp":"2026-07-30T01:02:04Z","message":{"role":"assistant","content":[{"type":"thinking","thinking":"SECRET THINKING"},{"type":"text","text":"intermediate assistant text"},{"type":"tool_use","id":"tool-1","name":"Bash","input":{"command":"secret command"}}]}}
{"type":"user","sessionId":"session-id","timestamp":"2026-07-30T01:02:05Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"tool-1","content":"SECRET TOOL OUTPUT"}]}}
{"type":"assistant","sessionId":"session-id","timestamp":"2026-07-30T01:02:06Z","message":{"role":"assistant","content":[{"type":"thinking","thinking":"SECRET FINAL THINKING"},{"type":"text","text":"final assistant answer"}]}}
{"type":"user","uuid":"turn-2","sessionId":"session-id","timestamp":"2026-07-30T01:02:07Z","message":{"role":"user","content":"next user text"}}
{"type":"assistant","sessionId":"session-id","timestamp":"2026-07-30T01:02:08Z","message":{"role":"assistant","content":[{"type":"text","text":"next assistant answer"}]}}
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	messages, err := (Claude{}).ExtractTurn(path, "turn-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 2 ||
		messages[0].Role != "user" || messages[0].Content != "actual user text" ||
		messages[1].Role != "assistant" || messages[1].Content != "final assistant answer" {
		t.Fatalf("messages = %#v", messages)
	}
	serialized := messages[0].Content + messages[1].Content
	for _, excluded := range []string{
		"SECRET THINKING", "SECRET TOOL OUTPUT", "secret command",
		"intermediate assistant text", "next user text", "next assistant answer",
	} {
		if strings.Contains(serialized, excluded) {
			t.Fatalf("raw dialogue contains excluded text %q: %#v", excluded, messages)
		}
	}
}

func TestCodexExtractTurnReturnsUserAndFinalAssistantTextOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	content := `{"timestamp":"2026-07-30T01:00:00Z","type":"session_meta","payload":{"id":"session-id","cwd":"/repo"}}
{"timestamp":"2026-07-30T01:00:01Z","type":"turn_context","payload":{"turn_id":"turn-1","cwd":"/repo"}}
{"timestamp":"2026-07-30T01:00:02Z","type":"event_msg","payload":{"type":"user_message","message":"actual user text"}}
{"timestamp":"2026-07-30T01:00:03Z","type":"response_item","payload":{"type":"reasoning","summary":[{"type":"summary_text","text":"SECRET THINKING"}]}}
{"timestamp":"2026-07-30T01:00:04Z","type":"response_item","payload":{"type":"function_call_output","call_id":"call-1","output":"SECRET TOOL OUTPUT"}}
{"timestamp":"2026-07-30T01:00:05Z","type":"response_item","payload":{"type":"message","role":"assistant","phase":"commentary","content":[{"type":"output_text","text":"intermediate assistant text"}]}}
{"timestamp":"2026-07-30T01:00:05.5Z","type":"turn_context","payload":{"turn_id":"turn-1","cwd":"/resumed-repo"}}
{"timestamp":"2026-07-30T01:00:06Z","type":"response_item","payload":{"type":"message","role":"assistant","phase":"final_answer","content":[{"type":"output_text","text":"final assistant answer"}]}}
{"timestamp":"2026-07-30T01:00:07Z","type":"turn_context","payload":{"turn_id":"turn-2","cwd":"/repo"}}
{"timestamp":"2026-07-30T01:00:08Z","type":"event_msg","payload":{"type":"user_message","message":"next user text"}}
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	messages, err := (Codex{}).ExtractTurn(path, "turn-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 2 ||
		messages[0].Role != "user" || messages[0].Content != "actual user text" ||
		messages[1].Role != "assistant" || messages[1].Content != "final assistant answer" {
		t.Fatalf("messages = %#v", messages)
	}
	serialized := messages[0].Content + messages[1].Content
	for _, excluded := range []string{
		"SECRET THINKING", "SECRET TOOL OUTPUT",
		"intermediate assistant text", "next user text",
	} {
		if strings.Contains(serialized, excluded) {
			t.Fatalf("raw dialogue contains excluded text %q: %#v", excluded, messages)
		}
	}
}

func TestFeedstockIDIsStableAndSeparateFromSourceTurnID(t *testing.T) {
	first := FeedstockID("claude", "session-id", "turn-id")
	second := FeedstockID("claude", "session-id", "turn-id")
	if first != second {
		t.Fatalf("same source identity produced %q and %q", first, second)
	}
	if first == "turn-id" || !strings.HasPrefix(first, "fs-") {
		t.Fatalf("feedstock ID %q is not an opaque Knowbrew ID", first)
	}
	if first == FeedstockID("claude", "session-id", "different-turn") {
		t.Fatal("different source turns produced the same feedstock ID")
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
			if feedstock.ID == "" || feedstock.TurnID == "" || feedstock.Session.ID == "" ||
				len(feedstock.Dialogue) == 0 || feedstock.Dialogue[0].Content == "" || feedstock.Timestamp.IsZero() {
				t.Fatalf("%s: incomplete feedstock metadata: %#v", test.environment, feedstock)
			}
		}
		messages, err := test.parser.ExtractTurn(path, feedstocks[0].TurnID)
		if err != nil {
			t.Fatalf("%s: extract first turn: %v", test.environment, err)
		}
		if len(messages) == 0 || messages[0].Role != "user" ||
			messages[0].Content != feedstocks[0].Dialogue[0].Content {
			t.Fatalf("%s: raw first turn = %#v", test.environment, messages)
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
			content := test.content
			if test.name == "Claude" {
				content += `{"type":"user","sessionId":"session","timestamp":"2026-07-30T01:00:02Z","message":{"role":"user","content":"third"}}` + "\n"
			} else {
				content += `{"timestamp":"2026-07-30T01:00:03Z","type":"event_msg","payload":{"type":"user_message","message":"third"}}` + "\n"
			}
			if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
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
