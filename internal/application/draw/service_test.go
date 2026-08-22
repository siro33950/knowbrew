package draw

import (
	"context"
	"io"
	"time"

	"github.com/siro33950/knowbrew/internal/adapters/config"
	persistenceadapter "github.com/siro33950/knowbrew/internal/adapters/persistence"
	"github.com/siro33950/knowbrew/internal/adapters/persistence/markdownstore"
	progressui "github.com/siro33950/knowbrew/internal/adapters/progress"
	"github.com/siro33950/knowbrew/internal/adapters/runlock"
	sourceadapter "github.com/siro33950/knowbrew/internal/adapters/source"
	"github.com/siro33950/knowbrew/internal/application/agent"
	"github.com/siro33950/knowbrew/internal/application/diagnostic"
	applicationsource "github.com/siro33950/knowbrew/internal/application/source"
	"github.com/siro33950/knowbrew/internal/domain"
)

const DefaultLookback = 24 * time.Hour

func collectFiles(cfg config.Config, options Options, now time.Time) ([]SourceFile, error) {
	settings := settingsFromConfig(cfg)
	return sourceadapter.New(settings.Sources).Collect(settings.Sources, applicationsource.Selection{
		Paths: options.Paths, MaxTurns: options.MaxTurns, Sources: options.Sources,
		ModifiedSince: options.ModifiedSince, ModifiedUntil: options.ModifiedUntil,
		Order: options.Order,
	}, now)
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

func drawPromptForTest(
	cfg config.Config,
	dataStore *store.Store,
	feedstockID string,
	feedstocks []domain.Feedstock,
	snapshots ...map[string][]domain.FeedstockCandidate,
) (string, []diagnostic.Warning, error) {
	repository := &persistenceadapter.Markdown{Store: dataStore}
	settings := promptSettingsForTest(cfg, dataStore.Root)
	return drawPrompt(
		settings, sourceadapter.New(settings.Sources),
		repository,
		feedstockID, feedstocks, snapshots...,
	)
}

func draftForTest(
	ctx context.Context,
	dataStore *store.Store,
	draft Draft,
) error {
	return ApplyDraft(
		ctx, &persistenceadapter.Markdown{Store: dataStore},
		draft,
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
	settings := settingsFromConfig(cfg)
	service := Service{
		Settings:   settings,
		Repository: &persistenceadapter.Markdown{Store: dataStore},
		Sources:    sourceadapter.New(settings.Sources),
		Runner:     runner, Progress: progressui.From(progress),
		Claimer: runlock.FileClaimer{
			Root: cfg.Root, Namespace: "feedstock-claims",
		},
	}
	if len(indexes) > 0 {
		service.SearchIndex = indexes[0]
	}
	return service.RunWithOptions(ctx, options)
}

func promptSettingsForTest(cfg config.Config, root string) Settings {
	settings := settingsFromConfig(cfg)
	if len(settings.Sources) == 0 {
		settings.Sources = []ConfiguredSource{{
			Agent: "claude", Parser: "claude", Paths: []string{root},
		}}
	}
	return settings
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
			Agent: source.Agent, Parser: source.Parser, Paths: source.Paths,
		})
	}
	return Settings{
		Concurrency: cfg.Draw.Concurrency, ContextTurns: cfg.Draw.ContextTurns,
		MaxContextTurns: cfg.Draw.MaxContextTurns, Backend: cfg.LLM.Backend,
		Model: cfg.LLM.DrawDraftModel, ConfigPath: cfg.Path, Sources: sources,
	}
}
