package dialogue

import (
	"errors"

	"github.com/siro33950/knowbrew/internal/adapters/persistence/markdownstore"
	"github.com/siro33950/knowbrew/internal/domain"
)

type SourceReader interface {
	ReadTurn(agent, sessionID, turnID string) ([]domain.DialogueMessage, error)
}

type Query struct {
	Store  *store.Store
	Source SourceReader
}

func (reader Query) Read(feedstockID string) ([]domain.DialogueMessage, error) {
	if reader.Store == nil {
		return nil, errors.New("dialogue store is required")
	}
	if reader.Source == nil {
		return nil, errors.New("dialogue source reader is required")
	}
	feedstock, _, err := reader.Store.FindFeedstock(feedstockID)
	if err != nil {
		return nil, err
	}
	return reader.Source.ReadTurn(
		feedstock.Agent, feedstock.Session.ID, feedstock.TurnID,
	)
}
