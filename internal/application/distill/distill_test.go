package distill

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	progressui "github.com/siro33950/knowbrew/internal/adapters/progress"
	"github.com/siro33950/knowbrew/internal/application/agent"
	"github.com/siro33950/knowbrew/internal/application/diagnostic"
	"github.com/siro33950/knowbrew/internal/application/storage"
	"github.com/siro33950/knowbrew/internal/domain"
)

type fakeRepository struct {
	subjects   []domain.MasterEntry
	templates  []domain.DocumentTemplate
	knowledge  []storage.KnowledgeDocument
	documents  map[string]domain.DistilledDocument
	readErrors map[string]error
	writes     []domain.DistilledDocument
	deletes    []string
	guides     map[string]string
	guideError map[string]error
}

func (repository *fakeRepository) ReadWritingGuide(name string) (string, bool, error) {
	if err := repository.guideError[name]; err != nil {
		return "", false, err
	}
	content, exists := repository.guides[name]
	return content, exists, nil
}

func (repository *fakeRepository) EnsureLayout() error { return nil }

func (repository *fakeRepository) WithLock(_ context.Context, change func() error) error {
	return change()
}

func (repository *fakeRepository) LoadMasters(kind string) ([]domain.MasterEntry, []diagnostic.Warning, error) {
	if kind == "subjects" {
		return append([]domain.MasterEntry(nil), repository.subjects...), nil, nil
	}
	return nil, nil, nil
}

func (repository *fakeRepository) LoadTemplates() ([]domain.DocumentTemplate, []diagnostic.Warning, error) {
	return append([]domain.DocumentTemplate(nil), repository.templates...), nil, nil
}

func (repository *fakeRepository) ListKnowledge() ([]storage.KnowledgeDocument, []diagnostic.Warning, error) {
	return append([]storage.KnowledgeDocument(nil), repository.knowledge...), nil, nil
}

func (repository *fakeRepository) ListKnowledgeMetadata() (
	[]storage.KnowledgeMetadata,
	[]diagnostic.Warning,
	error,
) {
	metadata := make([]storage.KnowledgeMetadata, len(repository.knowledge))
	for index, document := range repository.knowledge {
		metadata[index] = storage.KnowledgeMetadata{Knowledge: document.Knowledge}
	}
	return metadata, nil, nil
}

func (repository *fakeRepository) Transaction(
	_ context.Context,
	change func(storage.Transaction) error,
) error {
	return change(fakeTransaction{})
}

func (repository *fakeRepository) ReadDistilledDocument(
	template domain.DocumentTemplate,
	subject string,
) (domain.DistilledDocument, bool, error) {
	document, exists := repository.documents[subject+"/"+template.Name]
	if err := repository.readErrors[subject+"/"+template.Name]; err != nil {
		return domain.DistilledDocument{}, false, err
	}
	return document, exists, nil
}

func (repository *fakeRepository) WriteDistilledDocument(
	template domain.DocumentTemplate,
	document domain.DistilledDocument,
) error {
	repository.writes = append(repository.writes, document)
	if repository.documents == nil {
		repository.documents = make(map[string]domain.DistilledDocument)
	}
	repository.documents[document.Subject+"/"+template.Name] = document
	return nil
}

func (repository *fakeRepository) DeleteDistilledDocument(
	template domain.DocumentTemplate,
	subject string,
) (bool, error) {
	key := subject + "/" + template.Name
	_, exists := repository.documents[key]
	delete(repository.documents, key)
	if exists {
		repository.deletes = append(repository.deletes, key)
	}
	return exists, nil
}

type fakeTransaction struct{}

func (fakeTransaction) StageKnowledge(domain.KnowledgeRecord) error               { return nil }
func (fakeTransaction) StageKnowledgeMetadata(domain.Knowledge) error             { return nil }
func (fakeTransaction) StageExtractedFeedstock(domain.Feedstock, time.Time) error { return nil }
func (fakeTransaction) DeleteKnowledge(string) error                              { return nil }

type fakeRunLock struct{}

func (fakeRunLock) Lock(context.Context) (func() error, error) {
	return func() error { return nil }, nil
}

type fakeCursor struct {
	position CursorPosition
	exists   bool
	saves    []CursorPosition
}

func (cursor *fakeCursor) Load() (CursorPosition, bool, error) {
	return cursor.position, cursor.exists, nil
}

func (cursor *fakeCursor) Save(position CursorPosition) error {
	cursor.position = position
	cursor.exists = true
	cursor.saves = append(cursor.saves, position)
	return nil
}

type runnerCall struct {
	task   agent.Task
	prompt string
}

type fakeRunner struct {
	calls   []runnerCall
	outputs map[agent.Task][]json.RawMessage
	usages  map[agent.Task][]agent.Usage
}

func (runner *fakeRunner) Run(
	_ context.Context,
	task agent.Task,
	_ string,
	prompt string,
) (agent.RunResult, error) {
	runner.calls = append(runner.calls, runnerCall{task: task, prompt: prompt})
	outputs := runner.outputs[task]
	output := outputs[0]
	runner.outputs[task] = outputs[1:]
	var usage agent.Usage
	if usages := runner.usages[task]; len(usages) > 0 {
		usage = usages[0]
		runner.usages[task] = usages[1:]
	}
	return agent.RunResult{Output: output, Usage: usage}, nil
}

func testTemplate() domain.DocumentTemplate {
	return domain.DocumentTemplate{
		Name: "concept", Description: "Concept.", Output: "concept.md",
		Purpose: "Explain the subject.", Covers: []string{"Purpose"},
		Structure: "# {{subject}}",
	}
}

func testKnowledge(id, subject, statement string, approved bool) storage.KnowledgeDocument {
	organizedAt := time.Now().UTC()
	knowledge := domain.Knowledge{
		ID: id, Subject: subject, Type: "property", Approved: approved,
		OrganizedAt: &organizedAt,
	}
	knowledge.Status = domain.EffectiveKnowledgeStatus(knowledge)
	return storage.KnowledgeDocument{Knowledge: knowledge, Statement: statement}
}

func TestB013B032RunUsesOnlyOrganizedApprovedCurrentKnowledge(t *testing.T) {
	template := testTemplate()
	rootID := "kn-0000000000000001"
	candidateID := "kn-0000000000000002"
	pendingID := "kn-0000000000000003"
	repository := &fakeRepository{
		subjects:  []domain.MasterEntry{{Name: "knowbrew", Documents: []string{"concept"}}},
		templates: []domain.DocumentTemplate{template},
		knowledge: []storage.KnowledgeDocument{
			testKnowledge(rootID, "knowbrew", "Existing evidence.", true),
			testKnowledge(candidateID, "knowbrew", "New evidence.", true),
			testKnowledge(pendingID, "knowbrew", "Pending evidence.", false),
			{Knowledge: domain.Knowledge{ID: "kn-subjectless", Type: "property"}, Statement: "Subjectless evidence."},
		},
		documents: map[string]domain.DistilledDocument{
			"knowbrew/concept": {
				Subject: "knowbrew", Template: "concept", KnowledgeIDs: []string{rootID},
				Body: "Old document body that selection must not receive.",
			},
		},
		guides: map[string]string{
			"common":    "COMMON WRITING RULE",
			"knowledge": "KNOWLEDGE WRITING RULE",
			"document":  "DOCUMENT WRITING RULE",
		},
	}
	runner := &fakeRunner{outputs: map[agent.Task][]json.RawMessage{
		agent.TaskDistillSelect:   {json.RawMessage(`{"knowledge_references":["K001"]}`)},
		agent.TaskDistillGenerate: {json.RawMessage(`{"body":"# knowbrew\n\nNew body.","knowledge_references":["K002"]}`)},
	}}
	service := Service{
		Settings:   Settings{Backend: "codex-cli", Model: "sol"},
		Repository: repository, Lifecycle: repository, Runner: runner, RunLock: fakeRunLock{},
	}
	summary, err := service.Run(context.Background(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(runner.calls) != 2 || runner.calls[0].task != agent.TaskDistillSelect ||
		runner.calls[1].task != agent.TaskDistillGenerate {
		t.Fatalf("runner calls = %#v", runner.calls)
	}
	selection := runner.calls[0].prompt
	if !strings.Contains(selection, `"reference": "K001"`) || strings.Contains(selection, candidateID) ||
		strings.Contains(selection, rootID) ||
		strings.Contains(selection, "Old document body") || strings.Contains(selection, pendingID) {
		t.Fatalf("selection prompt has wrong inputs:\n%s", selection)
	}
	for _, forbidden := range []string{
		"COMMON WRITING RULE", "KNOWLEDGE WRITING RULE", "DOCUMENT WRITING RULE",
	} {
		if strings.Contains(selection, forbidden) {
			t.Fatalf("selection prompt contains writing guide %q:\n%s", forbidden, selection)
		}
	}
	generation := runner.calls[1].prompt
	if !strings.Contains(generation, `"reference": "K001"`) ||
		!strings.Contains(generation, `"reference": "K002"`) ||
		strings.Contains(generation, rootID) || strings.Contains(generation, candidateID) ||
		strings.Contains(generation, pendingID) {
		t.Fatalf("generation prompt has wrong inputs:\n%s", generation)
	}
	for _, required := range []string{
		"COMMON WRITING RULE",
		"DOCUMENT WRITING RULE",
		"Combine related Knowledge into a coherent explanation instead of listing records individually.",
	} {
		if !strings.Contains(generation, required) {
			t.Fatalf("generation prompt does not contain %q:\n%s", required, generation)
		}
	}
	if strings.Contains(generation, "KNOWLEDGE WRITING RULE") {
		t.Fatalf("generation prompt contains knowledge-only writing rules:\n%s", generation)
	}
	if strings.Contains(generation, "Write in the language and style required by the user's configuration") {
		t.Fatalf("generation prompt retained externalized writing instructions:\n%s", generation)
	}
	if len(repository.writes) != 1 || len(repository.writes[0].KnowledgeIDs) != 1 ||
		repository.writes[0].KnowledgeIDs[0] != candidateID {
		t.Fatalf("writes = %#v", repository.writes)
	}
	if summary.DocumentsUpdated != 1 || summary.KnowledgeSelected != 1 || summary.KnowledgeUsed != 1 {
		t.Fatalf("summary = %#v", summary)
	}
}

func TestRunMaxContinuesFromCursorAcrossDocuments(t *testing.T) {
	concept := testTemplate()
	reference := testTemplate()
	reference.Name = "reference"
	reference.Output = "reference.md"
	repository := &fakeRepository{
		subjects: []domain.MasterEntry{{
			Name: "knowbrew", Documents: []string{"concept", "reference"},
		}},
		templates: []domain.DocumentTemplate{concept, reference},
		knowledge: []storage.KnowledgeDocument{
			testKnowledge("kn-0000000000000001", "knowbrew", "Evidence.", true),
		},
	}
	runner := &fakeRunner{outputs: map[agent.Task][]json.RawMessage{
		agent.TaskDistillSelect: {
			json.RawMessage(`{"knowledge_references":["K001"]}`),
			json.RawMessage(`{"knowledge_references":["K001"]}`),
		},
		agent.TaskDistillGenerate: {
			json.RawMessage(`{"body":"# Concept","knowledge_references":["K001"]}`),
			json.RawMessage(`{"body":"# Reference","knowledge_references":["K001"]}`),
		},
	}}
	cursor := &fakeCursor{}
	service := Service{
		Repository: repository, Lifecycle: repository, Runner: runner,
		RunLock: fakeRunLock{}, Cursor: cursor,
	}

	first, err := service.Run(context.Background(), Options{Max: 1})
	if err != nil {
		t.Fatal(err)
	}
	if first.DocumentsAvailable != 2 || first.DocumentsSelected != 1 ||
		first.DocumentsPlanned != 1 || len(repository.writes) != 1 ||
		repository.writes[0].Template != "concept" {
		t.Fatalf("first summary = %#v, writes = %#v", first, repository.writes)
	}
	if cursor.position != (CursorPosition{Subject: "knowbrew", Template: "concept"}) {
		t.Fatalf("first cursor = %#v", cursor.position)
	}

	second, err := service.Run(context.Background(), Options{Max: 1})
	if err != nil {
		t.Fatal(err)
	}
	if second.DocumentsAvailable != 2 || second.DocumentsSelected != 1 ||
		len(repository.writes) != 2 || repository.writes[1].Template != "reference" {
		t.Fatalf("second summary = %#v, writes = %#v", second, repository.writes)
	}
	if cursor.position != (CursorPosition{Subject: "knowbrew", Template: "reference"}) {
		t.Fatalf("second cursor = %#v", cursor.position)
	}
}

func TestRunMaxAdvancesCursorPastFailedDocument(t *testing.T) {
	template := testTemplate()
	repository := &fakeRepository{
		subjects: []domain.MasterEntry{
			{Name: "broken", Documents: []string{"missing"}},
			{Name: "knowbrew", Documents: []string{"concept"}},
		},
		templates: []domain.DocumentTemplate{template},
		knowledge: []storage.KnowledgeDocument{
			testKnowledge("kn-0000000000000001", "knowbrew", "Evidence.", true),
		},
	}
	runner := &fakeRunner{outputs: map[agent.Task][]json.RawMessage{
		agent.TaskDistillSelect: {
			json.RawMessage(`{"knowledge_references":["K001"]}`),
		},
		agent.TaskDistillGenerate: {
			json.RawMessage(`{"body":"# Concept","knowledge_references":["K001"]}`),
		},
	}}
	cursor := &fakeCursor{}
	service := Service{
		Repository: repository, Lifecycle: repository, Runner: runner,
		RunLock: fakeRunLock{}, Cursor: cursor,
	}

	first, err := service.Run(context.Background(), Options{Max: 1})
	if err != nil {
		t.Fatal(err)
	}
	if first.DocumentsFailed != 1 || cursor.position.Subject != "broken" {
		t.Fatalf("first summary = %#v, cursor = %#v", first, cursor.position)
	}
	second, err := service.Run(context.Background(), Options{Max: 1})
	if err != nil {
		t.Fatal(err)
	}
	if second.DocumentsCreated != 1 || len(repository.writes) != 1 ||
		repository.writes[0].Subject != "knowbrew" {
		t.Fatalf("second summary = %#v, writes = %#v", second, repository.writes)
	}
}

func TestRunProgressUsesPhaseLocalUsage(t *testing.T) {
	repository := &fakeRepository{
		subjects:  []domain.MasterEntry{{Name: "knowbrew", Documents: []string{"concept"}}},
		templates: []domain.DocumentTemplate{testTemplate()},
		knowledge: []storage.KnowledgeDocument{
			testKnowledge("kn-0000000000000001", "knowbrew", "Evidence.", true),
		},
	}
	runner := &fakeRunner{
		outputs: map[agent.Task][]json.RawMessage{
			agent.TaskDistillSelect: {
				json.RawMessage(`{"knowledge_references":["K001"]}`),
			},
			agent.TaskDistillGenerate: {
				json.RawMessage(`{"body":"# Concept","knowledge_references":["K001"]}`),
			},
		},
		usages: map[agent.Task][]agent.Usage{
			agent.TaskDistillSelect:   {{InputTokens: 100, OutputTokens: 10}},
			agent.TaskDistillGenerate: {{InputTokens: 200, OutputTokens: 20}},
		},
	}
	var output strings.Builder
	service := Service{
		Settings: Settings{Backend: "codex-cli", Model: "sol"}, Repository: repository,
		Lifecycle: repository, Runner: runner, Progress: progressui.From(&output), RunLock: fakeRunLock{},
	}
	summary, err := service.Run(context.Background(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	text := output.String()
	for _, required := range []string{
		"Selecting Knowledge · 0/1 documents · in 0 tokens / out 0 tokens",
		"Knowledge selection complete · 1/1 documents · in 100 tokens / out 10 tokens",
		"Generating documents · 0/1 documents · in 0 tokens / out 0 tokens",
		"Document generation complete · 1/1 documents · in 200 tokens / out 20 tokens",
	} {
		if !strings.Contains(text, required) {
			t.Fatalf("progress does not contain %q:\n%s", required, text)
		}
	}
	for _, forbidden := range []string{"Selected Knowledge for", "Generated document", "Distilling ·"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("non-verbose progress contains %q:\n%s", forbidden, text)
		}
	}
	if summary.Usage.TotalInputTokens != 300 || summary.Usage.OutputTokens != 30 {
		t.Fatalf("summary usage = %#v", summary.Usage)
	}
}

func TestRunVerboseProgressIncludesPerDocumentLines(t *testing.T) {
	repository := &fakeRepository{
		subjects:  []domain.MasterEntry{{Name: "knowbrew", Documents: []string{"concept"}}},
		templates: []domain.DocumentTemplate{testTemplate()},
		knowledge: []storage.KnowledgeDocument{
			testKnowledge("kn-0000000000000001", "knowbrew", "Evidence.", true),
		},
	}
	runner := &fakeRunner{outputs: map[agent.Task][]json.RawMessage{
		agent.TaskDistillSelect: {
			json.RawMessage(`{"knowledge_references":["K001"]}`),
		},
		agent.TaskDistillGenerate: {
			json.RawMessage(`{"body":"# Concept","knowledge_references":["K001"]}`),
		},
	}}
	var output strings.Builder
	service := Service{
		Repository: repository, Lifecycle: repository, Runner: runner,
		Progress: progressui.New(&output, false, 0, true), RunLock: fakeRunLock{},
	}
	if _, err := service.Run(context.Background(), Options{}); err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{
		"Selected Knowledge for knowbrew/concept",
		"Generated document knowbrew/concept",
	} {
		if !strings.Contains(output.String(), required) {
			t.Fatalf("verbose progress does not contain %q:\n%s", required, output.String())
		}
	}
}

func TestRecordFailureUsesPhaseSpecificLabel(t *testing.T) {
	var output strings.Builder
	service := Service{}
	display := progressui.From(&output)
	job := &documentJob{subject: "knowbrew", template: domain.DocumentTemplate{Name: "concept"}}
	var summary Summary

	service.recordFailure(&summary, display, job, "selection", errors.New("select error"))
	service.recordFailure(&summary, display, job, "generation", errors.New("generate error"))

	for _, required := range []string{
		"Knowledge selection failed · knowbrew/concept · selection · select error",
		"Document generation failed · knowbrew/concept · generation · generate error",
	} {
		if !strings.Contains(output.String(), required) {
			t.Fatalf("progress does not contain %q:\n%s", required, output.String())
		}
	}
}

func TestRunDeletesExistingDocumentWhenNoEvidenceRemains(t *testing.T) {
	template := testTemplate()
	repository := &fakeRepository{
		subjects:  []domain.MasterEntry{{Name: "knowbrew", Documents: []string{"concept"}}},
		templates: []domain.DocumentTemplate{template},
		documents: map[string]domain.DistilledDocument{
			"knowbrew/concept": {
				Subject: "knowbrew", Template: "concept",
				KnowledgeIDs: []string{"kn-0000000000000001"}, Body: "Old body.",
			},
		},
	}
	runner := &fakeRunner{outputs: map[agent.Task][]json.RawMessage{}}
	service := Service{Repository: repository, Lifecycle: repository, Runner: runner, RunLock: fakeRunLock{}}
	summary, err := service.Run(context.Background(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if summary.DocumentsDeleted != 1 || len(repository.deletes) != 1 || len(runner.calls) != 0 {
		t.Fatalf("summary = %#v, deletes = %#v, calls = %#v", summary, repository.deletes, runner.calls)
	}
}

func TestRunSkipsSubjectsWithoutRequestedDocuments(t *testing.T) {
	repository := &fakeRepository{
		subjects: []domain.MasterEntry{{Name: "knowbrew"}}, templates: []domain.DocumentTemplate{testTemplate()},
	}
	runner := &fakeRunner{}
	service := Service{Repository: repository, Lifecycle: repository, Runner: runner, RunLock: fakeRunLock{}}
	summary, err := service.Run(context.Background(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if summary.DocumentsPlanned != 0 || len(runner.calls) != 0 {
		t.Fatalf("summary = %#v, calls = %#v", summary, runner.calls)
	}
}

func TestRunRejectsAgentKnowledgeIDThatWasNotSupplied(t *testing.T) {
	repository := &fakeRepository{
		subjects:  []domain.MasterEntry{{Name: "knowbrew", Documents: []string{"concept"}}},
		templates: []domain.DocumentTemplate{testTemplate()},
		knowledge: []storage.KnowledgeDocument{
			testKnowledge("kn-0000000000000001", "knowbrew", "Evidence.", true),
		},
	}
	runner := &fakeRunner{outputs: map[agent.Task][]json.RawMessage{
		agent.TaskDistillSelect: {json.RawMessage(`{"knowledge_references":["K999"]}`)},
	}}
	service := Service{Repository: repository, Lifecycle: repository, Runner: runner, RunLock: fakeRunLock{}}
	summary, err := service.Run(context.Background(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if summary.DocumentsFailed != 1 || len(repository.writes) != 0 ||
		!strings.Contains(summary.Failures[0].Reason, "was not supplied") {
		t.Fatalf("summary = %#v, writes = %#v", summary, repository.writes)
	}
}

func TestRunContinuesWhenOneExistingDocumentCannotBeRead(t *testing.T) {
	concept := testTemplate()
	reference := testTemplate()
	reference.Name = "reference"
	reference.Output = "reference.md"
	repository := &fakeRepository{
		subjects: []domain.MasterEntry{{
			Name: "knowbrew", Documents: []string{"concept", "reference"},
		}},
		templates: []domain.DocumentTemplate{concept, reference},
		knowledge: []storage.KnowledgeDocument{
			testKnowledge("kn-0000000000000001", "knowbrew", "Evidence.", true),
		},
		documents:  make(map[string]domain.DistilledDocument),
		readErrors: map[string]error{"knowbrew/concept": errors.New("broken frontmatter")},
	}
	runner := &fakeRunner{outputs: map[agent.Task][]json.RawMessage{
		agent.TaskDistillSelect: {
			json.RawMessage(`{"knowledge_references":["K001"]}`),
		},
		agent.TaskDistillGenerate: {
			json.RawMessage(`{"body":"# Reference","knowledge_references":["K001"]}`),
		},
	}}
	service := Service{Repository: repository, Lifecycle: repository, Runner: runner, RunLock: fakeRunLock{}}
	summary, err := service.Run(context.Background(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if summary.DocumentsFailed != 1 || summary.DocumentsCreated != 1 || len(repository.writes) != 1 ||
		repository.writes[0].Template != "reference" {
		t.Fatalf("summary = %#v, writes = %#v", summary, repository.writes)
	}
}

func TestRunContinuesWhenAnotherSubjectReferencesMissingTemplate(t *testing.T) {
	template := testTemplate()
	repository := &fakeRepository{
		subjects: []domain.MasterEntry{
			{Name: "broken", Documents: []string{"missing"}},
			{Name: "knowbrew", Documents: []string{"concept"}},
		},
		templates: []domain.DocumentTemplate{template},
		knowledge: []storage.KnowledgeDocument{
			testKnowledge("kn-0000000000000001", "knowbrew", "Evidence.", true),
		},
	}
	runner := &fakeRunner{outputs: map[agent.Task][]json.RawMessage{
		agent.TaskDistillSelect: {
			json.RawMessage(`{"knowledge_references":["K001"]}`),
		},
		agent.TaskDistillGenerate: {
			json.RawMessage(`{"body":"# Concept","knowledge_references":["K001"]}`),
		},
	}}
	service := Service{Repository: repository, Lifecycle: repository, Runner: runner, RunLock: fakeRunLock{}}
	summary, err := service.Run(context.Background(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if summary.DocumentsPlanned != 2 || summary.DocumentsFailed != 1 || summary.DocumentsCreated != 1 ||
		len(summary.Failures) != 1 || summary.Failures[0].Template != "missing" {
		t.Fatalf("summary = %#v", summary)
	}
}

func TestRunChecksEveryUnselectedKnowledgeAcrossBatches(t *testing.T) {
	knowledge := make([]storage.KnowledgeDocument, 0, selectionBatchSize*2+1)
	for index := 0; index < selectionBatchSize*2+1; index++ {
		id := fmt.Sprintf("kn-%016x", index+1)
		knowledge = append(knowledge, testKnowledge(id, "knowbrew", "Evidence.", true))
	}
	repository := &fakeRepository{
		subjects:  []domain.MasterEntry{{Name: "knowbrew", Documents: []string{"concept"}}},
		templates: []domain.DocumentTemplate{testTemplate()},
		knowledge: knowledge,
	}
	runner := &fakeRunner{outputs: map[agent.Task][]json.RawMessage{
		agent.TaskDistillSelect: {
			json.RawMessage(`{"knowledge_references":[]}`),
			json.RawMessage(`{"knowledge_references":[]}`),
			json.RawMessage(`{"knowledge_references":[]}`),
		},
	}}
	service := Service{Repository: repository, Lifecycle: repository, Runner: runner, RunLock: fakeRunLock{}}
	if _, err := service.Run(context.Background(), Options{}); err != nil {
		t.Fatal(err)
	}
	if len(runner.calls) != 3 {
		t.Fatalf("runner calls = %d, want 3 selection batches", len(runner.calls))
	}
	allPrompts := strings.Join([]string{
		runner.calls[0].prompt, runner.calls[1].prompt, runner.calls[2].prompt,
	}, "\n")
	if count := strings.Count(allPrompts, `"reference":`); count != len(knowledge) {
		t.Fatalf("selection references = %d, want %d", count, len(knowledge))
	}
	for _, document := range knowledge {
		if strings.Contains(allPrompts, document.Knowledge.ID) {
			t.Fatalf("selection prompt exposes Knowledge ID %s", document.Knowledge.ID)
		}
	}
}
