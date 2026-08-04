package draw

import (
	"context"
	"io"
	"path/filepath"
	"time"

	"github.com/siro33950/knowbrew/internal/adapters/config"
	invocationadapter "github.com/siro33950/knowbrew/internal/adapters/invocation"
	persistenceadapter "github.com/siro33950/knowbrew/internal/adapters/persistence"
	"github.com/siro33950/knowbrew/internal/adapters/persistence/markdownstore"
	progressui "github.com/siro33950/knowbrew/internal/adapters/progress"
	"github.com/siro33950/knowbrew/internal/adapters/runlock"
	sourceadapter "github.com/siro33950/knowbrew/internal/adapters/source"
	"github.com/siro33950/knowbrew/internal/application/agent"
	"github.com/siro33950/knowbrew/internal/application/diagnostic"
	"github.com/siro33950/knowbrew/internal/domain"
)

const DefaultLookback = 24 * time.Hour

func collectFiles(cfg config.Config, options Options, now time.Time) ([]SourceFile, error) {
	return (sourceadapter.Gateway{}).Collect(settingsFromConfig(cfg).Sources, options, now)
}

func ensureRepositorySubjectForTest(
	ctx context.Context,
	dataStore *store.Store,
	candidate *domain.FeedstockCandidate,
) (int, []diagnostic.Warning, error) {
	return ensureRepositorySubject(
		ctx, &persistenceadapter.Markdown{Store: dataStore}, sourceadapter.Gateway{}, candidate,
	)
}

func summaryPromptForTest(
	_ config.Config,
	dataStore *store.Store,
	feedstockID string,
	snapshots ...map[string][]domain.FeedstockCandidate,
) (string, []diagnostic.Warning, error) {
	return summaryPrompt(
		sourceadapter.Gateway{}, &persistenceadapter.Markdown{Store: dataStore},
		feedstockID, snapshots...,
	)
}

func annotationPromptForTest(
	cfg config.Config,
	dataStore *store.Store,
	feedstockID string,
	feedstocks []domain.Feedstock,
	snapshots ...map[string][]domain.FeedstockCandidate,
) (string, []diagnostic.Warning, error) {
	return annotationPrompt(
		settingsFromConfig(cfg), sourceadapter.Gateway{},
		&persistenceadapter.Markdown{Store: dataStore},
		feedstockID, feedstocks, snapshots...,
	)
}

func annotateForTest(
	ctx context.Context,
	dataStore *store.Store,
	annotation Annotation,
) (int, error) {
	return Annotate(
		ctx, &persistenceadapter.Markdown{Store: dataStore},
		invocationadapter.Guard{Root: dataStore.Root}, annotation,
	)
}

func summarizeForTest(
	ctx context.Context,
	dataStore *store.Store,
	feedstockID,
	summary string,
) error {
	return Summarize(
		ctx, &persistenceadapter.Markdown{Store: dataStore},
		invocationadapter.Guard{Root: dataStore.Root}, feedstockID, summary,
	)
}

func Run(
	ctx context.Context,
	cfg config.Config,
	paths []string,
	runner agent.Runner,
	progress io.Writer,
) (Summary, error) {
	return RunWithOptions(ctx, cfg, Options{Paths: paths}, runner, progress)
}

func RunWithOptions(
	ctx context.Context,
	cfg config.Config,
	options Options,
	runner agent.Runner,
	progress io.Writer,
	indexes ...SearchIndex,
) (Summary, error) {
	dataStore, err := store.New(cfg.Root)
	if err != nil {
		return Summary{}, err
	}
	service := Service{
		Settings:   settingsFromConfig(cfg),
		Repository: &persistenceadapter.Markdown{Store: dataStore},
		Sources:    sourceadapter.Gateway{},
		Runner:     runner, Progress: progressui.From(progress),
		RunLock: runlock.FileLock{
			Path: filepath.Join(cfg.Root, ".knowbrew", "state", "draw.lock"),
			Name: "draw",
		},
	}
	if len(indexes) > 0 {
		service.SearchIndex = indexes[0]
	}
	return service.RunWithOptions(ctx, options)
}

type recordingSearchIndex struct {
	calls  int
	failOn map[int]error
}

func (index *recordingSearchIndex) Sync(context.Context) ([]diagnostic.Warning, error) {
	index.calls++
	return nil, index.failOn[index.calls]
}

func settingsFromConfig(cfg config.Config) Settings {
	sources := make([]ConfiguredSource, 0, len(cfg.Sources))
	for _, source := range cfg.Sources {
		sources = append(sources, ConfiguredSource{
			Agent: source.Agent, Parser: source.Parser, Path: source.Path,
		})
	}
	return Settings{
		Concurrency: cfg.Draw.Concurrency, ContextTurns: cfg.Draw.ContextTurns,
		MaxContextTurns: cfg.Draw.MaxContextTurns, Backend: cfg.LLM.Backend,
		Model: cfg.LLM.DrawModel, ConfigPath: cfg.Path, Sources: sources,
	}
}
