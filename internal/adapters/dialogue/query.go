package dialogue

import (
	"github.com/siro33950/knowbrew/internal/adapters/persistence/markdownstore"
	"github.com/siro33950/knowbrew/internal/adapters/query"
	"github.com/siro33950/knowbrew/internal/domain"
)

type Query struct {
	Store *store.Store
}

func (reader Query) Read(feedstockID string) ([]domain.DialogueMessage, error) {
	return query.ExtractRawDialogue(reader.Store, feedstockID)
}
