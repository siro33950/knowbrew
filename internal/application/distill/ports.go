package distill

import (
	"context"

	"github.com/siro33950/knowbrew/internal/application/agent"
	"github.com/siro33950/knowbrew/internal/application/diagnostic"
	knowledgeapp "github.com/siro33950/knowbrew/internal/application/knowledge"
	"github.com/siro33950/knowbrew/internal/application/storage"
	"github.com/siro33950/knowbrew/internal/domain"
)

type Repository interface {
	EnsureLayout() error
	WithLock(context.Context, func() error) error
	LoadMasters(string) ([]domain.MasterEntry, []diagnostic.Warning, error)
	LoadTemplates() ([]domain.DocumentTemplate, []diagnostic.Warning, error)
	ListKnowledge() ([]storage.KnowledgeDocument, []diagnostic.Warning, error)
	ReadDistilledDocument(domain.DocumentTemplate, string) (domain.DistilledDocument, bool, error)
	WriteDistilledDocument(domain.DocumentTemplate, domain.DistilledDocument) error
	DeleteDistilledDocument(domain.DocumentTemplate, string) (bool, error)
	ReadWritingGuide(string) (string, bool, error)
}

type Settings struct {
	Backend string
	Model   string
}

type Options struct {
	Subject  string
	Template string
	Max      int
}

type CursorPosition struct {
	Subject  string `json:"subject"`
	Template string `json:"template"`
}

type Cursor interface {
	Load() (CursorPosition, bool, error)
	Save(CursorPosition) error
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
	Runner     agent.Runner
	Progress   Progress
	RunLock    RunLock
	Cursor     Cursor
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
