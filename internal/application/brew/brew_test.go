package brew

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/siro33950/knowbrew/internal/adapters/config"
	"github.com/siro33950/knowbrew/internal/adapters/persistence/knowledgefmt"
	"github.com/siro33950/knowbrew/internal/adapters/persistence/markdownstore"
	"github.com/siro33950/knowbrew/internal/application/agent"
	"github.com/siro33950/knowbrew/internal/application/diagnostic"
	"github.com/siro33950/knowbrew/internal/domain"
)

func TestSubmitRejectsDuplicateStatementInSameTurn(t *testing.T) {
	dataStore := newBrewStore(t, "knowbrew")
	feedstock := writePendingFeedstock(t, dataStore, "fs-duplicate", "knowbrew")
	invocation := newMemoryInvocation(feedstock.ID)
	if _, err := Catalog(repositoryForTest(dataStore), invocation, "knowbrew", nil); err != nil {
		t.Fatal(err)
	}
	first := newCandidate("The Brew statement is independently identifiable.")
	if _, err := Submit(repositoryForTest(dataStore), invocation, SubmitInput{
		FeedstockID: feedstock.ID, Knowledge: first,
	}); err != nil {
		t.Fatal(err)
	}
	second := newCandidate("  the brew statement is independently identifiable.  ")
	if _, err := Submit(repositoryForTest(dataStore), invocation, SubmitInput{
		FeedstockID: feedstock.ID, Knowledge: second,
	}); err == nil || !strings.Contains(err.Error(), "duplicates a submitted statement") {
		t.Fatalf("error = %v", err)
	}
}

func TestSubmitIgnoresMissingConflictTargetInSubmittedState(t *testing.T) {
	dataStore := newBrewStore(t, "knowbrew")
	feedstock := writePendingFeedstock(t, dataStore, "fs-corrupt-submitted", "knowbrew")
	writeKnowledge(t, dataStore, "kn-existing", feedstock, "knowbrew", "Existing statement.")
	invocation := newMemoryInvocation(feedstock.ID)
	if _, err := Catalog(repositoryForTest(dataStore), invocation, "knowbrew", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := Show(repositoryForTest(dataStore), invocation, []string{"kn-existing"}); err != nil {
		t.Fatal(err)
	}
	invocation.state.Submitted = []domain.KnowledgeCandidate{{
		Resolution: domain.Resolution{Kind: domain.ResolutionConflicts},
	}}
	candidate := newCandidate("A conflicting statement.")
	candidate.Resolution = domain.Resolution{
		Kind: domain.ResolutionConflicts, KnowledgeIDs: []string{"kn-existing"},
	}
	result, err := Submit(repositoryForTest(dataStore), invocation, SubmitInput{
		FeedstockID: feedstock.ID,
		Knowledge:   candidate,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Submitted != 2 {
		t.Fatalf("result = %#v", result)
	}
}

func TestSubmitRejectsAnnotateInvocation(t *testing.T) {
	dataStore := newBrewStore(t, "knowbrew")
	feedstock := writePendingFeedstock(t, dataStore, "fs-annotate-invocation", "knowbrew")
	t.Setenv(config.InvocationIDEnvironment, "annotate-invocation")
	t.Setenv(config.InvocationFeedstockEnvironment, feedstock.ID)
	t.Setenv(config.InvocationTaskEnvironment, string(agent.TaskDraw))
	_, err := Submit(repositoryForTest(dataStore), invocationForTest(dataStore), SubmitInput{
		FeedstockID: feedstock.ID,
		Knowledge:   newCandidate("Annotate must not submit Knowledge."),
	})
	if err == nil || !strings.Contains(err.Error(), "only inside a Brew invocation") {
		t.Fatalf("error = %v", err)
	}
}

func TestCatalogCanRunRepeatedlyPerSubjectAndDetectsStaleDigest(t *testing.T) {
	dataStore := newBrewStore(t, "knowbrew", "other")
	feedstock := writePendingFeedstock(t, dataStore, "fs-catalog", "knowbrew")
	t.Setenv(config.InvocationIDEnvironment, "inv-catalog")
	t.Setenv(config.InvocationFeedstockEnvironment, feedstock.ID)
	t.Setenv(config.InvocationTaskEnvironment, string(agent.TaskBrew))
	guard := invocationForTest(dataStore)
	for _, subject := range []string{"knowbrew", "other", "knowbrew"} {
		if _, err := Catalog(repositoryForTest(dataStore), guard, subject, nil); err != nil {
			t.Fatal(err)
		}
	}
	state, err := guard.ReadState()
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Subjects) != 2 || state.Subjects["knowbrew"].Digest == "" || state.Subjects["other"].Digest == "" {
		t.Fatalf("state = %#v", state)
	}
	writeKnowledge(t, dataStore, "kn-new-catalog-entry", feedstock, "knowbrew", "A new catalog entry.")
	_, err = Submit(repositoryForTest(dataStore), guard, SubmitInput{
		FeedstockID: feedstock.ID,
		Knowledge:   newCandidate("Another independent statement."),
	})
	if !errors.Is(err, ErrStaleDecision) {
		t.Fatalf("error = %v", err)
	}
}

func TestSubmitRejectsResolutionTargetThatWasNotShown(t *testing.T) {
	dataStore := newBrewStore(t, "knowbrew")
	feedstock := writePendingFeedstock(t, dataStore, "fs-unshown", "knowbrew")
	writeKnowledge(t, dataStore, "kn-existing", feedstock, "knowbrew", "Existing statement.")
	invocation := newMemoryInvocation(feedstock.ID)
	if _, err := Catalog(repositoryForTest(dataStore), invocation, "knowbrew", nil); err != nil {
		t.Fatal(err)
	}
	candidate := newCandidate("Existing statement.")
	candidate.Resolution = domain.Resolution{
		Kind: domain.ResolutionEquivalent, KnowledgeIDs: []string{"kn-existing"},
	}
	_, err := Submit(repositoryForTest(dataStore), invocation, SubmitInput{
		FeedstockID: feedstock.ID, Knowledge: candidate,
	})
	if err == nil || !strings.Contains(err.Error(), "current inspected Knowledge head") {
		t.Fatalf("error = %v", err)
	}
}

func TestShowRejectsUncatalogedKnowledgeBeforeReading(t *testing.T) {
	dataStore := newBrewStore(t, "knowbrew")
	repository := &trackingRepository{Repository: repositoryForTest(dataStore)}
	_, err := Show(repository, newMemoryInvocation("fs-show"), []string{"kn-not-cataloged"})
	if err == nil || !strings.Contains(err.Error(), "not present in an invocation catalog") {
		t.Fatalf("error = %v", err)
	}
	if repository.findKnowledgeCalls != 0 {
		t.Fatalf("FindKnowledge calls = %d", repository.findKnowledgeCalls)
	}
}

func TestApplyCreatesMultipleKnowledgeAndBrewsFeedstockAtomically(t *testing.T) {
	dataStore := newBrewStore(t, "knowbrew")
	feedstock := writePendingFeedstock(t, dataStore, "fs-multiple", "knowbrew")
	invocation := newMemoryInvocation(feedstock.ID)
	if _, err := Catalog(repositoryForTest(dataStore), invocation, "knowbrew", nil); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		"First independently maintainable statement.",
		"Second independently maintainable statement.",
	} {
		if _, err := Submit(repositoryForTest(dataStore), invocation, SubmitInput{
			FeedstockID: feedstock.ID, Knowledge: newCandidate(statement),
		}); err != nil {
			t.Fatal(err)
		}
	}
	state, err := invocation.ReadState()
	if err != nil {
		t.Fatal(err)
	}
	result, err := Apply(context.Background(), repositoryForTest(dataStore), feedstock.ID, state)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Resolutions) != 2 {
		t.Fatalf("result = %#v", result)
	}
	files, warnings, err := dataStore.ListAllKnowledge()
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 0 || len(files) != 2 {
		t.Fatalf("files = %#v, warnings = %#v", files, warnings)
	}
	for _, file := range files {
		if _, err := uuid.Parse(strings.TrimPrefix(file.Knowledge.ID, "kn-")); err != nil {
			t.Fatalf("knowledge ID %q is not UUID-backed: %v", file.Knowledge.ID, err)
		}
		if len(file.Knowledge.Feedstocks) != 1 || file.Knowledge.Feedstocks[0] != feedstock.ID {
			t.Fatalf("knowledge = %#v", file.Knowledge)
		}
	}
	stored, _, err := dataStore.FindFeedstock(feedstock.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.BrewedAt == nil {
		t.Fatal("feedstock was not brewed")
	}
}

func TestApplyRollsBackWholeTurnWhenOneCandidateFails(t *testing.T) {
	dataStore := newBrewStore(t, "knowbrew")
	feedstock := writePendingFeedstock(t, dataStore, "fs-rollback", "knowbrew")
	writeKnowledge(t, dataStore, "kn-same-source", feedstock, "knowbrew", "Original statement.")
	invocation := newMemoryInvocation(feedstock.ID)
	if _, err := Catalog(repositoryForTest(dataStore), invocation, "knowbrew", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := Show(repositoryForTest(dataStore), invocation, []string{"kn-same-source"}); err != nil {
		t.Fatal(err)
	}
	if _, err := Submit(repositoryForTest(dataStore), invocation, SubmitInput{
		FeedstockID: feedstock.ID, Knowledge: newCandidate("A valid new statement."),
	}); err != nil {
		t.Fatal(err)
	}
	conflict := newCandidate("A conflicting statement from the same turn.")
	conflict.Resolution = domain.Resolution{
		Kind: domain.ResolutionConflicts, KnowledgeIDs: []string{"kn-same-source"},
	}
	if _, err := Submit(repositoryForTest(dataStore), invocation, SubmitInput{
		FeedstockID: feedstock.ID, Knowledge: conflict,
	}); err != nil {
		t.Fatal(err)
	}
	state, err := invocation.ReadState()
	if err != nil {
		t.Fatal(err)
	}
	_, err = Apply(context.Background(), repositoryForTest(dataStore), feedstock.ID, state)
	if err == nil || !strings.Contains(err.Error(), "shares source feedstock") {
		t.Fatalf("error = %v", err)
	}
	files, _, err := dataStore.ListAllKnowledge()
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files[0].Knowledge.ID != "kn-same-source" {
		t.Fatalf("files = %#v", files)
	}
	stored, _, err := dataStore.FindFeedstock(feedstock.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.BrewedAt != nil {
		t.Fatalf("feedstock was partially brewed: %#v", stored)
	}
}

func TestApplyWithNoSubmittedCandidatesMarksFeedstockBrewed(t *testing.T) {
	dataStore := newBrewStore(t, "knowbrew")
	feedstock := writePendingFeedstock(t, dataStore, "fs-empty-result", "knowbrew")
	if _, err := Apply(
		context.Background(), repositoryForTest(dataStore), feedstock.ID, agent.ReadState{},
	); err != nil {
		t.Fatal(err)
	}
	stored, _, err := dataStore.FindFeedstock(feedstock.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.BrewedAt == nil {
		t.Fatal("feedstock was not marked brewed")
	}
}

func TestRunRejectsRegisteredCountMismatchWithoutBrewingFeedstock(t *testing.T) {
	dataStore := newBrewStore(t, "knowbrew")
	feedstock := writePendingFeedstock(t, dataStore, "fs-count-mismatch", "knowbrew")
	repository := repositoryForTest(dataStore)
	service := Service{
		Repository: repository,
		Lifecycle:  repository,
		Dialogue:   dialogueMap{},
		Runner: fixedRunner{result: agent.RunResult{
			Output: json.RawMessage(`{"registered":0}`),
			Reads: agent.ReadState{Submitted: []domain.KnowledgeCandidate{
				newCandidate("A candidate was registered."),
			}},
		}},
		RunLock: immediateRunLock{},
	}
	summary, err := service.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if summary.FeedstocksFailed != 1 || summary.FeedstocksProcessed != 0 ||
		summary.FeedstocksPending != 1 || len(summary.Failures) != 1 ||
		!strings.Contains(summary.Failures[0].Reason, "reported 0 registered candidates") {
		t.Fatalf("summary = %#v", summary)
	}
	stored, _, err := dataStore.FindFeedstock(feedstock.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.BrewedAt != nil {
		t.Fatalf("feedstock was brewed: %#v", stored)
	}
}

func TestRunAcceptsRegisteredZeroAndBrewsFeedstock(t *testing.T) {
	dataStore := newBrewStore(t, "knowbrew")
	feedstock := writePendingFeedstock(t, dataStore, "fs-count-zero", "knowbrew")
	repository := repositoryForTest(dataStore)
	service := Service{
		Repository: repository,
		Lifecycle:  repository,
		Dialogue:   dialogueMap{},
		Runner: fixedRunner{result: agent.RunResult{
			Output: json.RawMessage(`{"registered":0}`),
		}},
		RunLock: immediateRunLock{},
	}
	summary, err := service.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if summary.FeedstocksFailed != 0 || summary.FeedstocksProcessed != 1 ||
		summary.FeedstocksPending != 0 {
		t.Fatalf("summary = %#v", summary)
	}
	stored, _, err := dataStore.FindFeedstock(feedstock.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.BrewedAt == nil {
		t.Fatal("feedstock was not brewed")
	}
}

func TestValidateSubmittedCandidatesCachesVocabularyAndCatalogBySubject(t *testing.T) {
	dataStore := newBrewStore(t, "knowbrew", "other")
	base := repositoryForTest(dataStore)
	_, knowbrewDigest, err := catalogSnapshot(base, "knowbrew")
	if err != nil {
		t.Fatal(err)
	}
	_, otherDigest, err := catalogSnapshot(base, "other")
	if err != nil {
		t.Fatal(err)
	}
	repository := &trackingRepository{Repository: base}
	state := agent.ReadState{
		Subjects: map[string]agent.SubjectReadState{
			"knowbrew": {Digest: knowbrewDigest},
			"other":    {Digest: otherDigest},
		},
		Submitted: []domain.KnowledgeCandidate{
			newCandidate("First knowbrew statement."),
			newCandidate("Second knowbrew statement."),
			newCandidateForSubject("other", "First other statement."),
			newCandidateForSubject("other", "Second other statement."),
		},
	}
	if err := validateSubmittedCandidates(repository, state); err != nil {
		t.Fatal(err)
	}
	if repository.loadMastersCalls != 2 {
		t.Fatalf("LoadMasters calls = %d, want 2", repository.loadMastersCalls)
	}
	if repository.listKnowledgeCalls != 2 {
		t.Fatalf("ListKnowledge calls = %d, want 2", repository.listKnowledgeCalls)
	}
}

func TestFeedstockPromptContainsTurnMastersAndNoDrawTypeCandidates(t *testing.T) {
	dataStore := newBrewStore(t, "knowbrew")
	target := writePendingFeedstock(t, dataStore, "fs-prompt", "knowbrew")
	target.CWD = "/workspace/knowbrew"
	target.Repo = "https://github.com/example/knowbrew.git"
	reader := dialogueMap{target.ID: {
		{Role: "user", Content: "Use UUID-backed Knowledge IDs."},
		{Role: "assistant", Content: "Implemented UUID-backed IDs."},
	}}
	prompt, warnings, err := feedstockPrompt(
		repositoryForTest(dataStore), reader, Settings{}, []domain.Feedstock{target}, target, "WRITING RULE",
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 0 {
		t.Fatalf("warnings = %#v", warnings)
	}
	for _, required := range []string{
		`"feedstock_id": "fs-prompt"`, `"summary":`, `"target_dialogue":`,
		`"role": "user"`, `"role": "assistant"`, `"subject_master":`,
		`"knowledge_type_master":`, `"cwd": "/workspace/knowbrew"`,
		`"repo": "https://github.com/example/knowbrew.git"`, "WRITING RULE",
		"statement that identifies its subject matter without the source dialogue",
		`return {"registered": N}`, "run catalog again",
	} {
		if !strings.Contains(prompt, required) {
			t.Fatalf("prompt does not contain %q:\n%s", required, prompt)
		}
	}
	if strings.Contains(prompt, `"types": [`) {
		t.Fatalf("prompt contains Draw type candidates:\n%s", prompt)
	}
}

func TestCollectPendingFeedstocksSkipsEmptyTypes(t *testing.T) {
	now := time.Now().UTC()
	annotated := now.Add(time.Minute)
	withTypes := domain.Feedstock{
		Schema: domain.SchemaVersion, ID: "fs-with", TurnID: "turn-with",
		Session: domain.SessionRef{ID: "session"}, Timestamp: now, Agent: "codex",
		Summary: "summary", Types: []domain.KnowledgeType{"property"}, AnnotatedAt: &annotated,
	}
	withoutTypes := withTypes
	withoutTypes.ID = "fs-without"
	withoutTypes.TurnID = "turn-without"
	withoutTypes.Types = nil
	pending := collectPendingFeedstocks([]domain.Feedstock{withoutTypes, withTypes})
	if len(pending) != 1 || pending[0].ID != withTypes.ID {
		t.Fatalf("pending = %#v", pending)
	}
}

type memoryInvocation struct {
	feedstockID string
	state       agent.ReadState
}

func newMemoryInvocation(feedstockID string) *memoryInvocation {
	return &memoryInvocation{
		feedstockID: feedstockID,
		state:       agent.ReadState{Subjects: make(map[string]agent.SubjectReadState)},
	}
}

func (invocation *memoryInvocation) ValidateFeedstock(id string) error {
	if id != invocation.feedstockID {
		return errors.New("wrong feedstock")
	}
	return nil
}

func (*memoryInvocation) IsBrewInvocation() bool { return true }

func (invocation *memoryInvocation) RecordCatalog(subject string, ids []string, digest string) error {
	previous := invocation.state.Subjects[subject]
	invocation.state.Subjects[subject] = agent.SubjectReadState{
		Catalog: domain.UniqueSorted(append(previous.Catalog, ids...)), Digest: digest,
	}
	return nil
}

func (invocation *memoryInvocation) RecordInspected(ids []string) error {
	invocation.state.Inspected = domain.UniqueSorted(append(invocation.state.Inspected, ids...))
	return nil
}

func (invocation *memoryInvocation) RecordSubmitted(candidate domain.KnowledgeCandidate) error {
	invocation.state.Submitted = append(invocation.state.Submitted, candidate)
	return nil
}

func (invocation *memoryInvocation) ReadState() (agent.ReadState, error) {
	state := invocation.state
	state.Subjects = make(map[string]agent.SubjectReadState, len(invocation.state.Subjects))
	for subject, entry := range invocation.state.Subjects {
		state.Subjects[subject] = agent.SubjectReadState{
			Catalog: append([]string(nil), entry.Catalog...), Digest: entry.Digest,
		}
	}
	state.Inspected = append([]string(nil), invocation.state.Inspected...)
	state.Submitted = append([]domain.KnowledgeCandidate(nil), invocation.state.Submitted...)
	return state, nil
}

type dialogueMap map[string][]domain.DialogueMessage

func (reader dialogueMap) Read(id string) ([]domain.DialogueMessage, error) {
	return append([]domain.DialogueMessage(nil), reader[id]...), nil
}

func newBrewStore(t *testing.T, subjects ...string) *store.Store {
	t.Helper()
	dataStore, err := store.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := dataStore.EnsureLayout(); err != nil {
		t.Fatal(err)
	}
	for _, subject := range subjects {
		if _, err := dataStore.EnsureMaster("subjects", domain.MasterEntry{
			Name: subject, Definition: "Knowledge owned by " + subject + ".",
		}); err != nil {
			t.Fatal(err)
		}
	}
	return dataStore
}

func writePendingFeedstock(t *testing.T, dataStore *store.Store, id, subject string) domain.Feedstock {
	t.Helper()
	now := time.Now().UTC()
	annotated := now.Add(time.Minute)
	feedstock := domain.Feedstock{
		Schema: domain.SchemaVersion, ID: id, TurnID: "turn-" + id,
		Session: domain.SessionRef{ID: "session"}, Timestamp: now, Agent: "codex",
		Summary: "A durable statement was established for " + subject + ".",
		Types:   []domain.KnowledgeType{"property"}, AnnotatedAt: &annotated,
	}
	if err := dataStore.WriteFeedstock(feedstock); err != nil {
		t.Fatal(err)
	}
	return feedstock
}

func writeKnowledge(
	t *testing.T,
	dataStore *store.Store,
	id string,
	feedstock domain.Feedstock,
	subject,
	statement string,
) {
	t.Helper()
	body, err := knowledgefmt.Encode(statement, "")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	knowledge := domain.Knowledge{
		ID: id, Created: now, Updated: now, EstablishedBy: feedstock.ID,
		Type: "property", Subject: subject, Feedstocks: []string{feedstock.ID},
	}
	if err := dataStore.WriteNewKnowledge(id, knowledge, body); err != nil {
		t.Fatal(err)
	}
}

func newCandidate(statement string) domain.KnowledgeCandidate {
	return newCandidateForSubject("knowbrew", statement)
}

func newCandidateForSubject(subject, statement string) domain.KnowledgeCandidate {
	return domain.KnowledgeCandidate{
		Type: "property", Subject: subject, Statement: statement,
		Resolution: domain.Resolution{Kind: domain.ResolutionNew},
	}
}

type fixedRunner struct {
	result agent.RunResult
}

func (runner fixedRunner) Run(context.Context, agent.Task, string, string) (agent.RunResult, error) {
	return runner.result, nil
}

type immediateRunLock struct{}

func (immediateRunLock) Lock(context.Context) (func() error, error) {
	return func() error { return nil }, nil
}

type trackingRepository struct {
	Repository
	findKnowledgeCalls int
	loadMastersCalls   int
	listKnowledgeCalls int
}

func (repository *trackingRepository) FindKnowledge(id string) (KnowledgeDocument, error) {
	repository.findKnowledgeCalls++
	return repository.Repository.FindKnowledge(id)
}

func (repository *trackingRepository) LoadMasters(
	kind string,
) ([]domain.MasterEntry, []diagnostic.Warning, error) {
	repository.loadMastersCalls++
	return repository.Repository.LoadMasters(kind)
}

func (repository *trackingRepository) ListKnowledge() (
	[]KnowledgeDocument,
	[]diagnostic.Warning,
	error,
) {
	repository.listKnowledgeCalls++
	return repository.Repository.ListKnowledge()
}

var _ Invocation = (*memoryInvocation)(nil)
var _ DialogueReader = dialogueMap{}
var _ agent.Runner = fixedRunner{}
var _ RunLock = immediateRunLock{}
var _ Repository = (*trackingRepository)(nil)
