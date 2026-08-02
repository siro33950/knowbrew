package brew

import (
	"context"
	"time"

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
	WithLock(context.Context, func() error) error
	ListFeedstocks() ([]domain.Feedstock, []diagnostic.Warning, error)
	GetFeedstock(string) (domain.Feedstock, error)
	WriteBrewedFeedstock(domain.Feedstock, time.Time) error
	LoadMasters(string) ([]domain.MasterEntry, []diagnostic.Warning, error)
	KnowledgeTypes() ([]domain.MasterEntry, error)
	ListKnowledge() ([]KnowledgeDocument, []diagnostic.Warning, error)
	FindKnowledge(string) (KnowledgeDocument, error)
	Transaction(context.Context, func(Transaction) error) error
}

type Settings struct {
	ContextTurns int
	Backend      string
	Model        string
}

type DialogueReader interface {
	Read(string) ([]domain.DialogueMessage, error)
}

type Invocation interface {
	ValidateFeedstock(string) error
	ValidateAssertion(string) error
	IsAssertionInvocation() bool
	RecordCatalog(string, []string, string) error
	RecordInspected([]string) error
	ReadState() (agent.ReadState, error)
}

type RunLock interface {
	Lock(context.Context) (func() error, error)
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
	Dialogue   DialogueReader
	Runner     agent.Runner
	Progress   Progress
	RunLock    RunLock
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
