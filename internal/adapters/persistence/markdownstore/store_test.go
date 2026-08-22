package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/gofrs/flock"
	"github.com/siro33950/knowbrew/internal/domain"
)

func TestFeedstockIsImmutableExceptExtractedAt(t *testing.T) {
	dataStore, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	feedstock := validFeedstock()
	feedstock.Types = nil
	if err := dataStore.WriteFeedstock(feedstock); err != nil {
		t.Fatal(err)
	}
	changed := feedstock
	changed.Summary = "A changed summary."
	if err := dataStore.WriteFeedstock(changed); err == nil {
		t.Fatal("expected immutable feedstock update to fail")
	}
	extractedAt := time.Now().UTC()
	if err := dataStore.WriteExtractedFeedstock(feedstock, extractedAt); err != nil {
		t.Fatal(err)
	}
	stored, _, err := dataStore.FindFeedstock(feedstock.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.ExtractedAt == nil || !stored.ExtractedAt.Equal(extractedAt) {
		t.Fatalf("extracted_at = %v", stored.ExtractedAt)
	}
	later := extractedAt.Add(time.Hour)
	if err := dataStore.WriteExtractedFeedstock(stored, later); err != nil {
		t.Fatal(err)
	}
	stored, _, _ = dataStore.FindFeedstock(feedstock.ID)
	if !stored.ExtractedAt.Equal(extractedAt) {
		t.Fatal("extracted_at was changed after first processing")
	}
}

func TestKnowledgeEstablishedAtUsesNewestSupportingSource(t *testing.T) {
	dataStore, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	older := validFeedstock()
	older.Timestamp = time.Now().UTC().Add(-2 * time.Hour)
	newer := older
	newer.ID = "fs-newer"
	newer.TurnID = "turn-newer"
	newer.Timestamp = older.Timestamp.Add(time.Hour)
	for _, feedstock := range []domain.Feedstock{older, newer} {
		if err := dataStore.WriteFeedstock(feedstock); err != nil {
			t.Fatal(err)
		}
	}
	explicit, err := dataStore.KnowledgeEstablishedAt(domain.Knowledge{
		EstablishedBy: older.ID,
		Feedstocks:    []string{older.ID, newer.ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !explicit.Equal(newer.Timestamp) {
		t.Fatalf("established time = %s, want newest source %s", explicit, newer.Timestamp)
	}
	legacy, err := dataStore.KnowledgeEstablishedAt(domain.Knowledge{
		Feedstocks: []string{older.ID, newer.ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !legacy.Equal(newer.Timestamp) {
		t.Fatalf("legacy established time = %s, want newest evidence %s", legacy, newer.Timestamp)
	}
}

func TestKnowledgeLifecycleAndFeedstocks(t *testing.T) {
	dataStore, _ := New(t.TempDir())
	feedstock := validFeedstock()
	if err := dataStore.WriteFeedstock(feedstock); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	knowledge := domain.Knowledge{
		Created: now, Updated: now, Type: domain.KnowledgeType("property"),
		OrganizedAt: &now,
		Subject:     "subject", Feedstocks: []string{feedstock.ID}, Status: domain.StatusActive,
	}
	if err := dataStore.WriteNewKnowledge("testing-rule", knowledge, "# Testing rule"); err != nil {
		t.Fatal(err)
	}
	path, _ := dataStore.KnowledgePath("testing-rule")
	stored, _, err := dataStore.ReadKnowledge(path)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != domain.StatusPending {
		t.Fatalf("new status = %q, want pending", stored.Status)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "\nstatus:") ||
		!strings.Contains(string(data), "approved: false") {
		t.Fatalf("new knowledge frontmatter =\n%s", data)
	}
	data = []byte(strings.Replace(string(data), "approved: false", "approved: true", 1))
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	stored, _, err = dataStore.ReadKnowledge(path)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != domain.StatusActive {
		t.Fatalf("manually activated status = %q, want active", stored.Status)
	}
	if err := dataStore.InvalidateKnowledge("testing-rule", []string{feedstock.ID}, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	stored, _, err = dataStore.ReadKnowledge(path)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != domain.StatusInvalidated || stored.InvalidatedAt == nil {
		t.Fatalf("invalidated knowledge = %#v", stored)
	}
	if err := dataStore.AddKnowledgeFeedstocks("testing-rule", []string{feedstock.ID}, now); err == nil {
		t.Fatal("expected adding a feedstock to invalidated knowledge to fail")
	}
}

func TestApprovedSuccessorRetiresPredecessorOnLaterReconciliation(t *testing.T) {
	dataStore, _ := New(t.TempDir())
	feedstock := validFeedstock()
	if err := dataStore.WriteFeedstock(feedstock); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	base := domain.Knowledge{
		Created: now, Updated: now, Type: domain.KnowledgeType("property"),
		OrganizedAt: &now,
		Subject:     "subject",
		Feedstocks:  []string{feedstock.ID},
	}
	if err := dataStore.WriteNewKnowledge("old-rule", base, "## Claim\n\nUse the old rule."); err != nil {
		t.Fatal(err)
	}
	if err := dataStore.WriteNewKnowledge("combined-rule", domain.Knowledge{
		Created: now, Updated: now, Type: domain.KnowledgeType("property"),
		OrganizedAt: &now,
		Subject:     "subject",
		Feedstocks:  []string{feedstock.ID}, Supersedes: []string{"old-rule"},
	}, "## Claim\n\nUse the combined rule."); err != nil {
		t.Fatal(err)
	}
	successorPath, _ := dataStore.KnowledgePath("combined-rule")
	data, err := os.ReadFile(successorPath)
	if err != nil {
		t.Fatal(err)
	}
	data = []byte(strings.Replace(string(data), "approved: false", "approved: true", 1))
	if err := os.WriteFile(successorPath, data, 0o644); err != nil {
		t.Fatal(err)
	}
	changed, warnings, err := dataStore.ReconcileKnowledgeLifecycle(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if changed != 1 || len(warnings) != 0 {
		t.Fatalf("changed = %d, warnings = %#v", changed, warnings)
	}
	oldPath, _ := dataStore.KnowledgePath("old-rule")
	old, _, err := dataStore.ReadKnowledge(oldPath)
	if err != nil {
		t.Fatal(err)
	}
	if old.Status != domain.StatusSuperseded ||
		old.SupersededBy != "combined-rule" ||
		old.SupersededAt == nil {
		t.Fatalf("old knowledge = %#v", old)
	}
	oldData, err := os.ReadFile(oldPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(oldData), `superseded_by: "[[combined-rule]]"`) ||
		strings.Contains(string(oldData), "\nstatus:") {
		t.Fatalf("retired frontmatter =\n%s", oldData)
	}
}

func TestPendingSuccessorRetiresAndRestoresPendingPredecessor(t *testing.T) {
	for _, test := range []struct {
		name            string
		changeSuccessor func(*testing.T, *Store, string)
	}{
		{
			name: "deleted",
			changeSuccessor: func(t *testing.T, _ *Store, path string) {
				t.Helper()
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "invalidated",
			changeSuccessor: func(t *testing.T, dataStore *Store, _ string) {
				t.Helper()
				if err := dataStore.InvalidateKnowledge(
					"combined-rule",
					[]string{validFeedstock().ID},
					time.Now().UTC(),
				); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "unlinked",
			changeSuccessor: func(t *testing.T, dataStore *Store, path string) {
				t.Helper()
				knowledge, body, err := dataStore.ReadKnowledge(path)
				if err != nil {
					t.Fatal(err)
				}
				knowledge.Supersedes = nil
				data, err := encodeKnowledge(knowledge, body)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, data, 0o644); err != nil {
					t.Fatal(err)
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			dataStore, _ := New(t.TempDir())
			feedstock := validFeedstock()
			if err := dataStore.WriteFeedstock(feedstock); err != nil {
				t.Fatal(err)
			}
			now := time.Now().UTC()
			base := domain.Knowledge{
				Created: now, Updated: now, Type: domain.KnowledgeType("property"),
				OrganizedAt: &now,
				Subject:     "subject",
				Feedstocks:  []string{feedstock.ID},
			}
			if err := dataStore.WriteNewKnowledge(
				"old-rule",
				base,
				"## Claim\n\nUse the old rule.",
			); err != nil {
				t.Fatal(err)
			}
			if err := dataStore.WriteNewKnowledge("combined-rule", domain.Knowledge{
				Created: now, Updated: now, Type: domain.KnowledgeType("property"),
				OrganizedAt: &now,
				Subject:     "subject",
				Feedstocks:  []string{feedstock.ID}, Supersedes: []string{"old-rule"},
			}, "## Claim\n\nUse the combined rule."); err != nil {
				t.Fatal(err)
			}
			changed, warnings, err := dataStore.ReconcileKnowledgeLifecycle(
				context.Background(),
			)
			if err != nil {
				t.Fatal(err)
			}
			if changed != 1 || len(warnings) != 0 {
				t.Fatalf("retire changed = %d, warnings = %#v", changed, warnings)
			}
			oldPath, _ := dataStore.KnowledgePath("old-rule")
			old, _, err := dataStore.ReadKnowledge(oldPath)
			if err != nil {
				t.Fatal(err)
			}
			if old.Status != domain.StatusSuperseded ||
				old.SupersededBy != "combined-rule" {
				t.Fatalf("retired old knowledge = %#v", old)
			}
			successorPath, _ := dataStore.KnowledgePath("combined-rule")
			test.changeSuccessor(t, dataStore, successorPath)
			changed, warnings, err = dataStore.ReconcileKnowledgeLifecycle(
				context.Background(),
			)
			if err != nil {
				t.Fatal(err)
			}
			if changed != 1 {
				t.Fatalf("restore changed = %d, warnings = %#v", changed, warnings)
			}
			old, _, err = dataStore.ReadKnowledge(oldPath)
			if err != nil {
				t.Fatal(err)
			}
			if old.Status != domain.StatusPending ||
				old.SupersededBy != "" ||
				old.SupersededAt != nil {
				t.Fatalf("restored old knowledge = %#v", old)
			}
		})
	}
}

func TestPendingSuccessorDoesNotRetireApprovedPredecessorUntilApproved(t *testing.T) {
	dataStore, _ := New(t.TempDir())
	feedstock := validFeedstock()
	if err := dataStore.WriteFeedstock(feedstock); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	base := domain.Knowledge{
		Created: now, Updated: now, Type: domain.KnowledgeType("property"),
		OrganizedAt: &now,
		Subject:     "subject",
		Feedstocks:  []string{feedstock.ID},
	}
	if err := dataStore.WriteNewKnowledge(
		"pending-rule",
		base,
		"## Claim\n\nUse the pending rule.",
	); err != nil {
		t.Fatal(err)
	}
	if err := dataStore.WriteNewKnowledge(
		"approved-rule",
		base,
		"## Claim\n\nUse the approved rule.",
	); err != nil {
		t.Fatal(err)
	}
	approvedPath, _ := dataStore.KnowledgePath("approved-rule")
	approvedData, err := os.ReadFile(approvedPath)
	if err != nil {
		t.Fatal(err)
	}
	approvedData = []byte(strings.Replace(
		string(approvedData),
		"approved: false",
		"approved: true",
		1,
	))
	if err := os.WriteFile(approvedPath, approvedData, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := dataStore.WriteNewKnowledge("combined-rule", domain.Knowledge{
		Created: now, Updated: now, Type: domain.KnowledgeType("property"),
		OrganizedAt: &now,
		Subject:     "subject",
		Feedstocks:  []string{feedstock.ID},
		Supersedes:  []string{"pending-rule", "approved-rule"},
	}, "## Claim\n\nUse the combined rule."); err != nil {
		t.Fatal(err)
	}
	changed, warnings, err := dataStore.ReconcileKnowledgeLifecycle(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if changed != 1 || len(warnings) != 0 {
		t.Fatalf("pending reconciliation changed = %d, warnings = %#v", changed, warnings)
	}
	pendingPath, _ := dataStore.KnowledgePath("pending-rule")
	pending, _, err := dataStore.ReadKnowledge(pendingPath)
	if err != nil {
		t.Fatal(err)
	}
	approved, _, err := dataStore.ReadKnowledge(approvedPath)
	if err != nil {
		t.Fatal(err)
	}
	if pending.Status != domain.StatusSuperseded ||
		pending.SupersededBy != "combined-rule" {
		t.Fatalf("pending predecessor = %#v", pending)
	}
	if approved.Status != domain.StatusActive || approved.SupersededBy != "" {
		t.Fatalf("approved predecessor changed early = %#v", approved)
	}
	successorPath, _ := dataStore.KnowledgePath("combined-rule")
	successorData, err := os.ReadFile(successorPath)
	if err != nil {
		t.Fatal(err)
	}
	successorData = []byte(strings.Replace(
		string(successorData),
		"approved: false",
		"approved: true",
		1,
	))
	if err := os.WriteFile(successorPath, successorData, 0o644); err != nil {
		t.Fatal(err)
	}
	changed, warnings, err = dataStore.ReconcileKnowledgeLifecycle(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if changed != 1 || len(warnings) != 0 {
		t.Fatalf("approved reconciliation changed = %d, warnings = %#v", changed, warnings)
	}
	approved, _, err = dataStore.ReadKnowledge(approvedPath)
	if err != nil {
		t.Fatal(err)
	}
	if approved.Status != domain.StatusSuperseded ||
		approved.SupersededBy != "combined-rule" {
		t.Fatalf("approved predecessor after activation = %#v", approved)
	}
}

func TestLegacyKnowledgeStatusIsReadWithoutRewritingTheFile(t *testing.T) {
	dataStore, _ := New(t.TempDir())
	feedstock := validFeedstock()
	if err := dataStore.WriteFeedstock(feedstock); err != nil {
		t.Fatal(err)
	}
	if err := dataStore.EnsureLayout(); err != nil {
		t.Fatal(err)
	}
	path, _ := dataStore.KnowledgePath("legacy-active-rule")
	legacy := fmt.Sprintf(`---
created: 2026-07-30T15:02:50Z
updated: 2026-07-30T15:02:50Z
organized_at: 2026-07-30T15:02:50Z
type: "[[property]]"
subject: "[[subject]]"
feedstocks:
  - "[[%s]]"
status: active
---

## Claim

Read legacy active knowledge.
`, feedstock.ID)
	if err := os.WriteFile(path, []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}
	knowledge, _, err := dataStore.ReadKnowledge(path)
	if err != nil {
		t.Fatal(err)
	}
	if !knowledge.Approved || knowledge.Status != domain.StatusActive {
		t.Fatalf("legacy knowledge = %#v", knowledge)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != legacy {
		t.Fatalf("legacy file was rewritten:\n%s", after)
	}
}

func TestMasterFilesUseFilenameIdentityAndAcceptLegacyFrontmatter(t *testing.T) {
	dataStore, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := dataStore.EnsureLayout(); err != nil {
		t.Fatal(err)
	}
	created, err := dataStore.EnsureMaster("subjects", domain.MasterEntry{
		Name:       "knowbrew",
		Definition: "The knowbrew command-line subject.",
		Includes:   []string{"knowledge and feedstock behavior"},
		Excludes:   []string{"unrelated command-line tools"},
		Aliases:    []string{"/work/knowbrew"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !created {
		t.Fatal("subject master was not created")
	}
	subjectPath := filepath.Join(dataStore.Root, "masters", "subjects", "knowbrew.md")
	data, err := os.ReadFile(subjectPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, forbidden := range []string{"\nname:", "\nstatus:", "\ncreated:", "\nupdated:"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("new master contains %q:\n%s", forbidden, text)
		}
	}
	for _, required := range []string{
		"definition: The knowbrew command-line subject.",
		"includes:",
		"- knowledge and feedstock behavior",
		"excludes:",
		"- unrelated command-line tools",
		"aliases:",
		"- /work/knowbrew",
	} {
		if !strings.Contains(text, required) {
			t.Fatalf("new master does not contain %q:\n%s", required, text)
		}
	}
	legacyPath := filepath.Join(dataStore.Root, "masters", "subjects", "renamed-subject.md")
	legacy := `---
name: stale-frontmatter-name
definition: A subject whose note was renamed.
status: invalidated
created: 2026-07-30T15:02:50Z
updated: 2026-07-30T15:02:50Z
---
`
	if err := os.WriteFile(legacyPath, []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}
	subjects, warnings, err := dataStore.LoadMasters("subjects")
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 0 {
		t.Fatalf("legacy master warnings = %#v", warnings)
	}
	if len(subjects) != 2 || subjects[1].Name != "renamed-subject" ||
		subjects[1].Definition != "A subject whose note was renamed." ||
		strings.Join(subjects[0].Includes, ",") != "knowledge and feedstock behavior" ||
		strings.Join(subjects[0].Excludes, ",") != "unrelated command-line tools" {
		t.Fatalf("legacy master = %#v", subjects)
	}
}

func TestEnsureSubjectMasterMergesAliasesAndPreservesContent(t *testing.T) {
	dataStore, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := dataStore.EnsureLayout(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dataStore.Root, "masters", "subjects", "knowbrew.md")
	original := `---
definition: The knowbrew subject.
example: A knowbrew example.
aliases:
  - /old/worktree
---

Human-maintained body.
`
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	created, err := dataStore.EnsureMaster("subjects", domain.MasterEntry{
		Name:       "knowbrew",
		Definition: "A replacement definition that must not be used.",
		Aliases: []string{
			"/old/worktree",
			"/new/worktree",
			"ssh://git@github.com/siro33950/knowbrew.git",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if created {
		t.Fatal("existing subject master was reported as created")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, required := range []string{
		"definition: The knowbrew subject.",
		"example: A knowbrew example.",
		"- /old/worktree",
		"- /new/worktree",
		"- ssh://git@github.com/siro33950/knowbrew.git",
		"Human-maintained body.",
	} {
		if !strings.Contains(text, required) {
			t.Fatalf("updated master does not contain %q:\n%s", required, text)
		}
	}
	if strings.Contains(text, "replacement definition") {
		t.Fatalf("existing definition was replaced:\n%s", text)
	}
}

func TestDefaultTypeMastersAreGeneratedOnlyWhenEmpty(t *testing.T) {
	dataStore, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := dataStore.EnsureLayout(); err != nil {
		t.Fatal(err)
	}
	types, warnings, err := dataStore.LoadMasters("types")
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 0 || len(types) != 8 {
		t.Fatalf("default types = %#v, warnings = %#v", types, warnings)
	}
	want := map[string]struct {
		definition string
		example    string
		excludes   []string
	}{
		"definition": {
			"The established meaning or boundary of a term or concept.",
			"A feedstock is an immutable record of one source turn.",
			[]string{
				"Governing policies, choices, or operating practices established as decisions.",
				"Intended outcomes or desired qualities.",
				"Personal or group preferences.",
				"Temporary task terminology, implementation notes, assignments, or one-time work instructions.",
			},
		},
		"property": {"A durable established attribute or capability of a subject.", "The service accepts JSON Lines input.", nil},
		"relation": {
			"An established relationship between two or more subjects or concepts.",
			"The archive belongs to the research collection.",
			[]string{
				"Governing policies, choices, or operating practices established as decisions.",
				"Intended outcomes or desired qualities.",
				"Personal or group preferences.",
				"Temporary task dependencies, implementation steps, assignments, or one-time work instructions.",
			},
		},
		"principle": {
			"An established generalized causal relationship, mechanism, or recurring tendency.",
			"Higher fermentation temperatures generally accelerate fermentation.",
			[]string{
				"Governing policies, choices, or operating practices established as decisions.",
				"Intended outcomes or desired qualities.",
				"Personal or group preferences.",
				"Temporary observations, task progress, implementation steps, assignments, or one-time work instructions.",
			},
		},
		"constraint": {
			"An established limit or required condition imposed by something other than a choice recorded here.",
			"The venue cannot admit more than 200 people.",
			[]string{
				"Governing policies, choices, or operating practices established as decisions.",
				"Intended outcomes or desired qualities.",
				"Personal or group preferences.",
				"Temporary task limits, implementation steps, assignments, or one-time work instructions.",
			},
		},
		"decision":   {"An established policy, rule, design direction, or operating practice that governs future behavior until changed.", "The local rebuildable index uses SQLite.", nil},
		"intent":     {"A durable intended outcome or quality that explains why a subject, rule, or design exists, independently of the current means used to achieve it.", "Feedstock classification remains consistent with its type candidates so unclassified records are not presented as ready for extraction.", nil},
		"preference": {"A stable stated preference of a person or group, rather than a one-time request or binding decision.", "The user prefers concise headings.", nil},
	}
	for _, entry := range types {
		expected, ok := want[entry.Name]
		if !ok {
			t.Fatalf("unexpected default type master = %#v", entry)
		}
		if entry.Definition != expected.definition || entry.Example != expected.example {
			t.Fatalf("default type master %q = %#v", entry.Name, entry)
		}
		if expected.excludes != nil && !slices.Equal(entry.Excludes, expected.excludes) {
			t.Fatalf("default type master %q excludes = %#v", entry.Name, entry.Excludes)
		}
		if entry.Name == "decision" {
			for _, exclusion := range []string{
				"Proposals or tentative options that have not been adopted.",
				"One-time tasks, implementation steps, and instructions limited to the current work.",
				"Intended outcomes or desired qualities without a governing policy, rule, design direction, or operating practice.",
				"Limits or required conditions imposed externally rather than established as a governing direction.",
			} {
				if !slices.Contains(entry.Excludes, exclusion) {
					t.Fatalf("default decision type does not exclude %q: %#v", exclusion, entry)
				}
			}
		}
		if entry.Name == "preference" {
			for _, inclusion := range []string{
				"Communication style preferences, such as language, tone, or level of detail.",
				"Response format preferences, such as bullet points versus prose, code examples, or use of tables.",
				"Stated background knowledge or expertise that shapes how much explanation is wanted.",
				"Preferences for tools, environments, or technology choices.",
				"Preferences about how work should proceed, such as how confirmations, reviews, or questions are handled.",
				"Standing statements of what a person does not want.",
			} {
				if !slices.Contains(entry.Includes, inclusion) {
					t.Fatalf("default preference type does not include %q: %#v", inclusion, entry)
				}
			}
			for _, exclusion := range []string{
				"Binding rules, policies, design directions, or operating practices established by an authorized instruction.",
				"One-time reactions, feedback, or requests limited to the current work.",
			} {
				if !slices.Contains(entry.Excludes, exclusion) {
					t.Fatalf("default preference type does not exclude %q: %#v", exclusion, entry)
				}
			}
		}
		delete(want, entry.Name)
	}
	if len(want) != 0 {
		t.Fatalf("missing default type masters = %#v", want)
	}

	typeDir := filepath.Join(dataStore.Root, "masters", "types")
	entries, err := os.ReadDir(typeDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Name() != "property.md" {
			if err := os.Remove(filepath.Join(typeDir, entry.Name())); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := dataStore.EnsureLayout(); err != nil {
		t.Fatal(err)
	}
	types, warnings, err = dataStore.LoadMasters("types")
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 0 || len(types) != 1 || types[0].Name != "property" {
		t.Fatalf("types after leaving one master = %#v, warnings = %#v", types, warnings)
	}
}

func TestDefaultTemplateMastersAreGeneratedOnlyWhenEmpty(t *testing.T) {
	dataStore, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := dataStore.EnsureLayout(); err != nil {
		t.Fatal(err)
	}

	templateDir := filepath.Join(dataStore.Root, "masters", "templates")
	entries, err := os.ReadDir(templateDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("layout generated templates outside init: %#v", entries)
	}
	if err := dataStore.EnsureDefaultTemplates(); err != nil {
		t.Fatal(err)
	}
	want := map[string][]string{
		"concept.md": {
			"description:", "output: concept.md", "purpose:", "readers:",
			"covers:", "excludes:", "completion:", "# {{subject}}", "## {{central concepts heading}}",
		},
		"decisions.md": {
			"description:", "output: decisions.md", "purpose:", "readers:",
			"covers:", "excludes:", "completion:", "## {{decision area}}", "**{{rationale label}}:**",
		},
		"glossary.md": {
			"description:", "output: glossary.md", "purpose:", "readers:",
			"covers:", "excludes:", "completion:", "## {{term}}", "**{{distinction label}}:**", "**{{related terms label}}:**",
		},
		"reference.md": {
			"description:", "output: reference.md", "purpose:", "readers:",
			"covers:", "excludes:", "completion:", "## {{reference area}}", "### {{reference item}}",
		},
	}
	entries, err = os.ReadDir(templateDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != len(want) {
		t.Fatalf("default template count = %d, want %d", len(entries), len(want))
	}
	for _, entry := range entries {
		required, ok := want[entry.Name()]
		if entry.IsDir() || !ok {
			t.Fatalf("unexpected default template = %#v", entry)
		}
		data, err := os.ReadFile(filepath.Join(templateDir, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		for _, value := range required {
			if !strings.Contains(string(data), value) {
				t.Fatalf("template %s does not contain %q:\n%s", entry.Name(), value, data)
			}
		}
	}

	custom := []byte("---\ndescription: Custom concept template.\n---\n\n# Custom\n")
	for _, entry := range entries {
		path := filepath.Join(templateDir, entry.Name())
		if entry.Name() == "concept.md" {
			if err := os.WriteFile(path, custom, 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
	}
	if err := dataStore.EnsureDefaultTemplates(); err != nil {
		t.Fatal(err)
	}
	entries, err = os.ReadDir(templateDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "concept.md" {
		t.Fatalf("templates after leaving one master = %#v", entries)
	}
	data, err := os.ReadFile(filepath.Join(templateDir, "concept.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != string(custom) {
		t.Fatalf("custom template was changed:\n%s", data)
	}
}

func TestKnowledgeTypeValidationUsesMasterFiles(t *testing.T) {
	dataStore, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := dataStore.EnsureLayout(); err != nil {
		t.Fatal(err)
	}
	typeDir := filepath.Join(dataStore.Root, "masters", "types")
	entries, err := os.ReadDir(typeDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if err := os.Remove(filepath.Join(typeDir, entry.Name())); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := dataStore.EnsureMaster("types", domain.MasterEntry{
		Name:       "observation",
		Definition: "A verified observation.",
		Example:    "The service returned HTTP 204.",
	}); err != nil {
		t.Fatal(err)
	}
	if err := dataStore.ValidateKnowledgeType("observation"); err != nil {
		t.Fatal(err)
	}
	if err := dataStore.ValidateKnowledgeType("property"); err == nil ||
		!strings.Contains(err.Error(), "not defined in masters/types") {
		t.Fatalf("removed type error = %v", err)
	}
	data, err := os.ReadFile(filepath.Join(typeDir, "observation.md"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, required := range []string{
		"definition: A verified observation.",
		"example: The service returned HTTP 204.",
	} {
		if !strings.Contains(text, required) {
			t.Fatalf("type master does not contain %q:\n%s", required, text)
		}
	}
}

func TestMasterReferencesWriteAsWikilinksAndReadAsPlainNames(t *testing.T) {
	dataStore, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	feedstock := validFeedstock()
	feedstock.Types = []domain.KnowledgeType{"property"}
	if err := dataStore.WriteFeedstock(feedstock); err != nil {
		t.Fatal(err)
	}
	feedstockPath, err := dataStore.FeedstockPath(feedstock)
	if err != nil {
		t.Fatal(err)
	}
	feedstockData, err := os.ReadFile(feedstockPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(feedstockData), "\ntopics:") {
		t.Fatalf("feedstock contains removed topics field:\n%s", feedstockData)
	}
	for _, required := range []string{
		`- "[[property]]"`,
	} {
		if !strings.Contains(string(feedstockData), required) {
			t.Fatalf("feedstock does not contain %q:\n%s", required, feedstockData)
		}
	}
	storedFeedstock, err := dataStore.ReadFeedstock(feedstockPath)
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(storedFeedstock.Types) != "[property]" {
		t.Fatalf("linked feedstock decoded as %#v", storedFeedstock)
	}
	rawFeedstock := strings.NewReplacer(
		`"[[property]]"`, "property",
	).Replace(string(feedstockData))
	if err := os.WriteFile(feedstockPath, []byte(rawFeedstock), 0o644); err != nil {
		t.Fatal(err)
	}
	storedFeedstock, err = dataStore.ReadFeedstock(feedstockPath)
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(storedFeedstock.Types) != "[property]" {
		t.Fatalf("plain feedstock decoded as %#v", storedFeedstock)
	}

	now := time.Now().UTC()
	knowledge := domain.Knowledge{
		Created: now, Updated: now, Type: domain.KnowledgeType("property"),
		OrganizedAt: &now,
		Subject:     "subject", Feedstocks: []string{feedstock.ID},
		Status: domain.StatusPending,
	}
	if err := dataStore.WriteNewKnowledge("linked-knowledge", knowledge, "# Linked knowledge"); err != nil {
		t.Fatal(err)
	}
	knowledgePath, err := dataStore.KnowledgePath("linked-knowledge")
	if err != nil {
		t.Fatal(err)
	}
	knowledgeData, err := os.ReadFile(knowledgePath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(knowledgeData), "\ntopics:") {
		t.Fatalf("knowledge contains removed topics field:\n%s", knowledgeData)
	}
	for _, required := range []string{
		`type: "[[property]]"`,
		`subject: "[[subject]]"`,
		`- "[[` + feedstock.ID + `]]"`,
	} {
		if !strings.Contains(string(knowledgeData), required) {
			t.Fatalf("knowledge does not contain %q:\n%s", required, knowledgeData)
		}
	}
	obsoleteKey := "pro" + "ject:"
	if strings.Contains(string(knowledgeData), obsoleteKey) {
		t.Fatalf("knowledge contains obsolete key:\n%s", knowledgeData)
	}
	obsoleteFeedstockKey := "sour" + "ces:"
	if strings.Contains(string(knowledgeData), obsoleteFeedstockKey) {
		t.Fatalf("knowledge contains obsolete provenance key:\n%s", knowledgeData)
	}
	storedKnowledge, _, err := dataStore.ReadKnowledge(knowledgePath)
	if err != nil {
		t.Fatal(err)
	}
	if storedKnowledge.Subject != "subject" ||
		storedKnowledge.Type != "property" ||
		strings.Join(storedKnowledge.Feedstocks, ",") != feedstock.ID {
		t.Fatalf("linked knowledge decoded as %#v", storedKnowledge)
	}
	rawKnowledge := strings.NewReplacer(
		`"[[property]]"`, "property",
		`"[[subject]]"`, "subject",
		`"[[`+feedstock.ID+`]]"`, feedstock.ID,
	).Replace(string(knowledgeData))
	if err := os.WriteFile(knowledgePath, []byte(rawKnowledge), 0o644); err != nil {
		t.Fatal(err)
	}
	storedKnowledge, _, err = dataStore.ReadKnowledge(knowledgePath)
	if err != nil {
		t.Fatal(err)
	}
	if storedKnowledge.Subject != "subject" ||
		storedKnowledge.Type != "property" ||
		strings.Join(storedKnowledge.Feedstocks, ",") != feedstock.ID {
		t.Fatalf("plain knowledge decoded as %#v", storedKnowledge)
	}
}

func TestEnsureLayoutDoesNotCreateTopicMasters(t *testing.T) {
	dataStore, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := dataStore.EnsureLayout(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dataStore.Root, "masters", "topics")
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("removed topic master directory exists or cannot be checked: %v", err)
	}
}

func TestWithLockWaitsUntilHeldLockIsReleased(t *testing.T) {
	dataStore, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := dataStore.EnsureLayout(); err != nil {
		t.Fatal(err)
	}
	lock := flock.New(filepath.Join(dataStore.Root, ".knowbrew", "state", "knowbrew.lock"))
	if err := lock.Lock(); err != nil {
		t.Fatal(err)
	}
	released := make(chan error, 1)
	go func() {
		time.Sleep(100 * time.Millisecond)
		released <- lock.Unlock()
	}()
	started := time.Now()
	err = dataStore.WithLock(context.Background(), func() error {
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := <-released; err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(started); elapsed < 75*time.Millisecond {
		t.Fatalf("store lock did not wait: %s", elapsed)
	}
}

func TestListingsSkipBrokenMarkdownAndCollectWarnings(t *testing.T) {
	dataStore, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := dataStore.EnsureLayout(); err != nil {
		t.Fatal(err)
	}
	feedstock := validFeedstock()
	if err := dataStore.WriteFeedstock(feedstock); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := dataStore.WriteNewKnowledge("valid-knowledge", domain.Knowledge{
		Created: now, Updated: now, Type: domain.KnowledgeType("property"),
		OrganizedAt: &now,
		Subject:     "subject", Feedstocks: []string{feedstock.ID}, Status: domain.StatusPending,
	}, "# Valid knowledge"); err != nil {
		t.Fatal(err)
	}
	if _, err := dataStore.EnsureMaster("subjects", domain.MasterEntry{
		Name: "testing", Definition: "Automated verification.",
	}); err != nil {
		t.Fatal(err)
	}
	brokenPaths := []string{
		filepath.Join(dataStore.Root, "feedstocks", "broken.md"),
		filepath.Join(dataStore.Root, "knowledge", "broken-knowledge.md"),
		filepath.Join(dataStore.Root, "masters", "subjects", "broken-master.md"),
	}
	for _, path := range brokenPaths {
		if err := os.WriteFile(path, []byte("---\nunknown_field: true\n---\nbroken\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	feedstocks, feedstockWarnings, err := dataStore.ListFeedstocks()
	if err != nil {
		t.Fatal(err)
	}
	if len(feedstocks) != 1 || len(feedstockWarnings) != 1 {
		t.Fatalf("feedstocks = %#v, warnings = %#v", feedstocks, feedstockWarnings)
	}
	knowledge, knowledgeWarnings, err := dataStore.ListKnowledge(true)
	if err != nil {
		t.Fatal(err)
	}
	if len(knowledge) != 1 || len(knowledgeWarnings) != 1 {
		t.Fatalf("knowledge = %#v, warnings = %#v", knowledge, knowledgeWarnings)
	}
	masters, masterWarnings, err := dataStore.LoadMasters("subjects")
	if err != nil {
		t.Fatal(err)
	}
	if len(masters) != 1 || len(masterWarnings) != 1 {
		t.Fatalf("masters = %#v, warnings = %#v", masters, masterWarnings)
	}
	for index, warnings := range [][]string{
		{feedstockWarnings[0].Path, feedstockWarnings[0].String()},
		{knowledgeWarnings[0].Path, knowledgeWarnings[0].String()},
		{masterWarnings[0].Path, masterWarnings[0].String()},
	} {
		if warnings[0] != brokenPaths[index] || !strings.HasPrefix(warnings[1], "skipped: ") {
			t.Fatalf("warning %d = %#v", index, warnings)
		}
	}
}

func TestFindFeedstockUsesSentinelForMissingFeedstock(t *testing.T) {
	dataStore, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = dataStore.FindFeedstock("claude-missing-t000001")
	if !errors.Is(err, ErrFeedstockNotFound) {
		t.Fatalf("error = %v, want ErrFeedstockNotFound", err)
	}
}

func TestFeedstockIgnoresSessionPathAndDoesNotWriteIt(t *testing.T) {
	dataStore, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	legacyPath := filepath.Join(t.TempDir(), "legacy.md")
	legacy := `---
schema: 8
id: fs-legacy-path
turn_id: turn-legacy
session:
  id: session-legacy
  path: /old/machine/session.jsonl
timestamp: 2026-07-30T01:00:00Z
agent: claude
types:
  - property
summary: A Feedstock session path is ignored.
drafted_at: 2026-07-30T01:01:00Z
---
`
	if err := os.WriteFile(legacyPath, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	feedstock, err := dataStore.ReadFeedstock(legacyPath)
	if err != nil {
		t.Fatal(err)
	}
	if feedstock.Session.ID != "session-legacy" {
		t.Fatalf("session = %#v", feedstock.Session)
	}

	fresh := validFeedstock()
	if err := dataStore.WriteFeedstock(fresh); err != nil {
		t.Fatal(err)
	}
	path, err := dataStore.FeedstockPath(fresh)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "path:") {
		t.Fatalf("new Feedstock persisted a physical source path:\n%s", data)
	}
}

func validFeedstock() domain.Feedstock {
	draftedAt := time.Date(2026, 7, 30, 1, 1, 0, 0, time.UTC)
	return domain.Feedstock{
		Schema: domain.SchemaVersion, ID: "claude-session-t000001",
		TurnID:    "turn-1",
		Session:   domain.SessionRef{ID: "session"},
		Timestamp: time.Date(2026, 7, 30, 1, 0, 0, 0, time.UTC),
		Agent:     "claude",
		Types:     []domain.KnowledgeType{"property"},
		Summary:   "The user requested testing.", DraftedAt: &draftedAt,
	}
}

func TestReadKnowledgeToleratesLegacyTriggerKey(t *testing.T) {
	dataStore, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := dataStore.EnsureLayout(); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	feedstock := domain.Feedstock{
		Schema: domain.SchemaVersion, ID: "fs-legacy", TurnID: "turn-legacy",
		Session:   domain.SessionRef{ID: "session"},
		Timestamp: now, Agent: "claude",
		Types:   []domain.KnowledgeType{"property"},
		Summary: "Legacy trigger fixture.", DraftedAt: &now,
	}
	if err := dataStore.WriteFeedstock(feedstock); err != nil {
		t.Fatal(err)
	}
	if err := dataStore.WriteNewKnowledge("legacy-rule", domain.Knowledge{
		Created: now, Updated: now, Type: domain.KnowledgeType("property"),
		OrganizedAt: &now,
		Subject:     "subject", Feedstocks: []string{"fs-legacy"},
		Status: domain.StatusPending,
	}, "## Claim\n\nLegacy trigger files stay readable."); err != nil {
		t.Fatal(err)
	}
	path, err := dataStore.KnowledgePath("legacy-rule")
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	updated := strings.Replace(string(data), "approved: false", "approved: false\ntrigger: always", 1)
	if updated == string(data) {
		t.Fatalf("fixture did not gain a trigger key:\n%s", data)
	}
	if err := os.WriteFile(path, []byte(updated), 0o644); err != nil {
		t.Fatal(err)
	}
	knowledge, _, err := dataStore.ReadKnowledge(path)
	if err != nil || knowledge.ID != "legacy-rule" {
		t.Fatalf("knowledge = %#v, error = %v", knowledge, err)
	}
}
