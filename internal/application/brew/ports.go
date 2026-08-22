package brew

import (
	"context"

	"github.com/siro33950/knowbrew/internal/application/agent"
	"github.com/siro33950/knowbrew/internal/application/diagnostic"
	knowledgeapp "github.com/siro33950/knowbrew/internal/application/knowledge"
	"github.com/siro33950/knowbrew/internal/application/storage"
	"github.com/siro33950/knowbrew/internal/domain"
)

type KnowledgeDocument = storage.KnowledgeDocument
type Transaction = storage.Transaction

type Repository interface {
	EnsureLayout() error
	GetFeedstock(string) (domain.Feedstock, error)
	LoadMasters(string) ([]domain.MasterEntry, []diagnostic.Warning, error)
	KnowledgeTypes() ([]domain.MasterEntry, error)
	ListKnowledge() ([]KnowledgeDocument, []diagnostic.Warning, error)
	Transaction(context.Context, func(Transaction) error) error
	ReadWritingGuide(string) (string, bool, error)
}

type Settings struct {
	Concurrency int
	Backend     string
	Model       string
}

type Options struct {
	Max int
}

type Claimer interface {
	Claim(context.Context, string) (func() error, error)
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
	Settings   Settings
	Repository Repository
	Lifecycle  knowledgeapp.Repository
	Runner     agent.Runner
	Progress   Progress
	Claimer    Claimer
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
