package knowledge

import (
	"context"
	"testing"
	"time"

	"github.com/siro33950/knowbrew/internal/application/diagnostic"
	"github.com/siro33950/knowbrew/internal/application/storage"
	"github.com/siro33950/knowbrew/internal/domain"
)

func TestReconcileDefinesOneAtomicLifecycleUpdate(t *testing.T) {
	now := time.Now().UTC()
	predecessor := domain.Knowledge{
		ID: "kn-predecessor", Created: now, Updated: now,
		OrganizedAt: &now,
		Type:        "property", Subject: "knowbrew", Feedstocks: []string{"fs-source"},
		Status: domain.StatusPending,
	}
	successor := domain.Knowledge{
		ID: "kn-successor", Created: now, Updated: now,
		OrganizedAt: &now,
		Type:        "property", Subject: "knowbrew", Feedstocks: []string{"fs-source"},
		Supersedes: []string{predecessor.ID},
		Status:     domain.StatusPending,
	}
	repository := &lifecycleRepository{
		metadata: []storage.KnowledgeMetadata{
			{Knowledge: predecessor, Location: "predecessor.md"},
			{Knowledge: successor, Location: "successor.md"},
		},
	}
	changed, warnings, err := Reconcile(context.Background(), repository)
	if err != nil {
		t.Fatal(err)
	}
	if changed != 1 || len(warnings) != 0 || repository.transactions != 1 {
		t.Fatalf("changed = %d, warnings = %#v, transactions = %d", changed, warnings, repository.transactions)
	}
	updated := repository.staged[predecessor.ID]
	if updated.Status != domain.StatusSuperseded || updated.SupersededBy != successor.ID {
		t.Fatalf("updated = %#v", updated)
	}
}

type lifecycleRepository struct {
	metadata     []storage.KnowledgeMetadata
	staged       map[string]domain.Knowledge
	transactions int
}

func (repository *lifecycleRepository) ListKnowledgeMetadata() (
	[]storage.KnowledgeMetadata,
	[]diagnostic.Warning,
	error,
) {
	return repository.metadata, nil, nil
}

func (repository *lifecycleRepository) Transaction(
	_ context.Context,
	change func(storage.Transaction) error,
) error {
	repository.transactions++
	repository.staged = make(map[string]domain.Knowledge)
	return change(lifecycleTransaction{repository: repository})
}

type lifecycleTransaction struct {
	repository *lifecycleRepository
}

func (transaction lifecycleTransaction) StageKnowledge(domain.KnowledgeRecord) error {
	return nil
}

func (transaction lifecycleTransaction) StageKnowledgeMetadata(knowledge domain.Knowledge) error {
	transaction.repository.staged[knowledge.ID] = knowledge
	return nil
}

func (transaction lifecycleTransaction) StageExtractedFeedstock(domain.Feedstock, time.Time) error {
	return nil
}

func (transaction lifecycleTransaction) DeleteKnowledge(string) error { return nil }
