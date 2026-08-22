package brew

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/siro33950/knowbrew/internal/adapters/llm"
	"github.com/siro33950/knowbrew/internal/adapters/persistence/markdownstore"
	"github.com/siro33950/knowbrew/internal/adapters/runlock"
	"github.com/siro33950/knowbrew/internal/application/diagnostic"
	"github.com/siro33950/knowbrew/internal/application/storage"
	"github.com/siro33950/knowbrew/internal/domain"
)

type subjectRunner struct {
	mu      sync.Mutex
	outputs map[string]json.RawMessage
	prompts map[string]string
	calls   int
	active  atomic.Int32
	maximum atomic.Int32
	barrier chan struct{}
}

type recordingSearchIndex struct {
	calls int
	err   error
}

func (index *recordingSearchIndex) Sync(context.Context) ([]diagnostic.Warning, error) {
	index.calls++
	return nil, index.err
}

func (runner *subjectRunner) Run(
	_ context.Context,
	task llm.Task,
	subject,
	prompt string,
) (llm.RunResult, error) {
	if task != llm.TaskBrew {
		return llm.RunResult{}, fmt.Errorf("task = %s", task)
	}
	current := runner.active.Add(1)
	defer runner.active.Add(-1)
	for {
		maximum := runner.maximum.Load()
		if current <= maximum || runner.maximum.CompareAndSwap(maximum, current) {
			break
		}
	}
	if runner.barrier != nil {
		<-runner.barrier
	}
	runner.mu.Lock()
	defer runner.mu.Unlock()
	runner.calls++
	if runner.prompts == nil {
		runner.prompts = make(map[string]string)
	}
	runner.prompts[subject] = prompt
	return llm.RunResult{Output: runner.outputs[subject]}, nil
}

func TestB012SubjectlessKnowledgeIsNotSelected(t *testing.T) {
	documents := []KnowledgeDocument{
		{Knowledge: domain.Knowledge{ID: "kn-subjectless"}},
		{Knowledge: domain.Knowledge{ID: "kn-subject", Subject: "knowbrew"}},
	}
	if got := pendingSubjects(documents); len(got) != 1 || got[0] != "knowbrew" {
		t.Fatalf("subjects = %#v", got)
	}
}

func TestBrewSynchronizesSearchIndexAfterCompletionAndWarnsOnFailure(t *testing.T) {
	_, repository := newBrewStore(t, t.TempDir(), "knowbrew")
	index := &recordingSearchIndex{err: errors.New("index unavailable")}
	service := Service{Repository: repository, SearchIndex: index}

	summary, err := service.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if index.calls != 1 {
		t.Fatalf("search index sync calls = %d, want 1", index.calls)
	}
	if len(summary.Warnings) != 1 ||
		!strings.Contains(summary.Warnings[0].Message, "index unavailable") {
		t.Fatalf("summary warnings = %#v", summary.Warnings)
	}
}

func TestB018B026B024BrewUsesStoredKnowledgeAndReportsChangedSubject(t *testing.T) {
	root := t.TempDir()
	dataStore, repository := newBrewStore(t, root, "knowbrew")
	input := seedKnowledge(t, dataStore, repository, "kn-input", "knowbrew", "Draft fact.", time.Now().UTC(), false)
	runner := &subjectRunner{outputs: map[string]json.RawMessage{
		"knowbrew": json.RawMessage(fmt.Sprintf(
			`{"actions":[{"knowledge_id":%q,"resolution":{"kind":"new","knowledge_ids":[],"draft":null}}]}`,
			input.Knowledge.ID,
		)),
	}}
	service := Service{
		Settings: Settings{Concurrency: 2}, Repository: repository, Runner: runner,
		Claimer: runlock.FileClaimer{Root: root, Namespace: "subject-claims"},
	}
	summary, err := service.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(summary.ChangedSubjects) != 1 || summary.ChangedSubjects[0] != "knowbrew" {
		t.Fatalf("summary = %#v", summary)
	}
	file, err := dataStore.FindKnowledge(input.Knowledge.ID)
	if err != nil {
		t.Fatal(err)
	}
	if file.Knowledge.OrganizedAt == nil {
		t.Fatalf("knowledge = %#v", file.Knowledge)
	}
	second, err := service.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if second.SubjectsSelected != 0 || runner.calls != 1 {
		t.Fatalf("second = %#v, calls = %d", second, runner.calls)
	}
}

func TestB020BrewPromptContainsEveryOrganizedHead(t *testing.T) {
	root := t.TempDir()
	dataStore, repository := newBrewStore(t, root, "knowbrew")
	base := time.Now().UTC().Add(-time.Hour)
	for index := range 36 {
		seedKnowledge(
			t, dataStore, repository, fmt.Sprintf("kn-head-%02d", index), "knowbrew",
			fmt.Sprintf("Existing fact %02d.", index), base.Add(time.Duration(index)*time.Minute), true,
		)
	}
	input := seedKnowledge(t, dataStore, repository, "kn-input", "knowbrew", "Equivalent fact.", time.Now().UTC(), false)
	target := "kn-head-35"
	runner := &subjectRunner{outputs: map[string]json.RawMessage{
		"knowbrew": json.RawMessage(fmt.Sprintf(
			`{"actions":[{"knowledge_id":%q,"resolution":{"kind":"equivalent","knowledge_ids":[%q],"draft":null}}]}`,
			input.Knowledge.ID, target,
		)),
	}}
	service := Service{Repository: repository, Runner: runner}
	if _, err := service.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	prompt := runner.prompts["knowbrew"]
	for index := range 36 {
		id := fmt.Sprintf("kn-head-%02d", index)
		if !strings.Contains(prompt, id) {
			t.Fatalf("prompt is missing %s", id)
		}
	}
	if _, err := dataStore.FindKnowledge(input.Knowledge.ID); err == nil {
		t.Fatal("equivalent input was not consumed")
	}
	file, err := dataStore.FindKnowledge(target)
	if err != nil {
		t.Fatal(err)
	}
	if len(file.Knowledge.Feedstocks) != 2 {
		t.Fatalf("target evidence = %#v", file.Knowledge.Feedstocks)
	}
}

func TestB021B022SubjectsRunConcurrentlyAndRemainIsolated(t *testing.T) {
	root := t.TempDir()
	dataStore, repository := newBrewStore(t, root, "alpha", "beta")
	alpha := seedKnowledge(t, dataStore, repository, "kn-alpha", "alpha", "Alpha fact.", time.Now().UTC(), false)
	beta := seedKnowledge(t, dataStore, repository, "kn-beta", "beta", "Beta fact.", time.Now().UTC(), false)
	barrier := make(chan struct{})
	runner := &subjectRunner{
		barrier: barrier,
		outputs: map[string]json.RawMessage{
			"alpha": newActionOutput(alpha.Knowledge.ID),
			"beta":  newActionOutput(beta.Knowledge.ID),
		},
	}
	service := Service{
		Settings: Settings{Concurrency: 2}, Repository: repository, Runner: runner,
		Claimer: runlock.FileClaimer{Root: root, Namespace: "subject-claims"},
	}
	done := make(chan error, 1)
	go func() {
		_, err := service.Run(context.Background())
		done <- err
	}()
	deadline := time.After(5 * time.Second)
	for runner.maximum.Load() < 2 {
		select {
		case <-deadline:
			t.Fatal("subjects did not run concurrently")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	close(barrier)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{alpha.Knowledge.ID, beta.Knowledge.ID} {
		file, err := dataStore.FindKnowledge(id)
		if err != nil {
			t.Fatal(err)
		}
		if file.Knowledge.OrganizedAt == nil {
			t.Fatalf("%s was not organized", id)
		}
	}
}

func TestB025DiscardDeletesInputAtomically(t *testing.T) {
	root := t.TempDir()
	dataStore, repository := newBrewStore(t, root, "knowbrew")
	input := seedKnowledge(t, dataStore, repository, "kn-discard", "knowbrew", "Temporary noise.", time.Now().UTC(), false)
	runner := &subjectRunner{outputs: map[string]json.RawMessage{
		"knowbrew": json.RawMessage(fmt.Sprintf(
			`{"actions":[{"knowledge_id":%q,"resolution":{"kind":"discard","knowledge_ids":[],"draft":null}}]}`,
			input.Knowledge.ID,
		)),
	}}
	service := Service{Repository: repository, Runner: runner}

	summary, err := service.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if summary.SubjectsProcessed != 1 || len(summary.ChangedSubjects) != 0 {
		t.Fatalf("summary = %#v", summary)
	}
	if _, err := dataStore.FindKnowledge(input.Knowledge.ID); err == nil {
		t.Fatal("discarded input still exists")
	}
}

func TestB027InvalidOrganizationPlanCanRetryOnlyUnfinishedInputs(t *testing.T) {
	root := t.TempDir()
	dataStore, repository := newBrewStore(t, root, "knowbrew")
	first := seedKnowledge(t, dataStore, repository, "kn-first", "knowbrew", "First fact.", time.Now().UTC(), false)
	second := seedKnowledge(t, dataStore, repository, "kn-second", "knowbrew", "Second fact.", time.Now().UTC().Add(time.Minute), false)
	snapshot, _, err := loadSubjectSnapshot(repository, newFeedstockCache(), "knowbrew")
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = ApplyOrganization(
		context.Background(), repository, newFeedstockCache(), snapshot, []domain.OrganizationAction{{
			KnowledgeID: first.Knowledge.ID,
			Resolution:  domain.Resolution{Kind: domain.ResolutionDiscard},
		}})
	if err == nil {
		t.Fatal("invalid plan succeeded")
	}
	for _, id := range []string{first.Knowledge.ID, second.Knowledge.ID} {
		if _, err := dataStore.FindKnowledge(id); err != nil {
			t.Fatalf("%s was partially consumed: %v", id, err)
		}
	}
	changed, _, err := ApplyOrganization(
		context.Background(), repository, newFeedstockCache(), snapshot, []domain.OrganizationAction{
			{KnowledgeID: first.Knowledge.ID, Resolution: domain.Resolution{Kind: domain.ResolutionNew}},
			{KnowledgeID: second.Knowledge.ID, Resolution: domain.Resolution{Kind: domain.ResolutionNew}},
		})
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("retry did not organize the unfinished inputs")
	}
	for _, id := range []string{first.Knowledge.ID, second.Knowledge.ID} {
		file, err := dataStore.FindKnowledge(id)
		if err != nil || file.Knowledge.OrganizedAt == nil {
			t.Fatalf("%s after retry = %#v, error = %v", id, file.Knowledge, err)
		}
	}
}

func TestB023BrewProcessesOnlyNewInputs(t *testing.T) {
	root := t.TempDir()
	dataStore, repository := newBrewStore(t, root, "knowbrew")
	first := seedKnowledge(t, dataStore, repository, "kn-first", "knowbrew", "First fact.", time.Now().UTC(), false)
	runner := &subjectRunner{outputs: map[string]json.RawMessage{"knowbrew": newActionOutput(first.Knowledge.ID)}}
	service := Service{Repository: repository, Runner: runner}
	if _, err := service.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	firstFile, err := dataStore.FindKnowledge(first.Knowledge.ID)
	if err != nil {
		t.Fatal(err)
	}
	firstUpdated := firstFile.Knowledge.Updated
	second := seedKnowledge(
		t, dataStore, repository, "kn-second", "knowbrew", "Second fact.",
		time.Now().UTC().Add(time.Minute), false,
	)
	runner.outputs["knowbrew"] = newActionOutput(second.Knowledge.ID)
	if _, err := service.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	firstFile, err = dataStore.FindKnowledge(first.Knowledge.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !firstFile.Knowledge.Updated.Equal(firstUpdated) {
		t.Fatalf("already organized Knowledge changed: %s -> %s", firstUpdated, firstFile.Knowledge.Updated)
	}
	secondFile, err := dataStore.FindKnowledge(second.Knowledge.ID)
	if err != nil || secondFile.Knowledge.OrganizedAt == nil {
		t.Fatalf("second Knowledge = %#v, error = %v", secondFile.Knowledge, err)
	}
}

func TestB023B027OrganizationLeavesInputsAddedAfterSnapshotForNextRun(t *testing.T) {
	root := t.TempDir()
	dataStore, repository := newBrewStore(t, root, "knowbrew")
	first := seedKnowledge(
		t, dataStore, repository, "kn-first", "knowbrew", "First fact.",
		time.Now().UTC(), false,
	)
	snapshot, _, err := loadSubjectSnapshot(repository, newFeedstockCache(), "knowbrew")
	if err != nil {
		t.Fatal(err)
	}
	second := seedKnowledge(
		t, dataStore, repository, "kn-second", "knowbrew", "Second fact.",
		time.Now().UTC().Add(time.Minute), false,
	)
	if _, _, err := ApplyOrganization(
		context.Background(), repository, newFeedstockCache(), snapshot, []domain.OrganizationAction{{
			KnowledgeID: first.Knowledge.ID,
			Resolution:  domain.Resolution{Kind: domain.ResolutionNew},
		}}); err != nil {
		t.Fatal(err)
	}
	firstFile, err := dataStore.FindKnowledge(first.Knowledge.ID)
	if err != nil || firstFile.Knowledge.OrganizedAt == nil {
		t.Fatalf("first Knowledge = %#v, error = %v", firstFile.Knowledge, err)
	}
	secondFile, err := dataStore.FindKnowledge(second.Knowledge.ID)
	if err != nil || secondFile.Knowledge.OrganizedAt != nil {
		t.Fatalf("new input = %#v, error = %v", secondFile.Knowledge, err)
	}
}

func newBrewStore(t *testing.T, root string, subjects ...string) (*store.Store, Repository) {
	t.Helper()
	dataStore, err := store.New(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, subject := range subjects {
		if _, err := dataStore.EnsureMaster("subjects", domain.MasterEntry{Name: subject}); err != nil {
			t.Fatal(err)
		}
	}
	return dataStore, repositoryForTest(dataStore)
}

func seedKnowledge(
	t *testing.T,
	dataStore *store.Store,
	repository Repository,
	id,
	subject,
	statement string,
	timestamp time.Time,
	organized bool,
) domain.KnowledgeRecord {
	t.Helper()
	draftedAt := timestamp
	extractedAt := timestamp
	feedstock := domain.Feedstock{
		Schema: domain.SchemaVersion, ID: "fs-" + id, TurnID: "turn-" + id,
		Session: domain.SessionRef{ID: "session-" + id}, Timestamp: timestamp,
		Agent: "codex", Types: []domain.KnowledgeType{"property"}, Summary: statement,
		DraftedAt: &draftedAt, ExtractedAt: &extractedAt,
	}
	if err := dataStore.WriteFeedstock(feedstock); err != nil {
		t.Fatal(err)
	}
	knowledge := domain.Knowledge{
		ID: id, Created: timestamp, Updated: timestamp, EstablishedBy: feedstock.ID,
		Type: "property", Subject: subject, Feedstocks: []string{feedstock.ID},
		Status: domain.StatusPending,
	}
	if organized {
		organizedAt := timestamp
		knowledge.OrganizedAt = &organizedAt
	}
	record := domain.KnowledgeRecord{
		Knowledge: knowledge, Statement: statement, Established: feedstock,
	}
	if err := repository.Transaction(context.Background(), func(transaction storage.Transaction) error {
		return transaction.StageKnowledge(record)
	}); err != nil {
		t.Fatal(err)
	}
	return record
}

func newActionOutput(id string) json.RawMessage {
	return json.RawMessage(fmt.Sprintf(
		`{"actions":[{"knowledge_id":%q,"resolution":{"kind":"new","knowledge_ids":[],"draft":null}}]}`,
		id,
	))
}
