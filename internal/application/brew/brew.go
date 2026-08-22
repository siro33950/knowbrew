package brew

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"

	"github.com/siro33950/knowbrew/internal/application/agent"
	"github.com/siro33950/knowbrew/internal/application/diagnostic"
	knowledgeapp "github.com/siro33950/knowbrew/internal/application/knowledge"
	"github.com/siro33950/knowbrew/internal/domain"
)

const defaultConcurrency = 5

type Summary struct {
	SubjectsSelected  int                  `json:"subjects_selected"`
	SubjectsPending   int                  `json:"subjects_pending"`
	SubjectsProcessed int                  `json:"subjects_processed"`
	SubjectsFailed    int                  `json:"subjects_failed"`
	ChangedSubjects   []string             `json:"changed_subjects"`
	Usage             agent.UsageReport    `json:"usage"`
	Failures          []SubjectFailure     `json:"failures,omitempty"`
	Warnings          []diagnostic.Warning `json:"warnings,omitempty"`
}

type SubjectFailure struct {
	Subject string `json:"subject"`
	Reason  string `json:"reason"`
}

type subjectRunResult struct {
	subject  string
	changed  bool
	usage    agent.Usage
	warnings []diagnostic.Warning
	err      error
}

func (service Service) Run(ctx context.Context) (Summary, error) {
	return service.RunWithOptions(ctx, Options{})
}

func (service Service) RunWithOptions(ctx context.Context, options Options) (Summary, error) {
	if options.Max < 0 {
		return Summary{}, errors.New("maximum subjects must be greater than zero")
	}
	if service.Repository == nil {
		return Summary{}, errors.New("brew repository is required")
	}
	if err := service.Repository.EnsureLayout(); err != nil {
		return Summary{}, err
	}
	ctx, err := withKnowledgeTypes(ctx, service.Repository)
	if err != nil {
		return Summary{}, err
	}
	display := service.progress()
	summary := Summary{
		ChangedSubjects: []string{},
		Usage: agent.NewUsageReport(
			service.Settings.Backend, service.Settings.Model, agent.Usage{},
		),
	}
	if service.Lifecycle != nil {
		_, warnings, reconcileErr := knowledgeapp.Reconcile(ctx, service.Lifecycle)
		diagnostic.Add(&summary.Warnings, display, warnings...)
		if reconcileErr != nil {
			return summary, reconcileErr
		}
	}
	documents, warnings, err := service.Repository.ListKnowledge()
	diagnostic.Add(&summary.Warnings, display, warnings...)
	if err != nil {
		return summary, err
	}
	allSubjects := pendingSubjects(documents)
	selected := allSubjects
	if options.Max > 0 && len(selected) > options.Max {
		selected = selected[:options.Max]
	}
	summary.SubjectsSelected = len(selected)
	if len(selected) > 0 && service.Runner == nil {
		return summary, errors.New("brew runner is required for pending subjects")
	}
	concurrency := service.Settings.Concurrency
	if concurrency == 0 {
		concurrency = defaultConcurrency
	}
	if concurrency < 1 {
		return summary, errors.New("brew concurrency must be at least 1")
	}
	concurrency = min(concurrency, len(selected))
	display.Start(fmt.Sprintf(
		"Brewing · 0/%d subjects · %d workers · %s",
		len(selected), concurrency, agent.FormatUsage(agent.Usage{}),
	))
	cache := newFeedstockCache()
	jobs := make(chan string)
	results := make(chan subjectRunResult, len(selected))
	var workers sync.WaitGroup
	for range concurrency {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for subject := range jobs {
				results <- service.processSubject(ctx, cache, subject)
			}
		}()
	}
	go func() {
		defer close(jobs)
		for _, subject := range selected {
			select {
			case jobs <- subject:
			case <-ctx.Done():
				return
			}
		}
	}()
	go func() {
		workers.Wait()
		close(results)
	}()
	var usage agent.Usage
	completed := 0
	for result := range results {
		completed++
		usage.Add(result.usage)
		diagnostic.Add(&summary.Warnings, display, result.warnings...)
		if result.err != nil {
			summary.SubjectsFailed++
			summary.Failures = append(summary.Failures, SubjectFailure{
				Subject: result.subject, Reason: result.err.Error(),
			})
			display.Errorf("Brewing failed · %s · %v", result.subject, result.err)
		} else {
			summary.SubjectsProcessed++
			if result.changed {
				summary.ChangedSubjects = append(summary.ChangedSubjects, result.subject)
			}
			display.Verbosef("Brewed %s", result.subject)
		}
		display.Update(fmt.Sprintf(
			"Brewing · %d/%d subjects · %d workers · %s",
			completed, len(selected), concurrency, agent.FormatUsage(usage),
		))
	}
	slices.Sort(summary.ChangedSubjects)
	summary.SubjectsPending = len(allSubjects) - summary.SubjectsProcessed
	summary.Usage = agent.NewUsageReport(service.Settings.Backend, service.Settings.Model, usage)
	display.Complete(fmt.Sprintf(
		"Brewing complete · %d/%d subjects · %s",
		completed, len(selected), agent.FormatUsage(usage),
	))
	if err := ctx.Err(); err != nil {
		return summary, err
	}
	if service.SearchIndex != nil {
		warnings, syncErr := service.SearchIndex.Sync(ctx)
		diagnostic.Add(&summary.Warnings, display, warnings...)
		if syncErr != nil {
			diagnostic.Add(&summary.Warnings, display,
				diagnostic.FromError("search index", syncErr),
			)
		}
	}
	return summary, nil
}

func (service Service) processSubject(
	ctx context.Context,
	cache *feedstockCache,
	subject string,
) (result subjectRunResult) {
	result.subject = subject
	release := func() error { return nil }
	if service.Claimer != nil {
		var err error
		release, err = service.Claimer.Claim(ctx, subject)
		if err != nil {
			result.err = err
			return result
		}
	}
	defer func() {
		if err := release(); result.err == nil && err != nil {
			result.err = err
		}
	}()
	snapshot, warnings, err := loadSubjectSnapshot(service.Repository, cache, subject)
	result.warnings = append(result.warnings, warnings...)
	if err != nil {
		result.err = err
		return result
	}
	if len(snapshot.Inputs) == 0 {
		return result
	}
	prompt, warnings, err := subjectPrompt(service.Repository, snapshot)
	result.warnings = append(result.warnings, warnings...)
	if err != nil {
		result.err = err
		return result
	}
	runResult, err := service.Runner.Run(ctx, agent.TaskBrew, subject, prompt)
	result.usage.Add(runResult.Usage)
	if err != nil {
		result.err = err
		return result
	}
	var output struct {
		Actions *[]domain.OrganizationAction `json:"actions"`
	}
	if err := agent.DecodeResult(runResult.Output, &output); err != nil {
		result.err = err
		return result
	}
	if output.Actions == nil {
		result.err = errors.New("brew result actions are required")
		return result
	}
	changed, applyWarnings, err := ApplyOrganization(
		ctx, service.Repository, cache, snapshot, *output.Actions,
	)
	result.warnings = append(result.warnings, applyWarnings...)
	result.changed = changed
	result.err = err
	return result
}

func pendingSubjects(documents []KnowledgeDocument) []string {
	seen := make(map[string]struct{})
	for _, document := range documents {
		if document.Knowledge.OrganizedAt != nil {
			continue
		}
		subject := domain.MasterName(document.Knowledge.Subject)
		if subject != "" {
			seen[subject] = struct{}{}
		}
	}
	result := make([]string, 0, len(seen))
	for subject := range seen {
		result = append(result, subject)
	}
	slices.Sort(result)
	return result
}

func withKnowledgeTypes(ctx context.Context, dataStore Repository) (context.Context, error) {
	entries, err := dataStore.KnowledgeTypes()
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name)
	}
	return agent.WithKnowledgeTypes(ctx, names), nil
}
