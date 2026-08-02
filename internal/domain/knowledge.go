package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

type ResolutionKind string

const (
	ResolutionNew         ResolutionKind = "new"
	ResolutionEquivalent  ResolutionKind = "equivalent"
	ResolutionComplements ResolutionKind = "complements"
	ResolutionConflicts   ResolutionKind = "conflicts"
)

type KnowledgeDraft struct {
	Type      KnowledgeType
	Subject   string
	Statement string
	Rationale string
	Trigger   string
}

type Resolution struct {
	Kind         ResolutionKind
	KnowledgeIDs []string
	Draft        *KnowledgeDraft
}

type KnowledgeRecord struct {
	Knowledge   Knowledge
	Statement   string
	Rationale   string
	Established Feedstock
}

type ResolutionResult struct {
	KnowledgeID string
	Outcome     string
	Changed     map[string]KnowledgeRecord
}

type LifecycleIssue struct {
	KnowledgeID string
	Err         error
}

func AssertionReference(feedstockID, assertionID string) string {
	return MasterName(feedstockID) + "#" + MasterName(assertionID)
}

func KnowledgeID(reference string) string {
	digest := sha256.Sum256([]byte(reference))
	return "kn-" + hex.EncodeToString(digest[:16])
}

func NewKnowledgeFromAssertion(source Feedstock, assertion Assertion, now time.Time) Knowledge {
	return newKnowledgeRecord(source, assertion, nil, now).Knowledge
}

func ResolveKnowledge(
	source Feedstock,
	assertion Assertion,
	resolution Resolution,
	records map[string]KnowledgeRecord,
	vocabulary Vocabulary,
	now time.Time,
) (ResolutionResult, error) {
	if assertion.Subject == "" {
		return ResolutionResult{}, errors.New("subjectless assertions cannot become Knowledge")
	}
	if now.IsZero() {
		return ResolutionResult{}, errors.New("resolution time is required")
	}
	if err := validateResolution(assertion, resolution, records, vocabulary); err != nil {
		return ResolutionResult{}, err
	}
	working := cloneKnowledgeRecords(records)
	changed := make(map[string]KnowledgeRecord)
	reference := AssertionReference(source.ID, assertion.ID)
	result := ResolutionResult{Changed: changed}
	switch resolution.Kind {
	case ResolutionNew:
		record := newKnowledgeRecord(source, assertion, nil, now)
		if _, exists := working[record.Knowledge.ID]; exists {
			return ResolutionResult{}, fmt.Errorf("knowledge ID collision: %s", record.Knowledge.ID)
		}
		working[record.Knowledge.ID] = record
		changed[record.Knowledge.ID] = record
		result.KnowledgeID = record.Knowledge.ID
		result.Outcome = "created"
	case ResolutionEquivalent:
		target := working[resolution.KnowledgeIDs[0]]
		target.Knowledge.Feedstocks = UniqueSorted(append(target.Knowledge.Feedstocks, source.ID))
		target.Knowledge.Assertions = UniqueSorted(append(target.Knowledge.Assertions, reference))
		target.Knowledge.Updated = now
		working[target.Knowledge.ID] = target
		changed[target.Knowledge.ID] = target
		result.KnowledgeID = target.Knowledge.ID
		result.Outcome = "evidence_added"
	case ResolutionConflicts:
		target := working[resolution.KnowledgeIDs[0]]
		compared := CompareFeedstocks(source, target.Established)
		if compared < 0 || (compared == 0 && reference < FirstAssertionReference(target.Knowledge)) {
			result.KnowledgeID = target.Knowledge.ID
			result.Outcome = "historical_conflict_ignored"
			break
		}
		record := newKnowledgeRecord(source, assertion, replacementPredecessors(target, working), now)
		if _, exists := working[record.Knowledge.ID]; exists {
			return ResolutionResult{}, fmt.Errorf("knowledge ID collision: %s", record.Knowledge.ID)
		}
		working[record.Knowledge.ID] = record
		changed[record.Knowledge.ID] = record
		retirePendingTarget(target, record.Knowledge.ID, now, working, changed)
		result.KnowledgeID = record.Knowledge.ID
		result.Outcome = "replaced"
	case ResolutionComplements:
		target := working[resolution.KnowledgeIDs[0]]
		draft := *resolution.Draft
		id := KnowledgeID(reference)
		if _, exists := working[id]; exists {
			return ResolutionResult{}, fmt.Errorf("knowledge ID collision: %s", id)
		}
		knowledge := Knowledge{
			ID: id, Created: now, Updated: now, EstablishedBy: source.ID,
			Type: draft.Type, Subject: MasterName(draft.Subject),
			Feedstocks: UniqueSorted(append(append([]string{}, target.Knowledge.Feedstocks...), source.ID)),
			Assertions: UniqueSorted(append(append([]string{}, target.Knowledge.Assertions...), reference)),
			Supersedes: replacementPredecessors(target, working), Trigger: strings.TrimSpace(draft.Trigger),
			Status: StatusPending,
		}
		record := KnowledgeRecord{
			Knowledge: knowledge, Statement: strings.TrimSpace(draft.Statement),
			Rationale: strings.TrimSpace(draft.Rationale), Established: source,
		}
		working[id] = record
		changed[id] = record
		retirePendingTarget(target, id, now, working, changed)
		result.KnowledgeID = id
		result.Outcome = "merged"
	}
	if err := ValidateKnowledgeGraph(working); err != nil {
		return ResolutionResult{}, err
	}
	return result, nil
}

func ValidateKnowledgeGraph(records map[string]KnowledgeRecord) error {
	graph := make(map[string][]string)
	eligibleSuccessors := make(map[string][]string)
	for id, record := range records {
		knowledge := record.Knowledge
		knowledge.Status = EffectiveKnowledgeStatus(knowledge)
		if err := ValidateKnowledge(knowledge); err != nil {
			return fmt.Errorf("validate knowledge %s: %w", id, err)
		}
		if strings.TrimSpace(record.Statement) == "" {
			return fmt.Errorf("validate knowledge %s body: statement is required", id)
		}
		for _, predecessor := range knowledge.Supersedes {
			if predecessor == id {
				return fmt.Errorf("knowledge %s supersedes itself", id)
			}
			if _, exists := records[predecessor]; !exists {
				return fmt.Errorf("knowledge %s supersedes missing knowledge %s", id, predecessor)
			}
			graph[id] = append(graph[id], predecessor)
			if knowledge.Status == StatusPending || knowledge.Status == StatusActive {
				eligibleSuccessors[predecessor] = append(eligibleSuccessors[predecessor], id)
			}
		}
		if knowledge.SupersededBy != "" {
			successor, exists := records[knowledge.SupersededBy]
			if !exists || !slices.Contains(successor.Knowledge.Supersedes, id) {
				return fmt.Errorf("knowledge %s has inconsistent superseded_by %s", id, knowledge.SupersededBy)
			}
		}
	}
	for predecessor, successors := range eligibleSuccessors {
		if len(UniqueSorted(successors)) > 1 {
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
	for id := range records {
		if err := visit(id); err != nil {
			return err
		}
	}
	claims := make(map[string]string)
	for _, record := range KnowledgeHeads(records, "") {
		key := record.Knowledge.Subject + "\x00" + strings.ToLower(strings.Join(strings.Fields(record.Statement), " "))
		if previous, exists := claims[key]; exists {
			return fmt.Errorf("duplicate current Knowledge claims: %s and %s", previous, record.Knowledge.ID)
		}
		claims[key] = record.Knowledge.ID
	}
	return nil
}

func KnowledgeHeads(records map[string]KnowledgeRecord, subject string) []KnowledgeRecord {
	current := make(map[string]KnowledgeRecord)
	for id, record := range records {
		status := EffectiveKnowledgeStatus(record.Knowledge)
		if (status == StatusPending || status == StatusActive) &&
			(subject == "" || record.Knowledge.Subject == subject) {
			current[id] = record
		}
	}
	successors := make(map[string][]string)
	for _, record := range current {
		for _, predecessor := range record.Knowledge.Supersedes {
			if _, exists := current[predecessor]; exists {
				successors[predecessor] = append(successors[predecessor], record.Knowledge.ID)
			}
		}
	}
	result := make([]KnowledgeRecord, 0, len(current))
	for id, record := range current {
		if len(successors[id]) == 0 {
			result = append(result, record)
		}
	}
	slices.SortFunc(result, func(left, right KnowledgeRecord) int {
		return strings.Compare(left.Knowledge.ID, right.Knowledge.ID)
	})
	return result
}

func ReconcileKnowledgeLifecycle(
	records map[string]Knowledge,
	now time.Time,
) (map[string]Knowledge, []LifecycleIssue) {
	working := make(map[string]Knowledge, len(records))
	for id, knowledge := range records {
		working[id] = knowledge
	}
	changed := make(map[string]Knowledge)
	var issues []LifecycleIssue
	for id, knowledge := range working {
		if knowledge.Status != StatusSuperseded || knowledge.Approved || knowledge.InvalidatedAt != nil {
			continue
		}
		successor, exists := working[knowledge.SupersededBy]
		if exists && successor.Status != StatusInvalidated && slices.Contains(successor.Supersedes, id) {
			continue
		}
		knowledge.SupersededBy = ""
		knowledge.SupersededAt = nil
		knowledge.Updated = now
		knowledge.Status = StatusPending
		working[id] = knowledge
		changed[id] = knowledge
	}
	graph := make(map[string][]string)
	targets := make(map[string][]string)
	for id, knowledge := range working {
		if knowledge.Status != StatusActive && knowledge.Status != StatusPending {
			continue
		}
		for _, target := range knowledge.Supersedes {
			graph[id] = append(graph[id], target)
			targets[target] = append(targets[target], id)
		}
	}
	for target, successors := range targets {
		successors = UniqueSorted(successors)
		knowledge, exists := working[target]
		if !exists {
			for _, successor := range successors {
				issues = append(issues, LifecycleIssue{
					KnowledgeID: successor,
					Err:         fmt.Errorf("supersedes target %q does not exist", target),
				})
			}
			continue
		}
		if knowledge.Status == StatusInvalidated {
			continue
		}
		eligible := make([]string, 0, len(successors))
		for _, successor := range successors {
			successorStatus := working[successor].Status
			if successorStatus == StatusActive ||
				(successorStatus == StatusPending && knowledge.Status == StatusPending) {
				eligible = append(eligible, successor)
			}
		}
		if len(eligible) == 0 {
			continue
		}
		if len(eligible) != 1 {
			issues = append(issues, LifecycleIssue{
				KnowledgeID: target,
				Err: fmt.Errorf(
					"multiple eligible knowledge records supersede %q: %s",
					target,
					strings.Join(eligible, ", "),
				),
			})
			continue
		}
		successor := eligible[0]
		if successor == target || graphReaches(graph, target, successor, map[string]bool{}) {
			issues = append(issues, LifecycleIssue{
				KnowledgeID: successor,
				Err:         fmt.Errorf("supersession cycle between %q and %q", successor, target),
			})
			continue
		}
		if knowledge.Status == StatusSuperseded {
			if knowledge.SupersededBy != successor {
				issues = append(issues, LifecycleIssue{
					KnowledgeID: target,
					Err: fmt.Errorf(
						"knowledge is already superseded by %q, not %q",
						knowledge.SupersededBy,
						successor,
					),
				})
			}
			continue
		}
		knowledge.SupersededBy = successor
		knowledge.SupersededAt = &now
		knowledge.Updated = now
		knowledge.Status = StatusSuperseded
		working[target] = knowledge
		changed[target] = knowledge
	}
	return changed, issues
}

func CompareFeedstocks(left, right Feedstock) int {
	if compared := left.Timestamp.Compare(right.Timestamp); compared != 0 {
		return compared
	}
	if compared := strings.Compare(left.Session.ID, right.Session.ID); compared != 0 {
		return compared
	}
	if compared := strings.Compare(left.TurnID, right.TurnID); compared != 0 {
		return compared
	}
	return strings.Compare(left.ID, right.ID)
}

func FirstAssertionReference(knowledge Knowledge) string {
	if len(knowledge.Assertions) == 0 {
		return ""
	}
	values := append([]string(nil), knowledge.Assertions...)
	slices.Sort(values)
	return values[0]
}

func validateResolution(
	assertion Assertion,
	resolution Resolution,
	records map[string]KnowledgeRecord,
	vocabulary Vocabulary,
) error {
	ids := UniqueSorted(resolution.KnowledgeIDs)
	if !slices.Equal(ids, resolution.KnowledgeIDs) {
		return errors.New("resolution knowledge_ids must be unique and sorted")
	}
	for _, id := range ids {
		record, exists := records[id]
		if !exists {
			return fmt.Errorf("resolution target %s was not a current Knowledge head", id)
		}
		if record.Knowledge.Subject != assertion.Subject {
			return fmt.Errorf("knowledge %s belongs to subject %q", id, record.Knowledge.Subject)
		}
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
		if err := validateKnowledgeDraft(assertion.Subject, *resolution.Draft, vocabulary); err != nil {
			return err
		}
	default:
		return fmt.Errorf("invalid resolution kind %q", resolution.Kind)
	}
	return nil
}

func validateKnowledgeDraft(subject string, draft KnowledgeDraft, vocabulary Vocabulary) error {
	if err := vocabulary.ValidateType(draft.Type); err != nil {
		return err
	}
	if MasterName(draft.Subject) != subject {
		return errors.New("merged draft must preserve the assertion subject")
	}
	if strings.TrimSpace(draft.Statement) == "" {
		return errors.New("knowledge statement is required")
	}
	if strings.ContainsAny(draft.Statement, "\r\n") {
		return errors.New("knowledge statement must be one line")
	}
	if draft.Trigger != "" && draft.Trigger != "always" {
		return fmt.Errorf("unsupported trigger %q", draft.Trigger)
	}
	return nil
}

func newKnowledgeRecord(source Feedstock, assertion Assertion, supersedes []string, now time.Time) KnowledgeRecord {
	reference := AssertionReference(source.ID, assertion.ID)
	id := KnowledgeID(reference)
	return KnowledgeRecord{
		Knowledge: Knowledge{
			ID: id, Created: now, Updated: now, EstablishedBy: source.ID,
			Type: assertion.Type, Subject: assertion.Subject, Feedstocks: []string{source.ID},
			Assertions: []string{reference}, Supersedes: UniqueSorted(supersedes),
			Trigger: assertion.Trigger, Status: StatusPending,
		},
		Statement: assertion.Statement, Rationale: assertion.Rationale, Established: source,
	}
}

func replacementPredecessors(target KnowledgeRecord, records map[string]KnowledgeRecord) []string {
	result := []string{target.Knowledge.ID}
	for _, id := range target.Knowledge.Supersedes {
		ancestor, exists := records[id]
		if exists && ancestor.Knowledge.Status == StatusActive {
			result = append(result, id)
		}
	}
	return UniqueSorted(result)
}

func retirePendingTarget(
	target KnowledgeRecord,
	successor string,
	now time.Time,
	records map[string]KnowledgeRecord,
	changed map[string]KnowledgeRecord,
) {
	if target.Knowledge.Status != StatusPending {
		return
	}
	target.Knowledge.SupersededBy = successor
	target.Knowledge.SupersededAt = &now
	target.Knowledge.Updated = now
	target.Knowledge.Status = StatusSuperseded
	records[target.Knowledge.ID] = target
	changed[target.Knowledge.ID] = target
}

func cloneKnowledgeRecords(records map[string]KnowledgeRecord) map[string]KnowledgeRecord {
	result := make(map[string]KnowledgeRecord, len(records))
	for id, record := range records {
		record.Knowledge.Feedstocks = append([]string(nil), record.Knowledge.Feedstocks...)
		record.Knowledge.Assertions = append([]string(nil), record.Knowledge.Assertions...)
		record.Knowledge.Supersedes = append([]string(nil), record.Knowledge.Supersedes...)
		result[id] = record
	}
	return result
}

func graphReaches(graph map[string][]string, current, target string, seen map[string]bool) bool {
	if current == target {
		return true
	}
	if seen[current] {
		return false
	}
	seen[current] = true
	for _, next := range graph[current] {
		if graphReaches(graph, next, target, seen) {
			return true
		}
	}
	return false
}
