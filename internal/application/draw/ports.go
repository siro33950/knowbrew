package draw

import (
	"context"
	"time"

	"github.com/siro33950/knowbrew/internal/application/agent"
	"github.com/siro33950/knowbrew/internal/application/diagnostic"
	applicationsource "github.com/siro33950/knowbrew/internal/application/source"
	"github.com/siro33950/knowbrew/internal/application/storage"
	"github.com/siro33950/knowbrew/internal/domain"
)

type Repository interface {
	EnsureLayout() error
	WithLock(context.Context, func() error) error
	WriteFeedstock(domain.Feedstock) error
	GetFeedstock(string) (domain.Feedstock, error)
	ListFeedstocks() ([]domain.Feedstock, []diagnostic.Warning, error)
	DraftFeedstock(string, string, []domain.KnowledgeType, time.Time) error
	LoadMasters(string) ([]domain.MasterEntry, []diagnostic.Warning, error)
	EnsureMaster(string, domain.MasterEntry) (bool, error)
	KnowledgeTypes() ([]domain.MasterEntry, error)
	ReadWritingGuide(string) (string, bool, error)
	Transaction(context.Context, func(storage.Transaction) error) error
}

type ConfiguredSource = applicationsource.Configured
type SourceFile = applicationsource.File
type SourceGateway = applicationsource.Gateway
type Options struct {
	Paths         []string
	MaxTurns      int
	Sources       []string
	ModifiedSince *time.Time
	ModifiedUntil *time.Time
	Order         Order
	Hook          bool
}
type Order = applicationsource.Order

const (
	OrderNewest = applicationsource.OrderNewest
	OrderOldest = applicationsource.OrderOldest
)

type Settings struct {
	Concurrency     int
	ContextTurns    int
	MaxContextTurns int
	Backend         string
	Model           string
	ConfigPath      string
	Sources         []ConfiguredSource
}

type Claimer interface {
	Claim(context.Context, string) (func() error, error)
}

type SearchIndex interface {
	Sync(context.Context) ([]diagnostic.Warning, error)
}

type Progress interface {
	Write([]byte) (int, error)
	Start(string)
	Update(string)
	Complete(string)
	Errorf(string, ...any)
	Verbosef(string, ...any)
}

type Service struct {
	Settings    Settings
	Repository  Repository
	Sources     SourceGateway
	Runner      agent.Runner
	Progress    Progress
	Claimer     Claimer
	SearchIndex SearchIndex
}

type silentProgress struct{}

func (silentProgress) Write(data []byte) (int, error) { return len(data), nil }
func (silentProgress) Start(string)                   {}
func (silentProgress) Update(string)                  {}
func (silentProgress) Complete(string)                {}
func (silentProgress) Errorf(string, ...any)          {}
func (silentProgress) Verbosef(string, ...any)        {}

func (service Service) progress() Progress {
	if service.Progress == nil {
		return silentProgress{}
	}
	return service.Progress
}
