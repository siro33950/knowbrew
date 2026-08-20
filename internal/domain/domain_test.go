package domain

import (
	"strings"
	"testing"
	"time"
)

func TestFeedstockTransitionsUseTypeCandidatesAndTurnBrewProgress(t *testing.T) {
	now := time.Date(2026, 8, 3, 1, 2, 3, 0, time.UTC)
	feedstock := validFeedstock("fs-transition", now)
	if err := feedstock.ApplySummary("  The user established durable behavior.  "); err != nil {
		t.Fatal(err)
	}
	if err := feedstock.ApplyAnnotation(
		[]KnowledgeType{"property", "decision", "property"},
		now.Add(time.Minute),
	); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(knowledgeTypeStringsForTest(feedstock.Types), ","); got != "decision,property" {
		t.Fatalf("types = %q", got)
	}
	if !feedstock.PendingBrew() {
		t.Fatal("annotated feedstock with type candidates is not pending")
	}
	if err := feedstock.ApplyBrewProgress(now.Add(2 * time.Minute)); err != nil {
		t.Fatal(err)
	}
	if feedstock.BrewedAt == nil || feedstock.PendingBrew() {
		t.Fatalf("feedstock = %#v", feedstock)
	}
}

func TestFeedstockWithoutTypeCandidatesIsNotPendingBrew(t *testing.T) {
	now := time.Date(2026, 8, 3, 1, 2, 3, 0, time.UTC)
	feedstock := validFeedstock("fs-empty", now)
	feedstock.Summary = "No durable Knowledge was found."
	if err := feedstock.ApplyAnnotation(nil, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if feedstock.PendingBrew() {
		t.Fatal("feedstock without type candidates is pending Brew")
	}
}

func TestValidateFeedstockRequiresNormalizedTypesAndSummaryWhenAnnotated(t *testing.T) {
	now := time.Date(2026, 8, 3, 1, 2, 3, 0, time.UTC)
	feedstock := validFeedstock("fs-invalid", now)
	annotated := now.Add(time.Minute)
	feedstock.AnnotatedAt = &annotated
	feedstock.Types = []KnowledgeType{"property", "decision"}
	if err := ValidateFeedstock(feedstock); err == nil || !strings.Contains(err.Error(), "summary") {
		t.Fatalf("error = %v", err)
	}
	feedstock.Summary = "A summary."
	if err := ValidateFeedstock(feedstock); err == nil || !strings.Contains(err.Error(), "unique and sorted") {
		t.Fatalf("error = %v", err)
	}
}

func TestResolveKnowledgeAppliesMultipleCandidatesToOneWorkingSet(t *testing.T) {
	vocabulary := testVocabulary()
	now := time.Date(2026, 8, 3, 0, 0, 0, 0, time.UTC)
	source := annotatedFeedstock("fs-source", now)
	ids := []string{"kn-first", "kn-second"}
	index := 0
	resolved, err := ResolveKnowledge(
		source,
		[]KnowledgeCandidate{
			candidate("First independently maintainable statement.", Resolution{Kind: ResolutionNew}),
			candidate("Second independently maintainable statement.", Resolution{Kind: ResolutionNew}),
		},
		nil,
		vocabulary,
		func() string {
			id := ids[index]
			index++
			return id
		},
		now.Add(time.Minute),
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(resolved.Results) != 2 || len(resolved.Changed) != 2 {
		t.Fatalf("resolved = %#v", resolved)
	}
	for _, id := range ids {
		if resolved.Changed[id].Knowledge.EstablishedBy != source.ID {
			t.Fatalf("record %s = %#v", id, resolved.Changed[id])
		}
	}
}

func TestResolveKnowledgeRejectsDuplicateGeneratedID(t *testing.T) {
	now := time.Date(2026, 8, 3, 0, 0, 0, 0, time.UTC)
	_, err := ResolveKnowledge(
		annotatedFeedstock("fs-source", now),
		[]KnowledgeCandidate{
			candidate("First independently maintainable statement.", Resolution{Kind: ResolutionNew}),
			candidate("Second independently maintainable statement.", Resolution{Kind: ResolutionNew}),
		},
		nil,
		testVocabulary(),
		func() string { return "kn-duplicate" },
		now.Add(time.Minute),
	)
	if err == nil || !strings.Contains(err.Error(), "knowledge candidate 2: knowledge ID kn-duplicate already exists") {
		t.Fatalf("error = %v", err)
	}
}

func TestResolveKnowledgeRejectsWholeBatchWhenOneCandidateFails(t *testing.T) {
	now := time.Date(2026, 8, 3, 0, 0, 0, 0, time.UTC)
	_, err := ResolveKnowledge(
		annotatedFeedstock("fs-source", now),
		[]KnowledgeCandidate{
			candidate("Valid statement.", Resolution{Kind: ResolutionNew}),
			{Type: "unknown", Subject: "knowbrew", Statement: "Invalid statement.", Resolution: Resolution{Kind: ResolutionNew}},
		},
		nil,
		testVocabulary(),
		func() string { return "kn-generated" },
		now.Add(time.Minute),
	)
	if err == nil || !strings.Contains(err.Error(), "knowledge candidate 2") {
		t.Fatalf("error = %v", err)
	}
}

func TestResolveKnowledgeRejectsConflictFromSameFeedstock(t *testing.T) {
	now := time.Date(2026, 8, 3, 0, 0, 0, 0, time.UTC)
	source := annotatedFeedstock("fs-source", now)
	records := map[string]KnowledgeRecord{
		"kn-existing": {
			Knowledge: Knowledge{
				ID: "kn-existing", Created: now, Updated: now, EstablishedBy: source.ID,
				Type: "property", Subject: "knowbrew", Feedstocks: []string{source.ID}, Status: StatusPending,
			},
			Statement: "Existing statement.", Established: source,
		},
	}
	_, err := ResolveKnowledge(
		source,
		[]KnowledgeCandidate{candidate("Conflicting statement.", Resolution{
			Kind: ResolutionConflicts, KnowledgeIDs: []string{"kn-existing"},
		})},
		records,
		testVocabulary(),
		func() string { return "kn-generated" },
		now.Add(time.Minute),
	)
	if err == nil || !strings.Contains(err.Error(), "shares source feedstock") {
		t.Fatalf("error = %v", err)
	}
}

func TestResolveKnowledgeUsesSourceTimeForConflicts(t *testing.T) {
	vocabulary := testVocabulary()
	base := time.Date(2026, 8, 3, 0, 0, 0, 0, time.UTC)
	originalSource := annotatedFeedstock("fs-original", base)
	original, err := ResolveKnowledge(
		originalSource,
		[]KnowledgeCandidate{candidate("Original behavior applies.", Resolution{Kind: ResolutionNew})},
		nil,
		vocabulary,
		func() string { return "kn-original" },
		base,
	)
	if err != nil {
		t.Fatal(err)
	}
	records := map[string]KnowledgeRecord{"kn-original": original.Changed["kn-original"]}
	older, err := ResolveKnowledge(
		annotatedFeedstock("fs-older", base.Add(-time.Hour)),
		[]KnowledgeCandidate{candidate("Older behavior applies.", Resolution{
			Kind: ResolutionConflicts, KnowledgeIDs: []string{"kn-original"},
		})},
		records,
		vocabulary,
		func() string { return "kn-older" },
		base.Add(time.Minute),
	)
	if err != nil {
		t.Fatal(err)
	}
	if older.Results[0].Outcome != "historical_conflict_ignored" || len(older.Changed) != 0 {
		t.Fatalf("older = %#v", older)
	}
	newer, err := ResolveKnowledge(
		annotatedFeedstock("fs-newer", base.Add(time.Hour)),
		[]KnowledgeCandidate{candidate("Newer behavior applies.", Resolution{
			Kind: ResolutionConflicts, KnowledgeIDs: []string{"kn-original"},
		})},
		records,
		vocabulary,
		func() string { return "kn-newer" },
		base.Add(2*time.Hour),
	)
	if err != nil {
		t.Fatal(err)
	}
	if newer.Results[0].Outcome != "replaced" || newer.Changed["kn-original"].Knowledge.SupersededBy != "kn-newer" {
		t.Fatalf("newer = %#v", newer)
	}
}

func TestResolveKnowledgeAcceptsStructuredComplement(t *testing.T) {
	vocabulary := testVocabulary()
	base := time.Date(2026, 8, 3, 0, 0, 0, 0, time.UTC)
	originalSource := annotatedFeedstock("fs-original", base)
	created, err := ResolveKnowledge(
		originalSource,
		[]KnowledgeCandidate{candidate("The API accepts effort.", Resolution{Kind: ResolutionNew})},
		nil,
		vocabulary,
		func() string { return "kn-original" },
		base,
	)
	if err != nil {
		t.Fatal(err)
	}
	statement := "Effort handling depends on the backend.\n\n- `api`: accepts effort\n- `ollama`: ignores effort"
	merged, err := ResolveKnowledge(
		annotatedFeedstock("fs-additional", base.Add(time.Hour)),
		[]KnowledgeCandidate{{
			Type: "property", Subject: "knowbrew", Statement: "Ollama ignores effort.",
			Resolution: Resolution{
				Kind: ResolutionComplements, KnowledgeIDs: []string{"kn-original"},
				Draft: &KnowledgeDraft{
					Type: "property", Subject: "knowbrew", Statement: statement,
				},
			},
		}},
		map[string]KnowledgeRecord{"kn-original": created.Changed["kn-original"]},
		vocabulary,
		func() string { return "kn-merged" },
		base.Add(2*time.Hour),
	)
	if err != nil {
		t.Fatal(err)
	}
	if merged.Results[0].Outcome != "merged" || merged.Changed["kn-merged"].Statement != statement {
		t.Fatalf("merged = %#v", merged)
	}
}

func TestResolveKnowledgeRejectsStructuredComplementChangingSubject(t *testing.T) {
	vocabulary := NewVocabulary(
		[]MasterEntry{{Name: "property"}},
		[]MasterEntry{{Name: "knowbrew"}, {Name: "other"}},
	)
	base := time.Date(2026, 8, 3, 0, 0, 0, 0, time.UTC)
	original, err := ResolveKnowledge(
		annotatedFeedstock("fs-original", base),
		[]KnowledgeCandidate{candidate("The API accepts effort.", Resolution{Kind: ResolutionNew})},
		nil,
		vocabulary,
		func() string { return "kn-original" },
		base,
	)
	if err != nil {
		t.Fatal(err)
	}
	_, err = ResolveKnowledge(
		annotatedFeedstock("fs-additional", base.Add(time.Hour)),
		[]KnowledgeCandidate{{
			Type: "property", Subject: "knowbrew", Statement: "Ollama ignores effort.",
			Resolution: Resolution{
				Kind: ResolutionComplements, KnowledgeIDs: []string{"kn-original"},
				Draft: &KnowledgeDraft{
					Type: "property", Subject: "other", Statement: "A merged statement.",
				},
			},
		}},
		map[string]KnowledgeRecord{"kn-original": original.Changed["kn-original"]},
		vocabulary,
		func() string { return "kn-merged" },
		base.Add(2*time.Hour),
	)
	if err == nil || !strings.Contains(err.Error(), "preserve the Knowledge subject") {
		t.Fatalf("error = %v", err)
	}
}

func TestReconcileKnowledgeLifecycleFollowsHumanStatusChanges(t *testing.T) {
	now := time.Date(2026, 8, 3, 0, 0, 0, 0, time.UTC)
	predecessor := Knowledge{
		ID: "kn-predecessor", Created: now, Updated: now,
		Type: "property", Subject: "knowbrew", Feedstocks: []string{"fs-source"}, Status: StatusPending,
	}
	successor := Knowledge{
		ID: "kn-successor", Created: now, Updated: now,
		Type: "property", Subject: "knowbrew", Feedstocks: []string{"fs-source"},
		Supersedes: []string{predecessor.ID}, Status: StatusPending,
	}
	changes, issues := ReconcileKnowledgeLifecycle(map[string]Knowledge{
		predecessor.ID: predecessor,
		successor.ID:   successor,
	}, now.Add(time.Minute))
	if len(issues) != 0 {
		t.Fatalf("issues = %#v", issues)
	}
	if changes[predecessor.ID].SupersededBy != successor.ID ||
		changes[predecessor.ID].Status != StatusSuperseded {
		t.Fatalf("changes = %#v", changes)
	}
}

func validFeedstock(id string, timestamp time.Time) Feedstock {
	return Feedstock{
		Schema: SchemaVersion, ID: id, TurnID: "turn-" + id,
		Session: SessionRef{ID: "session-" + id}, Timestamp: timestamp, Agent: "codex",
	}
}

func annotatedFeedstock(id string, timestamp time.Time) Feedstock {
	feedstock := validFeedstock(id, timestamp)
	annotated := timestamp.Add(time.Minute)
	feedstock.Summary = "A durable property was established."
	feedstock.Types = []KnowledgeType{"property"}
	feedstock.AnnotatedAt = &annotated
	return feedstock
}

func testVocabulary() Vocabulary {
	return NewVocabulary([]MasterEntry{{Name: "property"}}, []MasterEntry{{Name: "knowbrew"}})
}

func candidate(statement string, resolution Resolution) KnowledgeCandidate {
	return KnowledgeCandidate{
		Type: "property", Subject: "knowbrew", Statement: statement, Resolution: resolution,
	}
}

func knowledgeTypeStringsForTest(values []KnowledgeType) []string {
	result := make([]string, len(values))
	for index, value := range values {
		result[index] = string(value)
	}
	return result
}
