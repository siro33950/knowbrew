package search

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/siro33950/knowbrew/internal/application/diagnostic"
	"github.com/siro33950/knowbrew/internal/domain"
)

const (
	DefaultLimit       = 20
	MaximumLimit       = 100
	DefaultMaxTokens   = 2000
	minimumBranchDepth = 50
	rrfConstant        = 60.0
)

type Target string

const (
	TargetKnowledge Target = "knowledge"
	TargetFeedstock Target = "feedstock"
	TargetDocument  Target = "document"
)

type Mode string

const (
	ModeHybrid Mode = "hybrid"
	ModeText   Mode = "text"
	ModeVector Mode = "vector"
)

type Options struct {
	Target         Target
	Keywords       []string
	Subject        string
	Type           domain.KnowledgeType
	Since          *time.Time
	Until          *time.Time
	IncludePending bool
	Trigger        string
	Template       string
	Session        string
	Agent          string
	Last           int
	Limit          int
	MaxTokens      int
	Reindex        bool
	IncludeRetired bool
	Mode           Mode
}

type Result struct {
	ID        string                 `json:"id,omitempty"`
	Timestamp string                 `json:"timestamp,omitempty"`
	Agent     string                 `json:"agent,omitempty"`
	Session   string                 `json:"session,omitempty"`
	Summary   string                 `json:"summary,omitempty"`
	Subject   string                 `json:"subject,omitempty"`
	Subjects  []string               `json:"subjects,omitempty"`
	Type      domain.KnowledgeType   `json:"type,omitempty"`
	Types     []domain.KnowledgeType `json:"types,omitempty"`
	Claim     string                 `json:"claim,omitempty"`
	Template  string                 `json:"template,omitempty"`
	Path      string                 `json:"path,omitempty"`
}

type Response struct {
	Results   []Result             `json:"results"`
	Total     int                  `json:"total"`
	Returned  int                  `json:"returned"`
	HasMore   bool                 `json:"has_more"`
	Truncated bool                 `json:"truncated"`
	Warnings  []diagnostic.Warning `json:"warnings,omitempty"`
}

type RankedID struct {
	ID   string
	Rank int
}

type SyncReport struct {
	Documents      int       `json:"documents"`
	Embedded       int       `json:"embedded"`
	Deleted        int       `json:"deleted"`
	Model          string    `json:"model,omitempty"`
	IndexVersion   int       `json:"index_version"`
	SynchronizedAt time.Time `json:"synchronized_at"`
}

type Status struct {
	IndexVersion       int       `json:"index_version"`
	ExpectedVersion    int       `json:"expected_version"`
	Model              string    `json:"model,omitempty"`
	SemanticEnabled    bool      `json:"semantic_enabled"`
	Documents          int       `json:"documents"`
	Vectors            int       `json:"vectors"`
	Unsynchronized     int       `json:"unsynchronized"`
	LastSynchronizedAt time.Time `json:"last_synchronized_at,omitempty"`
	LastError          string    `json:"last_error,omitempty"`
}

type Gateway interface {
	ValidateType(domain.KnowledgeType) error
	Synchronize(context.Context, bool) (SyncReport, []diagnostic.Warning, error)
	Text(context.Context, Options, int) ([]RankedID, error)
	Vector(context.Context, Options, int) ([]RankedID, error)
	Chronological(context.Context, Options, int) ([]RankedID, error)
	Count(context.Context, Options, Mode) (int, error)
	Load(context.Context, Target, []string) ([]Result, error)
	SemanticEnabled() bool
	Status(context.Context) (Status, []diagnostic.Warning, error)
}

type Service struct {
	Gateway Gateway
}

func (service Service) Synchronize(
	ctx context.Context,
	rebuild bool,
) (SyncReport, []diagnostic.Warning, error) {
	if service.Gateway == nil {
		return SyncReport{}, nil, errors.New("search gateway is required")
	}
	return service.Gateway.Synchronize(ctx, rebuild)
}

func (service Service) Status(
	ctx context.Context,
) (Status, []diagnostic.Warning, error) {
	if service.Gateway == nil {
		return Status{}, nil, errors.New("search gateway is required")
	}
	return service.Gateway.Status(ctx)
}

func (service Service) Search(ctx context.Context, options Options) (Response, error) {
	if err := ValidateOptions(&options); err != nil {
		return Response{}, err
	}
	ranked, warnings, err := service.rank(ctx, options)
	if err != nil {
		return Response{}, err
	}
	mode := options.Mode
	if len(keywordTerms(options.Keywords)) == 0 || (mode == ModeHybrid && !service.Gateway.SemanticEnabled()) {
		mode = ModeText
	}
	total, err := service.Gateway.Count(ctx, options, mode)
	if err != nil {
		return Response{}, err
	}
	return service.response(ctx, options, ranked, total, warnings)
}

func (service Service) CandidateIDs(ctx context.Context, options Options) ([]string, error) {
	if err := ValidateOptions(&options); err != nil {
		return nil, err
	}
	ranked, _, err := service.rank(ctx, options)
	if err != nil {
		return nil, err
	}
	if len(ranked) > options.Limit {
		ranked = ranked[:options.Limit]
	}
	ids := make([]string, len(ranked))
	for index, candidate := range ranked {
		ids[index] = candidate.ID
	}
	return ids, nil
}

func (service Service) rank(
	ctx context.Context,
	options Options,
) ([]RankedID, []diagnostic.Warning, error) {
	if service.Gateway == nil {
		return nil, nil, errors.New("search gateway is required")
	}
	if options.Type != "" {
		if err := service.Gateway.ValidateType(options.Type); err != nil {
			return nil, nil, fmt.Errorf("invalid --type: %w", err)
		}
	}
	_, warnings, err := service.Gateway.Synchronize(ctx, options.Reindex)
	if err != nil {
		return nil, warnings, err
	}
	terms := keywordTerms(options.Keywords)
	if len(terms) == 0 {
		limit := options.Limit
		if options.Last > 0 {
			limit = options.Last
			options.Limit = options.Last
			ranked, err := service.Gateway.Chronological(ctx, options, limit)
			if err != nil {
				return nil, warnings, err
			}
			return ranked, warnings, nil
		}
		ranked, err := service.Gateway.Chronological(ctx, options, limit+1)
		if err != nil {
			return nil, warnings, err
		}
		return ranked, warnings, nil
	}

	mode := options.Mode
	if mode == "" {
		mode = ModeHybrid
	}
	semantic := service.Gateway.SemanticEnabled()
	if mode == ModeVector && !semantic {
		return nil, warnings, errors.New("vector search is disabled")
	}
	depth := max(minimumBranchDepth, options.Limit+1)
	if mode == ModeText || !semantic {
		ranked, err := service.Gateway.Text(ctx, options, depth)
		if err != nil {
			return nil, warnings, err
		}
		return ranked, warnings, nil
	}
	if mode == ModeVector {
		ranked, err := service.Gateway.Vector(ctx, options, depth)
		if err != nil {
			return nil, warnings, err
		}
		return ranked, warnings, nil
	}

	type branchResult struct {
		name   string
		ranked []RankedID
		err    error
	}
	results := make(chan branchResult, 2)
	go func() {
		ranked, branchErr := service.Gateway.Text(ctx, options, depth)
		results <- branchResult{name: "text", ranked: ranked, err: branchErr}
	}()
	go func() {
		ranked, branchErr := service.Gateway.Vector(ctx, options, depth)
		results <- branchResult{name: "vector", ranked: ranked, err: branchErr}
	}()
	branches := make(map[string][]RankedID, 2)
	var branchErrors []error
	for range 2 {
		result := <-results
		if result.err != nil {
			branchErrors = append(branchErrors, fmt.Errorf("%s search: %w", result.name, result.err))
			continue
		}
		branches[result.name] = result.ranked
	}
	if len(branchErrors) > 0 {
		return nil, warnings, errors.Join(branchErrors...)
	}
	return Fuse(branches["text"], branches["vector"]), warnings, nil
}

func (service Service) response(
	ctx context.Context,
	options Options,
	ranked []RankedID,
	total int,
	warnings []diagnostic.Warning,
) (Response, error) {
	response := Response{Results: make([]Result, 0), Total: total, Warnings: warnings}
	limit := options.Limit
	if options.Last > 0 {
		limit = options.Last
	}
	if len(ranked) > limit {
		response.HasMore = true
		ranked = ranked[:limit]
	}
	ids := make([]string, len(ranked))
	for index, candidate := range ranked {
		ids[index] = candidate.ID
	}
	loaded, err := service.Gateway.Load(ctx, options.Target, ids)
	if err != nil {
		return Response{}, err
	}
	byID := make(map[string]Result, len(loaded))
	for _, result := range loaded {
		byID[result.ID] = result
	}
	budget := options.MaxTokens * 4
	used := 0
	for _, id := range ids {
		result, exists := byID[id]
		if !exists {
			continue
		}
		encoded, _ := json.Marshal(result)
		if used+len(encoded) > budget {
			response.HasMore = true
			response.Truncated = true
			break
		}
		used += len(encoded)
		response.Results = append(response.Results, result)
	}
	response.Returned = len(response.Results)
	if response.Returned < response.Total {
		response.HasMore = true
	}
	response.Truncated = response.Truncated || response.HasMore
	return response, nil
}

func ValidateOptions(options *Options) error {
	if options.Target != TargetKnowledge && options.Target != TargetFeedstock && options.Target != TargetDocument {
		return errors.New("search target must be knowledge, feedstock, or document")
	}
	if options.Limit == 0 {
		options.Limit = DefaultLimit
	}
	if options.Limit < 1 || options.Limit > MaximumLimit {
		return fmt.Errorf("search limit must be between 1 and %d", MaximumLimit)
	}
	if options.MaxTokens <= 0 {
		options.MaxTokens = DefaultMaxTokens
	}
	if options.Mode == "" {
		options.Mode = ModeHybrid
	}
	if !slices.Contains([]Mode{ModeHybrid, ModeText, ModeVector}, options.Mode) {
		return errors.New("search mode must be hybrid, text, or vector")
	}
	if options.Type != "" {
		if options.Target == TargetDocument {
			return errors.New("--type is not valid for document")
		}
		options.Type = domain.KnowledgeType(strings.TrimSpace(string(options.Type)))
		if err := domain.ValidateKnowledgeTypeName(options.Type); err != nil {
			return fmt.Errorf("invalid --type: %w", err)
		}
	}
	options.Template = strings.TrimSpace(options.Template)
	if options.Template != "" && options.Target != TargetDocument {
		return errors.New("--template is only valid for document")
	}
	if options.Trigger != "" {
		if options.Target != TargetKnowledge {
			return errors.New("--trigger is only valid for knowledge")
		}
		if options.Trigger != "always" {
			return errors.New("--trigger must be always")
		}
		if options.IncludePending {
			return errors.New("--trigger and --include-pending cannot be used together")
		}
		if options.IncludeRetired {
			return errors.New("--trigger and --include-retired cannot be used together")
		}
	}
	if options.Target == TargetKnowledge {
		if options.Session != "" || options.Agent != "" || options.Last != 0 {
			return errors.New("--session, --agent, and --last are only valid for feedstock")
		}
		return nil
	}
	if options.Target == TargetDocument {
		if options.Session != "" || options.Agent != "" || options.Last != 0 {
			return errors.New("--session, --agent, and --last are only valid for feedstock")
		}
		if options.IncludePending || options.IncludeRetired {
			return errors.New("--include-pending and --include-retired are only valid for knowledge")
		}
		return nil
	}
	if options.IncludePending || options.IncludeRetired {
		return errors.New("--include-pending and --include-retired are only valid for knowledge")
	}
	if options.Last < 0 {
		return errors.New("--last must be greater than zero")
	}
	if options.Last > 0 && len(keywordTerms(options.Keywords)) > 0 {
		return errors.New("--last cannot be used with keywords")
	}
	return nil
}

func Fuse(branches ...[]RankedID) []RankedID {
	scores := make(map[string]float64)
	for _, branch := range branches {
		seen := make(map[string]struct{}, len(branch))
		for index, candidate := range branch {
			if candidate.ID == "" {
				continue
			}
			if _, exists := seen[candidate.ID]; exists {
				continue
			}
			seen[candidate.ID] = struct{}{}
			rank := candidate.Rank
			if rank < 1 {
				rank = index + 1
			}
			scores[candidate.ID] += 1 / (rrfConstant + float64(rank))
		}
	}
	ids := make([]string, 0, len(scores))
	for id := range scores {
		ids = append(ids, id)
	}
	slices.SortFunc(ids, func(left, right string) int {
		if scores[left] > scores[right] {
			return -1
		}
		if scores[left] < scores[right] {
			return 1
		}
		return strings.Compare(left, right)
	})
	result := make([]RankedID, len(ids))
	for index, id := range ids {
		result[index] = RankedID{ID: id, Rank: index + 1}
	}
	return result
}

func keywordTerms(keywords []string) []string {
	var terms []string
	for _, keyword := range keywords {
		terms = append(terms, strings.Fields(keyword)...)
	}
	return terms
}
