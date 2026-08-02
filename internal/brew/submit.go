package brew

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/siro33950/knowbrew/internal/config"
	"github.com/siro33950/knowbrew/internal/domain"
	"github.com/siro33950/knowbrew/internal/invocation"
	"github.com/siro33950/knowbrew/internal/knowledgefmt"
	"github.com/siro33950/knowbrew/internal/store"
)

type VerificationStatus string
type ResolutionKind string

const (
	VerificationVerified  VerificationStatus = "verified"
	VerificationCorrected VerificationStatus = "corrected"
	VerificationRejected  VerificationStatus = "rejected"

	ResolutionNew         ResolutionKind = "new"
	ResolutionEquivalent  ResolutionKind = "equivalent"
	ResolutionComplements ResolutionKind = "complements"
	ResolutionConflicts   ResolutionKind = "conflicts"
)

var ErrStaleDecision = errors.New("Knowledge changed while the assertion was being evaluated")

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

func Catalog(dataStore *store.Store, subject string) ([]CatalogEntry, error) {
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
		statement, _, err := knowledgeBody(file.Body)
		if err != nil {
			return nil, fmt.Errorf("read knowledge %s: %w", file.Knowledge.ID, err)
		}
		entries = append(entries, CatalogEntry{
			ID: file.Knowledge.ID, Type: file.Knowledge.Type,
			Subject: file.Knowledge.Subject, Statement: statement,
		})
		ids = append(ids, file.Knowledge.ID)
	}
	if err := invocation.RecordCatalog(dataStore.Root, subject, ids, digest); err != nil {
		return nil, err
	}
	return entries, nil
}

func Show(dataStore *store.Store, ids []string) ([]ShownKnowledge, error) {
	ids = domain.UniqueSorted(ids)
	if len(ids) == 0 {
		return nil, errors.New("knowledge show requires at least one ID")
	}
	state, err := invocation.CurrentReadState(dataStore.Root)
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
		statement, rationale, err := knowledgeBody(file.Body)
		if err != nil {
			return nil, err
		}
		result = append(result, ShownKnowledge{
			ID: id, Type: file.Knowledge.Type, Subject: file.Knowledge.Subject,
			Statement: statement, Rationale: rationale, Trigger: file.Knowledge.Trigger,
		})
	}
	if err := invocation.RecordInspected(dataStore.Root, ids); err != nil {
		return nil, err
	}
	return result, nil
}

// Submit remains an internal compatibility entry point. The agent is not
// granted this command; normal Brew applies the structured decision in its
// parent process.
func Submit(ctx context.Context, dataStore *store.Store, input SubmitInput) (SubmitResult, error) {
	if strings.TrimSpace(os.Getenv(config.InvocationIDEnvironment)) == "" ||
		strings.TrimSpace(os.Getenv(config.InvocationFeedstockEnvironment)) == "" ||
		strings.TrimSpace(os.Getenv(config.InvocationAssertionEnvironment)) == "" {
		return SubmitResult{}, errors.New("knowledge submit is available only inside an assertion invocation")
	}
	if err := invocation.ValidateFeedstock(input.FeedstockID); err != nil {
		return SubmitResult{}, err
	}
	if err := invocation.ValidateAssertion(input.AssertionID); err != nil {
		return SubmitResult{}, err
	}
	reads, err := invocation.CurrentReadState(dataStore.Root)
	if err != nil {
		return SubmitResult{}, err
	}
	return Apply(ctx, dataStore, input, reads)
}

func Apply(
	ctx context.Context,
	dataStore *store.Store,
	input SubmitInput,
	reads invocation.ReadState,
) (SubmitResult, error) {
	input.FeedstockID = strings.TrimSpace(input.FeedstockID)
	input.AssertionID = strings.TrimSpace(input.AssertionID)
	result := SubmitResult{
		FeedstockID: input.FeedstockID, AssertionID: input.AssertionID,
		Verification: input.Verification,
	}
	err := dataStore.Transaction(ctx, func(tx *store.Transaction) error {
		feedstock, _, err := dataStore.FindFeedstock(input.FeedstockID)
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
		targets, err := validateResolution(dataStore, assertion, input.Resolution, reads, heads)
		if err != nil {
			return err
		}
		all, warnings, err := dataStore.ListAllKnowledge()
		if err != nil {
			return err
		}
		if len(warnings) != 0 {
			return fmt.Errorf("read Knowledge before commit: %s", warnings[0].String())
		}
		files := make(map[string]store.KnowledgeFile, len(all)+1)
		for _, file := range all {
			files[file.Knowledge.ID] = file
		}
		changed := make(map[string]struct{})
		now := time.Now().UTC()
		knowledgeID, outcome, err := planResolution(
			dataStore, feedstock, assertion, *input.Resolution, targets, files, changed, now,
		)
		if err != nil {
			return err
		}
		feedstock.BrewedAssertions = append(feedstock.BrewedAssertions, assertion.ID)
		if err := validateKnowledgeGraph(files); err != nil {
			return err
		}
		ids := make([]string, 0, len(changed))
		for id := range changed {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		for _, id := range ids {
			file := files[id]
			if err := tx.StageKnowledge(file.Knowledge, file.Body); err != nil {
				return err
			}
		}
		if err := tx.StageBrewedFeedstock(feedstock, now); err != nil {
			return err
		}
		result.KnowledgeID = knowledgeID
		result.Outcome = outcome
		for _, target := range targets {
			result.Targets = append(result.Targets, target.Knowledge.ID)
		}
		return nil
	})
	return result, err
}

func validateVerification(dataStore *store.Store, current domain.Assertion, input SubmitInput) error {
	switch input.Verification {
	case VerificationVerified:
		if input.CorrectedAssertion != nil || input.Resolution == nil {
			return errors.New("verified requires a resolution and no corrected assertion")
		}
	case VerificationCorrected:
		if input.CorrectedAssertion == nil || input.Resolution == nil {
			return errors.New("corrected requires a corrected assertion and resolution")
		}
		corrected := *input.CorrectedAssertion
		corrected.ID = current.ID
		if domain.MasterName(corrected.Subject) != current.Subject {
			return errors.New("Brew cannot change an assertion subject")
		}
		if err := dataStore.ValidateKnowledgeType(corrected.Type); err != nil {
			return err
		}
		if strings.TrimSpace(corrected.Statement) == "" || strings.ContainsAny(corrected.Statement, "\r\n") {
			return errors.New("corrected assertion statement must be one non-empty line")
		}
		input.CorrectedAssertion.ID = current.ID
	case VerificationRejected:
		if input.CorrectedAssertion != nil || input.Resolution != nil {
			return errors.New("rejected does not accept corrected assertion or resolution")
		}
	default:
		return fmt.Errorf("invalid verification %q", input.Verification)
	}
	return nil
}

func validateResolution(
	dataStore *store.Store,
	assertion domain.Assertion,
	resolution *ResolutionInput,
	state invocation.ReadState,
	heads []store.KnowledgeFile,
) ([]store.KnowledgeFile, error) {
	if resolution == nil {
		return nil, errors.New("Knowledge resolution is required")
	}
	ids := domain.UniqueSorted(resolution.KnowledgeIDs)
	if !slices.Equal(ids, resolution.KnowledgeIDs) {
		return nil, errors.New("resolution knowledge_ids must be unique and sorted")
	}
	byID := make(map[string]store.KnowledgeFile, len(heads))
	for _, file := range heads {
		byID[file.Knowledge.ID] = file
	}
	targets := make([]store.KnowledgeFile, 0, len(ids))
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
	switch resolution.Kind {
	case ResolutionNew:
		if len(targets) != 0 || resolution.Draft != nil {
			return nil, errors.New("new requires no target and no draft")
		}
	case ResolutionEquivalent, ResolutionConflicts:
		if len(targets) != 1 || resolution.Draft != nil {
			return nil, fmt.Errorf("%s requires exactly one target and no draft", resolution.Kind)
		}
	case ResolutionComplements:
		if len(targets) != 1 || resolution.Draft == nil {
			return nil, errors.New("complements requires exactly one target and a draft")
		}
		if err := validateDraft(dataStore, assertion.Subject, *resolution.Draft); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("invalid resolution kind %q", resolution.Kind)
	}
	return targets, nil
}

func planResolution(
	dataStore *store.Store,
	feedstock domain.Feedstock,
	assertion domain.Assertion,
	resolution ResolutionInput,
	targets []store.KnowledgeFile,
	files map[string]store.KnowledgeFile,
	changed map[string]struct{},
	now time.Time,
) (string, string, error) {
	reference := assertionReference(feedstock.ID, assertion.ID)
	switch resolution.Kind {
	case ResolutionNew:
		id, file, err := newKnowledgeFile(feedstock, assertion, nil, now)
		if err != nil {
			return "", "", err
		}
		if _, exists := files[id]; exists {
			return "", "", fmt.Errorf("knowledge ID collision: %s", id)
		}
		files[id] = file
		changed[id] = struct{}{}
		return id, "created", nil
	case ResolutionEquivalent:
		target := files[targets[0].Knowledge.ID]
		target.Knowledge.Feedstocks = domain.UniqueSorted(append(target.Knowledge.Feedstocks, feedstock.ID))
		target.Knowledge.Assertions = domain.UniqueSorted(append(target.Knowledge.Assertions, reference))
		target.Knowledge.Updated = now
		files[target.Knowledge.ID] = target
		changed[target.Knowledge.ID] = struct{}{}
		return target.Knowledge.ID, "evidence_added", nil
	case ResolutionConflicts:
		target := files[targets[0].Knowledge.ID]
		compared, err := compareSourceToKnowledge(dataStore, feedstock, target.Knowledge)
		if err != nil {
			return "", "", err
		}
		if compared < 0 || (compared == 0 && reference < firstAssertionReference(target.Knowledge)) {
			return target.Knowledge.ID, "historical_conflict_ignored", nil
		}
		id, file, err := newKnowledgeFile(feedstock, assertion, replacementPredecessors(target, files), now)
		if err != nil {
			return "", "", err
		}
		if _, exists := files[id]; exists {
			return "", "", fmt.Errorf("knowledge ID collision: %s", id)
		}
		files[id] = file
		changed[id] = struct{}{}
		retirePendingTarget(target, id, now, files, changed)
		return id, "replaced", nil
	case ResolutionComplements:
		target := files[targets[0].Knowledge.ID]
		draft := *resolution.Draft
		id := knowledgeID(reference)
		if _, exists := files[id]; exists {
			return "", "", fmt.Errorf("knowledge ID collision: %s", id)
		}
		body, err := proposalBody(draft.Statement, draft.Rationale)
		if err != nil {
			return "", "", err
		}
		knowledge := domain.Knowledge{
			ID: id, Created: now, Updated: now, EstablishedBy: feedstock.ID,
			Type: draft.Type, Subject: domain.MasterName(draft.Subject),
			Feedstocks: domain.UniqueSorted(append(append([]string{}, target.Knowledge.Feedstocks...), feedstock.ID)),
			Assertions: domain.UniqueSorted(append(append([]string{}, target.Knowledge.Assertions...), reference)),
			Supersedes: replacementPredecessors(target, files), Trigger: strings.TrimSpace(draft.Trigger),
			Status: domain.StatusPending,
		}
		files[id] = store.KnowledgeFile{Knowledge: knowledge, Body: body}
		changed[id] = struct{}{}
		retirePendingTarget(target, id, now, files, changed)
		return id, "merged", nil
	default:
		return "", "", fmt.Errorf("invalid resolution kind %q", resolution.Kind)
	}
}

func newKnowledgeFile(
	feedstock domain.Feedstock,
	assertion domain.Assertion,
	supersedes []string,
	now time.Time,
) (string, store.KnowledgeFile, error) {
	id := knowledgeID(assertionReference(feedstock.ID, assertion.ID))
	body, err := proposalBody(assertion.Statement, assertion.Rationale)
	if err != nil {
		return "", store.KnowledgeFile{}, err
	}
	knowledge := knowledgeFromAssertion(feedstock, assertion, now)
	knowledge.ID = id
	knowledge.Supersedes = domain.UniqueSorted(supersedes)
	return id, store.KnowledgeFile{Knowledge: knowledge, Body: body}, nil
}

func replacementPredecessors(target store.KnowledgeFile, files map[string]store.KnowledgeFile) []string {
	result := []string{target.Knowledge.ID}
	for _, id := range target.Knowledge.Supersedes {
		ancestor, exists := files[id]
		if exists && ancestor.Knowledge.Status == domain.StatusActive {
			result = append(result, id)
		}
	}
	return domain.UniqueSorted(result)
}

func retirePendingTarget(
	target store.KnowledgeFile,
	successor string,
	now time.Time,
	files map[string]store.KnowledgeFile,
	changed map[string]struct{},
) {
	if target.Knowledge.Status != domain.StatusPending {
		return
	}
	target.Knowledge.SupersededBy = successor
	target.Knowledge.SupersededAt = &now
	target.Knowledge.Updated = now
	target.Knowledge.Status = domain.StatusSuperseded
	files[target.Knowledge.ID] = target
	changed[target.Knowledge.ID] = struct{}{}
}

func catalogSnapshot(dataStore *store.Store, subject string) ([]store.KnowledgeFile, string, error) {
	all, warnings, err := dataStore.ListAllKnowledge()
	if err != nil {
		return nil, "", err
	}
	if len(warnings) != 0 {
		return nil, "", fmt.Errorf("read Knowledge catalog: %s", warnings[0].String())
	}
	current := make(map[string]store.KnowledgeFile)
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
	files := make([]store.KnowledgeFile, 0, len(current))
	for id, file := range current {
		if len(successors[id]) == 0 {
			files = append(files, file)
		}
	}
	slices.SortFunc(files, func(left, right store.KnowledgeFile) int {
		return strings.Compare(left.Knowledge.ID, right.Knowledge.ID)
	})
	hash := sha256.New()
	for _, file := range files {
		digest, err := store.FileDigest(file.Path)
		if err != nil {
			return nil, "", err
		}
		hash.Write([]byte(file.Knowledge.ID))
		hash.Write([]byte{0})
		hash.Write([]byte(digest))
		hash.Write([]byte{0})
	}
	return files, hex.EncodeToString(hash.Sum(nil)), nil
}

func validateKnowledgeGraph(files map[string]store.KnowledgeFile) error {
	graph := make(map[string][]string)
	eligibleSuccessors := make(map[string][]string)
	for id, file := range files {
		knowledge := file.Knowledge
		knowledge.Status = domain.EffectiveKnowledgeStatus(knowledge)
		if err := domain.ValidateKnowledge(knowledge); err != nil {
			return fmt.Errorf("validate knowledge %s: %w", id, err)
		}
		if _, _, err := knowledgeBody(file.Body); err != nil {
			return fmt.Errorf("validate knowledge %s body: %w", id, err)
		}
		for _, predecessor := range knowledge.Supersedes {
			if predecessor == id {
				return fmt.Errorf("knowledge %s supersedes itself", id)
			}
			if _, exists := files[predecessor]; !exists {
				return fmt.Errorf("knowledge %s supersedes missing knowledge %s", id, predecessor)
			}
			graph[id] = append(graph[id], predecessor)
			if knowledge.Status == domain.StatusPending || knowledge.Status == domain.StatusActive {
				eligibleSuccessors[predecessor] = append(eligibleSuccessors[predecessor], id)
			}
		}
		if knowledge.SupersededBy != "" {
			successor, exists := files[knowledge.SupersededBy]
			if !exists || !slices.Contains(successor.Knowledge.Supersedes, id) {
				return fmt.Errorf("knowledge %s has inconsistent superseded_by %s", id, knowledge.SupersededBy)
			}
		}
	}
	for predecessor, successors := range eligibleSuccessors {
		if len(domain.UniqueSorted(successors)) > 1 {
			return fmt.Errorf("knowledge %s has multiple current successors", predecessor)
		}
	}
	visiting := make(map[string]bool)
	visited := make(map[string]bool)
	var visit func(string) error
	visit = func(id string) error {
		if visiting[id] {
			return fmt.Errorf("knowledge supersession cycle at %s", id)
		}
		if visited[id] {
			return nil
		}
		visiting[id] = true
		for _, next := range graph[id] {
			if err := visit(next); err != nil {
				return err
			}
		}
		visiting[id] = false
		visited[id] = true
		return nil
	}
	for id := range files {
		if err := visit(id); err != nil {
			return err
		}
	}
	claims := make(map[string]string)
	headsBySubject := make(map[string][]store.KnowledgeFile)
	for _, file := range files {
		headsBySubject[file.Knowledge.Subject] = append(headsBySubject[file.Knowledge.Subject], file)
	}
	for subject := range headsBySubject {
		heads, err := brewHeadsFromFiles(headsBySubject[subject])
		if err != nil {
			return err
		}
		for _, file := range heads {
			statement, _, _ := knowledgeBody(file.Body)
			key := subject + "\x00" + strings.ToLower(strings.Join(strings.Fields(statement), " "))
			if previous, exists := claims[key]; exists {
				return fmt.Errorf("duplicate current Knowledge claims: %s and %s", previous, file.Knowledge.ID)
			}
			claims[key] = file.Knowledge.ID
		}
	}
	return nil
}

func brewHeadsFromFiles(files []store.KnowledgeFile) ([]store.KnowledgeFile, error) {
	current := make(map[string]store.KnowledgeFile)
	for _, file := range files {
		status := domain.EffectiveKnowledgeStatus(file.Knowledge)
		if status == domain.StatusPending || status == domain.StatusActive {
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
	for predecessor, values := range successors {
		if len(domain.UniqueSorted(values)) > 1 {
			return nil, fmt.Errorf("knowledge %s has multiple current successors", predecessor)
		}
	}
	var result []store.KnowledgeFile
	for id, file := range current {
		if len(successors[id]) == 0 {
			result = append(result, file)
		}
	}
	return result, nil
}

func firstAssertionReference(knowledge domain.Knowledge) string {
	if len(knowledge.Assertions) == 0 {
		return ""
	}
	values := append([]string(nil), knowledge.Assertions...)
	slices.Sort(values)
	return values[0]
}

func compareSourceToKnowledge(dataStore *store.Store, source domain.Feedstock, knowledge domain.Knowledge) (int, error) {
	established, err := knowledgeEstablishedFeedstock(dataStore, knowledge)
	if err != nil {
		return 0, err
	}
	return compareFeedstocks(source, established), nil
}

func knowledgeEstablishedFeedstock(dataStore *store.Store, knowledge domain.Knowledge) (domain.Feedstock, error) {
	var established domain.Feedstock
	found := false
	for _, feedstockID := range domain.NormalizeMasterNames(knowledge.Feedstocks) {
		feedstock, _, err := dataStore.FindFeedstock(feedstockID)
		if err != nil {
			return domain.Feedstock{}, fmt.Errorf("read knowledge feedstock %s: %w", feedstockID, err)
		}
		if !found || compareFeedstocks(feedstock, established) > 0 {
			established = feedstock
			found = true
		}
	}
	if !found {
		return domain.Feedstock{}, errors.New("knowledge has no source feedstock")
	}
	return established, nil
}

func validateDraft(dataStore *store.Store, subject string, draft KnowledgeDraft) error {
	if err := dataStore.ValidateKnowledgeType(draft.Type); err != nil {
		return err
	}
	if domain.MasterName(draft.Subject) != subject {
		return errors.New("merged draft must preserve the assertion subject")
	}
	if _, err := proposalBody(draft.Statement, draft.Rationale); err != nil {
		return err
	}
	if draft.Trigger != "" && draft.Trigger != "always" {
		return fmt.Errorf("unsupported trigger %q", draft.Trigger)
	}
	return nil
}

func validateSubject(dataStore *store.Store, subject string) error {
	entries, _, err := dataStore.LoadMasters("subjects")
	if err != nil {
		return err
	}
	if slices.ContainsFunc(entries, func(entry domain.MasterEntry) bool { return entry.Name == subject }) {
		return nil
	}
	return fmt.Errorf("subject %q is not defined in masters/subjects", subject)
}

func knowledgeFromAssertion(feedstock domain.Feedstock, assertion domain.Assertion, now time.Time) domain.Knowledge {
	return domain.Knowledge{
		Created: now, Updated: now, EstablishedBy: feedstock.ID,
		Type: assertion.Type, Subject: assertion.Subject,
		Feedstocks: []string{feedstock.ID},
		Assertions: []string{assertionReference(feedstock.ID, assertion.ID)},
		Trigger:    assertion.Trigger, Status: domain.StatusPending,
	}
}

func knowledgeID(reference string) string {
	digest := sha256.Sum256([]byte(reference))
	return "kn-" + hex.EncodeToString(digest[:16])
}

func proposalBody(statement, rationale string) (string, error) {
	return knowledgefmt.Encode(statement, rationale)
}

func knowledgeBody(body string) (string, string, error) {
	return knowledgefmt.Decode(body)
}
