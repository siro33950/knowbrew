package store

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/siro33950/knowbrew/internal/diagnostic"
	"github.com/siro33950/knowbrew/internal/domain"
	"github.com/siro33950/knowbrew/internal/fsutil"
)

func (s *Store) ReplacePendingKnowledge(
	id string,
	replacement domain.Knowledge,
	body string,
	when time.Time,
) error {
	return s.ReplacePendingKnowledgeEvidence(id, replacement, body, when, true)
}

func (s *Store) ReplacePendingKnowledgeEvidence(
	id string,
	replacement domain.Knowledge,
	body string,
	when time.Time,
	inheritEvidence bool,
) error {
	path, err := s.KnowledgePath(id)
	if err != nil {
		return err
	}
	current, _, err := s.ReadKnowledge(path)
	if err != nil {
		return err
	}
	if current.Status != domain.StatusPending {
		return fmt.Errorf("only pending knowledge can be revised; %s is %s", id, current.Status)
	}
	replacement.Created = current.Created
	replacement.ID = current.ID
	replacement.Updated = when
	replacement.Approved = false
	replacement.InvalidatedAt = nil
	replacement.SupersededBy = ""
	replacement.SupersededAt = nil
	replacement.Status = domain.StatusPending
	replacement.EstablishedBy = domain.MasterName(replacement.EstablishedBy)
	replacement.Subject = domain.MasterName(replacement.Subject)
	if inheritEvidence {
		replacement.Feedstocks = append(current.Feedstocks, replacement.Feedstocks...)
		replacement.Assertions = append(current.Assertions, replacement.Assertions...)
	}
	replacement.Feedstocks = normalizeFeedstockLinks(replacement.Feedstocks)
	replacement.Assertions = normalizeAssertionLinks(replacement.Assertions)
	replacement.Supersedes = normalizeKnowledgeLinks(replacement.Supersedes)
	if slices.Contains(replacement.Supersedes, id) {
		return errors.New("knowledge cannot supersede itself")
	}
	types, err := s.NormalizeKnowledgeTypes([]domain.KnowledgeType{replacement.Type})
	if err != nil {
		return fmt.Errorf("knowledge type: %w", err)
	}
	replacement.Type = types[0]
	if err := domain.ValidateKnowledge(replacement); err != nil {
		return err
	}
	if strings.TrimSpace(body) == "" {
		return errors.New("knowledge body is required")
	}
	for _, feedstock := range replacement.Feedstocks {
		if _, _, err := s.FindFeedstock(feedstock); err != nil {
			return fmt.Errorf("invalid feedstock %s: %w", feedstock, err)
		}
	}
	data, err := encodeKnowledge(replacement, body)
	if err != nil {
		return err
	}
	return fsutil.AtomicWrite(path, data, 0o644)
}

// SupersedePendingKnowledge retires an unapproved predecessor as soon as a
// pending successor has absorbed it. Approved predecessors remain active until
// their successor is approved.
func (s *Store) SupersedePendingKnowledge(
	id,
	successor string,
	when time.Time,
) (bool, error) {
	if id == successor {
		return false, errors.New("knowledge cannot supersede itself")
	}
	path, err := s.KnowledgePath(id)
	if err != nil {
		return false, err
	}
	knowledge, body, err := s.ReadKnowledge(path)
	if err != nil {
		return false, err
	}
	if knowledge.Status == domain.StatusSuperseded {
		if knowledge.SupersededBy != successor {
			return false, fmt.Errorf(
				"knowledge %s is already superseded by %q, not %q",
				id,
				knowledge.SupersededBy,
				successor,
			)
		}
		return false, nil
	}
	if knowledge.Status != domain.StatusPending {
		return false, nil
	}
	knowledge.SupersededBy = successor
	knowledge.SupersededAt = &when
	knowledge.Updated = when
	knowledge.Status = domain.StatusSuperseded
	data, err := encodeKnowledge(knowledge, body)
	if err != nil {
		return false, err
	}
	if err := fsutil.AtomicWrite(path, data, 0o644); err != nil {
		return false, err
	}
	return true, nil
}

// ReconcileKnowledgeLifecycle observes direct human edits in the Vault.
// Pending successors retire pending predecessors immediately, while approved
// predecessors are retained until their successor is approved. If a pending
// successor is removed, invalidated, or stops naming its predecessor, the
// pending predecessor is restored. No CLI activation event is required.
func (s *Store) ReconcileKnowledgeLifecycle(
	ctx context.Context,
) (int, []diagnostic.Warning, error) {
	changed := 0
	var warnings []diagnostic.Warning
	err := s.WithLock(ctx, func() error {
		files, readWarnings, err := s.ListAllKnowledge()
		warnings = append(warnings, readWarnings...)
		if err != nil {
			return err
		}
		byID := make(map[string]KnowledgeFile, len(files))
		for _, file := range files {
			byID[file.Knowledge.ID] = file
		}
		now := time.Now().UTC()
		for _, file := range files {
			knowledge := file.Knowledge
			if knowledge.Status != domain.StatusSuperseded ||
				knowledge.Approved ||
				knowledge.InvalidatedAt != nil {
				continue
			}
			successor, exists := byID[knowledge.SupersededBy]
			if exists &&
				successor.Knowledge.Status != domain.StatusInvalidated &&
				slices.Contains(successor.Knowledge.Supersedes, file.Knowledge.ID) {
				continue
			}
			knowledge.SupersededBy = ""
			knowledge.SupersededAt = nil
			knowledge.Updated = now
			knowledge.Status = domain.StatusPending
			data, err := encodeKnowledge(knowledge, file.Body)
			if err != nil {
				return err
			}
			if err := fsutil.AtomicWrite(file.Path, data, 0o644); err != nil {
				return err
			}
			file.Knowledge = knowledge
			byID[file.Knowledge.ID] = file
			changed++
		}
		graph := make(map[string][]string)
		targets := make(map[string][]string)
		for _, file := range files {
			file = byID[file.Knowledge.ID]
			if file.Knowledge.Status != domain.StatusActive &&
				file.Knowledge.Status != domain.StatusPending {
				continue
			}
			for _, target := range file.Knowledge.Supersedes {
				graph[file.Knowledge.ID] = append(graph[file.Knowledge.ID], target)
				targets[target] = append(targets[target], file.Knowledge.ID)
			}
		}
		for target, successors := range targets {
			slices.Sort(successors)
			successors = slices.Compact(successors)
			targetFile, exists := byID[target]
			if !exists {
				for _, successor := range successors {
					warnings = append(warnings, diagnostic.FromError(
						byID[successor].Path,
						fmt.Errorf("supersedes target %q does not exist", target),
					))
				}
				continue
			}
			knowledge := targetFile.Knowledge
			if knowledge.Status == domain.StatusInvalidated {
				continue
			}
			eligible := make([]string, 0, len(successors))
			for _, successor := range successors {
				successorStatus := byID[successor].Knowledge.Status
				if successorStatus == domain.StatusActive ||
					(successorStatus == domain.StatusPending &&
						knowledge.Status == domain.StatusPending) {
					eligible = append(eligible, successor)
				}
			}
			if len(eligible) == 0 {
				continue
			}
			if len(eligible) != 1 {
				warnings = append(warnings, diagnostic.FromError(
					targetFile.Path,
					fmt.Errorf(
						"multiple eligible knowledge records supersede %q: %s",
						target,
						strings.Join(eligible, ", "),
					),
				))
				continue
			}
			successor := eligible[0]
			if successor == target || graphReaches(graph, target, successor, map[string]bool{}) {
				warnings = append(warnings, diagnostic.FromError(
					byID[successor].Path,
					fmt.Errorf("supersession cycle between %q and %q", successor, target),
				))
				continue
			}
			switch knowledge.Status {
			case domain.StatusSuperseded:
				if knowledge.SupersededBy != successor {
					warnings = append(warnings, diagnostic.FromError(
						targetFile.Path,
						fmt.Errorf(
							"knowledge is already superseded by %q, not %q",
							knowledge.SupersededBy,
							successor,
						),
					))
				}
				continue
			}
			didChange, err := s.SupersedePendingKnowledge(target, successor, now)
			if err != nil {
				return err
			}
			if didChange {
				changed++
				continue
			}
			knowledge.SupersededBy = successor
			knowledge.SupersededAt = &now
			knowledge.Updated = now
			knowledge.Status = domain.StatusSuperseded
			data, err := encodeKnowledge(knowledge, targetFile.Body)
			if err != nil {
				return err
			}
			if err := fsutil.AtomicWrite(targetFile.Path, data, 0o644); err != nil {
				return err
			}
			changed++
		}
		return nil
	})
	return changed, warnings, err
}

func graphReaches(
	graph map[string][]string,
	current,
	target string,
	seen map[string]bool,
) bool {
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
