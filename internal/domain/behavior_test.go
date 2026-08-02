package domain

import (
	"slices"
	"strings"
	"testing"
	"time"
)

func TestBuildAssertionsOwnsVocabularyNormalizationAndIdentity(t *testing.T) {
	vocabulary := NewVocabulary(
		[]MasterEntry{{Name: "property"}},
		[]MasterEntry{{Name: "knowbrew"}},
	)
	drafts := []AssertionDraft{{
		Type: "property", Subject: "[[knowbrew]]",
		Statement: "  Knowledge IDs are stable.  ", Rationale: "  IDs identify records.  ",
	}}
	first, err := BuildAssertions("fs-source", drafts, vocabulary)
	if err != nil {
		t.Fatal(err)
	}
	second, err := BuildAssertions("fs-source", drafts, vocabulary)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 1 || first[0] != second[0] {
		t.Fatalf("assertions = %#v, %#v", first, second)
	}
	if first[0].Subject != "knowbrew" || first[0].Statement != "Knowledge IDs are stable." ||
		!strings.HasPrefix(first[0].ID, "as-") {
		t.Fatalf("assertion = %#v", first[0])
	}
	if _, err := BuildAssertions("fs-source", append(drafts, drafts[0]), vocabulary); err == nil {
		t.Fatal("duplicate assertion was accepted")
	}
	if _, err := NewAssertion("fs-source", AssertionDraft{
		Type: "unknown", Statement: "Unknown types are rejected.",
	}, vocabulary); err == nil {
		t.Fatal("unknown type was accepted")
	}
}

func TestFeedstockTransitionsPreserveSubjectlessWork(t *testing.T) {
	now := time.Date(2026, 8, 3, 1, 2, 3, 0, time.UTC)
	feedstock := validFeedstock("fs-transition", now)
	if err := feedstock.ApplySummary("  The user established two properties.  "); err != nil {
		t.Fatal(err)
	}
	vocabulary := NewVocabulary([]MasterEntry{{Name: "property"}}, []MasterEntry{{Name: "knowbrew"}})
	assertions, err := BuildAssertions(feedstock.ID, []AssertionDraft{
		{Type: "property", Subject: "knowbrew", Statement: "Owned knowledge is brewed."},
		{Type: "property", Statement: "Unowned assertions remain available."},
	}, vocabulary)
	if err != nil {
		t.Fatal(err)
	}
	if err := feedstock.ApplyAnnotation(assertions, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if pending := feedstock.PendingAssertions(); len(pending) != 1 || pending[0].Subject != "knowbrew" {
		t.Fatalf("pending = %#v", pending)
	}
	if err := feedstock.ApplyBrewProgress(assertions, []string{assertions[0].ID}, now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if feedstock.BrewedAt == nil || !slices.Equal(feedstock.BrewedAssertions, []string{assertions[0].ID}) {
		t.Fatalf("feedstock = %#v", feedstock)
	}
}

func TestVerifyAssertionOwnsCorrectionRules(t *testing.T) {
	vocabulary := NewVocabulary([]MasterEntry{{Name: "property"}}, []MasterEntry{{Name: "knowbrew"}})
	current := Assertion{
		ID: "as-current", Type: "property", Subject: "knowbrew", Statement: "Old statement.",
	}
	corrected := &Assertion{
		Type: "property", Subject: "[[knowbrew]]", Statement: "  Corrected statement.  ",
	}
	verified, rejected, err := VerifyAssertion(
		current, VerificationCorrected, corrected, true, vocabulary,
	)
	if err != nil {
		t.Fatal(err)
	}
	if rejected || verified.ID != current.ID || verified.Subject != "knowbrew" ||
		verified.Statement != "Corrected statement." {
		t.Fatalf("verified = %#v, rejected = %v", verified, rejected)
	}
	wrongSubject := *corrected
	wrongSubject.Subject = "other"
	if _, _, err := VerifyAssertion(
		current, VerificationCorrected, &wrongSubject, true, vocabulary,
	); err == nil {
		t.Fatal("subject-changing correction was accepted")
	}
	if _, _, err := VerifyAssertion(
		current, VerificationRejected, nil, true, vocabulary,
	); err == nil {
		t.Fatal("rejected assertion accepted a resolution")
	}
}

func TestResolveKnowledgeUsesSourceTimeForConflicts(t *testing.T) {
	vocabulary := NewVocabulary([]MasterEntry{{Name: "property"}}, []MasterEntry{{Name: "knowbrew"}})
	base := time.Date(2026, 8, 3, 0, 0, 0, 0, time.UTC)
	originalSource := annotatedFeedstock("fs-original", base)
	originalAssertion := assertionFor(t, originalSource.ID, "Original behavior applies.", vocabulary)
	created, err := ResolveKnowledge(
		originalSource, originalAssertion, Resolution{Kind: ResolutionNew}, nil, vocabulary, base,
	)
	if err != nil {
		t.Fatal(err)
	}
	original := created.Changed[created.KnowledgeID]
	records := map[string]KnowledgeRecord{original.Knowledge.ID: original}

	olderSource := annotatedFeedstock("fs-older", base.Add(-time.Hour))
	olderAssertion := assertionFor(t, olderSource.ID, "Older behavior applies.", vocabulary)
	ignored, err := ResolveKnowledge(
		olderSource, olderAssertion,
		Resolution{Kind: ResolutionConflicts, KnowledgeIDs: []string{original.Knowledge.ID}},
		records, vocabulary, base.Add(time.Minute),
	)
	if err != nil {
		t.Fatal(err)
	}
	if ignored.Outcome != "historical_conflict_ignored" || len(ignored.Changed) != 0 {
		t.Fatalf("ignored = %#v", ignored)
	}

	newerSource := annotatedFeedstock("fs-newer", base.Add(time.Hour))
	newerAssertion := assertionFor(t, newerSource.ID, "Newer behavior applies.", vocabulary)
	replaced, err := ResolveKnowledge(
		newerSource, newerAssertion,
		Resolution{Kind: ResolutionConflicts, KnowledgeIDs: []string{original.Knowledge.ID}},
		records, vocabulary, base.Add(2*time.Hour),
	)
	if err != nil {
		t.Fatal(err)
	}
	if replaced.Outcome != "replaced" || replaced.KnowledgeID == original.Knowledge.ID {
		t.Fatalf("replaced = %#v", replaced)
	}
	if predecessor := replaced.Changed[original.Knowledge.ID].Knowledge; predecessor.SupersededBy != replaced.KnowledgeID {
		t.Fatalf("predecessor = %#v", predecessor)
	}
}

func TestReconcileKnowledgeLifecycleFollowsHumanStatusChanges(t *testing.T) {
	now := time.Date(2026, 8, 3, 0, 0, 0, 0, time.UTC)
	predecessor := Knowledge{
		ID: "kn-predecessor", Created: now, Updated: now,
		Type: "property", Subject: "knowbrew", Feedstocks: []string{"fs-source"},
		Assertions: []string{"fs-source#as-source"}, Status: StatusPending,
	}
	successor := Knowledge{
		ID: "kn-successor", Created: now, Updated: now,
		Type: "property", Subject: "knowbrew", Feedstocks: []string{"fs-source"},
		Assertions: []string{"fs-source#as-source"}, Supersedes: []string{predecessor.ID},
		Status: StatusPending,
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
		Session:   SessionRef{ID: "session-" + id, Path: "/sessions/" + id + ".jsonl"},
		Timestamp: timestamp, Agent: "codex",
	}
}

func annotatedFeedstock(id string, timestamp time.Time) Feedstock {
	feedstock := validFeedstock(id, timestamp)
	annotated := timestamp.Add(time.Minute)
	feedstock.Summary = "A durable property was established."
	feedstock.AnnotatedAt = &annotated
	return feedstock
}

func assertionFor(t *testing.T, feedstockID, statement string, vocabulary Vocabulary) Assertion {
	t.Helper()
	assertion, err := NewAssertion(feedstockID, AssertionDraft{
		Type: "property", Subject: "knowbrew", Statement: statement,
	}, vocabulary)
	if err != nil {
		t.Fatal(err)
	}
	return assertion
}
