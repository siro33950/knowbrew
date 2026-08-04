package distill

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/siro33950/knowbrew/internal/application/agent"
	"github.com/siro33950/knowbrew/internal/application/diagnostic"
	knowledgeapp "github.com/siro33950/knowbrew/internal/application/knowledge"
	"github.com/siro33950/knowbrew/internal/application/storage"
	"github.com/siro33950/knowbrew/internal/domain"
)

const selectionBatchSize = 50

type Summary struct {
	DocumentsPlanned  int                  `json:"documents_planned"`
	DocumentsCreated  int                  `json:"documents_created"`
	DocumentsUpdated  int                  `json:"documents_updated"`
	DocumentsDeleted  int                  `json:"documents_deleted"`
	DocumentsFailed   int                  `json:"documents_failed"`
	KnowledgeSelected int                  `json:"knowledge_selected"`
	KnowledgeUsed     int                  `json:"knowledge_used"`
	Usage             agent.UsageReport    `json:"usage"`
	Failures          []Failure            `json:"failures,omitempty"`
	Warnings          []diagnostic.Warning `json:"warnings,omitempty"`
}

type Failure struct {
	Subject  string `json:"subject"`
	Template string `json:"template"`
	Stage    string `json:"stage"`
	Reason   string `json:"reason"`
}

type knowledgeRecord struct {
	ID        string
	Type      domain.KnowledgeType
	Claim     string
	Rationale string
}

type documentJob struct {
	subject    string
	template   domain.DocumentTemplate
	existed    bool
	root       []knowledgeRecord
	candidates []knowledgeRecord
	selected   []knowledgeRecord
	prepareErr error
	failed     bool
}

func (service Service) Run(ctx context.Context, options Options) (Summary, error) {
	display := service.progress()
	if service.Repository == nil {
		return Summary{}, errors.New("distill repository is required")
	}
	if service.Lifecycle == nil {
		return Summary{}, errors.New("distill lifecycle repository is required")
	}
	if service.Runner == nil {
		return Summary{}, errors.New("distill runner is required")
	}
	if service.RunLock == nil {
		return Summary{}, errors.New("distill run lock is required")
	}
	if err := service.Repository.EnsureLayout(); err != nil {
		return Summary{}, err
	}
	unlock, err := service.RunLock.Lock(ctx)
	if err != nil {
		return Summary{}, err
	}
	defer func() { _ = unlock() }()

	summary := Summary{Usage: agent.NewUsageReport(
		service.Settings.Backend, service.Settings.Model, agent.Usage{},
	)}
	_, lifecycleWarnings, err := knowledgeapp.Reconcile(ctx, service.Lifecycle)
	diagnostic.Add(&summary.Warnings, display, lifecycleWarnings...)
	if err != nil {
		return summary, err
	}
	jobs, warnings, err := service.jobs(options)
	diagnostic.Add(&summary.Warnings, display, warnings...)
	if err != nil {
		return summary, err
	}
	summary.DocumentsPlanned = len(jobs)

	var selectionUsage agent.Usage
	display.Start(fmt.Sprintf(
		"Selecting Knowledge · 0/%d documents · %s",
		len(jobs), agent.FormatUsage(selectionUsage),
	))
	for index := range jobs {
		job := &jobs[index]
		if job.prepareErr != nil {
			service.recordFailure(&summary, display, job, "preparation", job.prepareErr)
			job.failed = true
		} else {
			selected, runUsage, selectErr := service.selectKnowledge(ctx, *job)
			selectionUsage.Add(runUsage)
			if selectErr != nil {
				service.recordFailure(&summary, display, job, "selection", selectErr)
				job.failed = true
			} else {
				job.selected = selected
				summary.KnowledgeSelected += len(selected)
				display.Verbosef("Selected Knowledge for %s/%s", job.subject, job.template.Name)
			}
		}
		display.Update(fmt.Sprintf(
			"Selecting Knowledge · %d/%d documents · %s",
			index+1, len(jobs), agent.FormatUsage(selectionUsage),
		))
	}
	display.Complete(fmt.Sprintf(
		"Knowledge selection complete · %d/%d documents · %s",
		len(jobs), len(jobs), agent.FormatUsage(selectionUsage),
	))

	writingInstructions := ""
	if len(jobs) > 0 {
		writingInstructions, err = loadWritingInstructions(service.Repository, "common", "document")
		if err != nil {
			return summary, err
		}
	}
	var generationUsage agent.Usage
	display.Start(fmt.Sprintf(
		"Generating documents · 0/%d documents · %s",
		len(jobs), agent.FormatUsage(generationUsage),
	))
	for index := range jobs {
		job := &jobs[index]
		if !job.failed {
			runUsage, generateErr := service.generateDocument(
				ctx, job, &summary, writingInstructions,
			)
			generationUsage.Add(runUsage)
			if generateErr != nil {
				service.recordFailure(&summary, display, job, "generation", generateErr)
				job.failed = true
			}
		}
		display.Update(fmt.Sprintf(
			"Generating documents · %d/%d documents · %s",
			index+1, len(jobs), agent.FormatUsage(generationUsage),
		))
	}
	usage := selectionUsage
	usage.Add(generationUsage)
	summary.Usage = agent.NewUsageReport(service.Settings.Backend, service.Settings.Model, usage)
	display.Complete(fmt.Sprintf(
		"Document generation complete · %d/%d documents · %s",
		len(jobs), len(jobs), agent.FormatUsage(generationUsage),
	))
	return summary, nil
}

func (service Service) jobs(options Options) ([]documentJob, []diagnostic.Warning, error) {
	subjects, warnings, err := service.Repository.LoadMasters("subjects")
	if err != nil {
		return nil, warnings, err
	}
	templates, templateWarnings, err := service.Repository.LoadTemplates()
	warnings = append(warnings, templateWarnings...)
	if err != nil {
		return nil, warnings, err
	}
	knowledge, knowledgeWarnings, err := service.Repository.ListKnowledge()
	warnings = append(warnings, knowledgeWarnings...)
	if err != nil {
		return nil, warnings, err
	}

	subjectFilter := domain.MasterName(options.Subject)
	templateFilter := domain.MasterName(options.Template)
	templateByName := make(map[string]domain.DocumentTemplate, len(templates))
	for _, template := range templates {
		templateByName[template.Name] = template
	}
	if templateFilter != "" {
		if _, exists := templateByName[templateFilter]; !exists {
			return nil, warnings, fmt.Errorf("template %q is not defined", templateFilter)
		}
	}
	slices.SortFunc(subjects, func(left, right domain.MasterEntry) int {
		return strings.Compare(left.Name, right.Name)
	})
	foundSubject := subjectFilter == ""
	var jobs []documentJob
	for _, subject := range subjects {
		if subject.Name != subjectFilter && subjectFilter != "" {
			continue
		}
		foundSubject = true
		outputs := make(map[string]string)
		for _, templateName := range subject.Documents {
			templateName = domain.MasterName(templateName)
			if templateName != templateFilter && templateFilter != "" {
				continue
			}
			template, exists := templateByName[templateName]
			if !exists {
				jobs = append(jobs, documentJob{
					subject:  subject.Name,
					template: domain.DocumentTemplate{Name: templateName},
					prepareErr: fmt.Errorf(
						"subject %s requests document %s but its template is not defined",
						subject.Name, templateName,
					),
				})
				continue
			}
			if previous, exists := outputs[template.Output]; exists {
				return nil, warnings, fmt.Errorf(
					"subject %s document definitions %s and %s use the same output %s",
					subject.Name, previous, template.Name, template.Output,
				)
			}
			outputs[template.Output] = template.Name
			job, prepareErr := service.prepareJob(subject.Name, template, knowledge)
			job.prepareErr = prepareErr
			jobs = append(jobs, job)
		}
	}
	if !foundSubject {
		return nil, warnings, fmt.Errorf("subject %q is not defined", subjectFilter)
	}
	if (subjectFilter != "" || templateFilter != "") && len(jobs) == 0 {
		return nil, warnings, errors.New("no Subject document matched the requested filters")
	}
	slices.SortFunc(jobs, func(left, right documentJob) int {
		if compared := strings.Compare(left.subject, right.subject); compared != 0 {
			return compared
		}
		return strings.Compare(left.template.Name, right.template.Name)
	})
	return jobs, warnings, nil
}

func (service Service) prepareJob(
	subject string,
	template domain.DocumentTemplate,
	knowledge []storage.KnowledgeDocument,
) (documentJob, error) {
	template.Structure = strings.ReplaceAll(template.Structure, "{{subject}}", subject)
	job := documentJob{subject: subject, template: template}
	existing, exists, err := service.Repository.ReadDistilledDocument(template, subject)
	if err != nil {
		return job, err
	}
	job.existed = exists
	eligible := make(map[string]knowledgeRecord)
	for _, document := range knowledge {
		if !domain.IsDistillableKnowledge(document.Knowledge, subject) {
			continue
		}
		eligible[document.Knowledge.ID] = knowledgeRecord{
			ID: document.Knowledge.ID, Type: document.Knowledge.Type,
			Claim: document.Statement, Rationale: document.Rationale,
		}
	}
	rootIDs := make(map[string]struct{})
	if exists {
		for _, id := range existing.KnowledgeIDs {
			if record, valid := eligible[id]; valid {
				job.root = append(job.root, record)
				rootIDs[id] = struct{}{}
			}
		}
	}
	for id, record := range eligible {
		if _, rooted := rootIDs[id]; !rooted {
			job.candidates = append(job.candidates, record)
		}
	}
	sortKnowledge(job.root)
	sortKnowledge(job.candidates)
	return job, nil
}

func (service Service) selectKnowledge(
	ctx context.Context,
	job documentJob,
) ([]knowledgeRecord, agent.Usage, error) {
	var usage agent.Usage
	selectedByID := make(map[string]knowledgeRecord)
	for start := 0; start < len(job.candidates); start += selectionBatchSize {
		end := min(start+selectionBatchSize, len(job.candidates))
		batch := job.candidates[start:end]
		evidence, byReference := referencedEvidence(batch)
		prompt, err := selectionPrompt(job.template, evidence)
		if err != nil {
			return nil, usage, err
		}
		result, err := service.Runner.Run(ctx, agent.TaskDistillSelect, "", prompt)
		usage.Add(result.Usage)
		if err != nil {
			return nil, usage, err
		}
		var decision struct {
			KnowledgeReferences []string `json:"knowledge_references"`
		}
		if err := agent.DecodeResult(result.Output, &decision); err != nil {
			return nil, usage, err
		}
		selected, err := resolveEvidenceReferences(decision.KnowledgeReferences, byReference)
		if err != nil {
			return nil, usage, err
		}
		for _, record := range selected {
			selectedByID[record.ID] = record
		}
	}
	selected := make([]knowledgeRecord, 0, len(selectedByID))
	for _, record := range selectedByID {
		selected = append(selected, record)
	}
	sortKnowledge(selected)
	return selected, usage, nil
}

func (service Service) generateDocument(
	ctx context.Context,
	job *documentJob,
	summary *Summary,
	writingInstructions string,
) (agent.Usage, error) {
	evidenceRecords := append(append([]knowledgeRecord(nil), job.root...), job.selected...)
	sortKnowledge(evidenceRecords)
	if len(evidenceRecords) == 0 {
		return agent.Usage{}, service.deleteDocument(ctx, job, summary)
	}
	evidence, byReference := referencedEvidence(evidenceRecords)
	prompt, err := generationPrompt(job.template, evidence, writingInstructions)
	if err != nil {
		return agent.Usage{}, err
	}
	result, err := service.Runner.Run(ctx, agent.TaskDistillGenerate, "", prompt)
	if err != nil {
		return result.Usage, err
	}
	var generated struct {
		Body                string   `json:"body"`
		KnowledgeReferences []string `json:"knowledge_references"`
	}
	if err := agent.DecodeResult(result.Output, &generated); err != nil {
		return result.Usage, err
	}
	used, err := resolveEvidenceReferences(generated.KnowledgeReferences, byReference)
	if err != nil {
		return result.Usage, err
	}
	if len(used) == 0 {
		if strings.TrimSpace(generated.Body) != "" {
			return result.Usage, errors.New("generated body must be empty when no Knowledge was used")
		}
		return result.Usage, service.deleteDocument(ctx, job, summary)
	}
	ids := make([]string, len(used))
	for index, record := range used {
		ids[index] = record.ID
	}
	document := domain.DistilledDocument{
		Subject: job.subject, Template: job.template.Name,
		KnowledgeIDs: ids, Body: generated.Body,
	}
	if err := domain.ValidateDistilledDocument(document); err != nil {
		return result.Usage, err
	}
	if err := service.Repository.WithLock(ctx, func() error {
		return service.Repository.WriteDistilledDocument(job.template, document)
	}); err != nil {
		return result.Usage, err
	}
	if job.existed {
		summary.DocumentsUpdated++
	} else {
		summary.DocumentsCreated++
	}
	summary.KnowledgeUsed += len(ids)
	service.progress().Verbosef("Generated document %s/%s", job.subject, job.template.Name)
	return result.Usage, nil
}

func (service Service) deleteDocument(
	ctx context.Context,
	job *documentJob,
	summary *Summary,
) error {
	if !job.existed {
		return nil
	}
	var deleted bool
	if err := service.Repository.WithLock(ctx, func() error {
		var err error
		deleted, err = service.Repository.DeleteDistilledDocument(job.template, job.subject)
		return err
	}); err != nil {
		return err
	}
	if deleted {
		summary.DocumentsDeleted++
	}
	return nil
}

func (service Service) recordFailure(
	summary *Summary,
	display Progress,
	job *documentJob,
	stage string,
	err error,
) {
	summary.DocumentsFailed++
	summary.Failures = append(summary.Failures, Failure{
		Subject: job.subject, Template: job.template.Name, Stage: stage, Reason: err.Error(),
	})
	failureLabel := "Distillation failed"
	switch stage {
	case "preparation", "selection":
		failureLabel = "Knowledge selection failed"
	case "generation":
		failureLabel = "Document generation failed"
	}
	display.Errorf("%s · %s/%s · %s · %v", failureLabel, job.subject, job.template.Name, stage, err)
}

func sortKnowledge(records []knowledgeRecord) {
	slices.SortFunc(records, func(left, right knowledgeRecord) int {
		return strings.Compare(left.ID, right.ID)
	})
}

func resolveEvidenceReferences(
	values []string,
	byReference map[string]knowledgeRecord,
) ([]knowledgeRecord, error) {
	resolved := make([]knowledgeRecord, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		reference := strings.TrimSpace(value)
		record, exists := byReference[reference]
		if !exists {
			return nil, fmt.Errorf("knowledge reference %s was not supplied for this decision", reference)
		}
		if _, exists := seen[reference]; exists {
			return nil, fmt.Errorf("duplicate Knowledge reference %q", reference)
		}
		seen[reference] = struct{}{}
		resolved = append(resolved, record)
	}
	sortKnowledge(resolved)
	return resolved, nil
}
