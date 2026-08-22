package domain

import (
	"slices"
	"strings"
	"testing"
	"time"
)

func TestFeedstockExtractionProgressIncludesEmptyTypes(t *testing.T) {
	now := time.Date(2026, 8, 3, 1, 2, 3, 0, time.UTC)
	feedstock := validFeedstock("fs-transition", now)
	if err := feedstock.ApplyDraft(
		"  No durable Knowledge was found.  ",
		nil,
		now.Add(time.Minute),
	); err != nil {
		t.Fatal(err)
	}
	if !feedstock.PendingExtraction() {
		t.Fatal("annotated feedstock is not pending extraction")
	}
	if err := feedstock.ApplyExtractionProgress(now.Add(2 * time.Minute)); err != nil {
		t.Fatal(err)
	}
	if feedstock.ExtractedAt == nil || feedstock.PendingExtraction() {
		t.Fatalf("feedstock = %#v", feedstock)
	}
}

func TestFeedstockDraftNormalizesTypes(t *testing.T) {
	now := time.Date(2026, 8, 3, 1, 2, 3, 0, time.UTC)
	feedstock := validFeedstock("fs-types", now)
	if err := feedstock.ApplyDraft(
		"A durable behavior was established.",
		[]KnowledgeType{"property", "decision", "property"},
		now.Add(time.Minute),
	); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(knowledgeTypeStringsForTest(feedstock.Types), ","); got != "decision,property" {
		t.Fatalf("types = %q", got)
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

func TestExtractKnowledgeCreatesUnorganizedRecords(t *testing.T) {
	now := time.Date(2026, 8, 3, 0, 0, 0, 0, time.UTC)
	records, err := ExtractKnowledge(
		annotatedFeedstock("fs-source", now),
		[]KnowledgeDraft{
			{Type: "property", Subject: "knowbrew", Statement: "A durable fact."},
			{Type: "property", Statement: "A subjectless fact."},
		},
		testVocabulary(),
		sequenceIDs("kn-first", "kn-second"),
		now.Add(time.Minute),
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 2 || records[0].Knowledge.OrganizedAt != nil || records[1].Knowledge.Subject != "" {
		t.Fatalf("records = %#v", records)
	}
	if records[0].Knowledge.ID != "kn-first" || records[0].Knowledge.EstablishedBy != "fs-source" {
		t.Fatalf("first record = %#v", records[0])
	}
}

func TestExtractKnowledgeRejectsWholeBatchOnDuplicateID(t *testing.T) {
	now := time.Date(2026, 8, 3, 0, 0, 0, 0, time.UTC)
	_, err := ExtractKnowledge(
		annotatedFeedstock("fs-source", now),
		[]KnowledgeDraft{
			{Type: "property", Statement: "First durable fact."},
			{Type: "property", Statement: "Second durable fact."},
		},
		testVocabulary(), func() string { return "kn-duplicate" }, now.Add(time.Minute),
	)
	if err == nil || !strings.Contains(err.Error(), "draft 2") {
		t.Fatalf("error = %v", err)
	}
}

func TestOrganizeRequiresExactlyOneActionPerInput(t *testing.T) {
	base := time.Date(2026, 8, 3, 0, 0, 0, 0, time.UTC)
	inputs := []KnowledgeRecord{
		unorganizedRecord("kn-first", "First fact.", base),
		unorganizedRecord("kn-second", "Second fact.", base.Add(time.Minute)),
	}
	_, err := OrganizeKnowledge(inputs, recordsByID(inputs...), []OrganizationAction{{
		KnowledgeID: "kn-first", Resolution: Resolution{Kind: ResolutionNew},
	}}, testVocabulary(), base.Add(time.Hour))
	if err == nil || !strings.Contains(err.Error(), "omits input kn-second") {
		t.Fatalf("error = %v", err)
	}
	_, err = OrganizeKnowledge(inputs, recordsByID(inputs...), []OrganizationAction{
		{KnowledgeID: "kn-first", Resolution: Resolution{Kind: ResolutionNew}},
		{KnowledgeID: "kn-first", Resolution: Resolution{Kind: ResolutionDiscard}},
	}, testVocabulary(), base.Add(time.Hour))
	if err == nil || !strings.Contains(err.Error(), "repeats input kn-first") {
		t.Fatalf("error = %v", err)
	}
}

func TestOrganizeNewEquivalentComplementAndDiscard(t *testing.T) {
	base := time.Date(2026, 8, 3, 0, 0, 0, 0, time.UTC)
	organizedAt := base.Add(-time.Hour)
	head := organizedRecord("kn-head", "Existing fact.", base.Add(-time.Hour), organizedAt)
	newInput := unorganizedRecord("kn-new", "Independent fact.", base)
	equivalent := unorganizedRecord("kn-equivalent", "Same fact.", base.Add(time.Minute))
	complement := unorganizedRecord("kn-complement", "Additional fact.", base.Add(2*time.Minute))
	discard := unorganizedRecord("kn-discard", "Noise.", base.Add(3*time.Minute))
	records := recordsByID(head, newInput, equivalent, complement, discard)
	mergedStatement := "Existing and additional facts."
	resolved, err := OrganizeKnowledge(
		[]KnowledgeRecord{discard, complement, newInput, equivalent},
		records,
		[]OrganizationAction{
			{KnowledgeID: "kn-discard", Resolution: Resolution{Kind: ResolutionDiscard}},
			{KnowledgeID: "kn-complement", Resolution: Resolution{
				Kind: ResolutionComplements, KnowledgeIDs: []string{"kn-head"},
				Draft: &KnowledgeDraft{Type: "property", Subject: "knowbrew", Statement: mergedStatement},
			}},
			{KnowledgeID: "kn-new", Resolution: Resolution{Kind: ResolutionNew}},
			{KnowledgeID: "kn-equivalent", Resolution: Resolution{
				Kind: ResolutionEquivalent, KnowledgeIDs: []string{"kn-head"},
			}},
		},
		testVocabulary(), base.Add(time.Hour),
	)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Changed["kn-new"].Knowledge.OrganizedAt == nil {
		t.Fatal("new input was not organized under its own ID")
	}
	merged := resolved.Changed["kn-complement"]
	if merged.Statement != mergedStatement || !slices.Equal(
		merged.Knowledge.Feedstocks,
		[]string{"fs-kn-complement", "fs-kn-equivalent", "fs-kn-head"},
	) {
		t.Fatalf("merged = %#v", merged)
	}
	if resolved.Changed["kn-head"].Knowledge.SupersededBy != "kn-complement" {
		t.Fatalf("retired head = %#v", resolved.Changed["kn-head"])
	}
	if !slices.Equal(resolved.Consumed, []string{
		"kn-new", "kn-equivalent", "kn-complement", "kn-discard",
	}) {
		t.Fatalf("consumed = %#v", resolved.Consumed)
	}
}

func TestOrganizeConflictsUseFeedstockChronology(t *testing.T) {
	base := time.Date(2026, 8, 3, 0, 0, 0, 0, time.UTC)
	organizedAt := base
	head := organizedRecord("kn-head", "Current fact.", base, organizedAt)
	older := unorganizedRecord("kn-older", "Historical fact.", base.Add(-time.Hour))
	resolved, err := OrganizeKnowledge(
		[]KnowledgeRecord{older}, recordsByID(head, older),
		[]OrganizationAction{{KnowledgeID: "kn-older", Resolution: Resolution{
			Kind: ResolutionConflicts, KnowledgeIDs: []string{"kn-head"},
		}}}, testVocabulary(), base.Add(time.Hour),
	)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Results[0].Outcome != "historical_conflict_ignored" || len(resolved.Changed) != 0 {
		t.Fatalf("older result = %#v", resolved)
	}
	newer := unorganizedRecord("kn-newer", "Replacement fact.", base.Add(2*time.Hour))
	resolved, err = OrganizeKnowledge(
		[]KnowledgeRecord{newer}, recordsByID(head, newer),
		[]OrganizationAction{{KnowledgeID: "kn-newer", Resolution: Resolution{
			Kind: ResolutionConflicts, KnowledgeIDs: []string{"kn-head"},
		}}}, testVocabulary(), base.Add(3*time.Hour),
	)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Changed["kn-head"].Knowledge.SupersededBy != "kn-newer" ||
		resolved.Changed["kn-newer"].Knowledge.OrganizedAt == nil {
		t.Fatalf("newer result = %#v", resolved)
	}
}

func TestOrganizeUsesChronologicalInputsRegardlessOfActionOrder(t *testing.T) {
	base := time.Date(2026, 8, 3, 0, 0, 0, 0, time.UTC)
	first := unorganizedRecord("kn-first", "First fact.", base)
	second := unorganizedRecord("kn-second", "Second fact.", base.Add(time.Minute))
	actions := []OrganizationAction{
		{KnowledgeID: "kn-second", Resolution: Resolution{
			Kind: ResolutionEquivalent, KnowledgeIDs: []string{"kn-first"},
		}},
		{KnowledgeID: "kn-first", Resolution: Resolution{Kind: ResolutionNew}},
	}
	resolved, err := OrganizeKnowledge(
		[]KnowledgeRecord{second, first}, recordsByID(first, second), actions,
		testVocabulary(), base.Add(time.Hour),
	)
	if err != nil {
		t.Fatal(err)
	}
	if got := resolved.Changed["kn-first"].Knowledge.Feedstocks; !slices.Equal(
		got, []string{"fs-kn-first", "fs-kn-second"},
	) {
		t.Fatalf("feedstocks = %#v", got)
	}
}

func TestOrganizeResolvesInitialHeadsAndEarlierInputsAfterPriorActions(t *testing.T) {
	base := time.Date(2026, 8, 3, 0, 0, 0, 0, time.UTC)
	organizedAt := base.Add(-time.Hour)
	head := organizedRecord("kn-head", "Existing fact.", base.Add(-time.Hour), organizedAt)
	first := unorganizedRecord("kn-first", "Equivalent fact.", base)
	second := unorganizedRecord("kn-second", "Additional fact.", base.Add(time.Minute))
	third := unorganizedRecord("kn-third", "Same merged fact.", base.Add(2*time.Minute))
	resolved, err := OrganizeKnowledge(
		[]KnowledgeRecord{first, second, third}, recordsByID(head, first, second, third),
		[]OrganizationAction{
			{KnowledgeID: first.Knowledge.ID, Resolution: Resolution{
				Kind: ResolutionEquivalent, KnowledgeIDs: []string{head.Knowledge.ID},
			}},
			{KnowledgeID: second.Knowledge.ID, Resolution: Resolution{
				Kind: ResolutionComplements, KnowledgeIDs: []string{first.Knowledge.ID},
				Draft: &KnowledgeDraft{
					Type: "property", Subject: "knowbrew", Statement: "Existing and additional facts.",
				},
			}},
			{KnowledgeID: third.Knowledge.ID, Resolution: Resolution{
				Kind: ResolutionEquivalent, KnowledgeIDs: []string{head.Knowledge.ID},
			}},
		},
		testVocabulary(), base.Add(time.Hour),
	)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Changed[head.Knowledge.ID].Knowledge.SupersededBy != second.Knowledge.ID {
		t.Fatalf("head = %#v", resolved.Changed[head.Knowledge.ID])
	}
	if got := resolved.Changed[second.Knowledge.ID].Knowledge.Feedstocks; !slices.Equal(
		got,
		[]string{"fs-kn-first", "fs-kn-head", "fs-kn-second", "fs-kn-third"},
	) {
		t.Fatalf("merged feedstocks = %#v", got)
	}
}

func TestKnowledgeHeadQueriesExcludeUnorganizedAndRequireExactSubject(t *testing.T) {
	base := time.Date(2026, 8, 3, 0, 0, 0, 0, time.UTC)
	organizedAt := base
	first := organizedRecord("kn-first", "First fact.", base, organizedAt)
	other := organizedRecord("kn-other", "Other fact.", base, organizedAt)
	other.Knowledge.Subject = "other"
	unorganized := unorganizedRecord("kn-unorganized", "Draft fact.", base)
	records := recordsByID(first, other, unorganized)
	if got := KnowledgeHeadsBySubject(records, ""); got != nil {
		t.Fatalf("empty subject heads = %#v", got)
	}
	if got := KnowledgeHeadsBySubject(records, "knowbrew"); len(got) != 1 || got[0].Knowledge.ID != "kn-first" {
		t.Fatalf("subject heads = %#v", got)
	}
	if got := KnowledgeHeads(records); len(got) != 2 {
		t.Fatalf("all heads = %#v", got)
	}
}

func TestUnorganizedDuplicateClaimsDoNotParticipateInGraphValidation(t *testing.T) {
	base := time.Date(2026, 8, 3, 0, 0, 0, 0, time.UTC)
	first := unorganizedRecord("kn-first", "Same fact.", base)
	second := unorganizedRecord("kn-second", "Same fact.", base.Add(time.Minute))
	if err := ValidateKnowledgeGraph(recordsByID(first, second)); err != nil {
		t.Fatal(err)
	}
	organizedAt := base
	first.Knowledge.OrganizedAt = &organizedAt
	second.Knowledge.OrganizedAt = &organizedAt
	if err := ValidateKnowledgeGraph(recordsByID(first, second)); err == nil ||
		!strings.Contains(err.Error(), "duplicate current") {
		t.Fatalf("error = %v", err)
	}
}

func TestLifecycleIgnoresUnorganizedKnowledge(t *testing.T) {
	now := time.Date(2026, 8, 3, 0, 0, 0, 0, time.UTC)
	organizedAt := now
	predecessor := Knowledge{
		ID: "kn-predecessor", Created: now, Updated: now, OrganizedAt: &organizedAt,
		Type: "property", Subject: "knowbrew", Feedstocks: []string{"fs-source"}, Status: StatusPending,
	}
	successor := Knowledge{
		ID: "kn-successor", Created: now, Updated: now, OrganizedAt: &organizedAt,
		Type: "property", Subject: "knowbrew", Feedstocks: []string{"fs-source"},
		Supersedes: []string{predecessor.ID}, Status: StatusPending,
	}
	unorganized := Knowledge{
		ID: "kn-unorganized", Created: now, Updated: now,
		Type: "property", Subject: "knowbrew", Feedstocks: []string{"fs-source"}, Status: StatusPending,
	}
	changes, issues := ReconcileKnowledgeLifecycle(map[string]Knowledge{
		predecessor.ID: predecessor,
		successor.ID:   successor,
		unorganized.ID: unorganized,
	}, now.Add(time.Minute))
	if len(issues) != 0 {
		t.Fatalf("issues = %#v", issues)
	}
	if changes[predecessor.ID].SupersededBy != successor.ID {
		t.Fatalf("changes = %#v", changes)
	}
	if _, exists := changes[unorganized.ID]; exists {
		t.Fatalf("unorganized change = %#v", changes[unorganized.ID])
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

func unorganizedRecord(id, statement string, timestamp time.Time) KnowledgeRecord {
	feedstock := annotatedFeedstock("fs-"+id, timestamp)
	return KnowledgeRecord{
		Knowledge: Knowledge{
			ID: id, Created: timestamp, Updated: timestamp, EstablishedBy: feedstock.ID,
			Type: "property", Subject: "knowbrew", Feedstocks: []string{feedstock.ID},
			Status: StatusPending,
		},
		Statement: statement, Established: feedstock,
	}
}

func organizedRecord(id, statement string, timestamp, organizedAt time.Time) KnowledgeRecord {
	record := unorganizedRecord(id, statement, timestamp)
	record.Knowledge.OrganizedAt = &organizedAt
	return record
}

func recordsByID(records ...KnowledgeRecord) map[string]KnowledgeRecord {
	result := make(map[string]KnowledgeRecord, len(records))
	for _, record := range records {
		result[record.Knowledge.ID] = record
	}
	return result
}

func testVocabulary() Vocabulary {
	return NewVocabulary(
		[]MasterEntry{{Name: "property"}},
		[]MasterEntry{{Name: "knowbrew"}, {Name: "other"}},
	)
}

func sequenceIDs(ids ...string) func() string {
	index := 0
	return func() string {
		id := ids[index]
		index++
		return id
	}
}

func knowledgeTypeStringsForTest(values []KnowledgeType) []string {
	result := make([]string, len(values))
	for index, value := range values {
		result[index] = string(value)
	}
	return result
}
