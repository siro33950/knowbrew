package search

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"

	"github.com/siro33950/knowbrew/internal/application/diagnostic"
	"github.com/siro33950/knowbrew/internal/domain"
)

func TestFuseUsesReciprocalRankFusionAndStableTies(t *testing.T) {
	got := Fuse(
		[]RankedID{{ID: "a", Rank: 1}, {ID: "b", Rank: 2}, {ID: "c", Rank: 3}},
		[]RankedID{{ID: "b", Rank: 1}, {ID: "a", Rank: 2}, {ID: "d", Rank: 3}},
	)
	want := []RankedID{{ID: "a", Rank: 1}, {ID: "b", Rank: 2}, {ID: "c", Rank: 3}, {ID: "d", Rank: 4}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Fuse() = %#v, want %#v", got, want)
	}
}

func TestVectorModeRequiresSemanticBackend(t *testing.T) {
	service := Service{Gateway: fakeGateway{}}
	_, err := service.Search(context.Background(), Options{
		Target: TargetKnowledge, Keywords: []string{"meaning"}, Mode: ModeVector,
	})
	if err == nil || err.Error() != "vector search is disabled" {
		t.Fatalf("Search() error = %v", err)
	}
}

func TestHybridSearchFusesBranchesAndInternalCandidatesDoNotLoadDocuments(t *testing.T) {
	gateway := &recordingGateway{
		semantic: true,
		text:     []RankedID{{ID: "a", Rank: 1}, {ID: "b", Rank: 2}},
		vector:   []RankedID{{ID: "b", Rank: 1}, {ID: "a", Rank: 2}},
		loaded: map[string]Result{
			"a": {ID: "a", Claim: "first"},
			"b": {ID: "b", Claim: "second"},
		},
	}
	service := Service{Gateway: gateway}
	ids, err := service.CandidateIDs(context.Background(), Options{
		Target: TargetKnowledge, Keywords: []string{"meaning"}, Limit: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(ids, []string{"a"}) {
		t.Fatalf("candidate IDs = %#v", ids)
	}
	if gateway.loadCalls != 0 || gateway.textCalls != 1 || gateway.vectorCalls != 1 {
		t.Fatalf("candidate gateway calls = %#v", gateway)
	}

	response, err := service.Search(context.Background(), Options{
		Target: TargetKnowledge, Keywords: []string{"meaning"}, Limit: 1, MaxTokens: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Results) != 1 || response.Results[0].ID != "a" || !response.HasMore {
		t.Fatalf("hybrid response = %#v", response)
	}
	if gateway.loadCalls != 1 {
		t.Fatalf("public search load calls = %d, want 1", gateway.loadCalls)
	}
}

func TestSearchUsesExactTotalReportedByGateway(t *testing.T) {
	gateway := &recordingGateway{
		text:   []RankedID{{ID: "a", Rank: 1}},
		loaded: map[string]Result{"a": {ID: "a", Claim: "first"}},
		total:  137,
	}
	response, err := (Service{Gateway: gateway}).Search(context.Background(), Options{
		Target: TargetKnowledge, Keywords: []string{"meaning"}, Mode: ModeText,
		Limit: 20, MaxTokens: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	if response.Total != 137 || !response.HasMore || !response.Truncated {
		t.Fatalf("response = %#v", response)
	}
	if gateway.countCalls != 1 {
		t.Fatalf("count calls = %d, want 1", gateway.countCalls)
	}
}

func TestQuerylessSearchRemainsChronologicalWithoutEmbedding(t *testing.T) {
	gateway := &recordingGateway{
		semantic: true,
		chrono:   []RankedID{{ID: "recent", Rank: 1}},
		loaded:   map[string]Result{"recent": {ID: "recent", Summary: "latest"}},
	}
	service := Service{Gateway: gateway}
	response, err := service.Search(context.Background(), Options{
		Target: TargetFeedstock, Limit: 20, MaxTokens: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Results) != 1 || response.Results[0].ID != "recent" {
		t.Fatalf("chronological response = %#v", response)
	}
	if gateway.chronoCalls != 1 || gateway.textCalls != 0 || gateway.vectorCalls != 0 {
		t.Fatalf("queryless gateway calls = %#v", gateway)
	}
}

func TestLastUsesItsExplicitResultCount(t *testing.T) {
	ranked := make([]RankedID, 30)
	loaded := make(map[string]Result, len(ranked))
	for index := range ranked {
		id := string(rune('a' + index))
		ranked[index] = RankedID{ID: id, Rank: index + 1}
		loaded[id] = Result{ID: id, Summary: id}
	}
	gateway := &recordingGateway{chrono: ranked, loaded: loaded}
	response, err := (Service{Gateway: gateway}).Search(context.Background(), Options{
		Target: TargetFeedstock, Last: 30, MaxTokens: 1000,
	})
	if err != nil {
		t.Fatal(err)
	}
	if response.Returned != 30 || response.HasMore {
		t.Fatalf("last response = %#v", response)
	}
}

func TestValidateOptionsDocumentTarget(t *testing.T) {
	options := Options{Target: TargetDocument, Subject: "alpha", Template: "concept"}
	if err := ValidateOptions(&options); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []Options{
		{Target: TargetDocument, Type: domain.KnowledgeType("decision")},
		{Target: TargetDocument, Trigger: "always"},
		{Target: TargetDocument, Session: "session"},
		{Target: TargetDocument, Agent: "claude"},
		{Target: TargetDocument, Last: 3},
		{Target: TargetDocument, IncludePending: true},
		{Target: TargetDocument, IncludeRetired: true},
		{Target: TargetKnowledge, Template: "concept"},
		{Target: TargetFeedstock, Template: "concept"},
	} {
		value := invalid
		if err := ValidateOptions(&value); err == nil {
			t.Fatalf("options %#v were accepted", invalid)
		}
	}
}

func TestSearchRejectsTypeMissingFromMaster(t *testing.T) {
	gateway := &recordingGateway{validateTypeErr: errors.New("not defined")}
	_, err := (Service{Gateway: gateway}).Search(context.Background(), Options{
		Target: TargetKnowledge, Type: domain.KnowledgeType("missing"),
	})
	if err == nil || err.Error() != "invalid --type: not defined" {
		t.Fatalf("type validation error = %v", err)
	}
	if gateway.syncCalls != 0 {
		t.Fatalf("sync calls = %d, want 0", gateway.syncCalls)
	}
}

func TestHybridModeUsesTextOnlyWhenSemanticSearchIsDisabled(t *testing.T) {
	gateway := &recordingGateway{
		text:   []RankedID{{ID: "text", Rank: 1}},
		loaded: map[string]Result{"text": {ID: "text", Claim: "text result"}},
	}
	service := Service{Gateway: gateway}
	response, err := service.Search(context.Background(), Options{
		Target: TargetKnowledge, Keywords: []string{"exact"}, Limit: 20, MaxTokens: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Results) != 1 || response.Results[0].ID != "text" {
		t.Fatalf("text-only response = %#v", response)
	}
	if gateway.textCalls != 1 || gateway.vectorCalls != 0 {
		t.Fatalf("disabled semantic calls = %#v", gateway)
	}
}

type fakeGateway struct{}

func (fakeGateway) ValidateType(domain.KnowledgeType) error { return nil }
func (fakeGateway) Synchronize(context.Context, bool) (SyncReport, []diagnostic.Warning, error) {
	return SyncReport{}, nil, nil
}
func (fakeGateway) Text(context.Context, Options, int) ([]RankedID, error)   { return nil, nil }
func (fakeGateway) Vector(context.Context, Options, int) ([]RankedID, error) { return nil, nil }
func (fakeGateway) Chronological(context.Context, Options, int) ([]RankedID, error) {
	return nil, nil
}
func (fakeGateway) Count(context.Context, Options, Mode) (int, error)        { return 0, nil }
func (fakeGateway) Load(context.Context, Target, []string) ([]Result, error) { return nil, nil }
func (fakeGateway) SemanticEnabled() bool                                    { return false }
func (fakeGateway) Status(context.Context) (Status, []diagnostic.Warning, error) {
	return Status{}, nil, nil
}

type recordingGateway struct {
	mu                                 sync.Mutex
	semantic                           bool
	text, vector, chrono               []RankedID
	loaded                             map[string]Result
	total                              int
	validateTypeErr                    error
	syncCalls, textCalls, vectorCalls  int
	chronoCalls, countCalls, loadCalls int
	statusCalls                        int
}

func (gateway *recordingGateway) ValidateType(domain.KnowledgeType) error {
	return gateway.validateTypeErr
}

func (gateway *recordingGateway) Synchronize(
	context.Context,
	bool,
) (SyncReport, []diagnostic.Warning, error) {
	gateway.mu.Lock()
	defer gateway.mu.Unlock()
	gateway.syncCalls++
	return SyncReport{}, nil, nil
}

func (gateway *recordingGateway) Text(context.Context, Options, int) ([]RankedID, error) {
	gateway.mu.Lock()
	defer gateway.mu.Unlock()
	gateway.textCalls++
	return append([]RankedID(nil), gateway.text...), nil
}

func (gateway *recordingGateway) Vector(context.Context, Options, int) ([]RankedID, error) {
	gateway.mu.Lock()
	defer gateway.mu.Unlock()
	gateway.vectorCalls++
	return append([]RankedID(nil), gateway.vector...), nil
}

func (gateway *recordingGateway) Chronological(context.Context, Options, int) ([]RankedID, error) {
	gateway.mu.Lock()
	defer gateway.mu.Unlock()
	gateway.chronoCalls++
	return append([]RankedID(nil), gateway.chrono...), nil
}

func (gateway *recordingGateway) Count(_ context.Context, options Options, mode Mode) (int, error) {
	gateway.mu.Lock()
	defer gateway.mu.Unlock()
	gateway.countCalls++
	if gateway.total != 0 {
		return gateway.total, nil
	}
	if len(keywordTerms(options.Keywords)) == 0 {
		return len(gateway.chrono), nil
	}
	ids := map[string]struct{}{}
	for _, candidate := range gateway.text {
		ids[candidate.ID] = struct{}{}
	}
	if mode != ModeText {
		for _, candidate := range gateway.vector {
			ids[candidate.ID] = struct{}{}
		}
	}
	return len(ids), nil
}

func (gateway *recordingGateway) Load(_ context.Context, _ Target, ids []string) ([]Result, error) {
	gateway.mu.Lock()
	defer gateway.mu.Unlock()
	gateway.loadCalls++
	results := make([]Result, 0, len(ids))
	for _, id := range ids {
		if value, exists := gateway.loaded[id]; exists {
			results = append(results, value)
		}
	}
	return results, nil
}

func (gateway *recordingGateway) SemanticEnabled() bool { return gateway.semantic }

func (gateway *recordingGateway) Status(context.Context) (Status, []diagnostic.Warning, error) {
	gateway.mu.Lock()
	defer gateway.mu.Unlock()
	gateway.statusCalls++
	return Status{}, nil, nil
}
