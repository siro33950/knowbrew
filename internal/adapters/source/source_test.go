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

func TestGatewayUsesConfiguredPathOrderForDuplicateSession(t *testing.T) {
	first := t.TempDir()
	second := t.TempDir()
	writeClaudeSession(t, filepath.Join(first, "first.jsonl"), "session-id", "turn-id", "first")
	writeClaudeSession(t, filepath.Join(second, "second.jsonl"), "session-id", "turn-id", "second")
	configured := []applicationsource.Configured{{
		Agent: "claude", Parser: "claude", Paths: []string{first, second},
	}}
	messages, err := New(configured).ReadTurn("claude", "session-id", "turn-id")
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) == 0 || messages[0].Content != "first" {
		t.Fatalf("messages = %#v", messages)
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
