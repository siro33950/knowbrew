package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gofrs/flock"
	"github.com/siro33950/knowbrew/internal/adapters/config"
	"github.com/siro33950/knowbrew/internal/adapters/persistence/markdownstore"
	"github.com/siro33950/knowbrew/internal/adapters/source/parser"
	"github.com/siro33950/knowbrew/internal/application/draw"
	"github.com/siro33950/knowbrew/internal/domain"
	"github.com/spf13/cobra"
)

func TestCommandSurfaceExposesDistill(t *testing.T) {
	root := newRootCommand()
	got := map[string]bool{}
	for _, command := range root.Commands() {
		got[command.Name()] = true
	}
	for _, name := range []string{"init", "draw", "brew", "distill", "show", "feedstock", "knowledge", "document", "index"} {
		if !got[name] {
			t.Errorf("missing command %q", name)
		}
	}
	if got["search"] {
		t.Fatal("legacy mixed search command must not be exposed")
	}
	if got["completion"] {
		t.Fatal("unexpected default completion command")
	}
}

func TestSearchModeAndIndexCommands(t *testing.T) {
	rootDir := t.TempDir()
	configPath := filepath.Join(t.TempDir(), "config.toml")
	configData := "root = " + quoteTOML(rootDir) + "\n\n" + llmConfigSection
	if err := os.WriteFile(configPath, []byte(configData), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(config.ConfigEnvironment, configPath)

	invalid := newRootCommand()
	invalid.SetArgs([]string{"feedstock", "meaning", "--search-mode", "invalid"})
	if err := invalid.Execute(); err == nil || !strings.Contains(err.Error(), "hybrid, text, or vector") {
		t.Fatalf("invalid search mode error = %v", err)
	}
	disabled := newRootCommand()
	disabled.SetArgs([]string{"feedstock", "meaning", "--search-mode", "vector"})
	if err := disabled.Execute(); err == nil || !strings.Contains(err.Error(), "vector search is disabled") {
		t.Fatalf("disabled vector error = %v", err)
	}

	for _, subcommand := range []string{"sync", "rebuild"} {
		var output bytes.Buffer
		command := newRootCommand()
		command.SetOut(&output)
		command.SetArgs([]string{"index", subcommand})
		if err := command.Execute(); err != nil {
			t.Fatalf("index %s: %v", subcommand, err)
		}
		if !strings.Contains(output.String(), `"sync"`) {
			t.Fatalf("index %s output = %s", subcommand, output.String())
		}
	}
	var output bytes.Buffer
	status := newRootCommand()
	status.SetOut(&output)
	status.SetArgs([]string{"index", "status"})
	if err := status.Execute(); err != nil {
		t.Fatal(err)
	}
	var response struct {
		Status struct {
			SemanticEnabled bool `json:"semantic_enabled"`
			Documents       int  `json:"documents"`
			Unsynchronized  int  `json:"unsynchronized"`
		} `json:"status"`
	}
	if err := json.Unmarshal(output.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Status.SemanticEnabled || response.Status.Documents != 0 || response.Status.Unsynchronized != 0 {
		t.Fatalf("index status = %#v", response.Status)
	}
}

func TestDrawAndBrewExposeVerboseFlag(t *testing.T) {
	for _, command := range []*cobra.Command{newDrawCommand(), newBrewCommand(), newDistillCommand()} {
		if command.Flags().Lookup("verbose") == nil {
			t.Fatalf("%s has no --verbose flag", command.Name())
		}
	}
}

func TestDistillExposesDocumentFilters(t *testing.T) {
	command := newDistillCommand()
	for _, name := range []string{"subject", "template", "max", "verbose"} {
		if command.Flags().Lookup(name) == nil {
			t.Fatalf("distill has no --%s flag", name)
		}
	}
}

func TestDrawBrewAndDistillExposeCommonMaxFlag(t *testing.T) {
	for _, command := range []*cobra.Command{newDrawCommand(), newBrewCommand(), newDistillCommand()} {
		if command.Flags().Lookup("max") == nil {
			t.Fatalf("%s has no --max flag", command.Name())
		}
	}
}

func TestDrawExposesBoundedSourceSelectionFlags(t *testing.T) {
	command := newDrawCommand()
	for _, name := range []string{"hook", "max", "order", "source", "since", "until"} {
		if command.Flags().Lookup(name) == nil {
			t.Fatalf("draw has no --%s flag", name)
		}
	}
	if command.Flags().Lookup("all") != nil {
		t.Fatal("draw still exposes unsafe --all flag")
	}
	if command.Flags().Lookup("max-turns") != nil {
		t.Fatal("draw still exposes legacy --max-turns flag")
	}
}

func TestDrawHookReadsStopTranscriptPath(t *testing.T) {
	path, err := drawHookTranscriptPath(strings.NewReader(`{
		"hook_event_name":"Stop",
		"transcript_path":"/tmp/session.jsonl"
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if path != "/tmp/session.jsonl" {
		t.Fatalf("transcript path = %q", path)
	}
}

func TestDrawHookRejectsInvalidInput(t *testing.T) {
	for _, input := range []string{
		`{"hook_event_name":"SessionStart","transcript_path":"/tmp/session.jsonl"}`,
		`{"hook_event_name":"Stop","transcript_path":"/tmp/session.jsonl"}{}`,
		`not json`,
	} {
		if _, err := drawHookTranscriptPath(strings.NewReader(input)); err == nil {
			t.Fatalf("input was accepted: %s", input)
		}
	}
}

func TestDrawHookAllowsMissingTranscriptPath(t *testing.T) {
	path, err := drawHookTranscriptPath(strings.NewReader(`{"hook_event_name":"Stop","transcript_path":null}`))
	if err != nil {
		t.Fatal(err)
	}
	if path != "" {
		t.Fatalf("transcript path = %q", path)
	}
}

func TestDrawHookSuppressesKnowbrewAgentInvocationBeforeReadingInput(t *testing.T) {
	t.Setenv(config.InvocationIDEnvironment, "invocation-id")
	command := newRootCommand()
	command.SetIn(strings.NewReader("invalid hook input"))
	command.SetArgs([]string{"draw", "--hook"})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
}

func TestDrawHookProcessesOnlyPayloadTranscriptAndWritesNoSummary(t *testing.T) {
	rootDir := t.TempDir()
	sourceDir := t.TempDir()
	transcriptPath := filepath.Join(sourceDir, "session.jsonl")
	if err := os.WriteFile(transcriptPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sourceDir, "unrelated.jsonl"), []byte("not json\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(t.TempDir(), "config.toml")
	configData := "root = " + quoteTOML(rootDir) + "\n\n" +
		llmConfigSection + "\n" +
		"[embedding]\nmodel = \"disabled\"\n\n" +
		"[[sources]]\nagent = \"codex\"\nparser = \"codex\"\npaths = [" + quoteTOML(sourceDir) + "]\n"
	if err := os.WriteFile(configPath, []byte(configData), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(config.ConfigEnvironment, configPath)
	payload, err := json.Marshal(map[string]any{
		"hook_event_name": "Stop", "transcript_path": transcriptPath,
	})
	if err != nil {
		t.Fatal(err)
	}

	var output bytes.Buffer
	command := newRootCommand()
	command.SetIn(bytes.NewReader(payload))
	command.SetOut(&output)
	command.SetArgs([]string{"draw", "--hook"})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	if output.Len() != 0 {
		t.Fatalf("hook output = %q", output.String())
	}
}

func TestDrawHookPassesHookOptionAndExcludesOnlyUnfinishedTurn(t *testing.T) {
	rootDir := t.TempDir()
	sourceDir := t.TempDir()
	transcriptPath := filepath.Join(sourceDir, "session.jsonl")
	transcript := `{"type":"user","uuid":"turn-1","sessionId":"session","timestamp":"2026-07-30T01:00:00Z","message":{"role":"user","content":"only turn"}}
{"type":"assistant","sessionId":"session","timestamp":"2026-07-30T01:00:01Z","message":{"role":"assistant","stop_reason":"end_turn","content":[{"type":"text","text":"done"}]}}
`
	if err := os.WriteFile(transcriptPath, []byte(transcript), 0o600); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(t.TempDir(), "config.toml")
	configData := "root = " + quoteTOML(rootDir) + "\n\n" +
		llmConfigSection + "\n" +
		"[embedding]\nmodel = \"disabled\"\n\n" +
		"[[sources]]\nagent = \"claude\"\nparser = \"claude\"\npaths = [" + quoteTOML(sourceDir) + "]\n"
	if err := os.WriteFile(configPath, []byte(configData), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(config.ConfigEnvironment, configPath)
	payload, err := json.Marshal(map[string]any{
		"hook_event_name": "Stop", "transcript_path": transcriptPath,
	})
	if err != nil {
		t.Fatal(err)
	}

	command := newRootCommand()
	command.SetIn(bytes.NewReader(payload))
	command.SetArgs([]string{"draw", "--hook"})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	dataStore, err := store.New(rootDir)
	if err != nil {
		t.Fatal(err)
	}
	feedstocks, warnings, err := dataStore.ListFeedstocks()
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 0 || len(feedstocks) != 0 {
		t.Fatalf("feedstocks = %#v, warnings = %#v", feedstocks, warnings)
	}
}

func TestDrawHookIgnoresRetiredGlobalLock(t *testing.T) {
	rootDir := t.TempDir()
	sourceDir := t.TempDir()
	transcriptPath := filepath.Join(sourceDir, "session.jsonl")
	if err := os.WriteFile(transcriptPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	stateDir := filepath.Join(rootDir, ".knowbrew", "state")
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	held := flock.New(filepath.Join(stateDir, "draw.lock"))
	locked, err := held.TryLock()
	if err != nil {
		t.Fatal(err)
	}
	if !locked {
		t.Fatal("could not hold the draw lock")
	}
	defer func() { _ = held.Unlock() }()

	configPath := filepath.Join(t.TempDir(), "config.toml")
	configData := "root = " + quoteTOML(rootDir) + "\n\n" +
		llmConfigSection + "\n" +
		"[embedding]\nmodel = \"disabled\"\n\n" +
		"[[sources]]\nagent = \"codex\"\nparser = \"codex\"\npaths = [" + quoteTOML(sourceDir) + "]\n"
	if err := os.WriteFile(configPath, []byte(configData), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(config.ConfigEnvironment, configPath)
	payload, err := json.Marshal(map[string]any{
		"hook_event_name": "Stop", "transcript_path": transcriptPath,
	})
	if err != nil {
		t.Fatal(err)
	}

	var output bytes.Buffer
	command := newRootCommand()
	command.SetIn(bytes.NewReader(payload))
	command.SetOut(&output)
	command.SetArgs([]string{"draw", "--hook"})
	if err := command.Execute(); err != nil {
		t.Fatalf("hook reported a busy lock as an error: %v", err)
	}
	if output.Len() != 0 {
		t.Fatalf("hook output = %q", output.String())
	}
}

func TestDrawHookRejectsExplicitArgumentsAndFilters(t *testing.T) {
	for _, args := range [][]string{
		{"draw", "--hook", "/tmp/session.jsonl"},
		{"draw", "--hook", "--max", "1"},
		{"draw", "--hook", "--source", "codex"},
		{"draw", "--hook", "--since", "1h"},
		{"draw", "--hook", "--until", "1h"},
		{"draw", "--hook", "--order", "oldest"},
		{"draw", "--hook", "--verbose"},
	} {
		command := newRootCommand()
		command.SetIn(strings.NewReader(`{"hook_event_name":"Stop","transcript_path":null}`))
		command.SetArgs(args)
		if err := command.Execute(); err == nil {
			t.Fatalf("hook arguments were accepted: %v", args)
		}
	}
}

func TestDrawRejectsUnknownOrder(t *testing.T) {
	command := newRootCommand()
	command.SetArgs([]string{"draw", "--order", "ascending"})
	err := command.Execute()
	if err == nil || err.Error() != `invalid --order: "ascending" is not newest or oldest` {
		t.Fatalf("--order error = %v", err)
	}
}

func TestCommandsRejectNonPositiveMax(t *testing.T) {
	commands := []string{"draw", "brew", "distill"}
	for _, value := range []string{"0", "-1"} {
		for _, name := range commands {
			command := newRootCommand()
			command.SetArgs([]string{name, "--max", value})
			err := command.Execute()
			if err == nil || err.Error() != "--max must be greater than zero" {
				t.Fatalf("%s --max %s error = %v", name, value, err)
			}
		}
	}
}

func TestParseDrawBoundaryAcceptsRelativeAndAbsoluteValues(t *testing.T) {
	location := time.FixedZone("test", 9*60*60)
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, location)
	tests := map[string]time.Time{
		"6h":                  now.Add(-6 * time.Hour),
		"7d":                  now.Add(-7 * 24 * time.Hour),
		"2w":                  now.Add(-14 * 24 * time.Hour),
		"2026-07-30":          time.Date(2026, 7, 30, 0, 0, 0, 0, location),
		"2026-07-30T03:04:05": time.Date(2026, 7, 30, 3, 4, 5, 0, location),
	}
	for input, want := range tests {
		got, err := parseDrawBoundary(input, now)
		if err != nil {
			t.Errorf("parseDrawBoundary(%q): %v", input, err)
			continue
		}
		if !got.Equal(want) {
			t.Errorf("parseDrawBoundary(%q) = %s, want %s", input, got, want)
		}
	}
	if _, err := parseDrawBoundary("yesterday", now); err == nil {
		t.Fatal("invalid draw boundary was accepted")
	}
}

func TestKnowledgeSearchEscapesSubcommandNamesAfterDoubleDash(t *testing.T) {
	rootDir := t.TempDir()
	configPath := filepath.Join(t.TempDir(), "config.toml")
	configData := "root = " + quoteTOML(rootDir) + "\n\n" + llmConfigSection
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

func TestSearchCommandsDoNotExposeTopicFlag(t *testing.T) {
	for _, args := range [][]string{
		{"feedstock", "--topic", "testing"},
		{"knowledge", "--topic", "testing"},
		{"document", "--topic", "testing"},
	} {
		command := newRootCommand()
		command.SetArgs(args)
		err := command.Execute()
		if err == nil || !strings.Contains(err.Error(), "unknown flag: --topic") {
			t.Fatalf("%v error = %v", args, err)
		}
	}
}

func TestFeedstockSearchDoesNotExposeSubjectFlag(t *testing.T) {
	command := newRootCommand()
	command.SetArgs([]string{"feedstock", "--subject", "testing"})
	err := command.Execute()
	if err == nil || !strings.Contains(err.Error(), "unknown flag: --subject") {
		t.Fatalf("error = %v", err)
	}
}

func TestDocumentCommandRejectsKnowledgeOnlyFlags(t *testing.T) {
	for _, args := range [][]string{
		{"document", "--type", "decision"},
		{"document", "--trigger", "always"},
		{"document", "--include-pending"},
		{"document", "--session", "session"},
	} {
		command := newRootCommand()
		command.SetArgs(args)
		err := command.Execute()
		if err == nil || !strings.Contains(err.Error(), "unknown flag") {
			t.Fatalf("%v error = %v", args, err)
		}
	}
}

func TestContextHookInjectsMatchedSubjectDocuments(t *testing.T) {
	rootDir := t.TempDir()
	dataStore, err := store.New(rootDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := dataStore.EnsureLayout(); err != nil {
		t.Fatal(err)
	}
	templateData := `---
description: Decisions document.
output: decisions.md
purpose: Record decisions.
inject: subject
---

# {{subject}}
`
	templatePath := filepath.Join(rootDir, "masters", "templates", "decisions.md")
	if err := os.WriteFile(templatePath, []byte(templateData), 0o644); err != nil {
		t.Fatal(err)
	}
	workDir := t.TempDir()
	if _, err := dataStore.EnsureMaster("subjects", domain.MasterEntry{
		Name: "alpha", Definition: "Alpha.", Documents: []string{"decisions"},
		Aliases: []string{workDir},
	}); err != nil {
		t.Fatal(err)
	}
	documentDir := filepath.Join(rootDir, "documents", "alpha")
	if err := os.MkdirAll(documentDir, 0o755); err != nil {
		t.Fatal(err)
	}
	documentData := `---
subject: "[[alpha]]"
template: "[[decisions]]"
knowledge:
  - "[[kn-0123456789abcdef]]"
---

# alpha

Alpha decisions body.
`
	if err := os.WriteFile(filepath.Join(documentDir, "decisions.md"), []byte(documentData), 0o644); err != nil {
		t.Fatal(err)
	}

	configPath := filepath.Join(t.TempDir(), "config.toml")
	configData := "root = " + quoteTOML(rootDir) + "\n\n" + llmConfigSection
	if err := os.WriteFile(configPath, []byte(configData), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(config.ConfigEnvironment, configPath)

	payload, err := json.Marshal(map[string]string{
		"hook_event_name": "SessionStart", "cwd": workDir,
	})
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	command := newRootCommand()
	command.SetOut(&output)
	command.SetIn(bytes.NewReader(payload))
	command.SetArgs([]string{"context", "--hook"})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		"untrusted reference data",
		"## Working context: alpha",
		"Alpha decisions body.",
	} {
		if !strings.Contains(output.String(), expected) {
			t.Fatalf("context output does not contain %q:\n%s", expected, output.String())
		}
	}
}

func TestContextHookFallsBackToProcessCwdOnEmptyStdin(t *testing.T) {
	rootDir := t.TempDir()
	dataStore, err := store.New(rootDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := dataStore.EnsureLayout(); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(t.TempDir(), "config.toml")
	configData := "root = " + quoteTOML(rootDir) + "\n\n" + llmConfigSection
	if err := os.WriteFile(configPath, []byte(configData), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(config.ConfigEnvironment, configPath)

	var output bytes.Buffer
	command := newRootCommand()
	command.SetOut(&output)
	command.SetIn(strings.NewReader(""))
	command.SetArgs([]string{"context", "--hook"})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	if output.String() != "" {
		t.Fatalf("empty root produced context output: %s", output.String())
	}
}

func TestContextRejectsNonPositiveExplicitMaxTokens(t *testing.T) {
	command := newRootCommand()
	command.SetArgs([]string{"context", "--max-tokens", "0"})
	err := command.Execute()
	if err == nil || !strings.Contains(err.Error(), "--max-tokens must be at least 1") {
		t.Fatalf("error = %v", err)
	}
}

func TestContextHookRejectsNonSessionStartEvents(t *testing.T) {
	command := newRootCommand()
	command.SetIn(strings.NewReader(`{"hook_event_name":"Stop","cwd":"/tmp"}`))
	command.SetArgs([]string{"context", "--hook"})
	err := command.Execute()
	if err == nil || !strings.Contains(err.Error(), "requires a SessionStart event") {
		t.Fatalf("error = %v", err)
	}
}

func TestContextHookSuppressedInsideKnowbrewInvocation(t *testing.T) {
	t.Setenv(config.InvocationIDEnvironment, "invocation")
	var output bytes.Buffer
	command := newRootCommand()
	command.SetOut(&output)
	command.SetIn(strings.NewReader("not json"))
	command.SetArgs([]string{"context", "--hook"})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	if output.String() != "" {
		t.Fatalf("internal invocation produced output: %s", output.String())
	}
}

func TestSearchFlagsAndHookOutputUsePlainMasterNames(t *testing.T) {
	rootDir := t.TempDir()
	dataStore, err := store.New(rootDir)
	if err != nil {
		t.Fatal(err)
	}
	annotatedAt := time.Now().UTC()
	feedstock := domain.Feedstock{
		Schema: domain.SchemaVersion, ID: "claude-session-t000001",
		TurnID:    "turn-1",
		Session:   domain.SessionRef{ID: "session"},
		Timestamp: annotatedAt, Agent: "claude",
		Types:   []domain.KnowledgeType{domain.KnowledgeType("property")},
		Summary: "The linked masters were used.", AnnotatedAt: &annotatedAt,
	}
	if err := dataStore.WriteFeedstock(feedstock); err != nil {
		t.Fatal(err)
	}
	if err := dataStore.WriteNewKnowledge("linked-rule", domain.Knowledge{
		Created: annotatedAt, Updated: annotatedAt, Type: domain.KnowledgeType("property"),
		Subject: "subject", Feedstocks: []string{feedstock.ID},
		Status: domain.StatusPending,
	}, "# Linked rule"); err != nil {
		t.Fatal(err)
	}
	knowledgePath, err := dataStore.KnowledgePath("linked-rule")
	if err != nil {
		t.Fatal(err)
	}
	knowledgeData, err := os.ReadFile(knowledgePath)
	if err != nil {
		t.Fatal(err)
	}
	knowledgeData = []byte(strings.Replace(
		string(knowledgeData),
		"approved: false",
		"approved: true",
		1,
	))
	if err := os.WriteFile(knowledgePath, knowledgeData, 0o644); err != nil {
		t.Fatal(err)
	}

	configPath := filepath.Join(t.TempDir(), "config.toml")
	configData := "root = " + quoteTOML(rootDir) + "\n\n" + llmConfigSection
	if err := os.WriteFile(configPath, []byte(configData), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(config.ConfigEnvironment, configPath)

	args := []string{
		"knowledge",
		"--subject", "subject",
	}
	var output bytes.Buffer
	command := newRootCommand()
	command.SetOut(&output)
	command.SetArgs(args)
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output.String(), "[[") ||
		!strings.Contains(output.String(), `"results"`) {
		t.Fatalf("search returned non-normalized JSON: %s", output.String())
	}
}

func TestShowRawFlagValidation(t *testing.T) {
	tests := []struct {
		args []string
		want string
	}{
		{
			args: []string{"show", "feedstock-1", "feedstock-2", "--raw"},
			want: "--raw requires exactly one feedstock ID",
		},
		{
			args: []string{"show", "feedstock-1", "--raw", "--page", "0"},
			want: "--page must be greater than zero",
		},
		{
			args: []string{"show", "feedstock-1", "--page", "2"},
			want: "--page requires --raw",
		},
	}
	for _, test := range tests {
		command := newRootCommand()
		command.SetArgs(test.args)
		err := command.Execute()
		if err == nil || err.Error() != test.want {
			t.Fatalf("%v error = %v, want %q", test.args, err, test.want)
		}
	}
}

func TestFeedstockDraftRejectsFeedstockOutsideTheInvocation(t *testing.T) {
	rootDir := t.TempDir()
	configPath := filepath.Join(t.TempDir(), "config.toml")
	configData := "root = " + quoteTOML(rootDir) + "\n\n" + llmConfigSection
	if err := os.WriteFile(configPath, []byte(configData), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(config.ConfigEnvironment, configPath)
	dataStore, err := store.New(rootDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"fs-assigned", "fs-other"} {
		if err := dataStore.WriteFeedstock(domain.Feedstock{
			Schema: domain.SchemaVersion, ID: id, TurnID: "turn-" + id,
			Session:   domain.SessionRef{ID: "session"},
			Timestamp: time.Now().UTC(), Agent: "claude",
		}); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv(config.InvocationIDEnvironment, "draft-invocation")
	t.Setenv(config.InvocationFeedstockEnvironment, "fs-assigned")

	foreign := newRootCommand()
	foreign.SetOut(&bytes.Buffer{})
	foreign.SetArgs([]string{
		"feedstock", "draft", "fs-other", "--summary", "The user stated a property.",
		"--type", "property",
	})
	if err := foreign.Execute(); err == nil {
		t.Fatal("draft wrote a feedstock outside the invocation")
	}
	stored, _, err := dataStore.FindFeedstock("fs-other")
	if err != nil {
		t.Fatal(err)
	}
	if stored.AnnotatedAt != nil {
		t.Fatalf("feedstock = %#v", stored)
	}

	assigned := newRootCommand()
	assigned.SetOut(&bytes.Buffer{})
	assigned.SetArgs([]string{
		"feedstock", "draft", "fs-assigned", "--summary", "The user stated a property.",
		"--type", "property",
	})
	if err := assigned.Execute(); err != nil {
		t.Fatal(err)
	}
	drawn, _, err := dataStore.FindFeedstock("fs-assigned")
	if err != nil {
		t.Fatal(err)
	}
	if drawn.AnnotatedAt == nil {
		t.Fatalf("feedstock = %#v", drawn)
	}
}

func TestFeedstockDraftTypeFlagsWriteSummaryAndMultipleCandidates(t *testing.T) {
	rootDir := t.TempDir()
	configPath := filepath.Join(t.TempDir(), "config.toml")
	configData := "root = " + quoteTOML(rootDir) + "\n\n" + llmConfigSection
	if err := os.WriteFile(configPath, []byte(configData), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KNOWBREW_CONFIG", configPath)
	dataStore, err := store.New(rootDir)
	if err != nil {
		t.Fatal(err)
	}
	feedstock := domain.Feedstock{
		Schema: domain.SchemaVersion, ID: "fs-type-flags", TurnID: "turn-type-flags",
		Session:   domain.SessionRef{ID: "session"},
		Timestamp: time.Now().UTC(), Agent: "claude",
	}
	if err := dataStore.WriteFeedstock(feedstock); err != nil {
		t.Fatal(err)
	}

	command := newRootCommand()
	command.SetArgs([]string{
		"feedstock", "draft", feedstock.ID,
		"--summary", "The user supplied an established property and relation.",
		"--type", "property",
		"--type", "relation",
	})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	stored, _, err := dataStore.FindFeedstock(feedstock.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Summary != "The user supplied an established property and relation." ||
		stored.AnnotatedAt == nil {
		t.Fatalf("feedstock = %#v", stored)
	}
	if len(stored.Types) != 2 {
		t.Fatalf("types = %#v", stored.Types)
	}
	if got := strings.Join([]string{string(stored.Types[0]), string(stored.Types[1])}, ","); got != "property,relation" {
		t.Fatalf("types = %#v", stored.Types)
	}

	invalidFeedstock := feedstock
	invalidFeedstock.ID = "fs-invalid-type"
	invalidFeedstock.TurnID = "turn-invalid-type"
	if err := dataStore.WriteFeedstock(invalidFeedstock); err != nil {
		t.Fatal(err)
	}
	invalid := newRootCommand()
	invalid.SetArgs([]string{
		"feedstock", "draft", invalidFeedstock.ID,
		"--summary", "summary",
		"--type", "other",
	})
	err = invalid.Execute()
	if err == nil || !strings.Contains(err.Error(), "not defined in masters/types") {
		t.Fatalf("invalid type error = %v", err)
	}

	removedFlagFeedstock := feedstock
	removedFlagFeedstock.ID = "fs-removed-assertion-flag"
	removedFlagFeedstock.TurnID = "turn-removed-assertion-flag"
	if err := dataStore.WriteFeedstock(removedFlagFeedstock); err != nil {
		t.Fatal(err)
	}
	removed := newRootCommand()
	removed.SetArgs([]string{
		"feedstock", "draft", removedFlagFeedstock.ID,
		"--summary", "summary",
		"--assertion", `{"type":"property"}`,
	})
	err = removed.Execute()
	if err == nil || !strings.Contains(err.Error(), "unknown flag: --assertion") {
		t.Fatalf("removed assertion flag error = %v", err)
	}
}

func TestFeedstockDraftReplacesTheSeparateSummarizeAndAnnotateCommands(t *testing.T) {
	rootDir := t.TempDir()
	configPath := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(configPath, []byte("root = "+quoteTOML(rootDir)+"\n\n"+llmConfigSection), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(config.ConfigEnvironment, configPath)
	dataStore, _ := store.New(rootDir)
	feedstock := domain.Feedstock{
		Schema: domain.SchemaVersion, ID: "fs-cli-draft", TurnID: "turn-cli-draft",
		Session:   domain.SessionRef{ID: "session"},
		Timestamp: time.Now().UTC(), Agent: "claude",
	}
	if err := dataStore.WriteFeedstock(feedstock); err != nil {
		t.Fatal(err)
	}
	subcommands := map[string]bool{}
	for _, command := range newRootCommand().Commands() {
		if command.Name() != "feedstock" {
			continue
		}
		for _, sub := range command.Commands() {
			subcommands[sub.Name()] = true
		}
	}
	if subcommands["summarize"] || subcommands["annotate"] {
		t.Fatalf("separate summarize and annotate commands still exist: %#v", subcommands)
	}
	if !subcommands["draft"] {
		t.Fatalf("draft command is missing: %#v", subcommands)
	}
	draft := newRootCommand()
	draft.SetArgs([]string{
		"feedstock", "draft", feedstock.ID,
		"--summary", "target summary",
		"--type", "property",
	})
	if err := draft.Execute(); err != nil {
		t.Fatal(err)
	}
	stored, _, err := dataStore.FindFeedstock(feedstock.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Summary != "target summary" || stored.AnnotatedAt == nil ||
		len(stored.Types) != 1 || stored.Types[0] != domain.KnowledgeType("property") {
		t.Fatalf("feedstock = %#v", stored)
	}
	repeated := newRootCommand()
	repeated.SetArgs([]string{
		"feedstock", "draft", feedstock.ID, "--summary", "replacement",
	})
	if err := repeated.Execute(); err == nil || !strings.Contains(err.Error(), "already drawn") {
		t.Fatalf("repeated draft error = %v", err)
	}
}

func TestFeedstockContextReadsBoundedTurnsFromSource(t *testing.T) {
	rootDir := t.TempDir()
	configPath := filepath.Join(t.TempDir(), "config.toml")
	sourceDir := t.TempDir()
	configData := "root = " + quoteTOML(rootDir) + "\n\n" + llmConfigSection + "\n[draw]\nconcurrency = 1\ncontext_turns = 0\nmax_context_turns = 1\n\n[[sources]]\nagent = \"claude\"\nparser = \"claude\"\npaths = [" + quoteTOML(sourceDir) + "]\n"
	if err := os.WriteFile(configPath, []byte(configData), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(config.ConfigEnvironment, configPath)
	sessionID := "source-context-session"
	logPath := filepath.Join(sourceDir, "session.jsonl")
	log := `{"type":"user","uuid":"turn-1","sessionId":"source-context-session","timestamp":"2026-07-30T01:00:00Z","message":{"role":"user","content":"before user"}}
{"type":"assistant","sessionId":"source-context-session","timestamp":"2026-07-30T01:00:01Z","message":{"role":"assistant","content":"before agent"}}
{"type":"user","uuid":"turn-2","sessionId":"source-context-session","timestamp":"2026-07-30T01:00:00Z","message":{"role":"user","content":"target user"}}
{"type":"assistant","sessionId":"source-context-session","timestamp":"2026-07-30T01:00:01Z","message":{"role":"assistant","content":"target agent"}}
{"type":"user","uuid":"turn-3","sessionId":"source-context-session","timestamp":"2026-07-30T01:00:00Z","message":{"role":"user","content":"after user"}}
`
	if err := os.WriteFile(logPath, []byte(log), 0o600); err != nil {
		t.Fatal(err)
	}
	feedstockID := parser.FeedstockID("claude", sessionID, "turn-2")
	dataStore, err := store.New(rootDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := dataStore.WriteFeedstock(domain.Feedstock{
		Schema: domain.SchemaVersion, ID: feedstockID, TurnID: "turn-2",
		Session:   domain.SessionRef{ID: sessionID},
		Timestamp: time.Date(2026, 7, 30, 1, 0, 0, 0, time.UTC), Agent: "claude",
	}); err != nil {
		t.Fatal(err)
	}
	t.Setenv(config.InvocationIDEnvironment, "context-invocation")
	t.Setenv(config.InvocationFeedstockEnvironment, feedstockID)
	command := newRootCommand()
	var output bytes.Buffer
	command.SetOut(&output)
	command.SetArgs([]string{"feedstock", "context", feedstockID})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	var response draw.AnnotationContext
	if err := json.Unmarshal(output.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.TargetUserInput != "target user" || len(response.PriorTurns) != 1 ||
		response.PriorTurns[0].Offset != -1 {
		t.Fatalf("context response = %#v", response)
	}
	if response.PriorTurns[0].UserInput != "before user" ||
		response.PriorTurns[0].AgentResponse != "before agent" {
		t.Fatalf("context turns = %#v", response.PriorTurns)
	}
	second := newRootCommand()
	second.SetOut(&bytes.Buffer{})
	second.SetArgs([]string{"feedstock", "context", feedstockID})
	if err := second.Execute(); err == nil || !strings.Contains(err.Error(), "already been loaded") {
		t.Fatalf("second context error = %v", err)
	}
}

func TestSubjectCreationFlagIsUnavailable(t *testing.T) {
	args := []string{
		"feedstock", "draft", "fs-source",
		"--summary", "The user stated a property.",
		"--new-subject", "invented=Invented subject.",
	}
	command := newRootCommand()
	command.SetArgs(args)
	err := command.Execute()
	if err == nil || !strings.Contains(err.Error(), "unknown flag: --new-subject") {
		t.Fatalf("%v error = %v", args, err)
	}
}

func TestKnowledgeCommandsUseFeedstockTerminologyOnly(t *testing.T) {
	root := newRootCommand()
	knowledge, _, err := root.Find([]string{"knowledge"})
	if err != nil {
		t.Fatal(err)
	}
	for _, command := range knowledge.Commands() {
		switch command.Name() {
		case "create", "add-feedstock", "add-source", "invalidate":
			t.Fatalf("obsolete knowledge mutation %q is still registered", command.Name())
		}
	}
	names := map[string]bool{}
	for _, command := range knowledge.Commands() {
		names[command.Name()] = true
	}
	if len(names) != 1 || !names["show"] {
		t.Fatalf("knowledge subcommands = %#v", names)
	}
}

func TestKnowledgeSearchBuildsIndex(t *testing.T) {
	rootDir := t.TempDir()
	configPath := filepath.Join(t.TempDir(), "config.toml")
	configData := "root = " + quoteTOML(rootDir) + "\n\n" + llmConfigSection
	if err := os.WriteFile(configPath, []byte(configData), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(config.ConfigEnvironment, configPath)

	var output bytes.Buffer
	command := newRootCommand()
	command.SetOut(&output)
	command.SetArgs([]string{"knowledge"})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	indexPath := filepath.Join(rootDir, ".knowbrew", "state", "index.sqlite")
	if _, err := os.Stat(indexPath); err != nil {
		t.Fatalf("knowledge search did not create the index at %s: %v", indexPath, err)
	}
}

// llmConfigSection writes the [llm] table with both Draw stage keys, which
// configuration loading requires.
const llmConfigSection = "[llm]\nbackend = \"claude-cli\"\n" +
	"draw_draft_model = \"\"\ndraw_draft_effort = \"\"\n" +
	"draw_extract_model = \"\"\ndraw_extract_effort = \"\"\n"

func quoteTOML(value string) string {
	return `"` + value + `"`
}
