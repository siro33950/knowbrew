package query

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/gofrs/flock"
	"github.com/siro33950/knowbrew/internal/adapters/fsutil"
	"github.com/siro33950/knowbrew/internal/adapters/markdown/frontmatter"
	"github.com/siro33950/knowbrew/internal/adapters/persistence/markdownstore"
	"github.com/siro33950/knowbrew/internal/domain"
)

func TestTargetedSearchVisibilityAndShow(t *testing.T) {
	dataStore := newStore(t)
	now := time.Date(2026, 7, 30, 1, 0, 0, 0, time.UTC)
	feedstock := writeFeedstock(t, dataStore, "claude-session-t000001", now, "focused testing")
	writeKnowledge(t, dataStore, "active-claim", feedstock.ID, domain.StatusActive, "focused testing", "always")
	writeKnowledge(t, dataStore, "pending-claim", feedstock.ID, domain.StatusPending, "focused pending", "")
	writeKnowledge(t, dataStore, "invalidated-claim", feedstock.ID, domain.StatusInvalidated, "focused invalidated", "")

	active, err := Search(context.Background(), dataStore, SearchOptions{
		Target: TargetKnowledge, Keywords: []string{"focused"}, Limit: 10, MaxTokens: 1000,
	})
	if err != nil {
		t.Fatal(err)
	}
	if active.Total != 1 || active.Results[0].ID != "active-claim" ||
		active.Results[0].Subject != "subject" || active.Results[0].Claim != "focused testing" ||
		len(active.Results[0].Subjects) != 0 {
		t.Fatalf("active knowledge search = %#v", active)
	}

	withPending, err := Search(context.Background(), dataStore, SearchOptions{
		Target: TargetKnowledge, Keywords: []string{"focused"}, IncludePending: true,
		Limit: 10, MaxTokens: 1000,
	})
	if err != nil {
		t.Fatal(err)
	}
	if withPending.Total != 2 {
		t.Fatalf("knowledge with pending = %#v", withPending)
	}
	for _, result := range withPending.Results {
		if result.ID == "invalidated-claim" {
			t.Fatalf("unexpected knowledge result = %#v", result)
		}
	}
	withRetired, err := Search(context.Background(), dataStore, SearchOptions{
		Target: TargetKnowledge, Keywords: []string{"focused"}, IncludeRetired: true,
		Limit: 10, MaxTokens: 1000,
	})
	if err != nil {
		t.Fatal(err)
	}
	if withRetired.Total != 2 {
		t.Fatalf("knowledge with retired = %#v", withRetired)
	}
	statuses := map[domain.Status]bool{}
	for _, result := range withRetired.Results {
		statuses[result.Status] = true
	}
	if !statuses[domain.StatusActive] || !statuses[domain.StatusInvalidated] {
		t.Fatalf("retired statuses = %#v", withRetired.Results)
	}

	facts, err := Search(context.Background(), dataStore, SearchOptions{
		Target: TargetFeedstock, Keywords: []string{"focused"}, Limit: 10, MaxTokens: 1000,
	})
	if err != nil {
		t.Fatal(err)
	}
	if facts.Total != 1 || facts.Results[0].ID != feedstock.ID {
		t.Fatalf("feedstock search = %#v", facts)
	}
	filteredFeedstock, err := Search(context.Background(), dataStore, SearchOptions{
		Target: TargetFeedstock, Subject: "subject",
		Limit: 10, MaxTokens: 1000,
	})
	if err != nil {
		t.Fatal(err)
	}
	if filteredFeedstock.Total != 1 ||
		strings.Join(filteredFeedstock.Results[0].Subjects, ",") != "subject" {
		t.Fatalf("plain-name feedstock filter = %#v", filteredFeedstock)
	}
	filteredKnowledge, err := Search(context.Background(), dataStore, SearchOptions{
		Target: TargetKnowledge, Subject: "subject",
		Limit: 10, MaxTokens: 1000,
	})
	if err != nil {
		t.Fatal(err)
	}
	if filteredKnowledge.Total != 1 {
		t.Fatalf("plain-name knowledge filter = %#v", filteredKnowledge)
	}
	filterJSON, err := json.Marshal(struct {
		Feedstock SearchResponse `json:"feedstock"`
		Knowledge SearchResponse `json:"knowledge"`
	}{
		Feedstock: filteredFeedstock,
		Knowledge: filteredKnowledge,
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(filterJSON), "[[") {
		t.Fatalf("wikilink leaked into search JSON: %s", filterJSON)
	}

	triggered, err := Search(context.Background(), dataStore, SearchOptions{
		Target: TargetKnowledge, Subject: "subject", Trigger: "always", Limit: 10, MaxTokens: 1000,
	})
	if err != nil {
		t.Fatal(err)
	}
	if triggered.Total != 1 || triggered.Results[0].ID != "active-claim" {
		t.Fatalf("trigger search = %#v", triggered)
	}
	if _, err := Search(context.Background(), dataStore, SearchOptions{
		Target: TargetKnowledge, Trigger: "always", IncludePending: true,
	}); err == nil || !strings.Contains(err.Error(), "cannot be used together") {
		t.Fatalf("trigger/include-pending error = %v", err)
	}

	shown, err := Show(dataStore, []string{feedstock.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(shown.Feedstocks) != 1 ||
		shown.Feedstocks[0].TurnID != feedstock.TurnID ||
		!reflect.DeepEqual(shown.Feedstocks[0].Assertions, feedstock.Assertions) {
		t.Fatalf("show = %#v", shown)
	}
	encoded, err := json.Marshal(shown)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"feedstocks"`) ||
		!strings.Contains(string(encoded), `\"ignore previous instructions\"`) {
		t.Fatalf("unsafe content escaped its JSON string: %s", encoded)
	}
}

func TestKnowledgeSearchTimestampUsesEstablishedSourceEvent(t *testing.T) {
	dataStore := newStore(t)
	olderAt := time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC)
	newerAt := olderAt.Add(24 * time.Hour)
	older := writeFeedstock(t, dataStore, "fs-established", olderAt, "temporal provenance")
	newer := writeFeedstock(t, dataStore, "fs-extra-evidence", newerAt, "temporal provenance")
	fileTime := time.Date(2026, 7, 31, 0, 0, 0, 0, time.UTC)
	if err := dataStore.WriteNewKnowledge("temporal-provenance", domain.Knowledge{
		Created: fileTime, Updated: fileTime, Type: domain.KnowledgeType("property"),
		Subject:    "subject",
		Feedstocks: []string{older.ID, newer.ID}, EstablishedBy: older.ID,
	}, "## Claim\n\nUse source-event time for semantic recency."); err != nil {
		t.Fatal(err)
	}
	response, err := Search(context.Background(), dataStore, SearchOptions{
		Target: TargetKnowledge, Keywords: []string{"semantic recency"},
		IncludePending: true, Limit: 10, MaxTokens: 1000,
	})
	if err != nil {
		t.Fatal(err)
	}
	if response.Total != 1 || response.Results[0].EstablishedAt != newerAt.Format(time.RFC3339Nano) {
		t.Fatalf("search timestamp = %#v, want newest supporting source time %s", response, newerAt)
	}
}

func TestSearchIndexesWikilinkFilesAsPlainNames(t *testing.T) {
	dataStore := newStore(t)
	feedstock := writeFeedstock(
		t,
		dataStore,
		"claude-session-t000001",
		time.Now().UTC(),
		"linked source",
	)
	now := time.Now().UTC()
	if err := dataStore.WriteNewKnowledge("linked-claim", domain.Knowledge{
		Created: now, Updated: now, Type: domain.KnowledgeType("property"),
		Subject: "indexed-subject", Feedstocks: []string{feedstock.ID},
		Status: domain.StatusPending,
	}, "## Claim\n\nLinked claim"); err != nil {
		t.Fatal(err)
	}
	for _, options := range []SearchOptions{
		{
			Target: TargetFeedstock, Subject: "subject",
			Limit: 10, MaxTokens: 1000,
		},
		{
			Target: TargetKnowledge, Subject: "indexed-subject",
			IncludePending: true, Limit: 10, MaxTokens: 1000,
		},
	} {
		response, err := Search(context.Background(), dataStore, options)
		if err != nil {
			t.Fatal(err)
		}
		if response.Total != 1 || len(response.Results) != 1 {
			t.Fatalf("linked search result = %#v", response)
		}
		data, err := json.Marshal(response)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(data), "[[") {
			t.Fatalf("wikilink leaked into search response: %s", data)
		}
	}
	bySubjectKeyword, err := Search(context.Background(), dataStore, SearchOptions{
		Target: TargetKnowledge, Keywords: []string{"indexed-subject"},
		IncludePending: true, Limit: 10, MaxTokens: 1000,
	})
	if err != nil {
		t.Fatal(err)
	}
	if bySubjectKeyword.Total != 1 || bySubjectKeyword.Results[0].ID != "linked-claim" {
		t.Fatalf("subject keyword search = %#v", bySubjectKeyword)
	}
}

func TestKnowledgeAndFeedstockTypeFilters(t *testing.T) {
	dataStore := newStore(t)
	feedstock := writeFeedstock(
		t,
		dataStore,
		"claude-session-t000001",
		time.Now().UTC(),
		"typed source",
	)
	writeKnowledge(
		t,
		dataStore,
		"typed-knowledge",
		feedstock.ID,
		domain.StatusActive,
		"typed claim",
		"",
	)
	feedstocks, err := Search(context.Background(), dataStore, SearchOptions{
		Target: TargetFeedstock, Type: domain.KnowledgeType("property"), Limit: 10, MaxTokens: 1000,
	})
	if err != nil {
		t.Fatal(err)
	}
	if feedstocks.Total != 1 ||
		fmt.Sprint(feedstocks.Results[0].Types) != "[property]" {
		t.Fatalf("feedstock type results = %#v", feedstocks)
	}
	knowledge, err := Search(context.Background(), dataStore, SearchOptions{
		Target: TargetKnowledge, Type: domain.KnowledgeType("property"), Limit: 10, MaxTokens: 1000,
	})
	if err != nil {
		t.Fatal(err)
	}
	if knowledge.Total != 1 || knowledge.Results[0].Type != domain.KnowledgeType("property") {
		t.Fatalf("knowledge type results = %#v", knowledge)
	}
	none, err := Search(context.Background(), dataStore, SearchOptions{
		Target: TargetFeedstock, Type: domain.KnowledgeType("decision"), Limit: 10, MaxTokens: 1000,
	})
	if err != nil {
		t.Fatal(err)
	}
	if none.Total != 0 {
		t.Fatalf("unexpected decision results = %#v", none)
	}
	if _, err := Search(context.Background(), dataStore, SearchOptions{
		Target: TargetFeedstock, Type: "other",
	}); err == nil || !strings.Contains(err.Error(), "not defined in masters/types") {
		t.Fatalf("invalid type error = %v", err)
	}
}

func TestShowRawPaginatesCompleteDialogueAsJSONStringValues(t *testing.T) {
	dataStore := newStore(t)
	logPath := filepath.Join(t.TempDir(), "session.jsonl")
	userText := strings.Repeat("あ", 5000)
	assistantText := strings.Repeat("response", 1800)
	content := fmt.Sprintf(
		"{\"type\":\"user\",\"uuid\":\"turn-raw\",\"sessionId\":\"session\",\"timestamp\":\"2026-07-30T01:00:00Z\",\"message\":{\"role\":\"user\",\"content\":%q}}\n"+
			"{\"type\":\"assistant\",\"sessionId\":\"session\",\"timestamp\":\"2026-07-30T01:00:01Z\",\"message\":{\"role\":\"assistant\",\"content\":[{\"type\":\"text\",\"text\":%q}]}}\n",
		userText,
		assistantText,
	)
	if err := os.WriteFile(logPath, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	feedstock := domain.Feedstock{
		Schema: domain.SchemaVersion,
		ID:     "fs-raw-dialogue",
		TurnID: "turn-raw",
		Session: domain.SessionRef{
			ID:   "session",
			Path: logPath,
		},
		Timestamp: time.Now().UTC(),
		Agent:     "claude",
	}
	if err := dataStore.WriteFeedstock(feedstock); err != nil {
		t.Fatal(err)
	}

	dialogue, err := ExtractRawDialogue(dataStore, feedstock.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(dialogue) != 2 ||
		dialogue[0].Role != "user" || dialogue[0].Content != userText ||
		dialogue[1].Role != "assistant" || dialogue[1].Content != assistantText {
		t.Fatalf("extracted raw dialogue = %#v", dialogue)
	}

	first, err := ShowRaw(dataStore, feedstock.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if first.TotalPages < 2 || !first.HasMore || first.Page != 1 ||
		first.FeedstockID != feedstock.ID || first.TurnID != feedstock.TurnID {
		t.Fatalf("first raw page = %#v", first)
	}
	var gotUser, gotAssistant strings.Builder
	for page := 1; page <= first.TotalPages; page++ {
		response, err := ShowRaw(dataStore, feedstock.ID, page)
		if err != nil {
			t.Fatal(err)
		}
		if response.Page != page || response.TotalPages != first.TotalPages ||
			response.HasMore != (page < first.TotalPages) {
			t.Fatalf("raw page %d = %#v", page, response)
		}
		encoded, err := json.Marshal(response)
		if err != nil {
			t.Fatal(err)
		}
		if !json.Valid(encoded) || !strings.Contains(string(encoded), `"content":"`) {
			t.Fatalf("raw page is not JSON-string encoded: %s", encoded)
		}
		for _, message := range response.Messages {
			switch message.Role {
			case "user":
				gotUser.WriteString(message.Content)
			case "assistant":
				gotAssistant.WriteString(message.Content)
			default:
				t.Fatalf("unexpected role %q", message.Role)
			}
		}
	}
	if gotUser.String() != userText || gotAssistant.String() != assistantText {
		t.Fatalf(
			"reconstructed lengths user=%d/%d assistant=%d/%d",
			gotUser.Len(),
			len(userText),
			gotAssistant.Len(),
			len(assistantText),
		)
	}
	if _, err := ShowRaw(dataStore, feedstock.ID, first.TotalPages+1); err == nil ||
		!strings.Contains(err.Error(), "exceeds total pages") {
		t.Fatalf("out-of-range page error = %v", err)
	}
}

func TestShowRawReportsMissingSourceLog(t *testing.T) {
	dataStore := newStore(t)
	feedstock := domain.Feedstock{
		Schema: domain.SchemaVersion,
		ID:     "fs-missing-source",
		TurnID: "turn-missing",
		Session: domain.SessionRef{
			ID:   "session",
			Path: filepath.Join(t.TempDir(), "missing.jsonl"),
		},
		Timestamp: time.Now().UTC(),
		Agent:     "claude",
	}
	if err := dataStore.WriteFeedstock(feedstock); err != nil {
		t.Fatal(err)
	}
	_, err := ShowRaw(dataStore, feedstock.ID, 1)
	if err == nil || !strings.Contains(err.Error(), "source log") ||
		!strings.Contains(err.Error(), "was not found") {
		t.Fatalf("error = %v", err)
	}
}

func TestPaginateDialoguePreservesUTF8WhenRuneExceedsPageSize(t *testing.T) {
	pages := paginateDialogue([]domain.DialogueMessage{
		{Role: "user", Content: "日本語"},
	}, 1)
	var reconstructed strings.Builder
	for _, page := range pages {
		for _, message := range page {
			if !utf8.ValidString(message.Content) {
				t.Fatalf("invalid UTF-8 page: %#v", page)
			}
			reconstructed.WriteString(message.Content)
		}
	}
	if reconstructed.String() != "日本語" {
		t.Fatalf("reconstructed = %q", reconstructed.String())
	}
}

func TestFeedstockChronologyLastAndFilters(t *testing.T) {
	dataStore := newStore(t)
	base := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	for index := 1; index <= 3; index++ {
		writeFeedstock(
			t,
			dataStore,
			fmt.Sprintf("claude-session-t%06d", index),
			base.Add(time.Duration(index)*time.Second),
			fmt.Sprintf("fact %d", index),
		)
	}

	newest, err := Search(context.Background(), dataStore, SearchOptions{
		Target: TargetFeedstock, Limit: 10, MaxTokens: 1000,
	})
	if err != nil {
		t.Fatal(err)
	}
	if newest.Results[0].ID != "claude-session-t000003" {
		t.Fatalf("newest-first results = %#v", newest.Results)
	}

	last, err := Search(context.Background(), dataStore, SearchOptions{
		Target: TargetFeedstock, Last: 2, Limit: 10, MaxTokens: 1000,
		Session: "session", Agent: "claude",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(last.Results) != 2 ||
		last.Results[0].ID != "claude-session-t000002" ||
		last.Results[1].ID != "claude-session-t000003" {
		t.Fatalf("--last results = %#v", last.Results)
	}
	if _, err := Search(context.Background(), dataStore, SearchOptions{
		Target: TargetFeedstock, Last: 1, Keywords: []string{"fact"},
	}); err == nil || !strings.Contains(err.Error(), "cannot be used with keywords") {
		t.Fatalf("--last keyword error = %v", err)
	}
}

func TestSearchSupportsJapaneseEnglishShortTermsAndTokenBudget(t *testing.T) {
	dataStore := newStore(t)
	feedstock := writeFeedstock(
		t,
		dataStore,
		"claude-session-t000001",
		time.Now().UTC(),
		"日本語の全文検索を改善し、Go CLIでreliableな索引を扱う。",
	)
	for _, keyword := range []string{"全文検索", "liable", "検索", "Go"} {
		t.Run(keyword, func(t *testing.T) {
			response, err := Search(context.Background(), dataStore, SearchOptions{
				Target: TargetFeedstock, Keywords: []string{keyword}, Limit: 10, MaxTokens: 1000,
			})
			if err != nil {
				t.Fatal(err)
			}
			if response.Total != 1 || response.Results[0].ID != feedstock.ID ||
				response.Results[0].Score == nil {
				t.Fatalf("response = %#v", response)
			}
		})
	}

	response, err := Search(context.Background(), dataStore, SearchOptions{
		Target: TargetFeedstock, Limit: 10, MaxTokens: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if response.Total != 1 || response.Returned != 0 || !response.Truncated {
		t.Fatalf("token-budget response = %#v", response)
	}
}

func TestIncrementalFeedstockSyncUpdatesClassificationFields(t *testing.T) {
	dataStore := newStore(t)
	feedstock := domain.Feedstock{
		Schema: domain.SchemaVersion, ID: "claude-session-t000001",
		TurnID:    "turn-1",
		Session:   domain.SessionRef{ID: "session", Path: "/logs/session.jsonl"},
		Timestamp: time.Now().UTC(), Agent: "claude", Subjects: []string{"subject"},
		Summary: "classification-summary-keyword",
	}
	if err := dataStore.WriteFeedstock(feedstock); err != nil {
		t.Fatal(err)
	}
	before, err := Search(context.Background(), dataStore, SearchOptions{
		Target: TargetFeedstock, Keywords: []string{"raw-before-classification"},
		Limit: 10, MaxTokens: 1000,
	})
	if err != nil {
		t.Fatal(err)
	}
	if before.Total != 0 {
		t.Fatalf("unannotated search = %#v", before)
	}
	if err := dataStore.AnnotateFeedstock(
		feedstock.ID,
		[]domain.Assertion{{
			ID: "as-classified", Type: "property", Subject: "subject",
			Statement: "assertion-search-keyword remains searchable.",
		}},
		time.Now().UTC(),
	); err != nil {
		t.Fatal(err)
	}
	after, err := Search(context.Background(), dataStore, SearchOptions{
		Target: TargetFeedstock, Keywords: []string{"classification-summary-keyword"},
		Type: domain.KnowledgeType("property"), Limit: 10, MaxTokens: 1000,
	})
	if err != nil {
		t.Fatal(err)
	}
	if after.Total != 1 || after.Results[0].ID != feedstock.ID ||
		after.Results[0].Summary != "classification-summary-keyword" {
		t.Fatalf("classified search = %#v", after)
	}
}

func TestKnowledgeMtimeUpdateAndDeletionAreImmediate(t *testing.T) {
	dataStore := newStore(t)
	now := time.Now().UTC()
	feedstock := writeFeedstock(t, dataStore, "claude-session-t000001", now, "source")
	path := writeKnowledge(t, dataStore, "status-change", feedstock.ID, domain.StatusPending, "status claim", "")

	hidden, err := Search(context.Background(), dataStore, SearchOptions{
		Target: TargetKnowledge, Keywords: []string{"status"}, Limit: 10, MaxTokens: 1000,
	})
	if err != nil {
		t.Fatal(err)
	}
	if hidden.Total != 0 {
		t.Fatalf("pending knowledge was visible: %#v", hidden)
	}

	knowledge, body, err := dataStore.ReadKnowledge(path)
	if err != nil {
		t.Fatal(err)
	}
	knowledge.Approved = true
	knowledge.Status = domain.StatusActive
	if err := writeKnowledgeForTest(path, knowledge, body); err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(path, future, future); err != nil {
		t.Fatal(err)
	}
	visible, err := Search(context.Background(), dataStore, SearchOptions{
		Target: TargetKnowledge, Keywords: []string{"status"}, Limit: 10, MaxTokens: 1000,
	})
	if err != nil {
		t.Fatal(err)
	}
	if visible.Total != 1 || visible.Results[0].ID != "status-change" {
		t.Fatalf("promoted knowledge search = %#v", visible)
	}

	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	deleted, err := Search(context.Background(), dataStore, SearchOptions{
		Target: TargetKnowledge, Keywords: []string{"status"}, Limit: 10, MaxTokens: 1000,
	})
	if err != nil {
		t.Fatal(err)
	}
	if deleted.Total != 0 {
		t.Fatalf("deleted knowledge remained indexed: %#v", deleted)
	}
}

func TestSearchReconcilesDirectApprovalAndHidesSupersededKnowledge(t *testing.T) {
	dataStore := newStore(t)
	now := time.Now().UTC()
	feedstock := writeFeedstock(t, dataStore, "claude-session-t000001", now, "lifecycle")
	writeKnowledge(
		t,
		dataStore,
		"old-lifecycle-rule",
		feedstock.ID,
		domain.StatusActive,
		"lifecycle old rule",
		"",
	)
	if err := dataStore.WriteNewKnowledge("new-lifecycle-rule", domain.Knowledge{
		Created: now, Updated: now, Type: domain.KnowledgeType("property"),
		Subject:    "subject",
		Feedstocks: []string{feedstock.ID},
		Supersedes: []string{"old-lifecycle-rule"},
	}, "## Claim\n\nlifecycle new rule"); err != nil {
		t.Fatal(err)
	}
	newPath, _ := dataStore.KnowledgePath("new-lifecycle-rule")
	successor, body, err := dataStore.ReadKnowledge(newPath)
	if err != nil {
		t.Fatal(err)
	}
	successor.Approved = true
	if err := writeKnowledgeForTest(newPath, successor, body); err != nil {
		t.Fatal(err)
	}
	active, err := Search(context.Background(), dataStore, SearchOptions{
		Target: TargetKnowledge, Keywords: []string{"lifecycle"},
		Limit: 10, MaxTokens: 1000,
	})
	if err != nil {
		t.Fatal(err)
	}
	if active.Total != 1 || active.Results[0].ID != "new-lifecycle-rule" {
		t.Fatalf("active lifecycle results = %#v", active)
	}
	if strings.Join(active.Results[0].Supersedes, ",") != "old-lifecycle-rule" {
		t.Fatalf("active lifecycle lineage = %#v", active.Results[0])
	}
	oldPath, _ := dataStore.KnowledgePath("old-lifecycle-rule")
	old, _, err := dataStore.ReadKnowledge(oldPath)
	if err != nil {
		t.Fatal(err)
	}
	if old.Status != domain.StatusSuperseded ||
		old.SupersededBy != "new-lifecycle-rule" {
		t.Fatalf("old lifecycle knowledge = %#v", old)
	}
	withRetired, err := Search(context.Background(), dataStore, SearchOptions{
		Target: TargetKnowledge, Keywords: []string{"lifecycle"}, IncludeRetired: true,
		Limit: 10, MaxTokens: 1000,
	})
	if err != nil {
		t.Fatal(err)
	}
	if withRetired.Total != 2 {
		t.Fatalf("retired lifecycle results = %#v", withRetired)
	}
}

func TestSearchHidesPendingPredecessorOfPendingSuccessor(t *testing.T) {
	dataStore := newStore(t)
	now := time.Now().UTC()
	feedstock := writeFeedstock(
		t,
		dataStore,
		"claude-session-t000001",
		now,
		"pending lifecycle",
	)
	writeKnowledge(
		t,
		dataStore,
		"old-pending-rule",
		feedstock.ID,
		domain.StatusPending,
		"pending lifecycle old rule",
		"",
	)
	if err := dataStore.WriteNewKnowledge("new-pending-rule", domain.Knowledge{
		Created: now, Updated: now, Type: domain.KnowledgeType("property"),
		Subject:    "subject",
		Feedstocks: []string{feedstock.ID},
		Supersedes: []string{"old-pending-rule"},
	}, "## Claim\n\npending lifecycle new rule"); err != nil {
		t.Fatal(err)
	}
	visible, err := Search(context.Background(), dataStore, SearchOptions{
		Target: TargetKnowledge, Keywords: []string{"pending lifecycle"},
		IncludePending: true, Limit: 10, MaxTokens: 1000,
	})
	if err != nil {
		t.Fatal(err)
	}
	if visible.Total != 1 || visible.Results[0].ID != "new-pending-rule" {
		t.Fatalf("pending lifecycle results = %#v", visible)
	}
	oldPath, _ := dataStore.KnowledgePath("old-pending-rule")
	old, _, err := dataStore.ReadKnowledge(oldPath)
	if err != nil {
		t.Fatal(err)
	}
	if old.Status != domain.StatusSuperseded ||
		old.SupersededBy != "new-pending-rule" {
		t.Fatalf("old pending knowledge = %#v", old)
	}
}

func TestIndexVersionMismatchAndCorruptionRebuildFromSource(t *testing.T) {
	dataStore := newStore(t)
	feedstock := writeFeedstock(t, dataStore, "claude-session-t000001", time.Now().UTC(), "rebuild source")
	options := SearchOptions{
		Target: TargetFeedstock, Keywords: []string{"rebuild"}, Limit: 10, MaxTokens: 1000,
	}
	if _, err := Search(context.Background(), dataStore, options); err != nil {
		t.Fatal(err)
	}
	indexPath := filepath.Join(dataStore.Root, ".knowbrew", "state", "index.sqlite")

	database, err := sql.Open("sqlite", indexPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`PRAGMA user_version=999`); err != nil {
		_ = database.Close()
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	versioned, err := Search(context.Background(), dataStore, options)
	if err != nil {
		t.Fatal(err)
	}
	if versioned.Total != 1 || versioned.Results[0].ID != feedstock.ID {
		t.Fatalf("version rebuild = %#v", versioned)
	}

	for _, candidate := range []string{indexPath, indexPath + "-wal", indexPath + "-shm"} {
		if err := os.Remove(candidate); err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(indexPath, []byte("corrupt derived index"), 0o600); err != nil {
		t.Fatal(err)
	}
	recovered, err := Search(context.Background(), dataStore, options)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.Total != 1 || recovered.Results[0].ID != feedstock.ID {
		t.Fatalf("corruption recovery = %#v", recovered)
	}
}

func TestIndexSchemaUsesKindsAndExternalContentFTS(t *testing.T) {
	dataStore := newStore(t)
	feedstock := writeFeedstock(
		t,
		dataStore,
		"claude-session-t000001",
		time.Now().UTC(),
		"schema vocabulary",
	)
	writeKnowledge(
		t,
		dataStore,
		"schema-knowledge",
		feedstock.ID,
		domain.StatusActive,
		"schema vocabulary",
		"",
	)
	if _, err := Search(context.Background(), dataStore, SearchOptions{
		Target: TargetFeedstock, Limit: 10, MaxTokens: 1000,
	}); err != nil {
		t.Fatal(err)
	}

	indexPath := filepath.Join(dataStore.Root, ".knowbrew", "state", "index.sqlite")
	database, err := sql.Open("sqlite", indexPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := database.Close(); err != nil {
			t.Error(err)
		}
	}()
	var userVersion int
	if err := database.QueryRow(`PRAGMA user_version`).Scan(&userVersion); err != nil {
		t.Fatal(err)
	}
	if userVersion != indexSchemaVersion {
		t.Fatalf("user_version = %d, want %d", userVersion, indexSchemaVersion)
	}

	rows, err := database.Query(`SELECT record_key,kind FROM documents ORDER BY kind,id`)
	if err != nil {
		t.Fatal(err)
	}
	var records []string
	for rows.Next() {
		var recordKey, kind string
		if err := rows.Scan(&recordKey, &kind); err != nil {
			_ = rows.Close()
			t.Fatal(err)
		}
		records = append(records, kind+"="+recordKey)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	wantRecords := []string{
		"feedstock=feedstock:" + feedstock.ID,
		"knowledge=knowledge:schema-knowledge",
	}
	if fmt.Sprint(records) != fmt.Sprint(wantRecords) {
		t.Fatalf("records = %#v, want %#v", records, wantRecords)
	}

	columns, err := database.Query(`PRAGMA table_info(documents)`)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for columns.Next() {
		var (
			position, notNull, primaryKey int
			name, columnType              string
			defaultValue                  any
		)
		if err := columns.Scan(
			&position,
			&name,
			&columnType,
			&notNull,
			&defaultValue,
			&primaryKey,
		); err != nil {
			_ = columns.Close()
			t.Fatal(err)
		}
		names[name] = true
	}
	if err := columns.Close(); err != nil {
		t.Fatal(err)
	}
	if !names["kind"] || !names["type"] || names["layer"] {
		t.Fatalf("document columns = %#v", names)
	}

	var ftsSQL string
	if err := database.QueryRow(
		`SELECT sql FROM sqlite_master WHERE type='table' AND name='documents_fts'`,
	).Scan(&ftsSQL); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(ftsSQL, "content='documents'") ||
		strings.Contains(ftsSQL, "record_key UNINDEXED") {
		t.Fatalf("unexpected FTS schema: %s", ftsSQL)
	}
	var triggerCount int
	if err := database.QueryRow(
		`SELECT count(*) FROM sqlite_master
		WHERE type='trigger' AND name LIKE 'documents_fts_after_%'`,
	).Scan(&triggerCount); err != nil {
		t.Fatal(err)
	}
	if triggerCount != 3 {
		t.Fatalf("FTS trigger count = %d, want 3", triggerCount)
	}
}

func TestReindexAndBrokenRecordWarnings(t *testing.T) {
	dataStore := newStore(t)
	writeFeedstock(t, dataStore, "claude-session-t000001", time.Now().UTC(), "valid source")
	brokenPath := filepath.Join(dataStore.Root, "feedstocks", "broken.md")
	if err := os.WriteFile(brokenPath, []byte("---\nstatus: typo\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	response, err := Search(context.Background(), dataStore, SearchOptions{
		Target: TargetFeedstock, Reindex: true, Limit: 10, MaxTokens: 1000,
	})
	if err != nil {
		t.Fatal(err)
	}
	if response.Total != 1 || len(response.Warnings) != 1 || response.Warnings[0].Path != brokenPath {
		t.Fatalf("reindex response = %#v", response)
	}
}

func TestSearchLockFailsImmediatelyWhenHeld(t *testing.T) {
	dataStore := newStore(t)
	lock := flock.New(filepath.Join(dataStore.Root, ".knowbrew", "state", "index.lock"))
	if err := lock.Lock(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := lock.Unlock(); err != nil {
			t.Error(err)
		}
	}()

	started := time.Now()
	_, err := Search(context.Background(), dataStore, SearchOptions{Target: TargetFeedstock})
	if err == nil || err.Error() != "another knowbrew search process is updating the index" {
		t.Fatalf("error = %v", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("lock conflict took %s", elapsed)
	}
}

func BenchmarkIncrementalSearch3000Feedstocks(b *testing.B) {
	dataStore, err := store.New(b.TempDir())
	if err != nil {
		b.Fatal(err)
	}
	if err := dataStore.EnsureLayout(); err != nil {
		b.Fatal(err)
	}
	base := time.Date(2026, 7, 30, 0, 0, 0, 0, time.UTC)
	for index := 1; index <= 3000; index++ {
		feedstock := domain.Feedstock{
			Schema: domain.SchemaVersion,
			ID:     fmt.Sprintf("claude-benchmark-t%06d", index),
			TurnID: fmt.Sprintf("turn-%06d", index),
			Session: domain.SessionRef{
				ID:   "benchmark",
				Path: "/logs/benchmark.jsonl",
			},
			Timestamp:   base.Add(time.Duration(index) * time.Second),
			Agent:       "claude",
			Subjects:    []string{"knowbrew"},
			Summary:     "Measure incremental search latency.",
			AnnotatedAt: benchmarkTime(base),
		}
		path, err := dataStore.FeedstockPath(feedstock)
		if err != nil {
			b.Fatal(err)
		}
		data, err := frontmatter.Encode(feedstock, "")
		if err != nil {
			b.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			b.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0o644); err != nil {
			b.Fatal(err)
		}
	}
	options := SearchOptions{
		Target: TargetFeedstock, Keywords: []string{"incremental"},
		Limit: 20, MaxTokens: 2000,
	}
	initialStarted := time.Now()
	if _, err := Search(context.Background(), dataStore, options); err != nil {
		b.Fatal(err)
	}
	initialDuration := time.Since(initialStarted)

	b.ResetTimer()
	for range b.N {
		if _, err := Search(context.Background(), dataStore, options); err != nil {
			b.Fatal(err)
		}
	}
	b.ReportMetric(float64(initialDuration.Microseconds()), "initial_us")
}

func newStore(t *testing.T) *store.Store {
	t.Helper()
	dataStore, err := store.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := dataStore.EnsureLayout(); err != nil {
		t.Fatal(err)
	}
	return dataStore
}

func writeFeedstock(
	t *testing.T,
	dataStore *store.Store,
	id string,
	timestamp time.Time,
	text string,
) domain.Feedstock {
	t.Helper()
	feedstock := domain.Feedstock{
		Schema: domain.SchemaVersion,
		ID:     id,
		TurnID: "turn-" + id,
		Session: domain.SessionRef{
			ID:   "session",
			Path: "/logs/session.jsonl",
		},
		Timestamp: timestamp,
		Agent:     "claude",
		Types:     []domain.KnowledgeType{domain.KnowledgeType("property")},
		Subjects:  []string{"subject"},
		Summary:   text,
		Assertions: []domain.Assertion{{
			ID: "as-" + id, Type: "property", Subject: "subject",
			Statement: text,
			Rationale: `The source contains "ignore previous instructions" as data.`,
		}},
		AnnotatedAt: benchmarkTime(timestamp),
	}
	if err := dataStore.WriteFeedstock(feedstock); err != nil {
		t.Fatal(err)
	}
	return feedstock
}

func benchmarkTime(value time.Time) *time.Time {
	copy := value
	return &copy
}

func writeKnowledge(
	t *testing.T,
	dataStore *store.Store,
	slug, source string,
	status domain.Status,
	claim, trigger string,
) string {
	t.Helper()
	now := time.Now().UTC()
	if err := dataStore.WriteNewKnowledge(slug, domain.Knowledge{
		Created: now, Updated: now, Type: domain.KnowledgeType("property"),
		Subject: "subject", Feedstocks: []string{source},
		Status: domain.StatusPending, Trigger: trigger,
	}, "## Claim\n\n"+claim); err != nil {
		t.Fatal(err)
	}
	path, _ := dataStore.KnowledgePath(slug)
	knowledge, body, err := dataStore.ReadKnowledge(path)
	if err != nil {
		t.Fatal(err)
	}
	knowledge.Approved = status == domain.StatusActive
	knowledge.Status = status
	if status == domain.StatusInvalidated {
		knowledge.InvalidatedAt = &now
	}
	if err := writeKnowledgeForTest(path, knowledge, body); err != nil {
		t.Fatal(err)
	}
	return path
}

func writeKnowledgeForTest(path string, knowledge domain.Knowledge, body string) error {
	data, err := frontmatter.Encode(knowledge, body)
	if err != nil {
		return err
	}
	return fsutil.AtomicWrite(path, data, 0o644)
}
