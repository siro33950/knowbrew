package brew

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/siro33950/knowbrew/internal/adapters/config"
	"github.com/siro33950/knowbrew/internal/adapters/invocation/state"
	"github.com/siro33950/knowbrew/internal/adapters/llm"
	"github.com/siro33950/knowbrew/internal/adapters/persistence/markdownstore"
	"github.com/siro33950/knowbrew/internal/application/agent"
	"github.com/siro33950/knowbrew/internal/domain"
)

func TestSubmitCreatesKnowledgeFromVerifiedAssertion(t *testing.T) {
	dataStore := newBrewStore(t)
	feedstock := writeAssertionFeedstock(t, dataStore, "fs-create", time.Now().UTC(), []domain.Assertion{
		testAssertion("as-create", "knowbrew", "Knowledge IDs are independent of claim wording."),
	})
	setInvocation(t, dataStore, feedstock.ID, "as-create", "inv-create")
	if _, err := catalogForTest(dataStore, "knowbrew"); err != nil {
		t.Fatal(err)
	}
	result, err := submitForTest(context.Background(), dataStore, SubmitInput{
		FeedstockID: feedstock.ID, AssertionID: "as-create", Verification: VerificationVerified,
		Resolution: &ResolutionInput{Kind: ResolutionNew},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != "created" || !strings.HasPrefix(result.KnowledgeID, "kn-") {
		t.Fatalf("result = %#v", result)
	}
	file, err := dataStore.FindKnowledge(result.KnowledgeID)
	if err != nil {
		t.Fatal(err)
	}
	if file.Knowledge.ID != result.KnowledgeID ||
		file.Knowledge.Subject != "knowbrew" ||
		!slices.Equal(file.Knowledge.Assertions, []string{"fs-create#as-create"}) {
		t.Fatalf("knowledge = %#v", file.Knowledge)
	}
	if !strings.Contains(file.Body, "## Claim\n\nKnowledge IDs") {
		t.Fatalf("body = %q", file.Body)
	}
	updated, _, err := dataStore.FindFeedstock(feedstock.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(updated.BrewedAssertions, []string{"as-create"}) || updated.BrewedAt == nil {
		t.Fatalf("brew state = %#v", updated)
	}
}

func TestSubmitRejectsAssertionAndRecomputesFeedstockTypes(t *testing.T) {
	dataStore := newBrewStore(t)
	feedstock := writeAssertionFeedstock(t, dataStore, "fs-reject", time.Now().UTC(), []domain.Assertion{
		testAssertion("as-reject", "knowbrew", "A request is knowledge."),
		testAssertion("as-keep", "", "A later subject may make this assertion brewable."),
	})
	setInvocation(t, dataStore, feedstock.ID, "as-reject", "inv-reject")
	result, err := submitForTest(context.Background(), dataStore, SubmitInput{
		FeedstockID: feedstock.ID, AssertionID: "as-reject", Verification: VerificationRejected,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != "rejected" {
		t.Fatalf("result = %#v", result)
	}
	updated, _, err := dataStore.FindFeedstock(feedstock.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(updated.Assertions) != 1 || updated.Assertions[0].ID != "as-keep" ||
		!slices.Equal(updated.Types, []domain.KnowledgeType{"property"}) {
		t.Fatalf("updated feedstock = %#v", updated)
	}
	if updated.BrewedAt != nil {
		t.Fatal("subjectless remaining assertion must keep the feedstock open for later subject assignment")
	}
}

func TestSubjectlessAssertionIsNotPendingOrMarkedBrewed(t *testing.T) {
	dataStore := newBrewStore(t)
	feedstock := writeAssertionFeedstock(t, dataStore, "fs-subjectless", time.Now().UTC(), []domain.Assertion{
		testAssertion("as-subjectless", "", "This assertion has no semantic owner yet."),
	})
	if pending := collectPendingAssertions([]domain.Feedstock{feedstock}); len(pending) != 0 {
		t.Fatalf("pending = %#v", pending)
	}
	if err := dataStore.WriteBrewedFeedstock(feedstock, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	updated, _, err := dataStore.FindFeedstock(feedstock.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.BrewedAt != nil {
		t.Fatal("subjectless-only feedstock was marked brewed")
	}
}

func TestSubmitCorrectsAssertionWithoutChangingIdentityOrSubject(t *testing.T) {
	dataStore := newBrewStore(t)
	feedstock := writeAssertionFeedstock(t, dataStore, "fs-correct", time.Now().UTC(), []domain.Assertion{
		testAssertion("as-correct", "knowbrew", "Knowledge uses wording as identity."),
	})
	bad := testAssertion("as-correct", "other", "Knowledge uses stable IDs independent of wording.")
	setInvocation(t, dataStore, feedstock.ID, "as-correct", "inv-bad-subject")
	if _, err := submitForTest(context.Background(), dataStore, SubmitInput{
		FeedstockID: feedstock.ID, AssertionID: "as-correct", Verification: VerificationCorrected,
		CorrectedAssertion: &bad, Resolution: &ResolutionInput{Kind: ResolutionNew},
	}); err == nil || !strings.Contains(err.Error(), "cannot change an assertion subject") {
		t.Fatalf("subject-change error = %v", err)
	}

	setInvocation(t, dataStore, feedstock.ID, "as-correct", "inv-correct")
	if _, err := catalogForTest(dataStore, "knowbrew"); err != nil {
		t.Fatal(err)
	}
	corrected := testAssertion("as-correct", "knowbrew", "Knowledge uses stable IDs independent of wording.")
	result, err := submitForTest(context.Background(), dataStore, SubmitInput{
		FeedstockID: feedstock.ID, AssertionID: "as-correct", Verification: VerificationCorrected,
		CorrectedAssertion: &corrected, Resolution: &ResolutionInput{Kind: ResolutionNew},
	})
	if err != nil {
		t.Fatal(err)
	}
	updated, _, err := dataStore.FindFeedstock(feedstock.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Assertions[0] != corrected || result.KnowledgeID == "" {
		t.Fatalf("updated = %#v, result = %#v", updated.Assertions, result)
	}
}

func TestEquivalentRequiresCatalogAndFullInspectionThenAddsEvidence(t *testing.T) {
	dataStore := newBrewStore(t)
	old := writeAssertionFeedstock(t, dataStore, "fs-old", time.Now().Add(-time.Hour).UTC(), []domain.Assertion{
		testAssertion("as-old", "knowbrew", "Stable IDs preserve Knowledge identity."),
	})
	writeKnowledge(t, dataStore, "kn-existing", old, old.Assertions[0], false)
	current := writeAssertionFeedstock(t, dataStore, "fs-equivalent", time.Now().UTC(), []domain.Assertion{
		testAssertion("as-equivalent", "knowbrew", "Stable IDs preserve Knowledge identity."),
	})
	setInvocation(t, dataStore, current.ID, "as-equivalent", "inv-equivalent")
	entries, err := catalogForTest(dataStore, "knowbrew")
	if err != nil || len(entries) != 1 {
		t.Fatalf("catalog = %#v, err = %v", entries, err)
	}
	if _, err := catalogForTest(dataStore, "knowbrew"); err == nil ||
		!strings.Contains(err.Error(), "already loaded subject catalog") {
		t.Fatalf("second catalog error = %v", err)
	}
	if _, err := submitForTest(context.Background(), dataStore, SubmitInput{
		FeedstockID: current.ID, AssertionID: "as-equivalent", Verification: VerificationVerified,
		Resolution: &ResolutionInput{Kind: ResolutionEquivalent, KnowledgeIDs: []string{"kn-existing"}},
	}); err == nil || !strings.Contains(err.Error(), "current inspected Knowledge head") {
		t.Fatalf("uninspected relation error = %v", err)
	}
	if _, err := showForTest(dataStore, []string{"kn-existing"}); err != nil {
		t.Fatal(err)
	}
	result, err := submitForTest(context.Background(), dataStore, SubmitInput{
		FeedstockID: current.ID, AssertionID: "as-equivalent", Verification: VerificationVerified,
		Resolution: &ResolutionInput{Kind: ResolutionEquivalent, KnowledgeIDs: []string{"kn-existing"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != "evidence_added" || result.KnowledgeID != "kn-existing" {
		t.Fatalf("result = %#v", result)
	}
	file, err := dataStore.FindKnowledge("kn-existing")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(file.Knowledge.Assertions, "fs-equivalent#as-equivalent") {
		t.Fatalf("assertions = %#v", file.Knowledge.Assertions)
	}
}

func TestComplementsCreatesOnePendingSuccessorAtomically(t *testing.T) {
	dataStore := newBrewStore(t)
	old := writeAssertionFeedstock(t, dataStore, "fs-part-one", time.Now().Add(-time.Hour).UTC(), []domain.Assertion{
		testAssertion("as-part-one", "knowbrew", "Knowledge IDs are stable."),
	})
	writeKnowledge(t, dataStore, "kn-combined", old, old.Assertions[0], false)
	current := writeAssertionFeedstock(t, dataStore, "fs-part-two", time.Now().UTC(), []domain.Assertion{
		testAssertion("as-part-two", "knowbrew", "Knowledge filenames use their stable IDs."),
	})
	setInvocation(t, dataStore, current.ID, "as-part-two", "inv-complements")
	_, _ = catalogForTest(dataStore, "knowbrew")
	_, _ = showForTest(dataStore, []string{"kn-combined"})
	draft := KnowledgeDraft{
		Type: "property", Subject: "knowbrew",
		Statement: "Knowledge has a stable ID and uses that ID as its filename.",
		Rationale: "Identity therefore does not depend on mutable wording.",
	}
	result, err := submitForTest(context.Background(), dataStore, SubmitInput{
		FeedstockID: current.ID, AssertionID: "as-part-two", Verification: VerificationVerified,
		Resolution: &ResolutionInput{
			Kind: ResolutionComplements, KnowledgeIDs: []string{"kn-combined"}, Draft: &draft,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != "merged" || result.KnowledgeID == "kn-combined" {
		t.Fatalf("result = %#v", result)
	}
	file, err := dataStore.FindKnowledge(result.KnowledgeID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(file.Body, draft.Statement) || len(file.Knowledge.Assertions) != 2 {
		t.Fatalf("merged = %#v\n%s", file.Knowledge, file.Body)
	}
}

func TestComplementsFromSameFeedstockAreMerged(t *testing.T) {
	dataStore := newBrewStore(t)
	feedstock := writeAssertionFeedstock(t, dataStore, "fs-same-source", time.Now().UTC(), []domain.Assertion{
		testAssertion("as-phase-count", "knowbrew", "Draw has three phases."),
		testAssertion("as-phase-order", "knowbrew", "Summarization finishes before assertion extraction starts."),
	})
	writeKnowledge(t, dataStore, "kn-phases", feedstock, feedstock.Assertions[0], false)
	setInvocation(t, dataStore, feedstock.ID, "as-phase-order", "inv-same-source-complements")
	_, _ = catalogForTest(dataStore, "knowbrew")
	_, _ = showForTest(dataStore, []string{"kn-phases"})
	draft := KnowledgeDraft{
		Type: "property", Subject: "knowbrew",
		Statement: "Draw has three phases, with summarization completed before assertion extraction starts.",
	}
	result, err := submitForTest(context.Background(), dataStore, SubmitInput{
		FeedstockID: feedstock.ID, AssertionID: "as-phase-order", Verification: VerificationVerified,
		Resolution: &ResolutionInput{
			Kind: ResolutionComplements, KnowledgeIDs: []string{"kn-phases"}, Draft: &draft,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != "merged" || result.KnowledgeID == "kn-phases" {
		t.Fatalf("result = %#v", result)
	}
	file, err := dataStore.FindKnowledge(result.KnowledgeID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(file.Body, draft.Statement) || len(file.Knowledge.Assertions) != 2 {
		t.Fatalf("merged = %#v\n%s", file.Knowledge, file.Body)
	}
}

func TestConflictsWithEqualTimestampsUseStableSourceOrder(t *testing.T) {
	dataStore := newBrewStore(t)
	when := time.Now().UTC()
	older := writeAssertionFeedstock(t, dataStore, "fs-order-a", when, []domain.Assertion{
		testAssertion("as-order-a", "knowbrew", "Draw has two phases."),
	})
	writeKnowledge(t, dataStore, "kn-order", older, older.Assertions[0], false)
	newer := writeAssertionFeedstock(t, dataStore, "fs-order-b", when, []domain.Assertion{
		testAssertion("as-order-b", "knowbrew", "Draw has three phases."),
	})
	setInvocation(t, dataStore, newer.ID, "as-order-b", "inv-stable-source-order")
	_, _ = catalogForTest(dataStore, "knowbrew")
	_, _ = showForTest(dataStore, []string{"kn-order"})
	result, err := submitForTest(context.Background(), dataStore, SubmitInput{
		FeedstockID: newer.ID, AssertionID: "as-order-b", Verification: VerificationVerified,
		Resolution: &ResolutionInput{Kind: ResolutionConflicts, KnowledgeIDs: []string{"kn-order"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != "replaced" || result.KnowledgeID == "kn-order" {
		t.Fatalf("result = %#v", result)
	}
}

func TestConflictsUsesNewestSourceTimeAndKeepsPendingIdentity(t *testing.T) {
	dataStore := newBrewStore(t)
	baseTime := time.Now().Add(-2 * time.Hour).UTC()
	old := writeAssertionFeedstock(t, dataStore, "fs-old-rule", baseTime, []domain.Assertion{
		testAssertion("as-old-rule", "knowbrew", "Draw uses five workers."),
	})
	writeKnowledge(t, dataStore, "kn-rule", old, old.Assertions[0], false)
	newer := writeAssertionFeedstock(t, dataStore, "fs-new-rule", baseTime.Add(time.Hour), []domain.Assertion{
		testAssertion("as-new-rule", "knowbrew", "Draw uses one worker."),
	})
	setInvocation(t, dataStore, newer.ID, "as-new-rule", "inv-new-conflict")
	_, _ = catalogForTest(dataStore, "knowbrew")
	_, _ = showForTest(dataStore, []string{"kn-rule"})
	result, err := submitForTest(context.Background(), dataStore, SubmitInput{
		FeedstockID: newer.ID, AssertionID: "as-new-rule", Verification: VerificationVerified,
		Resolution: &ResolutionInput{Kind: ResolutionConflicts, KnowledgeIDs: []string{"kn-rule"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != "replaced" || result.KnowledgeID == "kn-rule" {
		t.Fatalf("new result = %#v", result)
	}
	file, _ := dataStore.FindKnowledge(result.KnowledgeID)
	if !strings.Contains(file.Body, "one worker") ||
		!slices.Equal(file.Knowledge.Assertions, []string{"fs-new-rule#as-new-rule"}) {
		t.Fatalf("newest replacement = %#v\n%s", file.Knowledge, file.Body)
	}

	historical := writeAssertionFeedstock(t, dataStore, "fs-historical", baseTime.Add(-time.Hour), []domain.Assertion{
		testAssertion("as-historical", "knowbrew", "Draw uses ten workers."),
	})
	setInvocation(t, dataStore, historical.ID, "as-historical", "inv-old-conflict")
	_, _ = catalogForTest(dataStore, "knowbrew")
	_, _ = showForTest(dataStore, []string{result.KnowledgeID})
	currentKnowledgeID := result.KnowledgeID
	result, err = submitForTest(context.Background(), dataStore, SubmitInput{
		FeedstockID: historical.ID, AssertionID: "as-historical", Verification: VerificationVerified,
		Resolution: &ResolutionInput{Kind: ResolutionConflicts, KnowledgeIDs: []string{currentKnowledgeID}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != "historical_conflict_ignored" {
		t.Fatalf("historical result = %#v", result)
	}
	file, _ = dataStore.FindKnowledge(currentKnowledgeID)
	if !strings.Contains(file.Body, "one worker") {
		t.Fatalf("historical assertion regressed Knowledge: %s", file.Body)
	}
}

func TestActiveConflictCreatesPendingSuccessor(t *testing.T) {
	dataStore := newBrewStore(t)
	old := writeAssertionFeedstock(t, dataStore, "fs-active-old", time.Now().Add(-time.Hour).UTC(), []domain.Assertion{
		testAssertion("as-active-old", "knowbrew", "The format is old."),
	})
	writeKnowledge(t, dataStore, "kn-active", old, old.Assertions[0], true)
	current := writeAssertionFeedstock(t, dataStore, "fs-active-new", time.Now().UTC(), []domain.Assertion{
		testAssertion("as-active-new", "knowbrew", "The format is new."),
	})
	setInvocation(t, dataStore, current.ID, "as-active-new", "inv-active-conflict")
	_, _ = catalogForTest(dataStore, "knowbrew")
	_, _ = showForTest(dataStore, []string{"kn-active"})
	result, err := submitForTest(context.Background(), dataStore, SubmitInput{
		FeedstockID: current.ID, AssertionID: "as-active-new", Verification: VerificationVerified,
		Resolution: &ResolutionInput{Kind: ResolutionConflicts, KnowledgeIDs: []string{"kn-active"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.KnowledgeID == "kn-active" || result.Outcome != "replaced" {
		t.Fatalf("result = %#v", result)
	}
	predecessor, _ := dataStore.FindKnowledge("kn-active")
	successor, _ := dataStore.FindKnowledge(result.KnowledgeID)
	if predecessor.Knowledge.Status != domain.StatusActive || predecessor.Knowledge.SupersededBy != "" ||
		successor.Knowledge.Status != domain.StatusPending ||
		!slices.Equal(successor.Knowledge.Supersedes, []string{"kn-active"}) {
		t.Fatalf("predecessor = %#v, successor = %#v", predecessor.Knowledge, successor.Knowledge)
	}
}

func TestCatalogUsesPendingBrewHeadInsteadOfItsActivePredecessor(t *testing.T) {
	dataStore := newBrewStore(t)
	old := writeAssertionFeedstock(t, dataStore, "fs-head-old", time.Now().Add(-time.Hour).UTC(), []domain.Assertion{
		testAssertion("as-head-old", "knowbrew", "The active form is old."),
	})
	writeKnowledge(t, dataStore, "kn-head-active", old, old.Assertions[0], true)
	proposal := writeAssertionFeedstock(t, dataStore, "fs-head-new", time.Now().UTC(), []domain.Assertion{
		testAssertion("as-head-new", "knowbrew", "The proposed form is new."),
	})
	knowledge := domain.NewKnowledgeFromAssertion(proposal, proposal.Assertions[0], proposal.Timestamp)
	knowledge.ID = "kn-head-pending"
	knowledge.Supersedes = []string{"kn-head-active"}
	if err := dataStore.WriteNewKnowledge(
		knowledge.ID, knowledge, "## Claim\n\nThe proposed form is new.",
	); err != nil {
		t.Fatal(err)
	}
	setInvocation(t, dataStore, proposal.ID, "as-head-new", "inv-brew-head")
	entries, err := catalogForTest(dataStore, "knowbrew")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].ID != "kn-head-pending" {
		t.Fatalf("catalog = %#v", entries)
	}
}

func TestApplyRejectsStaleCatalogWithoutChangingFeedstock(t *testing.T) {
	dataStore := newBrewStore(t)
	old := writeAssertionFeedstock(t, dataStore, "fs-stale-old", time.Now().Add(-time.Hour).UTC(), []domain.Assertion{
		testAssertion("as-stale-old", "knowbrew", "The original statement is current."),
	})
	writeKnowledge(t, dataStore, "kn-stale", old, old.Assertions[0], false)
	current := writeAssertionFeedstock(t, dataStore, "fs-stale-new", time.Now().UTC(), []domain.Assertion{
		testAssertion("as-stale-new", "knowbrew", "The original statement is current."),
	})
	setInvocation(t, dataStore, current.ID, "as-stale-new", "inv-stale")
	if _, err := catalogForTest(dataStore, "knowbrew"); err != nil {
		t.Fatal(err)
	}
	if _, err := showForTest(dataStore, []string{"kn-stale"}); err != nil {
		t.Fatal(err)
	}
	path, _ := dataStore.KnowledgePath("kn-stale")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	data = []byte(strings.Replace(string(data), "The original statement is current.", "A human-edited statement is current.", 1))
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	reads, err := invocation.CurrentReadState(dataStore.Root)
	if err != nil {
		t.Fatal(err)
	}
	_, err = applyForTest(context.Background(), dataStore, SubmitInput{
		FeedstockID: current.ID, AssertionID: "as-stale-new", Verification: VerificationVerified,
		Resolution: &ResolutionInput{Kind: ResolutionEquivalent, KnowledgeIDs: []string{"kn-stale"}},
	}, agent.ReadState{
		Subject: reads.Subject, Catalog: reads.Catalog,
		CatalogDigest: reads.CatalogDigest, Inspected: reads.Inspected,
	})
	if !errors.Is(err, ErrStaleDecision) {
		t.Fatalf("stale error = %v", err)
	}
	updated, _, err := dataStore.FindFeedstock(current.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(updated.BrewedAssertions) != 0 || updated.BrewedAt != nil {
		t.Fatalf("stale decision changed Feedstock: %#v", updated)
	}
}

func TestAssertionPromptIncludesSourceContextAndSemanticSubjectWithoutAliases(t *testing.T) {
	dataStore := newBrewStore(t)
	writingDirectory := filepath.Join(dataStore.Root, "masters", "writing")
	for name, content := range map[string]string{
		"common.md":    "COMMON WRITING RULE",
		"knowledge.md": "KNOWLEDGE WRITING RULE",
		"document.md":  "DOCUMENT WRITING RULE",
	} {
		if err := os.WriteFile(filepath.Join(writingDirectory, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := dataStore.EnsureMaster("subjects", domain.MasterEntry{
		Name: "agent-model", Definition: "Model-specific agent behavior.",
		Includes: []string{"model behavior"}, Excludes: []string{"prompt architecture"},
		Aliases: []string{"/private/machine/path"},
	}); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(dataStore.Root, "session.jsonl")
	writeClaudeDialogue(t, source, "turn-prompt", "Use model-specific defaults.", "Verified model behavior.")
	when := time.Now().UTC()
	annotated := when
	feedstock := domain.Feedstock{
		Schema: domain.SchemaVersion, ID: "fs-prompt", TurnID: "turn-prompt",
		Session: domain.SessionRef{ID: "session", Path: source}, Timestamp: when,
		Agent: "claude", Summary: "summary", AnnotatedAt: &annotated,
		Types:      []domain.KnowledgeType{"property"},
		Assertions: []domain.Assertion{testAssertion("as-prompt", "agent-model", "Model defaults differ.")},
	}
	if err := dataStore.WriteFeedstock(feedstock); err != nil {
		t.Fatal(err)
	}
	prompt, warnings, err := assertionPromptForTest(dataStore, testConfig(dataStore.Root), []domain.Feedstock{feedstock}, feedstock, feedstock.Assertions[0])
	if err != nil || len(warnings) != 0 {
		t.Fatalf("prompt err = %v, warnings = %#v", err, warnings)
	}
	for _, required := range []string{
		"Use model-specific defaults.", "Verified model behavior.",
		"COMMON WRITING RULE", "KNOWLEDGE WRITING RULE",
		`"definition": "Model-specific agent behavior."`,
		`"includes"`, `"excludes"`, "Source verification", "Type qualification",
		"knowledge_type_master as the sole authority", "Do not apply a separate hard-coded category",
		"Subject candidates", "Full inspection",
		"Verify statement and rationale independently",
		"merely say the user requested, specified, confirmed, or explicitly stated",
		"same type, subject, and statement with an empty rationale",
		"A Knowledge unit answers one independently maintainable question",
		"peer item on the same mapping axis",
		"choose new even when it is closely related",
		"do not use it to repeat the statement, mapping, or source history",
	} {
		if !strings.Contains(prompt, required) {
			t.Fatalf("prompt missing %q:\n%s", required, prompt)
		}
	}
	if strings.Contains(prompt, "/private/machine/path") {
		t.Fatalf("machine alias leaked into semantic prompt:\n%s", prompt)
	}
	if strings.Contains(prompt, "DOCUMENT WRITING RULE") {
		t.Fatalf("brew prompt contains document-only writing rules:\n%s", prompt)
	}
	for _, forbidden := range []string{
		"applies_when", "task-local progress", "a workflow",
		"Use concise natural prose for a single proposition",
		"short lead sentence followed by a Markdown bullet list",
		"never serialize peer items into one sentence by chaining conjunctions",
		"Do not put headings inside statement",
	} {
		if strings.Contains(prompt, forbidden) {
			t.Fatalf("prompt contains obsolete qualification %q:\n%s", forbidden, prompt)
		}
	}
}

func TestExistingKnowledgeReferenceDoesNotRepairBrewProgress(t *testing.T) {
	dataStore := newBrewStore(t)
	feedstock := writeAssertionFeedstock(t, dataStore, "fs-repair", time.Now().UTC(), []domain.Assertion{
		testAssertion("as-repair", "knowbrew", "Evidence records support recovery."),
	})
	writeKnowledge(t, dataStore, "kn-repair", feedstock, feedstock.Assertions[0], false)
	updated, _, _ := dataStore.FindFeedstock(feedstock.ID)
	if len(updated.BrewedAssertions) != 0 || updated.BrewedAt != nil {
		t.Fatalf("partial Knowledge was incorrectly accepted as completed: %#v", updated)
	}
}

func newBrewStore(t *testing.T) *store.Store {
	t.Helper()
	dataStore, err := store.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := dataStore.EnsureLayout(); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"knowbrew", "other"} {
		if _, err := dataStore.EnsureMaster("subjects", domain.MasterEntry{Name: name}); err != nil {
			t.Fatal(err)
		}
	}
	return dataStore
}

func testConfig(root string) config.Config {
	return config.Config{
		Root: root,
		Draw: config.Draw{Concurrency: 1, ContextTurns: 3},
		LLM:  config.LLM{Backend: "codex-cli", BrewModel: "brew-model"},
	}
}

func testAssertion(id, subject, statement string) domain.Assertion {
	return domain.Assertion{
		ID: id, Type: "property", Subject: subject,
		Statement: statement,
	}
}

func writeAssertionFeedstock(
	t *testing.T,
	dataStore *store.Store,
	id string,
	when time.Time,
	assertions []domain.Assertion,
) domain.Feedstock {
	t.Helper()
	annotated := when
	types := make([]domain.KnowledgeType, 0, len(assertions))
	for _, assertion := range assertions {
		types = append(types, assertion.Type)
	}
	types, err := domain.NormalizeKnowledgeTypes(types)
	if err != nil {
		t.Fatal(err)
	}
	feedstock := domain.Feedstock{
		Schema: domain.SchemaVersion, ID: id, TurnID: "turn-" + id,
		Session:   domain.SessionRef{ID: "session", Path: filepath.Join(dataStore.Root, id+".jsonl")},
		Timestamp: when, Agent: "claude", Types: types, Summary: "summary",
		AnnotatedAt: &annotated, Assertions: assertions,
	}
	writeClaudeDialogue(t, feedstock.Session.Path, feedstock.TurnID, "user source", "assistant source")
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
	assertion domain.Assertion,
	approved bool,
) {
	t.Helper()
	knowledge := domain.NewKnowledgeFromAssertion(feedstock, assertion, feedstock.Timestamp)
	knowledge.ID = id
	if err := dataStore.WriteNewKnowledge(id, knowledge, "## Claim\n\n"+assertion.Statement); err != nil {
		t.Fatal(err)
	}
	if approved {
		path, err := dataStore.KnowledgePath(id)
		if err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		updated := strings.Replace(string(data), "approved: false", "approved: true", 1)
		if err := os.WriteFile(path, []byte(updated), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func setInvocation(t *testing.T, dataStore *store.Store, feedstockID, assertionID, invocationID string) {
	t.Helper()
	t.Setenv(config.InvocationIDEnvironment, invocationID)
	t.Setenv(config.InvocationFeedstockEnvironment, feedstockID)
	t.Setenv(config.InvocationAssertionEnvironment, assertionID)
	invocation.Cleanup(dataStore.Root, invocationID)
}

func writeClaudeDialogue(t *testing.T, path, turnID, user, assistant string) {
	t.Helper()
	lines := []map[string]any{
		{
			"type": "user", "uuid": turnID, "sessionId": "session", "timestamp": time.Now().UTC(),
			"message": map[string]any{"role": "user", "content": user},
		},
		{
			"type": "assistant", "sessionId": "session", "timestamp": time.Now().UTC(),
			"message": map[string]any{"role": "assistant", "content": []map[string]any{{"type": "text", "text": assistant}}},
		},
	}
	var encoded strings.Builder
	for _, line := range lines {
		data, err := json.Marshal(line)
		if err != nil {
			t.Fatal(err)
		}
		encoded.Write(data)
		encoded.WriteByte('\n')
	}
	if err := os.WriteFile(path, []byte(encoded.String()), 0o644); err != nil {
		t.Fatal(err)
	}
}

type resolvingRunner struct {
	store       *store.Store
	assertionID string
	invocation  string
}

func (runner resolvingRunner) Run(
	_ context.Context,
	task llm.Task,
	_ string,
	_ string,
) (llm.RunResult, error) {
	if task != llm.TaskBrew {
		return llm.RunResult{}, fmt.Errorf("task = %s", task)
	}
	_, digest, err := catalogSnapshot(repositoryForTest(runner.store), "knowbrew")
	if err != nil {
		return llm.RunResult{}, err
	}
	return llm.RunResult{
		Output: json.RawMessage(`{"verification":"verified","corrected_assertion":null,"resolution":{"kind":"new","knowledge_ids":[],"draft":null}}`),
		Usage:  llm.Usage{InputTokens: 100, OutputTokens: 10},
		Reads:  agent.ReadState{Subject: "knowbrew", CatalogDigest: digest},
	}, nil
}

func TestRunProcessesAssertionsOnceAndReportsAssertionProgress(t *testing.T) {
	dataStore := newBrewStore(t)
	feedstock := writeAssertionFeedstock(t, dataStore, "fs-run", time.Now().UTC(), []domain.Assertion{
		testAssertion("as-run", "knowbrew", "Brew resolves one assertion at a time."),
	})
	cfg := testConfig(dataStore.Root)
	var progress strings.Builder
	summary, err := runForTest(context.Background(), cfg, resolvingRunner{
		store: dataStore, assertionID: "as-run", invocation: "inv-run",
	}, &progress)
	if err != nil {
		t.Fatal(err)
	}
	if summary.AssertionsProcessed != 1 || summary.Created != 1 || summary.Usage.TotalTokens != 110 {
		t.Fatalf("summary = %#v", summary)
	}
	if !strings.Contains(progress.String(), "Brewing complete · 1/1 assertions") {
		t.Fatalf("progress = %q", progress.String())
	}
	second, err := runForTest(context.Background(), cfg, resolvingRunner{
		store: dataStore, assertionID: "as-run", invocation: "inv-run-second",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if second.AssertionsProcessed != 0 || second.Created != 0 {
		t.Fatalf("second summary = %#v", second)
	}
	updated, _, _ := dataStore.FindFeedstock(feedstock.ID)
	if updated.BrewedAt == nil {
		t.Fatal("feedstock was not completed")
	}
}

func TestRunMaxProcessesBoundedAssertionsAndReportsBacklog(t *testing.T) {
	dataStore := newBrewStore(t)
	oldest := writeAssertionFeedstock(t, dataStore, "fs-max-old", time.Now().Add(-time.Hour).UTC(), []domain.Assertion{
		testAssertion("as-max-old", "knowbrew", "Brew limits work by assertion."),
	})
	newest := writeAssertionFeedstock(t, dataStore, "fs-max-new", time.Now().UTC(), []domain.Assertion{
		testAssertion("as-max-new", "knowbrew", "A later assertion remains pending."),
	})
	cfg := testConfig(dataStore.Root)
	first, err := runWithOptionsForTest(
		context.Background(), cfg,
		resolvingRunner{store: dataStore}, nil, Options{Max: 1},
	)
	if err != nil {
		t.Fatal(err)
	}
	if first.AssertionsSelected != 1 || first.AssertionsProcessed != 1 || first.AssertionsPending != 1 {
		t.Fatalf("first summary = %#v", first)
	}
	oldestAfter, _, err := dataStore.FindFeedstock(oldest.ID)
	if err != nil {
		t.Fatal(err)
	}
	newestAfter, _, err := dataStore.FindFeedstock(newest.ID)
	if err != nil {
		t.Fatal(err)
	}
	if oldestAfter.BrewedAt == nil || newestAfter.BrewedAt != nil {
		t.Fatalf("oldest brewed_at = %v, newest brewed_at = %v", oldestAfter.BrewedAt, newestAfter.BrewedAt)
	}

	second, err := runWithOptionsForTest(
		context.Background(), cfg,
		resolvingRunner{store: dataStore}, nil, Options{Max: 1},
	)
	if err != nil {
		t.Fatal(err)
	}
	if second.AssertionsSelected != 1 || second.AssertionsProcessed != 1 || second.AssertionsPending != 0 {
		t.Fatalf("second summary = %#v", second)
	}
}

func TestRunRequiresPreBrewIndexSyncAndTreatsPostSyncAsWarning(t *testing.T) {
	t.Run("pre-sync failure stops brew", func(t *testing.T) {
		dataStore := newBrewStore(t)
		index := &recordingSearchIndex{failOn: map[int]error{1: errors.New("pre-sync failed")}}
		_, err := runForTest(context.Background(), testConfig(dataStore.Root), nil, nil, index)
		if err == nil || !strings.Contains(err.Error(), "synchronize search index before brewing") {
			t.Fatalf("pre-sync error = %v", err)
		}
		if index.calls != 1 {
			t.Fatalf("pre-sync calls = %d, want 1", index.calls)
		}
	})

	t.Run("post-sync failure is reported", func(t *testing.T) {
		dataStore := newBrewStore(t)
		index := &recordingSearchIndex{failOn: map[int]error{2: errors.New("post-sync failed")}}
		summary, err := runForTest(context.Background(), testConfig(dataStore.Root), nil, nil, index)
		if err != nil {
			t.Fatal(err)
		}
		if index.calls != 2 {
			t.Fatalf("sync calls = %d, want 2", index.calls)
		}
		if len(summary.Warnings) != 1 || !strings.Contains(summary.Warnings[0].Message, "post-sync failed") {
			t.Fatalf("post-sync warnings = %#v", summary.Warnings)
		}
	})
}
