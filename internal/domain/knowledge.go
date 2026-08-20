package domain

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

const MaxKnowledgePerFeedstock = 16

type ResolutionKind string

const (
	ResolutionNew         ResolutionKind = "new"
	ResolutionEquivalent  ResolutionKind = "equivalent"
	ResolutionComplements ResolutionKind = "complements"
	ResolutionConflicts   ResolutionKind = "conflicts"
)

type KnowledgeDraft struct {
	Type      KnowledgeType `json:"type"`
	Subject   string        `json:"subject"`
	Statement string        `json:"statement"`
	Rationale string        `json:"rationale"`
}

type Resolution struct {
	Kind         ResolutionKind  `json:"kind"`
	KnowledgeIDs []string        `json:"knowledge_ids"`
	Draft        *KnowledgeDraft `json:"draft"`
}

type KnowledgeCandidate struct {
	Type       KnowledgeType `json:"type"`
	Subject    string        `json:"subject"`
	Statement  string        `json:"statement"`
	Rationale  string        `json:"rationale"`
	Resolution Resolution    `json:"resolution"`
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
}

type KnowledgeResolution struct {
	Results []ResolutionResult
	Changed map[string]KnowledgeRecord
}

type LifecycleIssue struct {
	KnowledgeID string
	Err         error
}

func ResolveKnowledge(
	source Feedstock,
	candidates []KnowledgeCandidate,
	records map[string]KnowledgeRecord,
	vocabulary Vocabulary,
	newKnowledgeID func() string,
	now time.Time,
) (KnowledgeResolution, error) {
	if len(candidates) > MaxKnowledgePerFeedstock {
		return KnowledgeResolution{}, fmt.Errorf(
			"at most %d Knowledge candidates are allowed per feedstock",
			MaxKnowledgePerFeedstock,
		)
	}
	if now.IsZero() {
		return KnowledgeResolution{}, errors.New("resolution time is required")
	}
	if newKnowledgeID == nil {
		return KnowledgeResolution{}, errors.New("knowledge ID generator is required")
	}
	working := cloneKnowledgeRecords(records)
	changed := make(map[string]KnowledgeRecord)
	nextKnowledgeID := func() (string, error) {
		id := strings.TrimSpace(newKnowledgeID())
		if err := ValidateKnowledgeID(id); err != nil {
			return "", err
		}
		if _, exists := working[id]; exists {
			return "", fmt.Errorf("knowledge ID %s already exists", id)
		}
		return id, nil
	}
	result := KnowledgeResolution{
		Results: make([]ResolutionResult, 0, len(candidates)),
		Changed: changed,
	}
	for index, candidate := range candidates {
		candidate = normalizeKnowledgeCandidate(candidate)
		if err := validateKnowledgeCandidate(candidate, working, vocabulary); err != nil {
			return KnowledgeResolution{}, fmt.Errorf("knowledge candidate %d: %w", index+1, err)
		}
		resolution := candidate.Resolution
		itemResult := ResolutionResult{}
		switch resolution.Kind {
		case ResolutionNew:
			id, err := nextKnowledgeID()
			if err != nil {
				return KnowledgeResolution{}, fmt.Errorf("knowledge candidate %d: %w", index+1, err)
			}
			record, err := newKnowledgeRecord(source, candidate, nil, id, now)
			if err != nil {
				return KnowledgeResolution{}, fmt.Errorf("knowledge candidate %d: %w", index+1, err)
			}
			working[record.Knowledge.ID] = record
			changed[record.Knowledge.ID] = record
			itemResult.KnowledgeID = record.Knowledge.ID
			itemResult.Outcome = "created"
		case ResolutionEquivalent:
			target := working[resolution.KnowledgeIDs[0]]
			target.Knowledge.Feedstocks = UniqueSorted(append(target.Knowledge.Feedstocks, source.ID))
			target.Knowledge.Updated = now
			working[target.Knowledge.ID] = target
			changed[target.Knowledge.ID] = target
			itemResult.KnowledgeID = target.Knowledge.ID
			itemResult.Outcome = "evidence_added"
		case ResolutionConflicts:
			target := working[resolution.KnowledgeIDs[0]]
			if slices.Contains(target.Knowledge.Feedstocks, source.ID) {
				return KnowledgeResolution{}, fmt.Errorf(
					"knowledge candidate %d: conflicting Knowledge shares source feedstock %s",
					index+1,
					source.ID,
				)
			}
			compared := CompareFeedstocks(source, target.Established)
			if compared == 0 {
				return KnowledgeResolution{}, fmt.Errorf(
					"knowledge candidate %d: conflicting Knowledge has the same source turn",
					index+1,
				)
			}
			if compared < 0 {
				itemResult.KnowledgeID = target.Knowledge.ID
				itemResult.Outcome = "historical_conflict_ignored"
				break
			}
			id, err := nextKnowledgeID()
			if err != nil {
				return KnowledgeResolution{}, fmt.Errorf("knowledge candidate %d: %w", index+1, err)
			}
			record, err := newKnowledgeRecord(
				source,
				candidate,
				replacementPredecessors(target, working),
				id,
				now,
			)
			if err != nil {
				return KnowledgeResolution{}, fmt.Errorf("knowledge candidate %d: %w", index+1, err)
			}
			working[record.Knowledge.ID] = record
			changed[record.Knowledge.ID] = record
			retirePendingTarget(target, record.Knowledge.ID, now, working, changed)
			itemResult.KnowledgeID = record.Knowledge.ID
			itemResult.Outcome = "replaced"
		case ResolutionComplements:
			target := working[resolution.KnowledgeIDs[0]]
			draft := *resolution.Draft
			id, err := nextKnowledgeID()
			if err != nil {
				return KnowledgeResolution{}, fmt.Errorf("knowledge candidate %d: %w", index+1, err)
			}
			knowledge := Knowledge{
				ID: id, Created: now, Updated: now, EstablishedBy: source.ID,
				Type: draft.Type, Subject: MasterName(draft.Subject),
				Feedstocks: UniqueSorted(append(append([]string{}, target.Knowledge.Feedstocks...), source.ID)),
				Supersedes: replacementPredecessors(target, working),
				Status:     StatusPending,
			}
			record := KnowledgeRecord{
				Knowledge: knowledge, Statement: strings.TrimSpace(draft.Statement),
				Rationale: strings.TrimSpace(draft.Rationale), Established: source,
			}
			working[id] = record
			changed[id] = record
			retirePendingTarget(target, id, now, working, changed)
			itemResult.KnowledgeID = id
			itemResult.Outcome = "merged"
		}
		result.Results = append(result.Results, itemResult)
	}
	if err := ValidateKnowledgeGraph(working); err != nil {
		return KnowledgeResolution{}, err
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

type Vocabulary struct {
	types    map[KnowledgeType]struct{}
	subjects map[string]struct{}
}

func NewVocabulary(types, subjects []MasterEntry) Vocabulary {
	vocabulary := Vocabulary{
		types:    make(map[KnowledgeType]struct{}, len(types)),
		subjects: make(map[string]struct{}, len(subjects)),
	}
	for _, entry := range types {
		vocabulary.types[KnowledgeType(MasterName(entry.Name))] = struct{}{}
	}
	for _, entry := range subjects {
		vocabulary.subjects[MasterName(entry.Name)] = struct{}{}
	}
	return vocabulary
}

func (v Vocabulary) ValidateType(value KnowledgeType) error {
	value = KnowledgeType(strings.TrimSpace(string(value)))
	if err := ValidateKnowledgeTypeName(value); err != nil {
		return err
	}
	if _, exists := v.types[value]; !exists {
		return fmt.Errorf("knowledge type %q is not defined in masters/types", value)
	}
	return nil
}

func (v Vocabulary) ValidateSubject(value string) error {
	value = MasterName(value)
	if value == "" {
		return errors.New("knowledge subject is required")
	}
	if err := ValidateIdentifier(value, "knowledge subject"); err != nil {
		return err
	}
	if _, exists := v.subjects[value]; !exists {
		return fmt.Errorf("subject %q is not defined in masters/subjects", value)
	}
	return nil
}

func normalizeKnowledgeCandidate(candidate KnowledgeCandidate) KnowledgeCandidate {
	candidate.Type = KnowledgeType(strings.TrimSpace(string(candidate.Type)))
	candidate.Subject = MasterName(candidate.Subject)
	candidate.Statement = strings.TrimSpace(candidate.Statement)
	candidate.Rationale = strings.TrimSpace(candidate.Rationale)
	if candidate.Resolution.Draft != nil {
		draft := *candidate.Resolution.Draft
		draft.Type = KnowledgeType(strings.TrimSpace(string(draft.Type)))
		draft.Subject = MasterName(draft.Subject)
		draft.Statement = strings.TrimSpace(draft.Statement)
		draft.Rationale = strings.TrimSpace(draft.Rationale)
		candidate.Resolution.Draft = &draft
	}
	return candidate
}

func validateKnowledgeCandidate(
	candidate KnowledgeCandidate,
	records map[string]KnowledgeRecord,
	vocabulary Vocabulary,
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
	return validateResolution(candidate, records, vocabulary)
}

func validateResolution(candidate KnowledgeCandidate, records map[string]KnowledgeRecord, vocabulary Vocabulary) error {
	resolution := candidate.Resolution
	ids := UniqueSorted(resolution.KnowledgeIDs)
	if !slices.Equal(ids, resolution.KnowledgeIDs) {
		return errors.New("resolution knowledge_ids must be unique and sorted")
	}
	for _, id := range ids {
		record, exists := records[id]
		if !exists {
			return fmt.Errorf("resolution target %s was not a current Knowledge head", id)
		}
		if record.Knowledge.Subject != candidate.Subject {
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
		if err := validateKnowledgeDraft(candidate.Subject, *resolution.Draft, vocabulary); err != nil {
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
		return errors.New("merged draft must preserve the Knowledge subject")
	}
	if strings.TrimSpace(draft.Statement) == "" {
		return errors.New("knowledge statement is required")
	}
	if strings.Contains(draft.Statement, "\r") {
		return errors.New("knowledge statement must use LF line endings")
	}
	return nil
}

func newKnowledgeRecord(
	source Feedstock,
	candidate KnowledgeCandidate,
	supersedes []string,
	id string,
	now time.Time,
) (KnowledgeRecord, error) {
	id = strings.TrimSpace(id)
	if err := ValidateKnowledgeID(id); err != nil {
		return KnowledgeRecord{}, err
	}
	return KnowledgeRecord{
		Knowledge: Knowledge{
			ID: id, Created: now, Updated: now, EstablishedBy: source.ID,
			Type: candidate.Type, Subject: candidate.Subject, Feedstocks: []string{source.ID},
			Supersedes: UniqueSorted(supersedes),
			Status:     StatusPending,
		},
		Statement: candidate.Statement, Rationale: candidate.Rationale, Established: source,
	}, nil
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
