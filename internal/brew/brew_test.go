package brew

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gofrs/flock"
	"github.com/siro33950/knowbrew/internal/config"
	"github.com/siro33950/knowbrew/internal/domain"
	"github.com/siro33950/knowbrew/internal/frontmatter"
	"github.com/siro33950/knowbrew/internal/fsutil"
	"github.com/siro33950/knowbrew/internal/llm"
	"github.com/siro33950/knowbrew/internal/query"
	"github.com/siro33950/knowbrew/internal/store"
)

type creatingRunner struct {
	store  *store.Store
	source string
	called int
}

type capturingRunner struct {
	prompt string
}

func (runner *capturingRunner) Run(_ context.Context, _ llm.Task, _ string, prompt string) error {
	runner.prompt = prompt
	return nil
}

func (runner *creatingRunner) Run(ctx context.Context, task llm.Task, _, _ string) error {
	runner.called++
	if task != llm.TaskBrew {
		return nil
	}
	_, err := CreateKnowledge(ctx, runner.store, CreateInput{
		Slug: "focused-testing", AppliesWhen: "When modifying tests",
		Body: "# Run focused tests before the full suite", Sources: []string{runner.source},
		Topics: []string{"testing"}, NewTopics: []string{"testing=Automated software verification."},
	})
	return err
}

func TestBrewCreatesPendingKnowledgeAndMarksFeedstockOnce(t *testing.T) {
	root := t.TempDir()
	dataStore, _ := store.New(root)
	feedstock := domain.Feedstock{
		Schema: domain.SchemaVersion, ID: "claude-session-t000001",
		Session:   domain.SessionRef{ID: "session", Path: "/log"},
		Timestamp: time.Now().UTC(), Agent: "claude", UserQuote: "Run focused tests.",
		SpeechActs: []string{"instruction"}, Topics: []string{"testing"},
		Subjects: []string{"project"}, Summary: "The user requested focused tests.",
	}
	if err := dataStore.WriteFeedstock(feedstock); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{
		Root: root, Path: filepath.Join(root, ".knowbrew", "config.toml"),
		LLM: config.LLM{Backend: "claude-cli"},
	}
	runner := &creatingRunner{store: dataStore, source: feedstock.ID}
	summary, err := Run(context.Background(), cfg, runner, nil)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Created != 1 || summary.FeedstocksProcessed != 1 {
		t.Fatalf("summary = %#v", summary)
	}
	path, _ := dataStore.KnowledgePath("focused-testing")
	knowledge, _, err := dataStore.ReadKnowledge(path)
	if err != nil {
		t.Fatal(err)
	}
	if knowledge.Status != domain.StatusPending {
		t.Fatalf("knowledge status = %q", knowledge.Status)
	}
	if _, err := Run(context.Background(), cfg, runner, nil); err != nil {
		t.Fatal(err)
	}
	if runner.called != 1 {
		t.Fatalf("runner called %d times, want 1", runner.called)
	}
}

func TestBrewPromptContainsOnlyCandidateIDAndCLIInstructions(t *testing.T) {
	dataStore, feedstockID := storeWithFeedstock(t)
	feedstock, _, err := dataStore.FindFeedstock(feedstockID)
	if err != nil {
		t.Fatal(err)
	}
	feedstock.Summary = "SECRET FEEDSTOCK CONTENT"
	feedstock.UserQuote = "SECRET ORIGINAL CONTENT"
	feedstockPath, _ := dataStore.FeedstockPath(feedstock)
	data, err := frontmatter.Encode(feedstock, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := fsutil.AtomicWrite(feedstockPath, data, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := dataStore.WriteNewKnowledge("secret-context", domain.Knowledge{
		Created: time.Now().UTC(), Updated: time.Now().UTC(),
		AppliesWhen: "SECRET KNOWLEDGE CONDITION", Sources: []string{feedstockID},
		Status: domain.StatusPending,
	}, "# SECRET KNOWLEDGE BODY"); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{
		Root: dataStore.Root, Path: filepath.Join(dataStore.Root, ".knowbrew", "config.toml"),
		LLM: config.LLM{Backend: "claude-cli"},
	}
	runner := &capturingRunner{}
	if _, err := Run(context.Background(), cfg, runner, nil); err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{
		"SECRET FEEDSTOCK CONTENT",
		"SECRET ORIGINAL CONTENT",
		"SECRET KNOWLEDGE CONDITION",
		"SECRET KNOWLEDGE BODY",
	} {
		if strings.Contains(runner.prompt, forbidden) {
			t.Fatalf("prompt leaked %q:\n%s", forbidden, runner.prompt)
		}
	}
	for _, required := range []string{
		feedstockID,
		"knowbrew show",
		"knowbrew knowledge --include-pending -- <keywords>",
		"knowbrew feedstock -- <keywords>",
		"Always place search keywords after \"--\"",
	} {
		if !strings.Contains(runner.prompt, required) {
			t.Fatalf("prompt does not contain %q:\n%s", required, runner.prompt)
		}
	}
}

func TestKnowledgeOperationMustCiteInvocationFeedstock(t *testing.T) {
	dataStore, feedstockID := storeWithFeedstock(t)
	t.Setenv(config.InvocationFeedstockEnvironment, "claude-other-t000001")
	_, err := CreateKnowledge(context.Background(), dataStore, CreateInput{
		Slug: "wrong-evidence", AppliesWhen: "When testing", Body: "# Claim",
		Sources: []string{feedstockID}, Topics: []string{"testing"},
	})
	if err == nil || !strings.Contains(err.Error(), "must cite invocation feedstock") {
		t.Fatalf("error = %v, want invocation source rejection", err)
	}
}

func TestInvocationAllowsOnlyOneSuccessfulKnowledgeOperation(t *testing.T) {
	dataStore, feedstockID := storeWithFeedstock(t)
	t.Setenv(config.InvocationFeedstockEnvironment, feedstockID)
	t.Setenv(config.InvocationIDEnvironment, "invocation-1")
	first := CreateInput{
		Slug: "first-claim", AppliesWhen: "When testing", Body: "# First",
		Sources: []string{feedstockID}, Topics: []string{"testing"},
	}
	if _, err := CreateKnowledge(context.Background(), dataStore, first); err != nil {
		t.Fatal(err)
	}
	second := first
	second.Slug = "second-claim"
	second.Body = "# Second"
	if _, err := CreateKnowledge(context.Background(), dataStore, second); err == nil ||
		!strings.Contains(err.Error(), "already completed") {
		t.Fatalf("error = %v, want second-operation rejection", err)
	}
}

func TestFailedKnowledgeOperationReleasesInvocationClaim(t *testing.T) {
	dataStore, feedstockID := storeWithFeedstock(t)
	t.Setenv(config.InvocationFeedstockEnvironment, feedstockID)
	t.Setenv(config.InvocationIDEnvironment, "invocation-retry")
	input := CreateInput{
		Slug: "retry-claim", AppliesWhen: "When testing",
		Sources: []string{feedstockID}, Topics: []string{"testing"},
	}
	if _, err := CreateKnowledge(context.Background(), dataStore, input); err == nil {
		t.Fatal("expected empty knowledge body to fail")
	}
	input.Body = "# Valid retry"
	if _, err := CreateKnowledge(context.Background(), dataStore, input); err != nil {
		t.Fatalf("valid retry failed: %v", err)
	}
}

func TestReadCommandsDoNotConsumeInvocationMutationClaim(t *testing.T) {
	dataStore, feedstockID := storeWithFeedstock(t)
	t.Setenv(config.InvocationFeedstockEnvironment, feedstockID)
	t.Setenv(config.InvocationIDEnvironment, "read-before-write")
	if _, err := query.Search(context.Background(), dataStore, query.SearchOptions{
		Target: query.TargetFeedstock, Limit: 10, MaxTokens: 1000,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := CreateKnowledge(context.Background(), dataStore, CreateInput{
		Slug: "claim-after-read", AppliesWhen: "When testing reads",
		Body:    "# Read operations do not consume the write claim",
		Sources: []string{feedstockID}, Topics: []string{"testing"},
	}); err != nil {
		t.Fatalf("knowledge mutation after read failed: %v", err)
	}
}

func TestCreateKnowledgeRegistersUnknownProjectAsPendingSubject(t *testing.T) {
	dataStore, feedstockID := storeWithFeedstock(t)
	added, err := CreateKnowledge(context.Background(), dataStore, CreateInput{
		Slug: "project-claim", AppliesWhen: "When working on the project",
		Body: "# Project claim", Sources: []string{feedstockID}, Project: "knowbrew",
		NewSubjects: []string{"knowbrew=The knowbrew command-line project."},
	})
	if err != nil {
		t.Fatal(err)
	}
	if added != 1 {
		t.Fatalf("masters added = %d, want 1", added)
	}
	subjects, warnings, err := dataStore.LoadMasters("subjects")
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 0 {
		t.Fatalf("master warnings = %#v", warnings)
	}
	if len(subjects) != 1 || subjects[0].Name != "knowbrew" ||
		subjects[0].Status != domain.StatusPending {
		t.Fatalf("subjects = %#v", subjects)
	}
}

type retryingBrewRunner struct {
	failFeedstockID string
	failuresLeft    int
}

func (runner *retryingBrewRunner) Run(_ context.Context, task llm.Task, feedstockID, _ string) error {
	if task == llm.TaskBrew && feedstockID == runner.failFeedstockID && runner.failuresLeft > 0 {
		runner.failuresLeft--
		return errors.New("temporary brew failure")
	}
	return nil
}

func TestBrewContinuesAfterBackendFailureAndRetriesFeedstock(t *testing.T) {
	root := t.TempDir()
	dataStore, _ := store.New(root)
	base := time.Now().UTC()
	for index := 1; index <= 2; index++ {
		id := "claude-session-t00000" + string(rune('0'+index))
		if err := dataStore.WriteFeedstock(domain.Feedstock{
			Schema: domain.SchemaVersion, ID: id,
			Session:   domain.SessionRef{ID: "session", Path: "/log"},
			Timestamp: base.Add(time.Duration(index) * time.Second),
			Agent:     "claude", UserQuote: "Remember this.",
			SpeechActs: []string{"fact"}, Subjects: []string{"project"},
			Summary: "The user supplied a fact.",
		}); err != nil {
			t.Fatal(err)
		}
	}
	failedID := "claude-session-t000001"
	cfg := config.Config{
		Root: root, Path: filepath.Join(root, ".knowbrew", "config.toml"),
		LLM: config.LLM{Backend: "claude-cli"},
	}
	runner := &retryingBrewRunner{failFeedstockID: failedID, failuresLeft: 1}

	first, err := Run(context.Background(), cfg, runner, nil)
	if err != nil {
		t.Fatal(err)
	}
	if first.FeedstocksFailed != 1 || first.FeedstocksProcessed != 1 || first.Noop != 1 {
		t.Fatalf("first summary = %#v", first)
	}
	if len(first.Failures) != 1 || !strings.Contains(first.Failures[0].Reason, "temporary brew failure") {
		t.Fatalf("failures = %#v", first.Failures)
	}
	failed, _, err := dataStore.FindFeedstock(failedID)
	if err != nil {
		t.Fatal(err)
	}
	if failed.BrewedAt != nil {
		t.Fatalf("failed feedstock was marked brewed: %#v", failed.BrewedAt)
	}

	second, err := Run(context.Background(), cfg, runner, nil)
	if err != nil {
		t.Fatal(err)
	}
	if second.FeedstocksFailed != 0 || second.FeedstocksProcessed != 1 || second.Noop != 1 {
		t.Fatalf("second summary = %#v", second)
	}
	failed, _, err = dataStore.FindFeedstock(failedID)
	if err != nil {
		t.Fatal(err)
	}
	if failed.BrewedAt == nil {
		t.Fatal("retried feedstock was not marked brewed")
	}
}

func TestBrewLockFailsImmediatelyWhenHeld(t *testing.T) {
	root := t.TempDir()
	dataStore, _ := store.New(root)
	if err := dataStore.EnsureLayout(); err != nil {
		t.Fatal(err)
	}
	lock := flock.New(filepath.Join(root, ".state", "brew.lock"))
	if err := lock.Lock(); err != nil {
		t.Fatal(err)
	}
	defer lock.Unlock()
	cfg := config.Config{
		Root: root, Path: filepath.Join(root, ".knowbrew", "config.toml"),
		LLM: config.LLM{Backend: "claude-cli"},
	}

	started := time.Now()
	_, err := Run(context.Background(), cfg, nil, nil)
	if err == nil || err.Error() != "another knowbrew brew process is running" {
		t.Fatalf("error = %v", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("lock conflict took %s", elapsed)
	}
}

func TestBrewSkipsBrokenFeedstockAndReportsWarning(t *testing.T) {
	dataStore, feedstockID := storeWithFeedstock(t)
	brokenPath := filepath.Join(dataStore.Root, "feedstocks", "broken.md")
	if err := os.WriteFile(brokenPath, []byte("---\nstatus: typo\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{
		Root: dataStore.Root, Path: filepath.Join(dataStore.Root, ".knowbrew", "config.toml"),
		LLM: config.LLM{Backend: "claude-cli"},
	}
	runner := &retryingBrewRunner{}
	summary, err := Run(context.Background(), cfg, runner, nil)
	if err != nil {
		t.Fatal(err)
	}
	if summary.FeedstocksProcessed != 1 || len(summary.Warnings) != 1 {
		t.Fatalf("summary = %#v", summary)
	}
	if summary.Warnings[0].Path != brokenPath ||
		!strings.HasPrefix(summary.Warnings[0].Message, "skipped: "+brokenPath+":") {
		t.Fatalf("warning = %#v", summary.Warnings[0])
	}
	feedstock, _, err := dataStore.FindFeedstock(feedstockID)
	if err != nil {
		t.Fatal(err)
	}
	if feedstock.BrewedAt == nil {
		t.Fatal("valid feedstock was not processed")
	}
}

func storeWithFeedstock(t *testing.T) (*store.Store, string) {
	t.Helper()
	dataStore, err := store.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	feedstock := domain.Feedstock{
		Schema: domain.SchemaVersion, ID: "claude-session-t000001",
		Session:   domain.SessionRef{ID: "session", Path: "/log"},
		Timestamp: time.Now().UTC(), Agent: "claude", UserQuote: "Test this.",
		SpeechActs: []string{"request"}, Topics: []string{"testing"},
		Subjects: []string{"project"}, Summary: "The user requested tests.",
	}
	if err := dataStore.WriteFeedstock(feedstock); err != nil {
		t.Fatal(err)
	}
	return dataStore, feedstock.ID
}
