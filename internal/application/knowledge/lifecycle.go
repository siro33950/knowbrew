package knowledge

import (
	"context"
	"slices"
	"time"

	"github.com/siro33950/knowbrew/internal/application/diagnostic"
	"github.com/siro33950/knowbrew/internal/application/storage"
	"github.com/siro33950/knowbrew/internal/domain"
)

type Repository interface {
	ListKnowledgeMetadata() ([]storage.KnowledgeMetadata, []diagnostic.Warning, error)
	Transaction(context.Context, func(storage.Transaction) error) error
}

func Reconcile(ctx context.Context, repository Repository) (int, []diagnostic.Warning, error) {
	changed := 0
	var warnings []diagnostic.Warning
	err := repository.Transaction(ctx, func(transaction storage.Transaction) error {
		documents, readWarnings, err := repository.ListKnowledgeMetadata()
		warnings = append(warnings, readWarnings...)
		if err != nil {
			return err
		}
		byID := make(map[string]storage.KnowledgeMetadata, len(documents))
		records := make(map[string]domain.Knowledge, len(documents))
		for _, document := range documents {
			byID[document.Knowledge.ID] = document
			records[document.Knowledge.ID] = document.Knowledge
		}
		changes, issues := domain.ReconcileKnowledgeLifecycle(records, time.Now().UTC())
		for _, issue := range issues {
			location := issue.KnowledgeID
			if document, exists := byID[issue.KnowledgeID]; exists && document.Location != "" {
				location = document.Location
			}
			warnings = append(warnings, diagnostic.FromError(location, issue.Err))
		}
		ids := make([]string, 0, len(changes))
		for id := range changes {
			ids = append(ids, id)
		}
		slices.Sort(ids)
		for _, id := range ids {
			if err := transaction.StageKnowledgeMetadata(changes[id]); err != nil {
				return err
			}
			changed++
		}
		return nil
	})
	return changed, warnings, err
}
