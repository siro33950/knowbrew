package brew

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/siro33950/knowbrew/internal/application/agent"
	"github.com/siro33950/knowbrew/internal/domain"
)

type VerificationStatus = domain.VerificationStatus
type ResolutionKind = domain.ResolutionKind

const (
	VerificationVerified  = domain.VerificationVerified
	VerificationCorrected = domain.VerificationCorrected
	VerificationRejected  = domain.VerificationRejected

	ResolutionNew         = domain.ResolutionNew
	ResolutionEquivalent  = domain.ResolutionEquivalent
	ResolutionComplements = domain.ResolutionComplements
	ResolutionConflicts   = domain.ResolutionConflicts
)

var ErrStaleDecision = errors.New("knowledge changed while the assertion was being evaluated")

type KnowledgeDraft struct {
	Type      domain.KnowledgeType `json:"type"`
	Subject   string               `json:"subject"`
	Statement string               `json:"statement"`
	Rationale string               `json:"rationale,omitempty"`
	Trigger   string               `json:"trigger,omitempty"`
}

type ResolutionInput struct {
	Kind         ResolutionKind  `json:"kind"`
	KnowledgeIDs []string        `json:"knowledge_ids"`
	Draft        *KnowledgeDraft `json:"draft"`
}

type SubmitInput struct {
	FeedstockID        string
	AssertionID        string
	ExpectedAssertion  *domain.Assertion
	Verification       VerificationStatus
	CorrectedAssertion *domain.Assertion
	Resolution         *ResolutionInput
}

type SubmitResult struct {
	FeedstockID  string             `json:"feedstock_id"`
	AssertionID  string             `json:"assertion_id"`
	Verification VerificationStatus `json:"verification"`
	Outcome      string             `json:"outcome"`
	KnowledgeID  string             `json:"knowledge_id,omitempty"`
	Targets      []string           `json:"targets,omitempty"`
}

type CatalogEntry struct {
	ID        string               `json:"id"`
	Type      domain.KnowledgeType `json:"type"`
	Subject   string               `json:"subject"`
	Statement string               `json:"statement"`
}

type ShownKnowledge struct {
	ID        string               `json:"id"`
	Type      domain.KnowledgeType `json:"type"`
	Subject   string               `json:"subject"`
	Statement string               `json:"statement"`
	Rationale string               `json:"rationale,omitempty"`
	Trigger   string               `json:"trigger,omitempty"`
}

func Catalog(dataStore Repository, reads Invocation, subject string) ([]CatalogEntry, error) {
	subject = domain.MasterName(subject)
	if subject == "" {
		return nil, errors.New("knowledge catalog requires a subject")
	}
	if err := validateSubject(dataStore, subject); err != nil {
		return nil, err
	}
	files, digest, err := catalogSnapshot(dataStore, subject)
	if err != nil {
		return nil, err
	}
	entries := make([]CatalogEntry, 0, len(files))
	ids := make([]string, 0, len(files))
	for _, file := range files {
		entries = append(entries, CatalogEntry{
			ID: file.Knowledge.ID, Type: file.Knowledge.Type,
			Subject: file.Knowledge.Subject, Statement: file.Statement,
		})
		ids = append(ids, file.Knowledge.ID)
	}
	if err := reads.RecordCatalog(subject, ids, digest); err != nil {
		return nil, err
	}
	return entries, nil
}

func Show(dataStore Repository, reads Invocation, ids []string) ([]ShownKnowledge, error) {
	ids = domain.UniqueSorted(ids)
	if len(ids) == 0 {
		return nil, errors.New("knowledge show requires at least one ID")
	}
	state, err := reads.ReadState()
	if err != nil {
		return nil, err
	}
	result := make([]ShownKnowledge, 0, len(ids))
	for _, id := range ids {
		if !slices.Contains(state.Catalog, id) {
			return nil, fmt.Errorf("knowledge %s was not present in the invocation catalog", id)
		}
		file, err := dataStore.FindKnowledge(id)
		if err != nil {
			return nil, err
		}
		result = append(result, ShownKnowledge{
			ID: id, Type: file.Knowledge.Type, Subject: file.Knowledge.Subject,
			Statement: file.Statement, Rationale: file.Rationale, Trigger: file.Knowledge.Trigger,
		})
	}
	if err := reads.RecordInspected(ids); err != nil {
		return nil, err
	}
	return result, nil
}

// Submit remains an internal compatibility entry point. The agent is not
// granted this command; normal Brew applies the structured decision in its
// parent process.
func Submit(
	ctx context.Context,
	dataStore Repository,
	reads Invocation,
	input SubmitInput,
) (SubmitResult, error) {
	if !reads.IsAssertionInvocation() {
		return SubmitResult{}, errors.New("knowledge submit is available only inside an assertion invocation")
	}
	if err := reads.ValidateFeedstock(input.FeedstockID); err != nil {
		return SubmitResult{}, err
	}
	if err := reads.ValidateAssertion(input.AssertionID); err != nil {
		return SubmitResult{}, err
	}
	state, err := reads.ReadState()
	if err != nil {
		return SubmitResult{}, err
	}
	return Apply(ctx, dataStore, input, state)
}

func Apply(
	ctx context.Context,
	dataStore Repository,
	input SubmitInput,
	reads agent.ReadState,
) (SubmitResult, error) {
	input.FeedstockID = strings.TrimSpace(input.FeedstockID)
	input.AssertionID = strings.TrimSpace(input.AssertionID)
	result := SubmitResult{
		FeedstockID: input.FeedstockID, AssertionID: input.AssertionID,
		Verification: input.Verification,
	}
	err := dataStore.Transaction(ctx, func(tx Transaction) error {
		feedstock, err := dataStore.GetFeedstock(input.FeedstockID)
		if err != nil {
			return err
		}
		assertionIndex := slices.IndexFunc(feedstock.Assertions, func(assertion domain.Assertion) bool {
			return assertion.ID == input.AssertionID
		})
		if assertionIndex < 0 {
			return fmt.Errorf("assertion %s was not found in feedstock %s", input.AssertionID, input.FeedstockID)
		}
		if slices.Contains(feedstock.BrewedAssertions, input.AssertionID) {
			return fmt.Errorf("assertion %s is already brewed", input.AssertionID)
		}
		assertion := feedstock.Assertions[assertionIndex]
		if input.ExpectedAssertion != nil && assertion != *input.ExpectedAssertion {
			return ErrStaleDecision
		}
		if err := validateVerification(dataStore, assertion, input); err != nil {
			return err
		}
		if input.Verification == VerificationRejected {
			feedstock.Assertions = slices.Delete(feedstock.Assertions, assertionIndex, assertionIndex+1)
			if err := tx.StageBrewedFeedstock(feedstock, time.Now().UTC()); err != nil {
				return err
			}
			result.Outcome = "rejected"
			return nil
		}
		if input.Verification == VerificationCorrected {
			assertion = *input.CorrectedAssertion
			feedstock.Assertions[assertionIndex] = assertion
		}
		if assertion.Subject == "" {
			return errors.New("subjectless assertions cannot become Knowledge")
		}
		heads, digest, err := catalogSnapshot(dataStore, assertion.Subject)
		if err != nil {
			return err
		}
		if reads.Subject != assertion.Subject || reads.CatalogDigest == "" || reads.CatalogDigest != digest {
			return ErrStaleDecision
		}
		targets, err := validateResolution(assertion, input.Resolution, reads, heads)
		if err != nil {
			return err
		}
		all, warnings, err := dataStore.ListKnowledge()
		if err != nil {
			return err
		}
		if len(warnings) != 0 {
			return fmt.Errorf("read Knowledge before commit: %s", warnings[0].String())
		}
		records, err := knowledgeRecords(dataStore, all)
		if err != nil {
			return err
		}
		vocabulary, err := knowledgeVocabulary(dataStore)
		if err != nil {
			return err
		}
		now := time.Now().UTC()
		resolved, err := domain.ResolveKnowledge(
			feedstock, assertion, domainResolution(*input.Resolution), records, vocabulary, now,
		)
		if err != nil {
			return err
		}
		feedstock.BrewedAssertions = append(feedstock.BrewedAssertions, assertion.ID)
		ids := make([]string, 0, len(resolved.Changed))
		for id := range resolved.Changed {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		for _, id := range ids {
			record := resolved.Changed[id]
			if err := tx.StageKnowledge(record); err != nil {
				return err
			}
		}
		if err := tx.StageBrewedFeedstock(feedstock, now); err != nil {
			return err
		}
		result.KnowledgeID = resolved.KnowledgeID
		result.Outcome = resolved.Outcome
		for _, target := range targets {
			result.Targets = append(result.Targets, target.Knowledge.ID)
		}
		return nil
	})
	return result, err
}

func validateVerification(dataStore Repository, current domain.Assertion, input SubmitInput) error {
	vocabulary, err := knowledgeVocabulary(dataStore)
	if err != nil {
		return err
	}
	corrected, _, err := domain.VerifyAssertion(
		current, input.Verification, input.CorrectedAssertion, input.Resolution != nil, vocabulary,
	)
	if err != nil {
		return err
	}
	if input.CorrectedAssertion != nil {
		*input.CorrectedAssertion = corrected
	}
	return nil
}

func validateResolution(
	assertion domain.Assertion,
	resolution *ResolutionInput,
	state agent.ReadState,
	heads []KnowledgeDocument,
) ([]KnowledgeDocument, error) {
	if resolution == nil {
		return nil, errors.New("knowledge resolution is required")
	}
	ids := domain.UniqueSorted(resolution.KnowledgeIDs)
	if !slices.Equal(ids, resolution.KnowledgeIDs) {
		return nil, errors.New("resolution knowledge_ids must be unique and sorted")
	}
	byID := make(map[string]KnowledgeDocument, len(heads))
	for _, file := range heads {
		byID[file.Knowledge.ID] = file
	}
	targets := make([]KnowledgeDocument, 0, len(ids))
	for _, id := range ids {
		file, exists := byID[id]
		if !exists || !slices.Contains(state.Catalog, id) || !slices.Contains(state.Inspected, id) {
			return nil, fmt.Errorf("resolution target %s was not a current inspected Knowledge head", id)
		}
		if file.Knowledge.Subject != assertion.Subject {
			return nil, fmt.Errorf("knowledge %s belongs to subject %q", id, file.Knowledge.Subject)
		}
		targets = append(targets, file)
	}
	return targets, nil
}

func catalogSnapshot(dataStore Repository, subject string) ([]KnowledgeDocument, string, error) {
	all, warnings, err := dataStore.ListKnowledge()
	if err != nil {
		return nil, "", err
	}
	if len(warnings) != 0 {
		return nil, "", fmt.Errorf("read Knowledge catalog: %s", warnings[0].String())
	}
	current := make(map[string]KnowledgeDocument)
	for _, file := range all {
		if file.Knowledge.Subject == subject &&
			(file.Knowledge.Status == domain.StatusPending || file.Knowledge.Status == domain.StatusActive) {
			current[file.Knowledge.ID] = file
		}
	}
	successors := make(map[string][]string)
	for _, file := range current {
		for _, predecessor := range file.Knowledge.Supersedes {
			if _, exists := current[predecessor]; exists {
				successors[predecessor] = append(successors[predecessor], file.Knowledge.ID)
			}
		}
	}
	for predecessor, ids := range successors {
		ids = domain.UniqueSorted(ids)
		if len(ids) > 1 {
			return nil, "", fmt.Errorf("multiple current Knowledge successors for %s: %s", predecessor, strings.Join(ids, ", "))
		}
	}
	files := make([]KnowledgeDocument, 0, len(current))
	for id, file := range current {
		if len(successors[id]) == 0 {
			files = append(files, file)
		}
	}
	slices.SortFunc(files, func(left, right KnowledgeDocument) int {
		return strings.Compare(left.Knowledge.ID, right.Knowledge.ID)
	})
	hash := sha256.New()
	for _, file := range files {
		hash.Write([]byte(file.Knowledge.ID))
		hash.Write([]byte{0})
		hash.Write([]byte(file.Digest))
		hash.Write([]byte{0})
	}
	return files, hex.EncodeToString(hash.Sum(nil)), nil
}

func knowledgeEstablishedFeedstock(dataStore Repository, knowledge domain.Knowledge) (domain.Feedstock, error) {
	var established domain.Feedstock
	found := false
	for _, feedstockID := range domain.NormalizeMasterNames(knowledge.Feedstocks) {
		feedstock, err := dataStore.GetFeedstock(feedstockID)
		if err != nil {
			return domain.Feedstock{}, fmt.Errorf("read knowledge feedstock %s: %w", feedstockID, err)
		}
		if !found || domain.CompareFeedstocks(feedstock, established) > 0 {
			established = feedstock
			found = true
		}
	}
	if !found {
		return domain.Feedstock{}, errors.New("knowledge has no source feedstock")
	}
	return established, nil
}

func validateSubject(dataStore Repository, subject string) error {
	entries, _, err := dataStore.LoadMasters("subjects")
	if err != nil {
		return err
	}
	if slices.ContainsFunc(entries, func(entry domain.MasterEntry) bool { return entry.Name == subject }) {
		return nil
	}
	return fmt.Errorf("subject %q is not defined in masters/subjects", subject)
}

func knowledgeVocabulary(dataStore Repository) (domain.Vocabulary, error) {
	types, _, err := dataStore.LoadMasters("types")
	if err != nil {
		return domain.Vocabulary{}, err
	}
	subjects, _, err := dataStore.LoadMasters("subjects")
	if err != nil {
		return domain.Vocabulary{}, err
	}
	return domain.NewVocabulary(types, subjects), nil
}

func knowledgeRecords(
	dataStore Repository,
	files []KnowledgeDocument,
) (map[string]domain.KnowledgeRecord, error) {
	records := make(map[string]domain.KnowledgeRecord, len(files))
	for _, file := range files {
		established, err := knowledgeEstablishedFeedstock(dataStore, file.Knowledge)
		if err != nil {
			return nil, err
		}
		records[file.Knowledge.ID] = domain.KnowledgeRecord{
			Knowledge: file.Knowledge, Statement: file.Statement,
			Rationale: file.Rationale, Established: established,
		}
	}
	return records, nil
}

func domainResolution(input ResolutionInput) domain.Resolution {
	result := domain.Resolution{
		Kind:         domain.ResolutionKind(input.Kind),
		KnowledgeIDs: append([]string(nil), input.KnowledgeIDs...),
	}
	if input.Draft != nil {
		result.Draft = &domain.KnowledgeDraft{
			Type: input.Draft.Type, Subject: input.Draft.Subject,
			Statement: input.Draft.Statement, Rationale: input.Draft.Rationale,
			Trigger: input.Draft.Trigger,
		}
	}
	return result
}
