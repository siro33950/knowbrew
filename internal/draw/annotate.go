package draw

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/siro33950/knowbrew/internal/domain"
	"github.com/siro33950/knowbrew/internal/invocation"
	"github.com/siro33950/knowbrew/internal/store"
)

type Annotation struct {
	FeedstockID string
	Summary     string
	SpeechActs  []string
	Topics      []string
	Subjects    []string
	NewTopics   []string
	NewSubjects []string
}

func Annotate(ctx context.Context, dataStore *store.Store, annotation Annotation) (int, error) {
	if err := invocation.ValidateFeedstock(annotation.FeedstockID); err != nil {
		return 0, err
	}
	if err := ValidateSpeechActs(annotation.SpeechActs); err != nil {
		return 0, err
	}
	if strings.TrimSpace(annotation.Summary) == "" {
		return 0, fmt.Errorf("summary is required")
	}
	candidate, err := dataStore.ReadCandidate(annotation.FeedstockID)
	if err != nil {
		return 0, fmt.Errorf("read pending feedstock: %w", err)
	}
	definitions, err := parseDefinitions(annotation.NewTopics)
	if err != nil {
		return 0, fmt.Errorf("new topic: %w", err)
	}
	subjectDefinitions, err := parseDefinitions(annotation.NewSubjects)
	if err != nil {
		return 0, fmt.Errorf("new subject: %w", err)
	}
	for name := range definitions {
		annotation.Topics = append(annotation.Topics, name)
	}
	for name := range subjectDefinitions {
		annotation.Subjects = append(annotation.Subjects, name)
	}
	annotation.Topics = domain.UniqueSorted(annotation.Topics)
	annotation.Subjects = domain.UniqueSorted(annotation.Subjects)
	now := time.Now().UTC()
	added := 0
	err = dataStore.WithLock(ctx, func() error {
		claim, err := invocation.Claim(dataStore.Root)
		if err != nil {
			return err
		}
		succeeded := false
		defer func() {
			if !succeeded {
				invocation.Rollback(claim)
			}
		}()
		topicEntries, _, err := dataStore.LoadMasters("topics")
		if err != nil {
			return err
		}
		subjectEntries, _, err := dataStore.LoadMasters("subjects")
		if err != nil {
			return err
		}
		knownTopics := make(map[string]domain.Status, len(topicEntries))
		for _, entry := range topicEntries {
			knownTopics[entry.Name] = entry.Status
		}
		knownSubjects := make(map[string]domain.Status, len(subjectEntries))
		for _, entry := range subjectEntries {
			knownSubjects[entry.Name] = entry.Status
		}
		for _, topic := range domain.UniqueSorted(annotation.Topics) {
			status, exists := knownTopics[topic]
			if exists && status == domain.StatusInvalidated {
				return fmt.Errorf("topic %s is invalidated", topic)
			}
			if !exists {
				definition := definitions[topic]
				if definition == "" {
					definition = "Pending definition proposed during feedstock classification."
				}
				created, err := dataStore.EnsureMaster("topics", domain.MasterEntry{
					Name: topic, Definition: definition, Status: domain.StatusPending,
					Created: now, Updated: now,
				})
				if err != nil {
					return err
				}
				if created {
					added++
				}
			}
		}
		for _, subject := range domain.UniqueSorted(annotation.Subjects) {
			status, exists := knownSubjects[subject]
			if exists && status == domain.StatusInvalidated {
				return fmt.Errorf("subject %s is invalidated", subject)
			}
			if !exists {
				definition := subjectDefinitions[subject]
				if definition == "" {
					definition = "Pending definition proposed during feedstock classification."
				}
				created, err := dataStore.EnsureMaster("subjects", domain.MasterEntry{
					Name: subject, Definition: definition, Status: domain.StatusPending,
					Created: now, Updated: now,
				})
				if err != nil {
					return err
				}
				if created {
					added++
				}
			}
		}
		feedstock := domain.Feedstock{
			Schema: domain.SchemaVersion, ID: candidate.ID, Session: candidate.Session,
			Timestamp: candidate.Timestamp, Agent: candidate.Agent, CWD: candidate.CWD,
			Repo: candidate.Repo, Branch: candidate.Branch, Commands: candidate.Commands,
			FilesChanged: candidate.FilesChanged, Errors: candidate.Errors,
			UserQuote: candidate.UserQuote, SpeechActs: annotation.SpeechActs,
			Topics:   annotation.Topics,
			Subjects: domain.UniqueSorted(append(candidate.Subjects, annotation.Subjects...)),
			Summary:  annotation.Summary,
		}
		if len(feedstock.Subjects) == 0 {
			return errors.New("at least one subject is required")
		}
		if err := dataStore.WriteFeedstock(feedstock); err != nil {
			return err
		}
		if err := dataStore.RemoveCandidate(annotation.FeedstockID); err != nil {
			return err
		}
		succeeded = true
		return nil
	})
	return added, err
}

func parseDefinitions(values []string) (map[string]string, error) {
	definitions := map[string]string{}
	for _, value := range values {
		name, definition, ok := strings.Cut(value, "=")
		name = strings.TrimSpace(name)
		definition = strings.TrimSpace(definition)
		if !ok || name == "" || definition == "" {
			return nil, fmt.Errorf("%q must use name=one-line definition", value)
		}
		if strings.ContainsAny(definition, "\r\n") {
			return nil, fmt.Errorf("%q definition must be one line", name)
		}
		definitions[name] = definition
	}
	return definitions, nil
}
