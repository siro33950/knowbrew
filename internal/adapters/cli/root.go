package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/siro33950/knowbrew/internal/adapters/config"
	dialogueadapter "github.com/siro33950/knowbrew/internal/adapters/dialogue"
	embeddingadapter "github.com/siro33950/knowbrew/internal/adapters/embedding"
	invocationadapter "github.com/siro33950/knowbrew/internal/adapters/invocation"
	"github.com/siro33950/knowbrew/internal/adapters/invocation/state"
	"github.com/siro33950/knowbrew/internal/adapters/llm"
	persistenceadapter "github.com/siro33950/knowbrew/internal/adapters/persistence"
	"github.com/siro33950/knowbrew/internal/adapters/persistence/markdownstore"
	"github.com/siro33950/knowbrew/internal/adapters/progress"
	"github.com/siro33950/knowbrew/internal/adapters/query"
	"github.com/siro33950/knowbrew/internal/adapters/runlock"
	"github.com/siro33950/knowbrew/internal/adapters/runstate"
	"github.com/siro33950/knowbrew/internal/adapters/setup"
	sourceadapter "github.com/siro33950/knowbrew/internal/adapters/source"
	"github.com/siro33950/knowbrew/internal/application/brew"
	"github.com/siro33950/knowbrew/internal/application/diagnostic"
	"github.com/siro33950/knowbrew/internal/application/distill"
	"github.com/siro33950/knowbrew/internal/application/draw"
	"github.com/siro33950/knowbrew/internal/application/inject"
	knowledgeapp "github.com/siro33950/knowbrew/internal/application/knowledge"
	searchapp "github.com/siro33950/knowbrew/internal/application/search"
	"github.com/siro33950/knowbrew/internal/domain"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

var version = "dev"

func Execute() error {
	command := newRootCommand()
	return command.Execute()
}

func newRootCommand() *cobra.Command {
	root := &cobra.Command{
		Use:           "knowbrew",
		Short:         "Brew durable knowledge from AI agent session logs",
		SilenceUsage:  true,
		SilenceErrors: true,
		Version:       version,
	}
	root.CompletionOptions.DisableDefaultCmd = true
	root.AddCommand(
		newInitCommand(),
		newDrawCommand(),
		newBrewCommand(),
		newDistillCommand(),
		newShowCommand(),
		newFeedstockCommand(),
		newKnowledgeCommand(),
		newDocumentCommand(),
		newContextCommand(),
		newIndexCommand(),
	)
	return root
}

func newInitCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "init",
		Short: "Interactively initialize a knowbrew knowledge root",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			return setup.RunInteractive()
		},
	}
}

func newDrawCommand() *cobra.Command {
	var (
		verbose bool
		hook    bool
		maximum int
		sources []string
		since   string
		until   string
	)
	command := &cobra.Command{
		Use:   "draw [path...]",
		Short: "Acquire feedstocks, then classify unannotated records concurrently",
		Args:  cobra.ArbitraryArgs,
		RunE: func(command *cobra.Command, paths []string) error {
			if hook {
				if strings.TrimSpace(os.Getenv(config.InvocationIDEnvironment)) != "" {
					return nil
				}
				for _, name := range []string{"max", "source", "since", "until", "verbose"} {
					if command.Flags().Changed(name) {
						return fmt.Errorf("--hook cannot be used with --%s", name)
					}
				}
				if len(paths) > 0 {
					return errors.New("--hook does not accept explicit paths")
				}
				path, err := drawHookTranscriptPath(command.InOrStdin())
				if err != nil {
					return err
				}
				if path == "" {
					return nil
				}
				paths = []string{path}
			}
			now := time.Now()
			options := draw.Options{Paths: paths, Sources: sources}
			if command.Flags().Changed("max") {
				if maximum < 1 {
					return errors.New("--max must be greater than zero")
				}
				options.MaxTurns = maximum
			}
			if command.Flags().Changed("since") {
				value, err := parseDrawBoundary(since, now)
				if err != nil {
					return fmt.Errorf("invalid --since: %w", err)
				}
				options.ModifiedSince = &value
			}
			if command.Flags().Changed("until") {
				value, err := parseDrawBoundary(until, now)
				if err != nil {
					return fmt.Errorf("invalid --until: %w", err)
				}
				options.ModifiedUntil = &value
			}
			display := progressDisplay(verbose)
			if hook {
				display = progress.From(nil)
			}
			cfg, runner, err := loadRunner(verbose)
			if err != nil {
				return err
			}
			dataStore, err := store.New(cfg.Root)
			if err != nil {
				return err
			}
			settings := drawSettings(cfg)
			sourceGateway := sourceadapter.NewCached(cfg.Root, settings.Sources)
			service := draw.Service{
				Settings: settings, Repository: repositoryFor(dataStore),
				Sources: sourceGateway, Runner: runner, Progress: display,
				RunLock: runlock.FileLock{
					Path:      filepath.Join(cfg.Root, ".knowbrew", "state", "draw.lock"),
					Name:      "draw",
					Immediate: hook,
				},
				SearchIndex: drawSearchIndex{Config: cfg, Store: dataStore},
			}
			summary, err := service.RunWithOptions(command.Context(), options)
			if err != nil {
				if hook && errors.Is(err, runlock.ErrBusy) {
					return nil
				}
				var acquisitionErr draw.AcquisitionFailuresError
				if errors.As(err, &acquisitionErr) {
					if hook {
						return err
					}
					if writeErr := writeJSON(command.OutOrStdout(), summary); writeErr != nil {
						return errors.Join(err, writeErr)
					}
					return err
				}
				display.Abort()
				return err
			}
			if hook {
				return nil
			}
			return writeJSON(command.OutOrStdout(), summary)
		},
	}
	command.Flags().BoolVar(&hook, "hook", false, "Read a Stop hook payload from stdin and draw its transcript")
	command.Flags().BoolVar(&verbose, "verbose", false, "Stream agent output and per-record progress")
	command.Flags().IntVar(&maximum, "max", 0, "Process at most N unfinished turns across all history")
	command.Flags().StringSliceVar(&sources, "source", nil, "Limit configured sources to claude or codex")
	command.Flags().StringVar(&since, "since", "", "Use logs modified since a duration ago or timestamp")
	command.Flags().StringVar(&until, "until", "", "Use logs modified until a duration ago or timestamp")
	return command
}

func drawHookTranscriptPath(reader io.Reader) (string, error) {
	var input struct {
		Event          string  `json:"hook_event_name"`
		TranscriptPath *string `json:"transcript_path"`
	}
	decoder := json.NewDecoder(reader)
	if err := decoder.Decode(&input); err != nil {
		return "", fmt.Errorf("decode draw hook input: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			err = errors.New("multiple JSON values")
		}
		return "", fmt.Errorf("decode draw hook input: %w", err)
	}
	if input.Event != "Stop" {
		return "", fmt.Errorf("draw hook requires a Stop event, got %q", input.Event)
	}
	if input.TranscriptPath == nil {
		return "", nil
	}
	return strings.TrimSpace(*input.TranscriptPath), nil
}

func drawSettings(cfg config.Config) draw.Settings {
	sources := configuredSources(cfg)
	return draw.Settings{
		Concurrency: cfg.Draw.Concurrency, ContextTurns: cfg.Draw.ContextTurns,
		MaxContextTurns: cfg.Draw.MaxContextTurns, Backend: cfg.LLM.Backend,
		Model: cfg.LLM.DrawModel, ConfigPath: cfg.Path, Sources: sources,
	}
}

func configuredSources(cfg config.Config) []draw.ConfiguredSource {
	sources := make([]draw.ConfiguredSource, 0, len(cfg.Sources))
	for _, source := range cfg.Sources {
		sources = append(sources, draw.ConfiguredSource{
			Agent: source.Agent, Parser: source.Parser, Paths: source.Paths,
		})
	}
	return sources
}

func parseDrawBoundary(value string, now time.Time) (time.Time, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, errors.New("value is required")
	}
	if duration, err := time.ParseDuration(value); err == nil {
		if duration < 0 {
			return time.Time{}, errors.New("relative duration must not be negative")
		}
		return now.Add(-duration), nil
	}
	if len(value) > 1 {
		unit := value[len(value)-1]
		if unit == 'd' || unit == 'w' {
			count, err := strconv.Atoi(value[:len(value)-1])
			if err == nil && count >= 0 {
				multiplier := 24 * time.Hour
				if unit == 'w' {
					multiplier *= 7
				}
				return now.Add(-time.Duration(count) * multiplier), nil
			}
		}
	}
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05", "2006-01-02"} {
		parsed, err := time.ParseInLocation(layout, value, now.Location())
		if err == nil {
			return parsed, nil
		}
	}
	return time.Time{}, fmt.Errorf(
		"%q is not a duration such as 24h or 7d, an RFC3339 timestamp, or a date",
		value,
	)
}

func newBrewCommand() *cobra.Command {
	var (
		verbose bool
		maximum int
	)
	command := &cobra.Command{
		Use:   "brew",
		Short: "Brew unresolved subjectful assertions into pending knowledge",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			options := brew.Options{}
			if command.Flags().Changed("max") {
				if maximum < 1 {
					return errors.New("--max must be greater than zero")
				}
				options.Max = maximum
			}
			display := progressDisplay(verbose)
			cfg, runner, err := loadRunner(verbose)
			if err != nil {
				return err
			}
			repository, err := persistenceadapter.New(cfg.Root)
			if err != nil {
				return err
			}
			index, err := searchSynchronizer(cfg, repository.Store)
			if err != nil {
				return err
			}
			sourceGateway := sourceadapter.NewCached(cfg.Root, configuredSources(cfg))
			service := brew.Service{
				Settings: brew.Settings{
					ContextTurns: cfg.Draw.ContextTurns,
					Backend:      cfg.LLM.Backend,
					Model:        cfg.LLM.BrewModel,
				},
				Repository: repository,
				Lifecycle:  repository,
				Dialogue: dialogueadapter.Query{
					Store: repository.Store, Source: sourceGateway,
				},
				Runner:   runner,
				Progress: display,
				RunLock: runlock.FileLock{
					Path: filepath.Join(cfg.Root, ".knowbrew", "state", "brew.lock"),
					Name: "brew",
				},
				SearchIndex: index,
			}
			summary, err := service.RunWithOptions(command.Context(), options)
			closeErr := index.Close()
			if err != nil {
				display.Abort()
				return errors.Join(err, closeErr)
			}
			if closeErr != nil {
				return closeErr
			}
			return writeJSON(os.Stdout, summary)
		},
	}
	command.Flags().IntVar(&maximum, "max", 0, "Process at most N unresolved assertions")
	command.Flags().BoolVar(&verbose, "verbose", false, "Stream agent output and per-record progress")
	return command
}

func newDistillCommand() *cobra.Command {
	var verbose bool
	var subject, template string
	var maximum int
	command := &cobra.Command{
		Use:   "distill",
		Short: "Regenerate Subject documents from approved current Knowledge",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			if command.Flags().Changed("max") && maximum < 1 {
				return errors.New("--max must be greater than zero")
			}
			display := progressDisplay(verbose)
			cfg, runner, err := loadRunner(verbose)
			if err != nil {
				return err
			}
			repository, err := persistenceadapter.New(cfg.Root)
			if err != nil {
				return err
			}
			service := distill.Service{
				Settings: distill.Settings{
					Backend: cfg.LLM.Backend,
					Model:   cfg.LLM.DistillModel,
				},
				Repository: repository,
				Lifecycle:  repository,
				Runner:     runner,
				Progress:   display,
				RunLock: runlock.FileLock{
					Path: filepath.Join(cfg.Root, ".knowbrew", "state", "distill.lock"),
					Name: "distill",
				},
				Cursor: runstate.DistillCursor{
					Path: filepath.Join(cfg.Root, ".knowbrew", "state", "distill-cursor.json"),
				},
			}
			summary, err := service.Run(command.Context(), distill.Options{
				Subject: subject, Template: template, Max: maximum,
			})
			if err != nil {
				display.Abort()
				return err
			}
			return writeJSON(os.Stdout, summary)
		},
	}
	command.Flags().StringVar(&subject, "subject", "", "Limit distillation to one Subject")
	command.Flags().StringVar(&template, "template", "", "Limit distillation to one assigned Template")
	command.Flags().IntVar(&maximum, "max", 0, "Process at most N Subject documents")
	command.Flags().BoolVar(&verbose, "verbose", false, "Stream agent output and per-document progress")
	return command
}

func newShowCommand() *cobra.Command {
	var (
		raw  bool
		page int
	)
	command := &cobra.Command{
		Use:   "show <feedstock-id...>",
		Short: "Show feedstock records or source-turn dialogue as JSON",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(command *cobra.Command, ids []string) error {
			if raw {
				if len(ids) != 1 {
					return errors.New("--raw requires exactly one feedstock ID")
				}
				if page < 1 {
					return errors.New("--page must be greater than zero")
				}
			} else if command.Flags().Changed("page") {
				return errors.New("--page requires --raw")
			}
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			dataStore, err := store.New(cfg.Root)
			if err != nil {
				return err
			}
			if raw {
				sourceGateway := sourceadapter.NewCached(cfg.Root, configuredSources(cfg))
				reader := dialogueadapter.Query{Store: dataStore, Source: sourceGateway}
				response, err := query.ShowRaw(dataStore, reader, ids[0], page)
				if err != nil {
					return err
				}
				return writeJSON(os.Stdout, response)
			}
			response, err := query.Show(dataStore, ids)
			if err != nil {
				return err
			}
			return writeJSON(os.Stdout, response)
		},
	}
	command.Flags().BoolVar(&raw, "raw", false, "Read user text and the final assistant response from the source log")
	command.Flags().IntVar(&page, "page", 1, "One-based raw-dialogue page")
	return command
}

type searchFlags struct {
	subject, typeValue, since, until string
	mode                             string
	limit, maxTokens                 int
	reindex                          bool
}

func addSearchFlags(command *cobra.Command, flags *searchFlags) {
	command.Flags().StringVar(&flags.subject, "subject", "", "Filter by exact subject")
	command.Flags().StringVar(&flags.typeValue, "type", "", "Filter by exact knowledge type")
	command.Flags().StringVar(&flags.since, "since", "", "Filter at or after an RFC3339 timestamp or YYYY-MM-DD date")
	command.Flags().StringVar(&flags.until, "until", "", "Filter at or before an RFC3339 timestamp or YYYY-MM-DD date")
	command.Flags().IntVar(&flags.limit, "limit", 20, "Maximum returned results")
	command.Flags().IntVar(&flags.maxTokens, "max-tokens", 2000, "Approximate maximum JSON result tokens")
	command.Flags().BoolVar(&flags.reindex, "reindex", false, "Fully rebuild the derived search index")
	command.Flags().StringVar(&flags.mode, "search-mode", string(searchapp.ModeHybrid), "Search mode: hybrid, text, or vector")
}

func runSearch(
	command *cobra.Command,
	target searchapp.Target,
	keywords []string,
	flags searchFlags,
	options searchapp.Options,
) (searchapp.Response, error) {
	cfg, err := config.Load()
	if err != nil {
		return searchapp.Response{}, err
	}
	dataStore, err := store.New(cfg.Root)
	if err != nil {
		return searchapp.Response{}, err
	}
	since, err := parseOptionalTime(flags.since, false)
	if err != nil {
		return searchapp.Response{}, fmt.Errorf("invalid --since: %w", err)
	}
	until, err := parseOptionalTime(flags.until, true)
	if err != nil {
		return searchapp.Response{}, fmt.Errorf("invalid --until: %w", err)
	}
	options.Target = target
	options.Keywords = keywords
	options.Subject = flags.subject
	if flags.typeValue != "" {
		knowledgeType := domain.KnowledgeType(flags.typeValue)
		if err := domain.ValidateKnowledgeTypeName(knowledgeType); err != nil {
			return searchapp.Response{}, fmt.Errorf("invalid --type: %w", err)
		}
		options.Type = knowledgeType
	}
	options.Since = since
	options.Until = until
	options.Limit = flags.limit
	options.MaxTokens = flags.maxTokens
	options.Reindex = flags.reindex
	options.Mode = searchapp.Mode(flags.mode)
	if options.Trigger != "" {
		service := searchapp.Service{Gateway: query.Gateway{Store: dataStore}}
		return service.Search(command.Context(), options)
	}
	encoder, err := embeddingadapter.Open(cfg.Root, cfg.Embedding)
	if err != nil {
		return searchapp.Response{}, err
	}
	service := searchapp.Service{Gateway: query.Gateway{Store: dataStore, Encoder: encoder}}
	response, searchErr := service.Search(command.Context(), options)
	if encoder == nil {
		return response, searchErr
	}
	return response, errors.Join(searchErr, encoder.Close())
}

func newContextCommand() *cobra.Command {
	var hook bool
	var maxTokens int
	command := &cobra.Command{
		Use:   "context",
		Short: "Print session-start context assembled from distilled Subject documents",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			if _, internalInvocation := os.LookupEnv(config.InvocationIDEnvironment); internalInvocation {
				return nil
			}
			cwd := ""
			if hook {
				payloadCWD, err := contextHookCWD(command.InOrStdin())
				if err != nil {
					return err
				}
				cwd = payloadCWD
			}
			if cwd == "" {
				if value, err := os.Getwd(); err == nil {
					cwd = value
				}
			}
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			dataStore, err := store.New(cfg.Root)
			if err != nil {
				return err
			}
			if !command.Flags().Changed("max-tokens") {
				maxTokens = cfg.Context.MaxTokens
			}
			discover := func() string {
				return sourceadapter.Gateway{}.DiscoverRepository(command.Context(), cwd)
			}
			output, warnings, err := inject.Build(repositoryFor(dataStore), cwd, discover, maxTokens)
			if err != nil {
				return err
			}
			for _, warning := range warnings {
				_, _ = fmt.Fprintf(command.ErrOrStderr(), "warning: %s: %s\n", warning.Path, warning.Reason)
			}
			if output != "" {
				_, _ = fmt.Fprint(command.OutOrStdout(), output)
			}
			return nil
		},
	}
	command.Flags().BoolVar(&hook, "hook", false, "Read the SessionStart hook payload from stdin")
	command.Flags().IntVar(&maxTokens, "max-tokens", 0, "Approximate maximum injected context tokens (defaults to [context] max_tokens)")
	return command
}

func contextHookCWD(reader io.Reader) (string, error) {
	var input struct {
		Event string  `json:"hook_event_name"`
		CWD   *string `json:"cwd"`
	}
	decoder := json.NewDecoder(reader)
	if err := decoder.Decode(&input); err != nil {
		if errors.Is(err, io.EOF) {
			return "", nil
		}
		return "", fmt.Errorf("decode context hook input: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			err = errors.New("multiple JSON values")
		}
		return "", fmt.Errorf("decode context hook input: %w", err)
	}
	if input.Event != "SessionStart" {
		return "", fmt.Errorf("context hook requires a SessionStart event, got %q", input.Event)
	}
	if input.CWD == nil {
		return "", nil
	}
	return strings.TrimSpace(*input.CWD), nil
}

func newDocumentCommand() *cobra.Command {
	var flags searchFlags
	var template string
	command := &cobra.Command{
		Use:   "document [keywords...]",
		Short: "Search distilled Subject documents as JSON",
		Args:  cobra.ArbitraryArgs,
		RunE: func(command *cobra.Command, keywords []string) error {
			response, err := runSearch(command, searchapp.TargetDocument, keywords, flags, searchapp.Options{
				Template: template,
			})
			if err != nil {
				return err
			}
			return writeJSON(command.OutOrStdout(), response)
		},
	}
	command.Flags().StringVar(&flags.subject, "subject", "", "Filter by exact subject")
	command.Flags().StringVar(&template, "template", "", "Filter by exact template name")
	command.Flags().StringVar(&flags.since, "since", "", "Filter at or after an RFC3339 timestamp or YYYY-MM-DD date")
	command.Flags().StringVar(&flags.until, "until", "", "Filter at or before an RFC3339 timestamp or YYYY-MM-DD date")
	command.Flags().IntVar(&flags.limit, "limit", 20, "Maximum returned results")
	command.Flags().IntVar(&flags.maxTokens, "max-tokens", 2000, "Approximate maximum JSON result tokens")
	command.Flags().BoolVar(&flags.reindex, "reindex", false, "Fully rebuild the derived search index")
	command.Flags().StringVar(&flags.mode, "search-mode", string(searchapp.ModeHybrid), "Search mode: hybrid, text, or vector")
	return command
}

func newFeedstockCommand() *cobra.Command {
	var (
		flags          searchFlags
		session, agent string
		last           int
	)
	parent := &cobra.Command{
		Use:   "feedstock [keywords...]",
		Short: "Search immutable feedstock records as JSON",
		Args:  cobra.ArbitraryArgs,
		RunE: func(command *cobra.Command, keywords []string) error {
			if command.Flags().Changed("last") && last <= 0 {
				return errors.New("--last must be greater than zero")
			}
			response, err := runSearch(command, searchapp.TargetFeedstock, keywords, flags, searchapp.Options{
				Session: session, Agent: agent, Last: last,
			})
			if err != nil {
				return err
			}
			return writeJSON(os.Stdout, response)
		},
	}
	addSearchFlags(parent, &flags)
	parent.Flags().StringVar(&session, "session", "", "Filter by exact source session ID")
	parent.Flags().StringVar(&agent, "agent", "", "Filter by agent name")
	parent.Flags().IntVar(&last, "last", 0, "Return the latest N feedstocks in oldest-to-newest order")
	var assertions []string
	var summary string
	summarize := &cobra.Command{
		Use:   "summarize <feedstock-id>",
		Short: "Write the target-turn summary for one pending feedstock",
		Args:  cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			dataStore, err := store.New(cfg.Root)
			if err != nil {
				return err
			}
			if err := draw.Summarize(
				command.Context(), repositoryFor(dataStore),
				invocationadapter.Guard{Root: cfg.Root}, args[0], summary,
			); err != nil {
				return err
			}
			return writeJSON(os.Stdout, map[string]any{
				"feedstock_id": args[0], "summarized": true,
			})
		},
	}
	summarize.Flags().StringVar(&summary, "summary", "", "One- or two-sentence factual summary of the target turn")
	_ = summarize.MarkFlagRequired("summary")
	annotate := &cobra.Command{
		Use:   "annotate <feedstock-id>",
		Short: "Finalize assertions for one summarized feedstock",
		Args:  cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			dataStore, err := store.New(cfg.Root)
			if err != nil {
				return err
			}
			parsedAssertions, err := parseAssertionInputs(assertions)
			if err != nil {
				return err
			}
			_, err = draw.Annotate(
				command.Context(), repositoryFor(dataStore),
				invocationadapter.Guard{Root: cfg.Root}, draw.Annotation{
					FeedstockID: args[0], Assertions: parsedAssertions,
				})
			if err != nil {
				return err
			}
			return writeJSON(os.Stdout, map[string]any{
				"feedstock_id": args[0], "annotated": true,
			})
		},
	}
	annotate.Flags().StringArrayVar(&assertions, "assertion", nil, "Atomic assertion as JSON (repeatable)")
	contextCommand := &cobra.Command{
		Use:   "context <feedstock-id>",
		Short: "Load bounded source context for one ambiguous annotation",
		Args:  cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			if err := invocation.ValidateFeedstock(args[0]); err != nil {
				return err
			}
			dataStore, err := store.New(cfg.Root)
			if err != nil {
				return err
			}
			turnContext, warnings, err := draw.LoadAnnotationContext(
				sourceadapter.NewCached(cfg.Root, configuredSources(cfg)),
				repositoryFor(dataStore),
				args[0],
				cfg.Draw.MaxContextTurns,
			)
			if err != nil {
				return err
			}
			if _, internal := os.LookupEnv(config.InvocationIDEnvironment); internal {
				if err := invocation.RecordAnnotationContext(cfg.Root); err != nil {
					return err
				}
			}
			return writeJSON(command.OutOrStdout(), struct {
				draw.AnnotationContext
				Warnings []diagnostic.Warning `json:"warnings,omitempty"`
			}{AnnotationContext: turnContext, Warnings: warnings})
		},
	}
	parent.AddCommand(summarize, annotate, contextCommand)
	return parent
}

func parseAssertionInputs(values []string) ([]draw.AssertionInput, error) {
	assertions := make([]draw.AssertionInput, 0, len(values))
	for index, value := range values {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal([]byte(value), &fields); err != nil {
			return nil, fmt.Errorf("invalid --assertion %d: %w", index+1, err)
		}
		rawSubject, exists := fields["subject"]
		if !exists {
			return nil, fmt.Errorf("invalid --assertion %d: subject is required; use an empty string when none applies", index+1)
		}
		var explicitSubject string
		if err := json.Unmarshal(rawSubject, &explicitSubject); err != nil {
			return nil, fmt.Errorf("invalid --assertion %d: subject must be a string", index+1)
		}
		decoder := json.NewDecoder(strings.NewReader(value))
		decoder.DisallowUnknownFields()
		var assertion draw.AssertionInput
		if err := decoder.Decode(&assertion); err != nil {
			return nil, fmt.Errorf("invalid --assertion %d: %w", index+1, err)
		}
		if err := decoder.Decode(&struct{}{}); err != io.EOF {
			if err == nil {
				err = errors.New("multiple JSON values")
			}
			return nil, fmt.Errorf("invalid --assertion %d: %w", index+1, err)
		}
		assertions = append(assertions, assertion)
	}
	return assertions, nil
}

func newKnowledgeCommand() *cobra.Command {
	var (
		flags                          searchFlags
		includePending, includeRetired bool
		trigger                        string
	)
	parent := &cobra.Command{
		Use:     "knowledge [keywords...]",
		Aliases: []string{"kn"},
		Short:   "Search knowledge as JSON or apply validated knowledge operations",
		Args:    cobra.ArbitraryArgs,
		RunE: func(command *cobra.Command, keywords []string) error {
			if trigger != "" {
				if trigger != "always" {
					return errors.New("--trigger must be always")
				}
				if includePending {
					return errors.New("--trigger and --include-pending cannot be used together")
				}
				if includeRetired {
					return errors.New("--trigger and --include-retired cannot be used together")
				}
				if _, internalInvocation := os.LookupEnv(config.InvocationIDEnvironment); internalInvocation {
					return writeJSON(command.OutOrStdout(), map[string]any{
						"approved_rules": make([]approvedRule, 0),
						"total":          0,
						"returned":       0,
						"has_more":       false,
						"truncated":      false,
					})
				}
			}
			response, err := runSearch(command, searchapp.TargetKnowledge, keywords, flags, searchapp.Options{
				IncludePending: includePending,
				IncludeRetired: includeRetired, Trigger: trigger,
			})
			if err != nil {
				return err
			}
			if trigger != "" {
				rules := make([]approvedRule, len(response.Results))
				for index, result := range response.Results {
					rules[index] = approvedRule{ID: result.ID, Claim: result.Claim, Subject: result.Subject}
				}
				output := map[string]any{
					"approved_rules": rules,
					"total":          response.Total,
					"returned":       response.Returned,
					"has_more":       response.HasMore,
					"truncated":      response.Truncated,
				}
				if len(response.Warnings) > 0 {
					output["warnings"] = response.Warnings
				}
				return writeJSON(command.OutOrStdout(), output)
			}
			return writeJSON(command.OutOrStdout(), response)
		},
	}
	addSearchFlags(parent, &flags)
	parent.Flags().BoolVar(&includePending, "include-pending", false, "Include pending knowledge")
	parent.Flags().BoolVar(&includeRetired, "include-retired", false, "Include invalidated and superseded knowledge")
	parent.Flags().StringVar(&trigger, "trigger", "", "Filter active knowledge by trigger; currently only always")
	parent.AddCommand(newKnowledgeCatalogCommand(), newKnowledgeShowCommand(), newKnowledgeSubmitCommand())
	return parent
}

type approvedRule struct {
	ID      string `json:"id"`
	Claim   string `json:"claim"`
	Subject string `json:"subject,omitempty"`
}

func newKnowledgeCatalogCommand() *cobra.Command {
	var subject, queryText string
	command := &cobra.Command{
		Use:    "catalog",
		Short:  "List compact Knowledge semantics for one assertion subject",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			dataStore, err := store.New(cfg.Root)
			if err != nil {
				return err
			}
			encoder, err := embeddingadapter.Open(cfg.Root, cfg.Embedding)
			if err != nil {
				return err
			}
			searchService := searchapp.Service{Gateway: query.Gateway{Store: dataStore, Encoder: encoder}}
			candidateIDs, err := searchService.CandidateIDs(command.Context(), searchapp.Options{
				Target: searchapp.TargetKnowledge, Keywords: []string{queryText}, Subject: subject,
				IncludePending: true, Limit: 30,
			})
			if encoder != nil {
				err = errors.Join(err, encoder.Close())
			}
			if err != nil {
				return err
			}
			entries, err := brew.Catalog(
				repositoryFor(dataStore), invocationadapter.Guard{Root: dataStore.Root},
				subject, candidateIDs,
			)
			if err != nil {
				return err
			}
			return writeJSON(command.OutOrStdout(), map[string]any{
				"subject": domain.MasterName(subject), "knowledge": entries,
			})
		},
	}
	command.Flags().StringVar(&subject, "subject", "", "Exact assertion subject")
	command.Flags().StringVar(&queryText, "query", "", "Verified or corrected assertion statement")
	_ = command.MarkFlagRequired("subject")
	_ = command.MarkFlagRequired("query")
	return command
}

func newKnowledgeShowCommand() *cobra.Command {
	command := &cobra.Command{
		Use:   "show <knowledge-id...>",
		Short: "Show knowledge records of any lifecycle state as JSON",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(command *cobra.Command, ids []string) error {
			dataStore, err := configuredStore()
			if err != nil {
				return err
			}
			if _, internal := os.LookupEnv(config.InvocationIDEnvironment); internal {
				results, err := brew.Show(
					repositoryFor(dataStore), invocationadapter.Guard{Root: dataStore.Root}, ids,
				)
				if err != nil {
					return err
				}
				return writeJSON(command.OutOrStdout(), map[string]any{
					"knowledge": results,
				})
			}
			_, warnings, err := knowledgeapp.Reconcile(command.Context(), repositoryFor(dataStore))
			if err != nil {
				return err
			}
			type shownKnowledge struct {
				ID        string           `json:"id"`
				Knowledge domain.Knowledge `json:"knowledge"`
				Body      string           `json:"body"`
				Path      string           `json:"path"`
			}
			results := make([]shownKnowledge, 0, len(ids))
			for _, id := range ids {
				file, err := dataStore.FindKnowledge(id)
				if err != nil {
					return err
				}
				results = append(results, shownKnowledge{
					ID: id, Knowledge: file.Knowledge, Body: file.Body, Path: file.Path,
				})
			}
			return writeJSON(command.OutOrStdout(), map[string]any{
				"knowledge": results,
				"warnings":  warnings,
			})
		},
	}
	return command
}

func newKnowledgeSubmitCommand() *cobra.Command {
	var (
		assertionID, verificationValue, correctedValue, resolutionValue string
	)
	command := &cobra.Command{
		Use:   "submit <feedstock-id>",
		Short: "Submit one semantic knowledge decision for deterministic application",
		Args:  cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			dataStore, err := configuredStore()
			if err != nil {
				return err
			}
			var corrected *domain.Assertion
			if strings.TrimSpace(correctedValue) != "" {
				value := domain.Assertion{}
				if err := decodeStrictJSON(correctedValue, &value); err != nil {
					return fmt.Errorf("decode --corrected-assertion: %w", err)
				}
				corrected = &value
			}
			var resolution *brew.ResolutionInput
			if strings.TrimSpace(resolutionValue) != "" {
				value := brew.ResolutionInput{}
				if err := decodeStrictJSON(resolutionValue, &value); err != nil {
					return fmt.Errorf("decode --resolution: %w", err)
				}
				resolution = &value
			}
			result, err := brew.Submit(
				command.Context(), repositoryFor(dataStore),
				invocationadapter.Guard{Root: dataStore.Root}, brew.SubmitInput{
					FeedstockID: args[0], AssertionID: assertionID,
					Verification:       brew.VerificationStatus(verificationValue),
					CorrectedAssertion: corrected, Resolution: resolution,
				})
			if err != nil {
				return err
			}
			return writeJSON(command.OutOrStdout(), result)
		},
	}
	command.Flags().StringVar(&assertionID, "assertion", "", "Assertion ID")
	command.Flags().StringVar(&verificationValue, "verification", "", "Source verification: verified, corrected, or rejected")
	command.Flags().StringVar(&correctedValue, "corrected-assertion", "", "Complete corrected assertion as JSON")
	command.Flags().StringVar(&resolutionValue, "resolution", "", "Complete semantic resolution as JSON")
	_ = command.MarkFlagRequired("assertion")
	_ = command.MarkFlagRequired("verification")
	return command
}

func decodeStrictJSON(value string, target any) error {
	decoder := json.NewDecoder(strings.NewReader(value))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	return nil
}

func configuredStore() (*store.Store, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	return store.New(cfg.Root)
}

func newIndexCommand() *cobra.Command {
	parent := &cobra.Command{
		Use:   "index",
		Short: "Synchronize, rebuild, or inspect derived search indexes",
		Args:  cobra.NoArgs,
	}
	parent.AddCommand(
		&cobra.Command{
			Use:   "sync",
			Short: "Synchronize changed Markdown records into the search indexes",
			Args:  cobra.NoArgs,
			RunE: func(command *cobra.Command, _ []string) error {
				return withConfiguredSearch(func(service searchapp.Service) error {
					report, warnings, err := service.Synchronize(command.Context(), false)
					if err != nil {
						return err
					}
					return writeJSON(command.OutOrStdout(), map[string]any{
						"sync": report, "warnings": warnings,
					})
				})
			},
		},
		&cobra.Command{
			Use:   "rebuild",
			Short: "Rebuild all derived search indexes from Markdown",
			Args:  cobra.NoArgs,
			RunE: func(command *cobra.Command, _ []string) error {
				return withConfiguredSearch(func(service searchapp.Service) error {
					report, warnings, err := service.Synchronize(command.Context(), true)
					if err != nil {
						return err
					}
					return writeJSON(command.OutOrStdout(), map[string]any{
						"sync": report, "warnings": warnings,
					})
				})
			},
		},
		&cobra.Command{
			Use:   "status",
			Short: "Inspect search index synchronization state",
			Args:  cobra.NoArgs,
			RunE: func(command *cobra.Command, _ []string) error {
				return withConfiguredSearch(func(service searchapp.Service) error {
					status, warnings, err := service.Status(command.Context())
					if err != nil {
						return err
					}
					return writeJSON(command.OutOrStdout(), map[string]any{
						"status": status, "warnings": warnings,
					})
				})
			},
		},
	)
	return parent
}

func withConfiguredSearch(run func(searchapp.Service) error) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	dataStore, err := store.New(cfg.Root)
	if err != nil {
		return err
	}
	encoder, err := embeddingadapter.Open(cfg.Root, cfg.Embedding)
	if err != nil {
		return err
	}
	service := searchapp.Service{Gateway: query.Gateway{Store: dataStore, Encoder: encoder}}
	runErr := run(service)
	if encoder == nil {
		return runErr
	}
	return errors.Join(runErr, encoder.Close())
}

func searchSynchronizer(cfg config.Config, dataStore *store.Store) (query.Synchronizer, error) {
	encoder, err := embeddingadapter.Open(cfg.Root, cfg.Embedding)
	if err != nil {
		return query.Synchronizer{}, err
	}
	return query.Synchronizer{Service: searchapp.Service{
		Gateway: query.Gateway{Store: dataStore, Encoder: encoder},
	}, Encoder: encoder}, nil
}

type drawSearchIndex struct {
	Config config.Config
	Store  *store.Store
}

func (index drawSearchIndex) Sync(ctx context.Context) ([]diagnostic.Warning, error) {
	encoder, err := embeddingadapter.Open(index.Config.Root, index.Config.Embedding)
	if err != nil {
		return nil, err
	}
	service := searchapp.Service{Gateway: query.Gateway{Store: index.Store, Encoder: encoder}}
	_, warnings, syncErr := service.Synchronize(ctx, false)
	if encoder == nil {
		return warnings, syncErr
	}
	return warnings, errors.Join(syncErr, encoder.Close())
}

func repositoryFor(dataStore *store.Store) *persistenceadapter.Markdown {
	return &persistenceadapter.Markdown{Store: dataStore}
}

func loadRunner(verbose bool) (config.Config, llm.Runner, error) {
	cfg, err := config.Load()
	if err != nil {
		return config.Config{}, nil, err
	}
	executable, err := os.Executable()
	if err != nil {
		return config.Config{}, nil, err
	}
	var runnerProgress io.Writer
	if verbose {
		runnerProgress = os.Stderr
	}
	runner, err := llm.New(cfg, executable, cfg.Root, runnerProgress, verbose)
	return cfg, runner, err
}

func progressDisplay(verbose bool) *progress.Display {
	terminal := term.IsTerminal(int(os.Stderr.Fd())) && !verbose
	width := 0
	if terminal {
		if columns, _, err := term.GetSize(int(os.Stderr.Fd())); err == nil {
			width = columns
		}
	}
	return progress.New(os.Stderr, terminal, width, verbose)
}

func parseOptionalTime(value string, endOfDay bool) (*time.Time, error) {
	if strings.TrimSpace(value) == "" {
		return nil, nil
	}
	if parsed, err := time.Parse(time.RFC3339Nano, value); err == nil {
		return &parsed, nil
	}
	parsed, err := time.ParseInLocation("2006-01-02", value, time.Local)
	if err != nil {
		return nil, errors.New("use RFC3339 or YYYY-MM-DD")
	}
	if endOfDay {
		parsed = parsed.Add(24*time.Hour - time.Nanosecond)
	}
	return &parsed, nil
}

func writeJSON(writer io.Writer, value any) error {
	encoder := json.NewEncoder(writer)
	encoder.SetIndent("", "  ")
	encoder.SetEscapeHTML(true)
	return encoder.Encode(value)
}
