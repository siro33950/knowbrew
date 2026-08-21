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

	"github.com/google/uuid"
	"github.com/siro33950/knowbrew/internal/application/agent"
	"github.com/siro33950/knowbrew/internal/domain"
)

type ResolutionKind = domain.ResolutionKind

const (
	ResolutionNew         = domain.ResolutionNew
	ResolutionEquivalent  = domain.ResolutionEquivalent
	ResolutionComplements = domain.ResolutionComplements
	ResolutionConflicts   = domain.ResolutionConflicts
)

var ErrStaleDecision = errors.New("knowledge changed while the feedstock was being evaluated")

type SubmitInput struct {
	FeedstockID string
	Knowledge   domain.KnowledgeCandidate
}

type SubmitResult struct {
	FeedstockID string `json:"feedstock_id"`
	Submitted   int    `json:"submitted"`
}

type ApplyResult struct {
	FeedstockID string                    `json:"feedstock_id"`
	Resolutions []domain.ResolutionResult `json:"resolutions"`
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
}

func Catalog(
	dataStore Repository,
	reads Invocation,
	subject string,
	candidateIDs []string,
) ([]CatalogEntry, error) {
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
	byID := make(map[string]KnowledgeDocument, len(files))
	for _, file := range files {
		byID[file.Knowledge.ID] = file
	}
	if candidateIDs == nil {
		candidateIDs = make([]string, 0, len(files))
		for _, file := range files {
			candidateIDs = append(candidateIDs, file.Knowledge.ID)
		}
	}
	entries := make([]CatalogEntry, 0, len(candidateIDs))
	ids := make([]string, 0, len(candidateIDs))
	for _, id := range candidateIDs {
		file, exists := byID[id]
		if !exists {
			continue
		}
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
		cataloged := false
		for _, subjectState := range state.Subjects {
			if slices.Contains(subjectState.Catalog, id) {
				cataloged = true
				break
			}
		}
		if !cataloged {
			return nil, fmt.Errorf("knowledge %s was not present in an invocation catalog", id)
		}
		file, err := dataStore.FindKnowledge(id)
		if err != nil {
			return nil, err
		}
		subjectState, exists := state.Subjects[file.Knowledge.Subject]
		if !exists || !slices.Contains(subjectState.Catalog, id) {
			return nil, fmt.Errorf("knowledge %s was not present in its subject invocation catalog", id)
		}
		result = append(result, ShownKnowledge{
			ID: id, Type: file.Knowledge.Type, Subject: file.Knowledge.Subject,
			Statement: file.Statement, Rationale: file.Rationale,
		})
	}
	if err := reads.RecordInspected(ids); err != nil {
		return nil, err
	}
	return result, nil
}

func Submit(dataStore Repository, reads Invocation, input SubmitInput) (SubmitResult, error) {
	if !reads.IsBrewInvocation() {
		return SubmitResult{}, errors.New("knowledge submit is available only inside a Brew invocation")
	}
	input.FeedstockID = strings.TrimSpace(input.FeedstockID)
	if err := reads.ValidateFeedstock(input.FeedstockID); err != nil {
		return SubmitResult{}, err
	}
	state, err := reads.ReadState()
	if err != nil {
		return SubmitResult{}, err
	}
	if len(state.Submitted) >= domain.MaxKnowledgePerFeedstock {
		return SubmitResult{}, fmt.Errorf(
			"at most %d Knowledge candidates are allowed per feedstock",
			domain.MaxKnowledgePerFeedstock,
		)
	}
	candidate := normalizeCandidate(input.Knowledge)
	if err := validateCandidateForSubmission(dataStore, candidate, state); err != nil {
		return SubmitResult{}, err
	}
	statementKey := normalizedStatement(candidate.Statement)
	for _, submitted := range state.Submitted {
		if normalizedStatement(submitted.Statement) == statementKey {
			return SubmitResult{}, errors.New("knowledge candidate duplicates a submitted statement")
		}
		if submitted.Resolution.Kind == ResolutionConflicts &&
			len(submitted.Resolution.KnowledgeIDs) != 0 &&
			candidate.Resolution.Kind == ResolutionConflicts &&
			submitted.Resolution.KnowledgeIDs[0] == candidate.Resolution.KnowledgeIDs[0] {
			return SubmitResult{}, fmt.Errorf(
				"knowledge %s is already the conflicts target of a submitted candidate",
				candidate.Resolution.KnowledgeIDs[0],
			)
		}
	}
	if err := reads.RecordSubmitted(candidate); err != nil {
		return SubmitResult{}, err
	}
	return SubmitResult{FeedstockID: input.FeedstockID, Submitted: len(state.Submitted) + 1}, nil
}

func Apply(
	ctx context.Context,
	dataStore Repository,
	feedstockID string,
	reads agent.ReadState,
) (ApplyResult, error) {
	feedstockID = strings.TrimSpace(feedstockID)
	result := ApplyResult{FeedstockID: feedstockID}
	err := dataStore.Transaction(ctx, func(tx Transaction) error {
		feedstock, err := dataStore.GetFeedstock(feedstockID)
		if err != nil {
			return err
		}
		if !feedstock.PendingBrew() {
			return fmt.Errorf("feedstock %s is not pending Brew", feedstockID)
		}
		if err := validateSubmittedCandidates(dataStore, reads); err != nil {
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
			feedstock,
			reads.Submitted,
			records,
			vocabulary,
			func() string { return "kn-" + uuid.NewString() },
			now,
		)
		if err != nil {
			return err
		}
		ids := make([]string, 0, len(resolved.Changed))
		for id := range resolved.Changed {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		for _, id := range ids {
			if err := tx.StageKnowledge(resolved.Changed[id]); err != nil {
				return err
			}
		}
		if err := tx.StageBrewedFeedstock(feedstock, now); err != nil {
			return err
		}
		result.Resolutions = resolved.Results
		return nil
	})
	return result, err
}

func validateSubmittedCandidates(dataStore Repository, state agent.ReadState) error {
	if len(state.Submitted) > domain.MaxKnowledgePerFeedstock {
		return fmt.Errorf(
			"at most %d Knowledge candidates are allowed per feedstock",
			domain.MaxKnowledgePerFeedstock,
		)
	}
	seenStatements := make(map[string]struct{}, len(state.Submitted))
	replacementTargets := make(map[string]struct{})
	vocabulary, err := knowledgeVocabulary(dataStore)
	if err != nil {
		return err
	}
	type subjectSnapshot struct {
		heads  []KnowledgeDocument
		digest string
	}
	snapshots := make(map[string]subjectSnapshot)
	for index, candidate := range state.Submitted {
		candidate = normalizeCandidate(candidate)
		if err := validateCandidateBasics(candidate, vocabulary); err != nil {
			return fmt.Errorf("knowledge candidate %d: %w", index+1, err)
		}
		snapshot, exists := snapshots[candidate.Subject]
		if !exists {
			heads, digest, snapshotErr := catalogSnapshot(dataStore, candidate.Subject)
			if snapshotErr != nil {
				return fmt.Errorf("knowledge candidate %d: %w", index+1, snapshotErr)
			}
			snapshot = subjectSnapshot{heads: heads, digest: digest}
			snapshots[candidate.Subject] = snapshot
		}
		if err := validateCandidateReadState(
			candidate,
			state,
			vocabulary,
			snapshot.heads,
			snapshot.digest,
		); err != nil {
			return fmt.Errorf("knowledge candidate %d: %w", index+1, err)
		}
		key := normalizedStatement(candidate.Statement)
		if _, exists := seenStatements[key]; exists {
			return fmt.Errorf("knowledge candidate %d duplicates a submitted statement", index+1)
		}
		seenStatements[key] = struct{}{}
		if candidate.Resolution.Kind == ResolutionConflicts ||
			candidate.Resolution.Kind == ResolutionComplements {
			target := candidate.Resolution.KnowledgeIDs[0]
			if _, exists := replacementTargets[target]; exists {
				return fmt.Errorf("knowledge %s is the replacement target of multiple candidates", target)
			}
			replacementTargets[target] = struct{}{}
		}
	}
	return nil
}

func validateCandidateForSubmission(
	dataStore Repository,
	candidate domain.KnowledgeCandidate,
	state agent.ReadState,
) error {
	vocabulary, err := knowledgeVocabulary(dataStore)
	if err != nil {
		return err
	}
	if err := validateCandidateBasics(candidate, vocabulary); err != nil {
		return err
	}
	heads, digest, err := catalogSnapshot(dataStore, candidate.Subject)
	if err != nil {
		return err
	}
	return validateCandidateReadState(candidate, state, vocabulary, heads, digest)
}

func validateCandidateBasics(
	candidate domain.KnowledgeCandidate,
	vocabulary domain.Vocabulary,
) error {
	if err := vocabulary.ValidateType(candidate.Type); err != nil {
		return fmt.Errorf("type: %w", err)
	}
	if err := vocabulary.ValidateSubject(candidate.Subject); err != nil {
		return err
	}
	if candidate.Statement == "" {
		return errors.New("knowledge statement is required")
	}
	if strings.Contains(candidate.Statement, "\r") {
		return errors.New("knowledge statement must use LF line endings")
	}
	return nil
}

func validateCandidateReadState(
	candidate domain.KnowledgeCandidate,
	state agent.ReadState,
	vocabulary domain.Vocabulary,
	heads []KnowledgeDocument,
	digest string,
) error {
	subjectState, exists := state.Subjects[candidate.Subject]
	if !exists || strings.TrimSpace(subjectState.Digest) == "" {
		return fmt.Errorf("subject %q has not been cataloged in this invocation", candidate.Subject)
	}
	if subjectState.Digest != digest {
		return ErrStaleDecision
	}
	return validateResolution(candidate, subjectState, state.Inspected, heads, vocabulary)
}

func validateResolution(
	candidate domain.KnowledgeCandidate,
	subjectState agent.SubjectReadState,
	inspected []string,
	heads []KnowledgeDocument,
	vocabulary domain.Vocabulary,
) error {
	resolution := candidate.Resolution
	ids := domain.UniqueSorted(resolution.KnowledgeIDs)
	if !slices.Equal(ids, resolution.KnowledgeIDs) {
		return errors.New("resolution knowledge_ids must be unique and sorted")
	}
	switch resolution.Kind {
	case ResolutionNew:
		if len(ids) != 0 || resolution.Draft != nil {
			return errors.New("new requires no target and no draft")
		}
	case ResolutionEquivalent, ResolutionConflicts:
		if len(ids) != 1 || resolution.Draft != nil {
			return fmt.Errorf("%s requires exactly one target and no draft", resolution.Kind)
		}
	case ResolutionComplements:
		if len(ids) != 1 || resolution.Draft == nil {
			return errors.New("complements requires exactly one target and a draft")
		}
		draft := resolution.Draft
		if err := vocabulary.ValidateType(draft.Type); err != nil {
			return fmt.Errorf("merged draft type: %w", err)
		}
		if domain.MasterName(draft.Subject) != candidate.Subject {
			return errors.New("merged draft must preserve the candidate subject")
		}
		if strings.TrimSpace(draft.Statement) == "" {
			return errors.New("merged draft statement is required")
		}
		if strings.Contains(draft.Statement, "\r") {
			return errors.New("merged draft statement must use LF line endings")
		}
	default:
		return fmt.Errorf("invalid resolution kind %q", resolution.Kind)
	}
	byID := make(map[string]KnowledgeDocument, len(heads))
	for _, file := range heads {
		byID[file.Knowledge.ID] = file
	}
	for _, id := range ids {
		file, exists := byID[id]
		if !exists || !slices.Contains(subjectState.Catalog, id) || !slices.Contains(inspected, id) {
			return fmt.Errorf("resolution target %s was not a current inspected Knowledge head", id)
		}
		if file.Knowledge.Subject != candidate.Subject {
			return fmt.Errorf("knowledge %s belongs to subject %q", id, file.Knowledge.Subject)
		}
	}
	return nil
}

func normalizeCandidate(candidate domain.KnowledgeCandidate) domain.KnowledgeCandidate {
	candidate.Type = domain.KnowledgeType(strings.TrimSpace(string(candidate.Type)))
	candidate.Subject = domain.MasterName(candidate.Subject)
	candidate.Statement = strings.TrimSpace(candidate.Statement)
	candidate.Rationale = strings.TrimSpace(candidate.Rationale)
	if candidate.Resolution.Draft != nil {
		draft := *candidate.Resolution.Draft
		draft.Type = domain.KnowledgeType(strings.TrimSpace(string(draft.Type)))
		draft.Subject = domain.MasterName(draft.Subject)
		draft.Statement = strings.TrimSpace(draft.Statement)
		draft.Rationale = strings.TrimSpace(draft.Rationale)
		candidate.Resolution.Draft = &draft
	}
	return candidate
}

func normalizedStatement(statement string) string {
	return strings.ToLower(strings.Join(strings.Fields(statement), " "))
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
