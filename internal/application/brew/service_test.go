package brew

import (
	"context"
	"io"
	"path/filepath"

	"github.com/siro33950/knowbrew/internal/adapters/config"
	dialogueadapter "github.com/siro33950/knowbrew/internal/adapters/dialogue"
	invocationadapter "github.com/siro33950/knowbrew/internal/adapters/invocation"
	persistenceadapter "github.com/siro33950/knowbrew/internal/adapters/persistence"
	"github.com/siro33950/knowbrew/internal/adapters/persistence/markdownstore"
	progressui "github.com/siro33950/knowbrew/internal/adapters/progress"
	"github.com/siro33950/knowbrew/internal/adapters/runlock"
	"github.com/siro33950/knowbrew/internal/application/agent"
	"github.com/siro33950/knowbrew/internal/application/diagnostic"
	"github.com/siro33950/knowbrew/internal/domain"
)

func repositoryForTest(dataStore *store.Store) *persistenceadapter.Markdown {
	return &persistenceadapter.Markdown{Store: dataStore}
}

func invocationForTest(dataStore *store.Store) invocationadapter.Guard {
	return invocationadapter.Guard{Root: dataStore.Root}
}

func catalogForTest(dataStore *store.Store, subject string) ([]CatalogEntry, error) {
	return Catalog(repositoryForTest(dataStore), invocationForTest(dataStore), subject, nil)
}

func showForTest(dataStore *store.Store, ids []string) ([]ShownKnowledge, error) {
	return Show(repositoryForTest(dataStore), invocationForTest(dataStore), ids)
}

func submitForTest(
	ctx context.Context,
	dataStore *store.Store,
	input SubmitInput,
) (SubmitResult, error) {
	return Submit(ctx, repositoryForTest(dataStore), invocationForTest(dataStore), input)
}

func applyForTest(
	ctx context.Context,
	dataStore *store.Store,
	input SubmitInput,
	reads agent.ReadState,
) (SubmitResult, error) {
	return Apply(ctx, repositoryForTest(dataStore), input, reads)
}

func assertionPromptForTest(
	dataStore *store.Store,
	cfg config.Config,
	feedstocks []domain.Feedstock,
	feedstock domain.Feedstock,
	assertion domain.Assertion,
) (string, []diagnostic.Warning, error) {
	repository := repositoryForTest(dataStore)
	writingInstructions, err := loadWritingInstructions(repository, "common", "knowledge")
	if err != nil {
		return "", nil, err
	}
	return assertionPrompt(
		repository, dialogueadapter.Query{Store: dataStore},
		Settings{ContextTurns: cfg.Draw.ContextTurns, Backend: cfg.LLM.Backend, Model: cfg.LLM.BrewModel},
		feedstocks, feedstock, assertion, writingInstructions,
	)
}

func runForTest(
	ctx context.Context,
	cfg config.Config,
	runner agent.Runner,
	progress io.Writer,
	indexes ...SearchIndex,
) (Summary, error) {
	dataStore, err := store.New(cfg.Root)
	if err != nil {
		return Summary{}, err
	}
	service := Service{
		Settings: Settings{
			ContextTurns: cfg.Draw.ContextTurns,
			Backend:      cfg.LLM.Backend,
			Model:        cfg.LLM.BrewModel,
		},
		Repository: repositoryForTest(dataStore),
		Lifecycle:  repositoryForTest(dataStore),
		Dialogue:   dialogueadapter.Query{Store: dataStore},
		Runner:     runner,
		Progress:   progressui.From(progress),
		RunLock: runlock.FileLock{
			Path: filepath.Join(cfg.Root, ".knowbrew", "state", "brew.lock"),
			Name: "brew",
		},
	}
	if len(indexes) > 0 {
		service.SearchIndex = indexes[0]
	}
	return service.Run(ctx)
}

type recordingSearchIndex struct {
	calls  int
	failOn map[int]error
}

func (index *recordingSearchIndex) Sync(context.Context) ([]diagnostic.Warning, error) {
	index.calls++
	return nil, index.failOn[index.calls]
}
