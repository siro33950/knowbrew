package query

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	searchapp "github.com/siro33950/knowbrew/internal/application/search"
	"github.com/siro33950/knowbrew/internal/domain"
)

func TestGatewaySynchronizesAndSearchesVectors(t *testing.T) {
	dataStore := newStore(t)
	feedstock := writeFeedstock(
		t, dataStore, "claude-session-t000001", time.Now().UTC(), "deployment rollback policy",
	)
	writeKnowledge(
		t, dataStore, "rollback-knowledge", feedstock.ID,
		domain.StatusActive, "deployments must support rollback",
	)
	service := searchapp.Service{Gateway: Gateway{Store: dataStore, Encoder: semanticFakeEncoder{}}}
	response, err := service.Search(context.Background(), searchapp.Options{
		Target: searchapp.TargetKnowledge, Keywords: []string{"undo a release"}, Limit: 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Results) != 1 || response.Results[0].ID != "rollback-knowledge" {
		t.Fatalf("semantic response = %#v", response)
	}
	status, _, err := service.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !status.SemanticEnabled || status.Vectors != 2 || status.Unsynchronized != 0 {
		t.Fatalf("status = %#v", status)
	}
}

func TestGatewayStatusReportsUnindexedFilesWithoutCreatingVectorDatabase(t *testing.T) {
	dataStore := newStore(t)
	writeFeedstock(t, dataStore, "fs-unindexed", time.Now().UTC(), "unindexed summary")
	gateway := Gateway{Store: dataStore, Encoder: semanticFakeEncoder{}}
	status, _, err := gateway.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.Documents != 0 || status.Vectors != 0 || status.Unsynchronized != 1 {
		t.Fatalf("unindexed status = %#v", status)
	}
	if _, err := os.Stat(gateway.vectorPath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("status created vector database: %v", err)
	}
}

func TestGatewaySearchReportsExactTotalBeyondRankingDepth(t *testing.T) {
	dataStore := newStore(t)
	now := time.Now().UTC()
	for index := range 73 {
		writeFeedstock(
			t, dataStore, fmt.Sprintf("fs-total-%03d", index),
			now.Add(time.Duration(index)*time.Second), "needle in every summary",
		)
	}
	service := searchapp.Service{Gateway: Gateway{Store: dataStore, Encoder: semanticFakeEncoder{}}}
	for _, mode := range []searchapp.Mode{searchapp.ModeText, searchapp.ModeHybrid, searchapp.ModeVector} {
		response, err := service.Search(context.Background(), searchapp.Options{
			Target: searchapp.TargetFeedstock, Keywords: []string{"needle"},
			Mode: mode, Limit: 20, MaxTokens: 1000,
		})
		if err != nil {
			t.Fatal(err)
		}
		if response.Total != 73 || response.Returned != 20 || !response.HasMore {
			t.Fatalf("%s response = %#v", mode, response)
		}
	}
}

func TestOpenVectorIndexReadOnlyRejectsWrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vectors.sqlite")
	database, err := openVectorIndex(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if err := ensureVectorSchema(context.Background(), database, semanticFakeEncoder{}, false); err != nil {
		_ = database.Close()
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	readOnly, err := openVectorIndexReadOnly(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = readOnly.Close() }()
	if _, err := readOnly.Exec(`INSERT INTO vector_metadata(key,value) VALUES('write','forbidden')`); err == nil {
		t.Fatal("read-only vector index accepted a write")
	}
}

func TestGatewayRebuildReplacesCorruptVectorDatabase(t *testing.T) {
	dataStore := newStore(t)
	writeFeedstock(t, dataStore, "fs-rebuild", time.Now().UTC(), "rebuild summary")
	gateway := Gateway{Store: dataStore, Encoder: semanticFakeEncoder{}}
	path := gateway.vectorPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("not a sqlite database"), 0o600); err != nil {
		t.Fatal(err)
	}
	report, _, err := gateway.Synchronize(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	if report.Embedded != 1 {
		t.Fatalf("rebuild report = %#v", report)
	}
}

func TestVectorIndexEmbedsOnlyRepresentativeTextPerKind(t *testing.T) {
	dataStore := newStore(t)
	now := time.Now().UTC()
	hidden := domain.Feedstock{
		Schema: domain.SchemaVersion, ID: "a-hidden-feedstock", TurnID: "turn-hidden",
		Session:   domain.SessionRef{ID: "session"},
		Timestamp: now, Agent: "claude", Summary: "weather forecast",
		Types:       []domain.KnowledgeType{"property"},
		AnnotatedAt: benchmarkTime(now),
	}
	if err := dataStore.WriteFeedstock(hidden); err != nil {
		t.Fatal(err)
	}
	real := writeFeedstock(t, dataStore, "z-real-feedstock", now.Add(time.Second), "rollback policy")
	hiddenPath := writeKnowledge(
		t, dataStore, "a-hidden-knowledge", hidden.ID,
		domain.StatusActive, "weather forecast",
	)
	knowledge, _, err := dataStore.ReadKnowledge(hiddenPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeKnowledgeForTest(hiddenPath, knowledge,
		"## Claim\n\nweather forecast\n\n## Rationale\n\nrollback appears only in the rationale"); err != nil {
		t.Fatal(err)
	}
	writeKnowledge(
		t, dataStore, "z-real-knowledge", real.ID,
		domain.StatusActive, "rollback policy",
	)
	writeDistilledDocumentFile(t, dataStore, "a-hidden-doc", "concept",
		"# hidden\n\nweather forecast\n\nrollback appears only in a later paragraph.\n")
	writeDistilledDocumentFile(t, dataStore, "z-real-doc", "concept",
		"# real\n\nrollback policy\n")
	service := searchapp.Service{Gateway: Gateway{Store: dataStore, Encoder: semanticFakeEncoder{}}}
	for _, test := range []struct {
		target searchapp.Target
		want   string
	}{
		{target: searchapp.TargetKnowledge, want: "z-real-knowledge"},
		{target: searchapp.TargetFeedstock, want: "z-real-feedstock"},
		{target: searchapp.TargetDocument, want: "z-real-doc/concept"},
	} {
		response, err := service.Search(context.Background(), searchapp.Options{
			Target: test.target, Keywords: []string{"undo a release"},
			Mode: searchapp.ModeVector, Limit: 1,
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(response.Results) != 1 || response.Results[0].ID != test.want {
			t.Fatalf("%s vector response = %#v", test.target, response)
		}
		if test.target == searchapp.TargetFeedstock &&
			(response.Results[0].Agent != "claude" || response.Results[0].Session != "session") {
			t.Fatalf("feedstock search metadata = %#v", response.Results[0])
		}
		if test.target == searchapp.TargetDocument &&
			(response.Results[0].Template != "concept" || response.Results[0].Subject != "z-real-doc") {
			t.Fatalf("document search metadata = %#v", response.Results[0])
		}
	}
	filtered, err := service.Search(context.Background(), searchapp.Options{
		Target: searchapp.TargetKnowledge, Keywords: []string{"undo a release"},
		Subject: "unrelated", Mode: searchapp.ModeVector, Limit: 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(filtered.Results) != 0 {
		t.Fatalf("machine filter leaked vector results: %#v", filtered)
	}
}

func TestVectorSyncUpdatesDeletesAndReportsPersistentFailure(t *testing.T) {
	dataStore := newStore(t)
	feedstock := writeFeedstock(t, dataStore, "fs-source", time.Now().UTC(), "source summary")
	path := writeKnowledge(
		t, dataStore, "changing-knowledge", feedstock.ID,
		domain.StatusActive, "original claim",
	)
	good := searchapp.Service{Gateway: Gateway{Store: dataStore, Encoder: semanticFakeEncoder{}}}
	initial, _, err := good.Synchronize(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	if initial.Embedded != 2 {
		t.Fatalf("initial sync = %#v", initial)
	}
	knowledge, _, err := dataStore.ReadKnowledge(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeKnowledgeForTest(path, knowledge, "## Claim\n\nupdated rollback claim"); err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(path, future, future); err != nil {
		t.Fatal(err)
	}
	failing := searchapp.Service{Gateway: Gateway{Store: dataStore, Encoder: failingEncoder{}}}
	if _, _, err := failing.Synchronize(context.Background(), false); err == nil ||
		!strings.Contains(err.Error(), "embedding failed") {
		t.Fatalf("failed sync error = %v", err)
	}
	status, _, err := failing.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(status.LastError, "embedding failed") ||
		status.LastSynchronizedAt.IsZero() || status.Unsynchronized != 1 {
		t.Fatalf("status after failed sync = %#v", status)
	}
	updated, _, err := good.Synchronize(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Embedded != 1 {
		t.Fatalf("updated sync = %#v", updated)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	deleted, _, err := good.Synchronize(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	if deleted.Deleted != 1 {
		t.Fatalf("deleted sync = %#v", deleted)
	}
	status, _, err = good.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.Vectors != 1 || status.Unsynchronized != 0 || status.LastError != "" {
		t.Fatalf("final status = %#v", status)
	}
}

func TestVectorSyncBatchesLargeIndexesAndResumesAfterFailure(t *testing.T) {
	dataStore := newStore(t)
	now := time.Now().UTC()
	for index := range 130 {
		writeFeedstock(
			t, dataStore, fmt.Sprintf("fs-%03d", index), now.Add(time.Duration(index)*time.Second),
			fmt.Sprintf("summary %03d", index),
		)
	}
	failing := &batchRecordingEncoder{failOnCall: 2}
	service := searchapp.Service{Gateway: Gateway{Store: dataStore, Encoder: failing}}
	if _, _, err := service.Synchronize(context.Background(), false); err == nil ||
		!strings.Contains(err.Error(), "batch failed") {
		t.Fatalf("first sync error = %v", err)
	}
	if fmt.Sprint(failing.batchSizes) != "[64 64]" {
		t.Fatalf("failed batch sizes = %v", failing.batchSizes)
	}
	status, _, err := service.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.Vectors != 64 || status.Unsynchronized != 66 {
		t.Fatalf("partial status = %#v", status)
	}

	resuming := &batchRecordingEncoder{}
	resumed, _, err := (searchapp.Service{Gateway: Gateway{
		Store: dataStore, Encoder: resuming,
	}}).Synchronize(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	if resumed.Embedded != 66 || fmt.Sprint(resuming.batchSizes) != "[64 2]" {
		t.Fatalf("resumed sync = %#v, batches = %v", resumed, resuming.batchSizes)
	}
}

type semanticFakeEncoder struct{}

func (semanticFakeEncoder) ID() string     { return "fake-v1" }
func (semanticFakeEncoder) Dimension() int { return 2 }
func (semanticFakeEncoder) Close() error   { return nil }
func (semanticFakeEncoder) EncodeDocuments(_ context.Context, texts []string) ([][]float32, error) {
	result := make([][]float32, len(texts))
	for index, text := range texts {
		result[index] = semanticVector(text)
	}
	return result, nil
}
func (semanticFakeEncoder) EncodeQuery(_ context.Context, text string) ([]float32, error) {
	return semanticVector(text), nil
}

func semanticVector(text string) []float32 {
	text = strings.ToLower(text)
	if strings.Contains(text, "rollback") || strings.Contains(text, "undo") {
		return []float32{1, 0}
	}
	return []float32{0, 1}
}

type failingEncoder struct{ semanticFakeEncoder }

func (failingEncoder) EncodeDocuments(context.Context, []string) ([][]float32, error) {
	return nil, errors.New("embedding failed")
}

type batchRecordingEncoder struct {
	semanticFakeEncoder
	batchSizes []int
	failOnCall int
}

func (encoder *batchRecordingEncoder) EncodeDocuments(
	ctx context.Context,
	texts []string,
) ([][]float32, error) {
	encoder.batchSizes = append(encoder.batchSizes, len(texts))
	if encoder.failOnCall > 0 && len(encoder.batchSizes) == encoder.failOnCall {
		return nil, errors.New("batch failed")
	}
	return encoder.semanticFakeEncoder.EncodeDocuments(ctx, texts)
}
