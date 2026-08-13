package source

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	applicationsource "github.com/siro33950/knowbrew/internal/application/source"
)

func TestGatewayResolvesSessionAcrossConfiguredPathsAfterMove(t *testing.T) {
	normal := t.TempDir()
	archive := t.TempDir()
	path := filepath.Join(normal, "session.jsonl")
	writeClaudeSession(t, path, "session-id", "turn-id", "from normal")
	configured := []applicationsource.Configured{{
		Agent: "claude", Parser: "claude", Paths: []string{normal, archive},
	}}
	messages, err := New(configured).ReadTurn("claude", "session-id", "turn-id")
	if err != nil || len(messages) == 0 || messages[0].Content != "from normal" {
		t.Fatalf("normal read = %#v, %v", messages, err)
	}
	moved := filepath.Join(archive, filepath.Base(path))
	if err := os.Rename(path, moved); err != nil {
		t.Fatal(err)
	}
	messages, err = New(configured).ReadTurn("claude", "session-id", "turn-id")
	if err != nil || len(messages) == 0 || messages[0].Content != "from normal" {
		t.Fatalf("archive read = %#v, %v", messages, err)
	}
}

func TestGatewayRejectsConflictingCopiesOfTheSameTurn(t *testing.T) {
	first := t.TempDir()
	second := t.TempDir()
	writeClaudeSession(t, filepath.Join(first, "first.jsonl"), "session-id", "turn-id", "first")
	writeClaudeSession(t, filepath.Join(second, "second.jsonl"), "session-id", "turn-id", "second")
	configured := []applicationsource.Configured{{
		Agent: "claude", Parser: "claude", Paths: []string{first, second},
	}}
	_, err := New(configured).ReadTurn("claude", "session-id", "turn-id")
	if err == nil || !strings.Contains(err.Error(), "conflicting source turn") {
		t.Fatalf("error = %v", err)
	}
}

func TestGatewayMergesDistinctAndIdenticalTurnsAcrossSessionFiles(t *testing.T) {
	first := t.TempDir()
	second := t.TempDir()
	writeClaudeSession(t, filepath.Join(first, "first.jsonl"), "session-id", "turn-1", "first")
	writeClaudeSession(t, filepath.Join(second, "duplicate.jsonl"), "session-id", "turn-1", "first")
	writeClaudeSession(t, filepath.Join(second, "second.jsonl"), "session-id", "turn-2", "second")
	configured := []applicationsource.Configured{{
		Agent: "claude", Parser: "claude", Paths: []string{first, second},
	}}
	gateway := New(configured)
	candidates, warnings, err := gateway.ParseSession("claude", "session-id")
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 0 || len(candidates) != 2 {
		t.Fatalf("candidates = %#v, warnings = %#v", candidates, warnings)
	}
	if candidates[0].TurnID != "turn-1" || candidates[1].TurnID != "turn-2" {
		t.Fatalf("candidates = %#v", candidates)
	}
	messages, err := gateway.ReadTurn("claude", "session-id", "turn-2")
	if err != nil || len(messages) == 0 || messages[0].Content != "second" {
		t.Fatalf("messages = %#v, error = %v", messages, err)
	}
}

func TestGatewayAllowsMissingAlternatePathButRequiresOneAvailablePath(t *testing.T) {
	available := t.TempDir()
	missing := filepath.Join(t.TempDir(), "missing")
	writeClaudeSession(t, filepath.Join(available, "session.jsonl"), "session-id", "turn-id", "available")
	configured := []applicationsource.Configured{{
		Agent: "claude", Parser: "claude", Paths: []string{missing, available},
	}}
	files, err := New(configured).Collect(configured, applicationsource.Selection{}, time.Now())
	if err != nil || len(files) != 1 {
		t.Fatalf("files = %#v, %v", files, err)
	}
	onlyMissing := []applicationsource.Configured{{
		Agent: "claude", Parser: "claude", Paths: []string{missing},
	}}
	if _, err := New(onlyMissing).Collect(onlyMissing, applicationsource.Selection{}, time.Now()); err == nil ||
		!strings.Contains(err.Error(), "no available paths") {
		t.Fatalf("missing error = %v", err)
	}
}

func TestGatewayRejectsExplicitPathOutsideConfiguredSources(t *testing.T) {
	configuredRoot := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.jsonl")
	writeClaudeSession(t, outside, "session-id", "turn-id", "outside")
	configured := []applicationsource.Configured{{
		Agent: "claude", Parser: "claude", Paths: []string{configuredRoot},
	}}
	_, err := New(configured).Collect(
		configured, applicationsource.Selection{Paths: []string{outside}}, time.Now(),
	)
	if err == nil || !strings.Contains(err.Error(), "outside configured source paths") {
		t.Fatalf("error = %v", err)
	}
}

func TestGatewayDoesNotCollectClaudeSubagentTranscripts(t *testing.T) {
	root := t.TempDir()
	writeClaudeSession(
		t,
		filepath.Join(root, "session.jsonl"),
		"session-id",
		"turn-id",
		"parent",
	)
	subagents := filepath.Join(root, "session-id", "subagents")
	if err := os.MkdirAll(subagents, 0o700); err != nil {
		t.Fatal(err)
	}
	writeClaudeSession(
		t,
		filepath.Join(subagents, "agent-child.jsonl"),
		"child-session",
		"child-turn",
		"child",
	)
	configured := []applicationsource.Configured{{
		Agent: "claude", Parser: "claude", Paths: []string{root},
	}}
	files, err := New(configured).Collect(
		configured,
		applicationsource.Selection{ModifiedSince: timePointer(time.Time{})},
		time.Now(),
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files[0].Path != filepath.Join(root, "session.jsonl") {
		t.Fatalf("files = %#v", files)
	}
}

func TestCachedGatewayAppendsCompletedTurnsFromParserCheckpoint(t *testing.T) {
	root := t.TempDir()
	sourceDirectory := t.TempDir()
	path := filepath.Join(sourceDirectory, "session.jsonl")
	writeClaudeSession(t, path, "session-id", "turn-1", "first")
	configured := []applicationsource.Configured{{
		Agent: "claude", Parser: "claude", Paths: []string{sourceDirectory},
	}}
	gateway := NewCached(root, configured)
	file := applicationsource.File{Agent: "claude", Parser: "claude", Path: path}
	initial, warnings, err := gateway.Parse(file)
	if err != nil || len(warnings) != 0 || len(initial) != 1 {
		t.Fatalf("initial = %#v, warnings = %#v, error = %v", initial, warnings, err)
	}
	appendClaudeSession(t, path, "session-id", "turn-2", "second")
	resumed, warnings, err := gateway.Parse(file)
	if err != nil || len(warnings) != 0 || len(resumed) != 2 {
		t.Fatalf("resumed = %#v, warnings = %#v, error = %v", resumed, warnings, err)
	}
	if resumed[0].Dialogue[0].Content != "first" || resumed[1].Dialogue[0].Content != "second" ||
		resumed[1].SourceSequence != 2 {
		t.Fatalf("resumed candidates = %#v", resumed)
	}
	entry, found, err := gateway.cache.loadByPath(gateway.cache.db, path)
	if err != nil || !found {
		t.Fatalf("cache entry found = %t, error = %v", found, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if entry.Checkpoint.Offset != info.Size() || len(entry.Candidates) != 2 {
		t.Fatalf("cache entry = %#v", entry)
	}
}

func TestCachedGatewayCompletesPreviouslyOpenTurn(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(t.TempDir(), "session.jsonl")
	user := `{"type":"user","uuid":"turn-1","sessionId":"session-id","timestamp":"2026-07-30T01:00:00Z","message":{"role":"user","content":"first"}}
`
	if err := os.WriteFile(path, []byte(user), 0o600); err != nil {
		t.Fatal(err)
	}
	gateway := NewCached(root, nil)
	file := applicationsource.File{Agent: "claude", Parser: "claude", Path: path}
	initial, _, err := gateway.Parse(file)
	if err != nil || len(initial) != 0 {
		t.Fatalf("initial = %#v, error = %v", initial, err)
	}
	appendText(t, path, `{"type":"assistant","sessionId":"session-id","timestamp":"2026-07-30T01:00:01Z","message":{"role":"assistant","stop_reason":"end_turn","content":[{"type":"text","text":"done"}]}}
`)
	completed, _, err := gateway.Parse(file)
	if err != nil || len(completed) != 1 {
		t.Fatalf("completed = %#v, error = %v", completed, err)
	}
	if completed[0].Dialogue[1].Content != "done" {
		t.Fatalf("completed candidate = %#v", completed[0])
	}
}

func TestCachedGatewayInvalidatesSameSizeRewrite(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(t.TempDir(), "session.jsonl")
	writeClaudeSession(t, path, "session-id", "turn-id", "first")
	gateway := NewCached(root, nil)
	file := applicationsource.File{Agent: "claude", Parser: "claude", Path: path}
	if _, _, err := gateway.Parse(file); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	rewritten := strings.Replace(string(data), `"content":"first"`, `"content":"burst"`, 1)
	if len(rewritten) != len(data) {
		t.Fatal("rewrite changed file size")
	}
	if err := os.WriteFile(path, []byte(rewritten), 0o600); err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(time.Second)
	if err := os.Chtimes(path, future, future); err != nil {
		t.Fatal(err)
	}
	reparsed, _, err := gateway.Parse(file)
	if err != nil || len(reparsed) != 1 {
		t.Fatalf("reparsed = %#v, error = %v", reparsed, err)
	}
	if reparsed[0].Dialogue[0].Content != "burst" {
		t.Fatalf("reparsed candidate = %#v", reparsed[0])
	}
}

func TestCachedGatewayReusesEntryAfterSourceMove(t *testing.T) {
	root := t.TempDir()
	firstDirectory := t.TempDir()
	secondDirectory := t.TempDir()
	path := filepath.Join(firstDirectory, "session.jsonl")
	writeClaudeSession(t, path, "session-id", "turn-id", "first")
	gateway := NewCached(root, nil)
	file := applicationsource.File{Agent: "claude", Parser: "claude", Path: path}
	if _, _, err := gateway.Parse(file); err != nil {
		t.Fatal(err)
	}
	moved := filepath.Join(secondDirectory, filepath.Base(path))
	if err := os.Rename(path, moved); err != nil {
		t.Fatal(err)
	}
	file.Path = moved
	candidates, _, err := gateway.Parse(file)
	if err != nil || len(candidates) != 1 || candidates[0].Dialogue[0].Content != "first" {
		t.Fatalf("moved candidates = %#v, error = %v", candidates, err)
	}
	if _, found, err := gateway.cache.loadByPath(gateway.cache.db, path); err != nil || found {
		t.Fatalf("old cache entry found = %t, error = %v", found, err)
	}
	if _, found, err := gateway.cache.loadByPath(gateway.cache.db, moved); err != nil || !found {
		t.Fatalf("moved cache entry found = %t, error = %v", found, err)
	}
}

func timePointer(value time.Time) *time.Time {
	return &value
}

func writeClaudeSession(t *testing.T, path, sessionID, turnID, user string) {
	t.Helper()
	contents := fmt.Sprintf(
		"{\"type\":\"user\",\"uuid\":%q,\"sessionId\":%q,\"timestamp\":\"2026-07-30T01:00:00Z\",\"message\":{\"role\":\"user\",\"content\":%q}}\n"+
			"{\"type\":\"assistant\",\"sessionId\":%q,\"timestamp\":\"2026-07-30T01:00:01Z\",\"message\":{\"role\":\"assistant\",\"stop_reason\":\"end_turn\",\"content\":[{\"type\":\"text\",\"text\":\"done\"}]}}\n",
		turnID, sessionID, user, sessionID,
	)
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}

func appendClaudeSession(t *testing.T, path, sessionID, turnID, user string) {
	t.Helper()
	contents := fmt.Sprintf(
		"{\"type\":\"user\",\"uuid\":%q,\"sessionId\":%q,\"timestamp\":\"2026-07-30T01:01:00Z\",\"message\":{\"role\":\"user\",\"content\":%q}}\n"+
			"{\"type\":\"assistant\",\"sessionId\":%q,\"timestamp\":\"2026-07-30T01:01:01Z\",\"message\":{\"role\":\"assistant\",\"stop_reason\":\"end_turn\",\"content\":[{\"type\":\"text\",\"text\":\"done\"}]}}\n",
		turnID, sessionID, user, sessionID,
	)
	appendText(t, path, contents)
}

func appendText(t *testing.T, path, content string) {
	t.Helper()
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString(content); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}
