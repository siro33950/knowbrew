package source

import (
	"context"
	"time"

	"github.com/siro33950/knowbrew/internal/application/diagnostic"
	"github.com/siro33950/knowbrew/internal/domain"
)

type Configured struct {
	Agent  string
	Parser string
	Paths  []string
}

type File struct {
	Agent  string
	Parser string
	Path   string
}

type Selection struct {
	Paths         []string
	MaxTurns      int
	Sources       []string
	ModifiedSince *time.Time
	ModifiedUntil *time.Time
}

type Gateway interface {
	Collect([]Configured, Selection, time.Time) ([]File, error)
	Parse(File) ([]domain.FeedstockCandidate, []diagnostic.Warning, error)
	ParseSession(string, string) ([]domain.FeedstockCandidate, []diagnostic.Warning, error)
	ExtractTurn(File, string) ([]domain.DialogueMessage, error)
	DiscoverRepository(context.Context, string) string
}
