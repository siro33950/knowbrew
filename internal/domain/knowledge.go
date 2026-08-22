package domain

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

type ResolutionKind string

const (
	ResolutionDiscard     ResolutionKind = "discard"
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

type OrganizationAction struct {
	KnowledgeID string     `json:"knowledge_id"`
	Resolution  Resolution `json:"resolution"`
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
	Results  []ResolutionResult
	Changed  map[string]KnowledgeRecord
	Consumed []string
}

type LifecycleIssue struct {
	KnowledgeID string
	Err         error
}

func ExtractKnowledge(
	source Feedstock,
	drafts []KnowledgeDraft,
	vocabulary Vocabulary,
	newKnowledgeID func() string,
	now time.Time,
) ([]KnowledgeRecord, error) {
	if source.AnnotatedAt == nil {
		return nil, fmt.Errorf("feedstock %s is not drawn", source.ID)
	}
	if source.ExtractedAt != nil {
		return nil, fmt.Errorf("feedstock %s is already extracted", source.ID)
	}
	if now.IsZero() {
		return nil, errors.New("extraction time is required")
	}
	if newKnowledgeID == nil {
		return nil, errors.New("knowledge ID generator is required")
	}
	seen := make(map[string]struct{}, len(drafts))
	result := make([]KnowledgeRecord, 0, len(drafts))
	for index, draft := range drafts {
		draft = normalizeKnowledgeDraft(draft)
		if err := validateExtractedDraft(draft, vocabulary); err != nil {
			return nil, fmt.Errorf("knowledge draft %d: %w", index+1, err)
		}
		id := strings.TrimSpace(newKnowledgeID())
		if err := ValidateKnowledgeID(id); err != nil {
			return nil, fmt.Errorf("knowledge draft %d: %w", index+1, err)
		}
		if _, exists := seen[id]; exists {
			return nil, fmt.Errorf("knowledge draft %d: knowledge ID %s already exists", index+1, id)
		}
		seen[id] = struct{}{}
		record := KnowledgeRecord{
			Knowledge: Knowledge{
				ID: id, Created: now, Updated: now, EstablishedBy: source.ID,
				Type: draft.Type, Subject: draft.Subject, Feedstocks: []string{source.ID},
				Status: StatusPending,
			},
			Statement: draft.Statement, Rationale: draft.Rationale, Established: source,
		}
		if err := ValidateKnowledge(record.Knowledge); err != nil {
			return nil, fmt.Errorf("knowledge draft %d: %w", index+1, err)
		}
		result = append(result, record)
	}
	return result, nil
}

func OrganizeKnowledge(
	inputs []KnowledgeRecord,
	records map[string]KnowledgeRecord,
	actions []OrganizationAction,
	vocabulary Vocabulary,
	now time.Time,
) (KnowledgeResolution, error) {
	if now.IsZero() {
		return KnowledgeResolution{}, errors.New("organization time is required")
	}
	ordered := append([]KnowledgeRecord(nil), inputs...)
	slices.SortFunc(ordered, func(left, right KnowledgeRecord) int {
		if compared := CompareFeedstocks(left.Established, right.Established); compared != 0 {
			return compared
		}
		return strings.Compare(left.Knowledge.ID, right.Knowledge.ID)
	})
	if len(ordered) == 0 {
		if len(actions) != 0 {
			return KnowledgeResolution{}, errors.New("organization plan contains actions without inputs")
		}
		return KnowledgeResolution{Changed: map[string]KnowledgeRecord{}}, nil
	}
	subject := MasterName(ordered[0].Knowledge.Subject)
	if err := vocabulary.ValidateSubject(subject); err != nil {
		return KnowledgeResolution{}, err
	}
	inputByID := make(map[string]KnowledgeRecord, len(ordered))
	for _, input := range ordered {
		id := input.Knowledge.ID
		if _, exists := inputByID[id]; exists {
			return KnowledgeResolution{}, fmt.Errorf("duplicate organization input %s", id)
		}
		if input.Knowledge.OrganizedAt != nil {
			return KnowledgeResolution{}, fmt.Errorf("knowledge %s is already organized", id)
		}
		if MasterName(input.Knowledge.Subject) != subject {
			return KnowledgeResolution{}, fmt.Errorf("knowledge %s belongs to subject %q", id, input.Knowledge.Subject)
		}
		if err := validateExtractedDraft(KnowledgeDraft{
			Type: input.Knowledge.Type, Subject: input.Knowledge.Subject,
			Statement: input.Statement, Rationale: input.Rationale,
		}, vocabulary); err != nil {
			return KnowledgeResolution{}, fmt.Errorf("knowledge %s: %w", id, err)
		}
		inputByID[id] = input
	}
	actionByID := make(map[string]OrganizationAction, len(actions))
	for _, action := range actions {
		action.KnowledgeID = strings.TrimSpace(action.KnowledgeID)
		if _, exists := inputByID[action.KnowledgeID]; !exists {
			return KnowledgeResolution{}, fmt.Errorf("organization plan references unknown input %s", action.KnowledgeID)
		}
		if _, exists := actionByID[action.KnowledgeID]; exists {
			return KnowledgeResolution{}, fmt.Errorf("organization plan repeats input %s", action.KnowledgeID)
		}
		actionByID[action.KnowledgeID] = normalizeOrganizationAction(action)
	}
	if len(actionByID) != len(inputByID) {
		for _, input := range ordered {
			if _, exists := actionByID[input.Knowledge.ID]; !exists {
				return KnowledgeResolution{}, fmt.Errorf("organization plan omits input %s", input.Knowledge.ID)
			}
		}
	}
	working := cloneKnowledgeRecords(records)
	initialTargets := make(map[string]struct{})
	resolvedTargets := make(map[string]string)
	for _, head := range KnowledgeHeadsBySubject(working, subject) {
		initialTargets[head.Knowledge.ID] = struct{}{}
		resolvedTargets[head.Knowledge.ID] = head.Knowledge.ID
	}
	processed := make(map[string]struct{}, len(ordered))
	result := KnowledgeResolution{
		Results:  make([]ResolutionResult, 0, len(ordered)),
		Changed:  make(map[string]KnowledgeRecord),
		Consumed: make([]string, 0, len(ordered)),
	}
	for _, input := range ordered {
		action := actionByID[input.Knowledge.ID]
		if err := validateOrganizationResolution(action.Resolution, subject, vocabulary); err != nil {
			return KnowledgeResolution{}, fmt.Errorf("knowledge %s: %w", input.Knowledge.ID, err)
		}
		item := ResolutionResult{KnowledgeID: input.Knowledge.ID}
		keepInput := false
		resolution := action.Resolution
		switch resolution.Kind {
		case ResolutionDiscard:
			item.Outcome = "discarded"
		case ResolutionNew:
			record := input
			record.Knowledge.OrganizedAt = &now
			record.Knowledge.Status = StatusPending
			record.Knowledge.Updated = now
			working[record.Knowledge.ID] = record
			result.Changed[record.Knowledge.ID] = record
			keepInput = true
			resolvedTargets[input.Knowledge.ID] = input.Knowledge.ID
			item.Outcome = "created"
		case ResolutionEquivalent, ResolutionComplements, ResolutionConflicts:
			referencedID := resolution.KnowledgeIDs[0]
			if _, exists := initialTargets[referencedID]; !exists {
				if _, earlier := processed[referencedID]; !earlier {
					return KnowledgeResolution{}, fmt.Errorf(
						"knowledge %s relation target %s is not an initial head or earlier input",
						input.Knowledge.ID, referencedID,
					)
				}
			}
			targetID, exists := resolveOrganizationTarget(resolvedTargets, referencedID)
			if !exists {
				return KnowledgeResolution{}, fmt.Errorf(
					"knowledge %s relation target %s was consumed without a current head",
					input.Knowledge.ID, referencedID,
				)
			}
			target, err := currentOrganizationTarget(working, subject, targetID)
			if err != nil {
				return KnowledgeResolution{}, fmt.Errorf("knowledge %s: %w", input.Knowledge.ID, err)
			}
			switch resolution.Kind {
			case ResolutionEquivalent:
				target.Knowledge.Feedstocks = UniqueSorted(append(
					target.Knowledge.Feedstocks, input.Knowledge.Feedstocks...,
				))
				target.Knowledge.Updated = now
				working[targetID] = target
				result.Changed[targetID] = target
				resolvedTargets[input.Knowledge.ID] = targetID
				item.KnowledgeID = targetID
				item.Outcome = "evidence_added"
			case ResolutionComplements:
				draft := normalizeKnowledgeDraft(*resolution.Draft)
				record := input
				record.Knowledge.Type = draft.Type
				record.Knowledge.Subject = draft.Subject
				record.Knowledge.Feedstocks = UniqueSorted(append(
					target.Knowledge.Feedstocks, input.Knowledge.Feedstocks...,
				))
				record.Knowledge.Supersedes = replacementPredecessors(target, working)
				record.Knowledge.OrganizedAt = &now
				record.Knowledge.Updated = now
				record.Knowledge.Status = StatusPending
				record.Statement = draft.Statement
				record.Rationale = draft.Rationale
				working[record.Knowledge.ID] = record
				result.Changed[record.Knowledge.ID] = record
				retirePendingTarget(target, record.Knowledge.ID, now, working, result.Changed)
				keepInput = true
				resolvedTargets[targetID] = record.Knowledge.ID
				resolvedTargets[input.Knowledge.ID] = input.Knowledge.ID
				item.Outcome = "merged"
			case ResolutionConflicts:
				for _, feedstockID := range input.Knowledge.Feedstocks {
					if slices.Contains(target.Knowledge.Feedstocks, feedstockID) {
						return KnowledgeResolution{}, fmt.Errorf(
							"conflicting Knowledge shares source feedstock %s", feedstockID,
						)
					}
				}
				compared := CompareFeedstocks(input.Established, target.Established)
				if compared == 0 {
					return KnowledgeResolution{}, errors.New("conflicting Knowledge has the same source turn")
				}
				if compared < 0 {
					resolvedTargets[input.Knowledge.ID] = targetID
					item.KnowledgeID = targetID
					item.Outcome = "historical_conflict_ignored"
					break
				}
				record := input
				record.Knowledge.Supersedes = replacementPredecessors(target, working)
				record.Knowledge.OrganizedAt = &now
				record.Knowledge.Updated = now
				record.Knowledge.Status = StatusPending
				working[record.Knowledge.ID] = record
				result.Changed[record.Knowledge.ID] = record
				retirePendingTarget(target, record.Knowledge.ID, now, working, result.Changed)
				keepInput = true
				resolvedTargets[targetID] = record.Knowledge.ID
				resolvedTargets[input.Knowledge.ID] = input.Knowledge.ID
				item.Outcome = "replaced"
			}
		}
		if !keepInput {
			delete(working, input.Knowledge.ID)
		}
		processed[input.Knowledge.ID] = struct{}{}
		result.Consumed = append(result.Consumed, input.Knowledge.ID)
		result.Results = append(result.Results, item)
	}
	if err := ValidateKnowledgeGraph(working); err != nil {
		return KnowledgeResolution{}, err
	}
	return result, nil
}

func resolveOrganizationTarget(targets map[string]string, id string) (string, bool) {
	seen := make(map[string]struct{})
	for {
		next, exists := targets[id]
		if !exists {
			return "", false
		}
		if next == id {
			return id, true
		}
		if _, exists := seen[id]; exists {
			return "", false
		}
		seen[id] = struct{}{}
		id = next
	}
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
		if knowledge.OrganizedAt == nil {
			continue
		}
		for _, predecessor := range knowledge.Supersedes {
			if predecessor == id {
				return fmt.Errorf("knowledge %s supersedes itself", id)
			}
			predecessorRecord, exists := records[predecessor]
			if !exists || predecessorRecord.Knowledge.OrganizedAt == nil {
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
	for id := range graph {
		if err := visit(id); err != nil {
			return err
		}
	}
	claims := make(map[string]string)
	for _, record := range KnowledgeHeads(records) {
		key := record.Knowledge.Subject + "\x00" + strings.ToLower(strings.Join(strings.Fields(record.Statement), " "))
		if previous, exists := claims[key]; exists {
			return fmt.Errorf("duplicate current Knowledge claims: %s and %s", previous, record.Knowledge.ID)
		}
		claims[key] = record.Knowledge.ID
	}
	return nil
}

func KnowledgeHeads(records map[string]KnowledgeRecord) []KnowledgeRecord {
	return knowledgeHeads(records, "", false)
}

func KnowledgeHeadsBySubject(records map[string]KnowledgeRecord, subject string) []KnowledgeRecord {
	subject = MasterName(subject)
	if subject == "" {
		return nil
	}
	return knowledgeHeads(records, subject, true)
}

func knowledgeHeads(
	records map[string]KnowledgeRecord,
	subject string,
	filterSubject bool,
) []KnowledgeRecord {
	current := make(map[string]KnowledgeRecord)
	for id, record := range records {
		if record.Knowledge.OrganizedAt == nil {
			continue
		}
		status := EffectiveKnowledgeStatus(record.Knowledge)
		if (status == StatusPending || status == StatusActive) &&
			(!filterSubject || record.Knowledge.Subject == subject) {
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
		if knowledge.OrganizedAt == nil {
			continue
		}
		knowledge.Status = EffectiveKnowledgeStatus(knowledge)
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

func (v Vocabulary) ValidateOptionalSubject(value string) error {
	if MasterName(value) == "" {
		return nil
	}
	return v.ValidateSubject(value)
}

func normalizeKnowledgeDraft(draft KnowledgeDraft) KnowledgeDraft {
	draft.Type = KnowledgeType(strings.TrimSpace(string(draft.Type)))
	draft.Subject = MasterName(draft.Subject)
	draft.Statement = strings.TrimSpace(draft.Statement)
	draft.Rationale = strings.TrimSpace(draft.Rationale)
	return draft
}

func validateExtractedDraft(draft KnowledgeDraft, vocabulary Vocabulary) error {
	if err := vocabulary.ValidateType(draft.Type); err != nil {
		return fmt.Errorf("type: %w", err)
	}
	if err := vocabulary.ValidateOptionalSubject(draft.Subject); err != nil {
		return err
	}
	if draft.Statement == "" {
		return errors.New("knowledge statement is required")
	}
	if strings.Contains(draft.Statement, "\r") {
		return errors.New("knowledge statement must use LF line endings")
	}
	return nil
}

func normalizeOrganizationAction(action OrganizationAction) OrganizationAction {
	action.KnowledgeID = strings.TrimSpace(action.KnowledgeID)
	if action.Resolution.Draft != nil {
		draft := normalizeKnowledgeDraft(*action.Resolution.Draft)
		action.Resolution.Draft = &draft
	}
	return action
}

func validateOrganizationResolution(
	resolution Resolution,
	subject string,
	vocabulary Vocabulary,
) error {
	ids := UniqueSorted(resolution.KnowledgeIDs)
	if !slices.Equal(ids, resolution.KnowledgeIDs) {
		return errors.New("resolution knowledge_ids must be unique and sorted")
	}
	switch resolution.Kind {
	case ResolutionDiscard, ResolutionNew:
		if len(ids) != 0 || resolution.Draft != nil {
			return fmt.Errorf("%s requires no target and no draft", resolution.Kind)
		}
	case ResolutionEquivalent, ResolutionConflicts:
		if len(ids) != 1 || resolution.Draft != nil {
			return fmt.Errorf("%s requires exactly one target and no draft", resolution.Kind)
		}
	case ResolutionComplements:
		if len(ids) != 1 || resolution.Draft == nil {
			return errors.New("complements requires exactly one target and a draft")
		}
		if err := validateKnowledgeDraft(subject, *resolution.Draft, vocabulary); err != nil {
			return err
		}
	default:
		return fmt.Errorf("invalid resolution kind %q", resolution.Kind)
	}
	return nil
}

func currentOrganizationTarget(
	records map[string]KnowledgeRecord,
	subject,
	id string,
) (KnowledgeRecord, error) {
	for _, head := range KnowledgeHeadsBySubject(records, subject) {
		if head.Knowledge.ID == id {
			return head, nil
		}
	}
	return KnowledgeRecord{}, fmt.Errorf("resolution target %s is not a current Knowledge head", id)
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

func replacementPredecessors(target KnowledgeRecord, records map[string]KnowledgeRecord) []string {
	result := []string{target.Knowledge.ID}
	for _, id := range target.Knowledge.Supersedes {
		ancestor, exists := records[id]
		if exists && EffectiveKnowledgeStatus(ancestor.Knowledge) == StatusActive {
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
	if EffectiveKnowledgeStatus(target.Knowledge) != StatusPending {
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
