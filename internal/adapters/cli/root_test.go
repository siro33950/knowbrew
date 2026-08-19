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
	configData := "root = " + quoteTOML(rootDir) + "\n\n[llm]\nbackend = \"claude-cli\"\n"
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
	for _, name := range []string{"hook", "max", "source", "since", "until"} {
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
		"[llm]\nbackend = \"claude-cli\"\n\n" +
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

func TestDrawHookExitsQuietlyWhileAnotherRunHoldsTheLock(t *testing.T) {
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
		"[llm]\nbackend = \"claude-cli\"\n\n" +
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
	configData := "root = " + quoteTOML(rootDir) + "\n\n[llm]\nbackend = \"claude-cli\"\n"
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
	configData := "root = " + quoteTOML(rootDir) + "\n\n[llm]\nbackend = \"claude-cli\"\n"
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
		Types:    []domain.KnowledgeType{domain.KnowledgeType("property")},
		Subjects: []string{"subject"},
		Summary:  "The linked masters were used.", AnnotatedAt: &annotatedAt,
		Assertions: []domain.Assertion{{
			ID: "as-linked", Type: "property", Subject: "subject",
			Statement: "Linked masters remain plain in JSON.",
		}},
	}
	if err := dataStore.WriteFeedstock(feedstock); err != nil {
		t.Fatal(err)
	}
	if err := dataStore.WriteNewKnowledge("linked-rule", domain.Knowledge{
		Created: annotatedAt, Updated: annotatedAt, Type: domain.KnowledgeType("property"),
		Subject: "subject", Feedstocks: []string{feedstock.ID},
		Status: domain.StatusPending, Trigger: "always",
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
	configData := "root = " + quoteTOML(rootDir) + "\n\n[llm]\nbackend = \"claude-cli\"\n"
	if err := os.WriteFile(configPath, []byte(configData), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(config.ConfigEnvironment, configPath)

	args := []string{
		"knowledge", "--trigger", "always",
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
		!strings.Contains(output.String(), `"approved_rules"`) {
		t.Fatalf("hook returned non-normalized JSON: %s", output.String())
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

func TestFeedstockAnnotateAssertionFlagsDeriveMultipleTypes(t *testing.T) {
	rootDir := t.TempDir()
	configPath := filepath.Join(t.TempDir(), "config.toml")
	configData := "root = " + quoteTOML(rootDir) + "\n\n[llm]\nbackend = \"claude-cli\"\n"
	if err := os.WriteFile(configPath, []byte(configData), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KNOWBREW_CONFIG", configPath)
	dataStore, err := store.New(rootDir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := dataStore.EnsureMaster("subjects", domain.MasterEntry{
		Name: "subject", Definition: "The existing test subject.",
	}); err != nil {
		t.Fatal(err)
	}
	feedstock := domain.Feedstock{
		Schema: domain.SchemaVersion, ID: "fs-type-flags", TurnID: "turn-type-flags",
		Session:   domain.SessionRef{ID: "session"},
		Timestamp: time.Now().UTC(), Agent: "claude",
		Subjects: []string{"subject"}, Summary: "The user supplied an established property and relation.",
	}
	if err := dataStore.WriteFeedstock(feedstock); err != nil {
		t.Fatal(err)
	}

	command := newRootCommand()
	command.SetArgs([]string{
		"feedstock", "annotate", feedstock.ID,
		"--assertion", `{"type":"property","subject":"subject","statement":"The value is stable."}`,
		"--assertion", `{"type":"relation","subject":"","statement":"The first value depends on the second."}`,
	})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	stored, _, err := dataStore.FindFeedstock(feedstock.ID)
	if err != nil {
		t.Fatal(err)
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
		"feedstock", "annotate", invalidFeedstock.ID,
		"--assertion", `{"type":"other","subject":"","statement":"The value is stable."}`,
	})
	err = invalid.Execute()
	if err == nil || !strings.Contains(err.Error(), "not defined in masters/types") {
		t.Fatalf("invalid type error = %v", err)
	}

	missingSubject := feedstock
	missingSubject.ID = "fs-missing-subject"
	missingSubject.TurnID = "turn-missing-subject"
	if err := dataStore.WriteFeedstock(missingSubject); err != nil {
		t.Fatal(err)
	}
	missing := newRootCommand()
	missing.SetArgs([]string{
		"feedstock", "annotate", missingSubject.ID,
		"--assertion", `{"type":"property","statement":"The value is stable."}`,
	})
	err = missing.Execute()
	if err == nil || !strings.Contains(err.Error(), "subject is required") {
		t.Fatalf("missing subject error = %v", err)
	}
}

func TestFeedstockSummarizeAndAnnotateAreSeparateCommands(t *testing.T) {
	rootDir := t.TempDir()
	configPath := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(configPath, []byte("root = "+quoteTOML(rootDir)+"\n\n[llm]\nbackend = \"claude-cli\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(config.ConfigEnvironment, configPath)
	dataStore, _ := store.New(rootDir)
	feedstock := domain.Feedstock{
		Schema: domain.SchemaVersion, ID: "fs-cli-phases", TurnID: "turn-cli-phases",
		Session:   domain.SessionRef{ID: "session"},
		Timestamp: time.Now().UTC(), Agent: "claude",
	}
	if err := dataStore.WriteFeedstock(feedstock); err != nil {
		t.Fatal(err)
	}
	summarize := newRootCommand()
	summarize.SetArgs([]string{"feedstock", "summarize", feedstock.ID, "--summary", "target summary"})
	if err := summarize.Execute(); err != nil {
		t.Fatal(err)
	}
	annotate := newRootCommand()
	annotate.SetArgs([]string{"feedstock", "annotate", feedstock.ID})
	if err := annotate.Execute(); err != nil {
		t.Fatal(err)
	}
	stored, _, err := dataStore.FindFeedstock(feedstock.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Summary != "target summary" || stored.AnnotatedAt == nil {
		t.Fatalf("feedstock = %#v", stored)
	}
	legacy := newRootCommand()
	legacy.SetArgs([]string{"feedstock", "annotate", feedstock.ID, "--summary", "replacement"})
	if err := legacy.Execute(); err == nil || !strings.Contains(err.Error(), "unknown flag: --summary") {
		t.Fatalf("legacy annotate summary error = %v", err)
	}
}

func TestFeedstockContextReadsBoundedTurnsFromSource(t *testing.T) {
	rootDir := t.TempDir()
	configPath := filepath.Join(t.TempDir(), "config.toml")
	sourceDir := t.TempDir()
	configData := "root = " + quoteTOML(rootDir) + "\n\n[llm]\nbackend = \"claude-cli\"\n\n[draw]\nconcurrency = 1\ncontext_turns = 0\nmax_context_turns = 1\n\n[[sources]]\nagent = \"claude\"\nparser = \"claude\"\npaths = [" + quoteTOML(sourceDir) + "]\n"
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
	for _, args := range [][]string{
		{
			"feedstock", "annotate", "fs-source",
			"--new-subject", "invented=Invented subject.",
		},
		{
			"knowledge", "submit", "fs-source",
			"--resolution", `{"kind":"new","knowledge_ids":[],"draft":null}`,
			"--new-subject", "invented=Invented subject.",
		},
	} {
		command := newRootCommand()
		command.SetArgs(args)
		err := command.Execute()
		if err == nil ||
			!strings.Contains(err.Error(), "unknown flag: --new-subject") {
			t.Fatalf("%v error = %v", args, err)
		}
	}
}

func TestKnowledgeSubmitRequiresInvocationAndValidatesType(t *testing.T) {
	rootDir := t.TempDir()
	configPath := filepath.Join(t.TempDir(), "config.toml")
	configData := "root = " + quoteTOML(rootDir) + "\n\n[llm]\nbackend = \"claude-cli\"\n"
	if err := os.WriteFile(configPath, []byte(configData), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KNOWBREW_CONFIG", configPath)
	dataStore, err := store.New(rootDir)
	if err != nil {
		t.Fatal(err)
	}
	feedstock := domain.Feedstock{
		Schema: domain.SchemaVersion, ID: "fs-source", TurnID: "turn-source",
		Session:   domain.SessionRef{ID: "session"},
		Timestamp: time.Now().UTC(), Agent: "claude",
		Types: []domain.KnowledgeType{"property"}, Summary: "The user supplied a reusable property.",
		AnnotatedAt: func() *time.Time { value := time.Now().UTC(); return &value }(),
		Assertions: []domain.Assertion{{
			ID: "as-source", Type: "property", Subject: "subject",
			Statement: "Use the tested behavior.",
		}},
	}
	if err := dataStore.WriteFeedstock(feedstock); err != nil {
		t.Fatal(err)
	}
	outside := newRootCommand()
	outside.SetArgs([]string{
		"knowledge", "submit", feedstock.ID,
		"--assertion", "as-source", "--verification", "verified",
	})
	if err := outside.Execute(); err == nil ||
		!strings.Contains(err.Error(), "only inside an assertion invocation") {
		t.Fatalf("outside invocation error = %v", err)
	}
	t.Setenv(config.InvocationFeedstockEnvironment, feedstock.ID)
	t.Setenv(config.InvocationAssertionEnvironment, "as-source")
	t.Setenv(config.InvocationIDEnvironment, "submit-invalid-type")
	invalid := newRootCommand()
	invalid.SetArgs([]string{
		"knowledge", "submit", feedstock.ID,
		"--assertion", "as-source", "--verification", "corrected",
		"--corrected-assertion", `{"id":"as-source","type":"other","subject":"subject","statement":"Use the tested behavior."}`,
		"--resolution", `{"kind":"new","knowledge_ids":[],"draft":null}`,
	})
	if err := invalid.Execute(); err == nil ||
		!strings.Contains(err.Error(), "not defined in masters/types") {
		t.Fatalf("invalid type error = %v", err)
	}
}

func TestKnowledgeSubmitCreatesIDBasedPendingKnowledgeWithSubject(t *testing.T) {
	rootDir := t.TempDir()
	configPath := filepath.Join(t.TempDir(), "config.toml")
	configData := "root = " + quoteTOML(rootDir) + "\n\n[llm]\nbackend = \"claude-cli\"\n"
	if err := os.WriteFile(configPath, []byte(configData), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(config.ConfigEnvironment, configPath)
	dataStore, err := store.New(rootDir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := dataStore.EnsureMaster("subjects", domain.MasterEntry{
		Name: "knowbrew", Definition: "The existing knowbrew subject.",
	}); err != nil {
		t.Fatal(err)
	}
	annotatedAt := time.Now().UTC()
	feedstock := domain.Feedstock{
		Schema: domain.SchemaVersion, ID: "fs-subject-flag", TurnID: "turn-subject-flag",
		Session:   domain.SessionRef{ID: "session"},
		Timestamp: annotatedAt, Agent: "claude",
		Types:       []domain.KnowledgeType{domain.KnowledgeType("property")},
		Summary:     "The user supplied a reusable fact.",
		AnnotatedAt: &annotatedAt,
		Assertions: []domain.Assertion{{
			ID: "as-subject", Type: "property", Subject: "knowbrew",
			Statement: "The subject flag preserves attribution.",
		}},
	}
	if err := dataStore.WriteFeedstock(feedstock); err != nil {
		t.Fatal(err)
	}
	t.Setenv(config.InvocationFeedstockEnvironment, feedstock.ID)
	t.Setenv(config.InvocationAssertionEnvironment, "as-subject")
	t.Setenv(config.InvocationIDEnvironment, "submit-subject")
	catalog := newRootCommand()
	catalog.SetArgs([]string{
		"knowledge", "catalog", "--subject", "knowbrew",
		"--query", "The subject flag preserves attribution.",
	})
	if err := catalog.Execute(); err != nil {
		t.Fatal(err)
	}
	var submitOutput bytes.Buffer
	command := newRootCommand()
	command.SetOut(&submitOutput)
	command.SetArgs([]string{
		"knowledge", "submit", feedstock.ID,
		"--assertion", "as-subject", "--verification", "verified",
		"--resolution", `{"kind":"new","knowledge_ids":[],"draft":null}`,
	})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	var submitted struct {
		KnowledgeID string `json:"knowledge_id"`
	}
	if err := json.Unmarshal(submitOutput.Bytes(), &submitted); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(submitted.KnowledgeID, "kn-") {
		t.Fatalf("submit output = %s", submitOutput.String())
	}
	path, err := dataStore.KnowledgePath(submitted.KnowledgeID)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `subject: "[[knowbrew]]"`) {
		t.Fatalf("knowledge subject is not a wikilink:\n%s", data)
	}
	if !strings.Contains(string(data), `established_by: "[[`+feedstock.ID+`]]"`) {
		t.Fatalf("knowledge established_by is not a feedstock link:\n%s", data)
	}
	if !strings.Contains(string(data), "approved: false") ||
		strings.Contains(string(data), "\nstatus:") ||
		!strings.Contains(string(data), "## Claim\n\nThe subject flag preserves attribution.") ||
		!strings.Contains(string(data), `- "[[`+feedstock.ID+`#as-subject]]"`) {
		t.Fatalf("knowledge was not rendered by the CLI:\n%s", data)
	}
	obsoleteKey := "pro" + "ject:"
	if strings.Contains(string(data), obsoleteKey) {
		t.Fatalf("knowledge contains obsolete key:\n%s", data)
	}
	var shown bytes.Buffer
	for _, key := range []string{
		config.InvocationIDEnvironment,
		config.InvocationFeedstockEnvironment,
		config.InvocationAssertionEnvironment,
	} {
		if err := os.Unsetenv(key); err != nil {
			t.Fatal(err)
		}
	}
	show := newRootCommand()
	show.SetOut(&shown)
	show.SetArgs([]string{"knowledge", "show", submitted.KnowledgeID})
	if err := show.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(shown.String(), `"status": "pending"`) ||
		!strings.Contains(shown.String(), `"approved": false`) ||
		!strings.Contains(shown.String(), `"id": "`+submitted.KnowledgeID+`"`) {
		t.Fatalf("knowledge show output = %s", shown.String())
	}

	legacy := newRootCommand()
	legacy.SetArgs([]string{
		"knowledge", "submit", feedstock.ID,
		"--assertion", "as-subject", "--verification", "verified",
		"--slug", "legacy-subject-flag",
		"--pro" + "ject", "knowbrew",
	})
	if err := legacy.Execute(); err == nil || !strings.Contains(err.Error(), "unknown flag") {
		t.Fatalf("removed flag error = %v", err)
	}
}

func TestKnowledgeCommandsUseFeedstockTerminologyOnly(t *testing.T) {
	legacyFlag := newRootCommand()
	legacyFlag.SetArgs([]string{
		"knowledge", "submit", "fs-legacy",
		"--relation", "new",
		"--slug", "legacy-source-flag",
		"--type", "property",
		"--applies-when", "When testing a removed flag",
		"--claim", "Use a removed flag.",
		"--source", "fs-legacy",
	})
	if err := legacyFlag.Execute(); err == nil || !strings.Contains(err.Error(), "unknown flag") {
		t.Fatalf("removed source flag error = %v", err)
	}

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
	if len(names) != 3 || !names["show"] || !names["catalog"] || !names["submit"] {
		t.Fatalf("knowledge subcommands = %#v", names)
	}
}

func TestInternalInvocationTriggerReturnsEmptyRulesWithoutIndex(t *testing.T) {
	rootDir := t.TempDir()
	configPath := filepath.Join(t.TempDir(), "config.toml")
	configData := "root = " + quoteTOML(rootDir) + "\n\n[llm]\nbackend = \"claude-cli\"\n"
	if err := os.WriteFile(configPath, []byte(configData), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(config.ConfigEnvironment, configPath)
	t.Setenv(config.InvocationIDEnvironment, "internal-invocation")

	var output bytes.Buffer
	command := newRootCommand()
	command.SetOut(&output)
	command.SetArgs([]string{"knowledge", "--trigger", "always"})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	var response struct {
		ApprovedRules []json.RawMessage `json:"approved_rules"`
		Total         int               `json:"total"`
		Returned      int               `json:"returned"`
		Truncated     bool              `json:"truncated"`
	}
	if err := json.Unmarshal(output.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.ApprovedRules == nil || len(response.ApprovedRules) != 0 ||
		response.Total != 0 || response.Returned != 0 || response.Truncated {
		t.Fatalf("response = %#v; JSON = %s", response, output.String())
	}
	indexPath := filepath.Join(rootDir, ".knowbrew", "state", "index.sqlite")
	if _, err := os.Stat(indexPath); !os.IsNotExist(err) {
		t.Fatalf("internal trigger created an index at %s: %v", indexPath, err)
	}
}

func TestNormalTriggerSearchStillBuildsIndex(t *testing.T) {
	previous, existed := os.LookupEnv(config.InvocationIDEnvironment)
	if err := os.Unsetenv(config.InvocationIDEnvironment); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if existed {
			_ = os.Setenv(config.InvocationIDEnvironment, previous)
		} else {
			_ = os.Unsetenv(config.InvocationIDEnvironment)
		}
	})
	rootDir := t.TempDir()
	configPath := filepath.Join(t.TempDir(), "config.toml")
	configData := "root = " + quoteTOML(rootDir) + "\n\n[llm]\nbackend = \"claude-cli\"\n\n[embedding]\nmodel = \"" + config.EmbeddingRuri + "\"\n"
	if err := os.WriteFile(configPath, []byte(configData), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(config.ConfigEnvironment, configPath)

	var output bytes.Buffer
	command := newRootCommand()
	command.SetOut(&output)
	command.SetArgs([]string{"knowledge", "--trigger", "always"})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	indexPath := filepath.Join(rootDir, ".knowbrew", "state", "index.sqlite")
	if _, err := os.Stat(indexPath); err != nil {
		t.Fatalf("normal trigger did not create the index at %s: %v", indexPath, err)
	}
}

func quoteTOML(value string) string {
	return `"` + value + `"`
}
