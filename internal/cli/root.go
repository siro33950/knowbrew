package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/siro33950/knowbrew/internal/brew"
	"github.com/siro33950/knowbrew/internal/config"
	"github.com/siro33950/knowbrew/internal/draw"
	"github.com/siro33950/knowbrew/internal/llm"
	"github.com/siro33950/knowbrew/internal/query"
	"github.com/siro33950/knowbrew/internal/setup"
	"github.com/siro33950/knowbrew/internal/store"
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
		newShowCommand(),
		newFeedstockCommand(),
		newKnowledgeCommand(),
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
	return &cobra.Command{
		Use:   "draw [path...]",
		Short: "Draw immutable feedstock facts from session logs",
		Args:  cobra.ArbitraryArgs,
		RunE: func(command *cobra.Command, paths []string) error {
			cfg, runner, err := loadRunner()
			if err != nil {
				return err
			}
			summary, err := draw.Run(command.Context(), cfg, paths, runner, progressWriter())
			if err != nil {
				return err
			}
			return writeJSON(os.Stdout, summary)
		},
	}
}

func newBrewCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "brew",
		Short: "Brew pending knowledge from unprocessed feedstocks",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			cfg, runner, err := loadRunner()
			if err != nil {
				return err
			}
			summary, err := brew.Run(command.Context(), cfg, runner, progressWriter())
			if err != nil {
				return err
			}
			return writeJSON(os.Stdout, summary)
		},
	}
}

func newShowCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "show <feedstock-id...>",
		Short: "Show selected immutable feedstock user originals as JSON",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(_ *cobra.Command, ids []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			dataStore, err := store.New(cfg.Root)
			if err != nil {
				return err
			}
			response, err := query.Show(dataStore, ids)
			if err != nil {
				return err
			}
			return writeJSON(os.Stdout, response)
		},
	}
}

type searchFlags struct {
	subject, topic, since, until string
	limit, maxTokens             int
	reindex                      bool
}

func addSearchFlags(command *cobra.Command, flags *searchFlags) {
	command.Flags().StringVar(&flags.subject, "subject", "", "Filter by exact subject")
	command.Flags().StringVar(&flags.topic, "topic", "", "Filter by exact topic")
	command.Flags().StringVar(&flags.since, "since", "", "Filter at or after an RFC3339 timestamp or YYYY-MM-DD date")
	command.Flags().StringVar(&flags.until, "until", "", "Filter at or before an RFC3339 timestamp or YYYY-MM-DD date")
	command.Flags().IntVar(&flags.limit, "limit", 20, "Maximum returned results")
	command.Flags().IntVar(&flags.maxTokens, "max-tokens", 2000, "Approximate maximum JSON result tokens")
	command.Flags().BoolVar(&flags.reindex, "reindex", false, "Fully rebuild the derived search index")
}

func runSearch(
	command *cobra.Command,
	target query.Target,
	keywords []string,
	flags searchFlags,
	options query.SearchOptions,
) (query.SearchResponse, error) {
	cfg, err := config.Load()
	if err != nil {
		return query.SearchResponse{}, err
	}
	dataStore, err := store.New(cfg.Root)
	if err != nil {
		return query.SearchResponse{}, err
	}
	since, err := parseOptionalTime(flags.since, false)
	if err != nil {
		return query.SearchResponse{}, fmt.Errorf("invalid --since: %w", err)
	}
	until, err := parseOptionalTime(flags.until, true)
	if err != nil {
		return query.SearchResponse{}, fmt.Errorf("invalid --until: %w", err)
	}
	options.Target = target
	options.Keywords = keywords
	options.Subject = flags.subject
	options.Topic = flags.topic
	options.Since = since
	options.Until = until
	options.Limit = flags.limit
	options.MaxTokens = flags.maxTokens
	options.Reindex = flags.reindex
	return query.Search(command.Context(), dataStore, options)
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
			response, err := runSearch(command, query.TargetFeedstock, keywords, flags, query.SearchOptions{
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
	var (
		summary                      string
		speechActs, topics, subjects []string
		newTopics, newSubjects       []string
	)
	annotate := &cobra.Command{
		Use:   "annotate <feedstock-id>",
		Short: "Finalize LLM classification for one pending feedstock",
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
			added, err := draw.Annotate(command.Context(), dataStore, draw.Annotation{
				FeedstockID: args[0], Summary: summary, SpeechActs: speechActs,
				Topics: topics, Subjects: subjects, NewTopics: newTopics, NewSubjects: newSubjects,
			})
			if err != nil {
				return err
			}
			return writeJSON(os.Stdout, map[string]any{
				"feedstock_id": args[0], "annotated": true, "masters_pending_added": added,
			})
		},
	}
	annotate.Flags().StringVar(&summary, "summary", "", "One- or two-sentence factual summary")
	annotate.Flags().StringArrayVar(&speechActs, "speech-act", nil, "Closed-vocabulary speech act (repeatable)")
	annotate.Flags().StringArrayVar(&topics, "topic", nil, "Topic master name (repeatable)")
	annotate.Flags().StringArrayVar(&subjects, "subject", nil, "Subject master name (repeatable)")
	annotate.Flags().StringArrayVar(&newTopics, "new-topic", nil, "New topic as name=one-line definition (repeatable)")
	annotate.Flags().StringArrayVar(&newSubjects, "new-subject", nil, "New subject as name=one-line definition (repeatable)")
	_ = annotate.MarkFlagRequired("summary")
	_ = annotate.MarkFlagRequired("speech-act")
	parent.AddCommand(annotate)
	return parent
}

func newKnowledgeCommand() *cobra.Command {
	var (
		flags          searchFlags
		includePending bool
		trigger        string
	)
	parent := &cobra.Command{
		Use:     "knowledge [keywords...]",
		Aliases: []string{"kn"},
		Short:   "Search knowledge as JSON or apply validated knowledge operations",
		Args:    cobra.ArbitraryArgs,
		RunE: func(command *cobra.Command, keywords []string) error {
			response, err := runSearch(command, query.TargetKnowledge, keywords, flags, query.SearchOptions{
				IncludePending: includePending,
				Trigger:        trigger,
			})
			if err != nil {
				return err
			}
			if trigger != "" {
				output := map[string]any{
					"approved_rules": response.Results,
					"total":          response.Total,
					"returned":       response.Returned,
					"truncated":      response.Truncated,
				}
				if len(response.Warnings) > 0 {
					output["warnings"] = response.Warnings
				}
				return writeJSON(os.Stdout, output)
			}
			return writeJSON(os.Stdout, response)
		},
	}
	addSearchFlags(parent, &flags)
	parent.Flags().BoolVar(&includePending, "include-pending", false, "Include pending knowledge; invalidated knowledge remains hidden")
	parent.Flags().StringVar(&trigger, "trigger", "", "Filter active knowledge by trigger; currently only always")
	parent.AddCommand(newKnowledgeCreateCommand(), newKnowledgeAddSourceCommand(), newKnowledgeInvalidateCommand())
	return parent
}

func newKnowledgeCreateCommand() *cobra.Command {
	var (
		appliesWhen, body, project, trigger string
		sources, topics, newTopics          []string
		newSubjects                         []string
	)
	command := &cobra.Command{
		Use:   "create <slug>",
		Short: "Create one pending knowledge record",
		Args:  cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			dataStore, err := configuredStore()
			if err != nil {
				return err
			}
			added, err := brew.CreateKnowledge(command.Context(), dataStore, brew.CreateInput{
				Slug: args[0], AppliesWhen: appliesWhen, Body: body, Sources: sources,
				Topics: topics, Project: project, Trigger: trigger, NewTopics: newTopics,
				NewSubjects: newSubjects,
			})
			if err != nil {
				return err
			}
			return writeJSON(os.Stdout, map[string]any{
				"slug": args[0], "created": true, "status": "pending",
				"masters_pending_added": added,
			})
		},
	}
	command.Flags().StringVar(&appliesWhen, "applies-when", "", "One-line retrieval condition")
	command.Flags().StringVar(&body, "body", "", "Markdown claim body")
	command.Flags().StringArrayVar(&sources, "source", nil, "Source feedstock ID (repeatable)")
	command.Flags().StringArrayVar(&topics, "topic", nil, "Topic master name (repeatable)")
	command.Flags().StringVar(&project, "project", "", "Owning project or subject")
	command.Flags().StringVar(&trigger, "trigger", "", "Optional trigger; currently only always")
	command.Flags().StringArrayVar(&newTopics, "new-topic", nil, "New topic as name=one-line definition (repeatable)")
	command.Flags().StringArrayVar(&newSubjects, "new-subject", nil, "New project subject as name=one-line definition")
	for _, name := range []string{"applies-when", "body", "source"} {
		_ = command.MarkFlagRequired(name)
	}
	return command
}

func newKnowledgeAddSourceCommand() *cobra.Command {
	var sources []string
	command := &cobra.Command{
		Use:   "add-source <slug>",
		Short: "Append validated source feedstocks to knowledge",
		Args:  cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			dataStore, err := configuredStore()
			if err != nil {
				return err
			}
			if err := brew.AddSource(command.Context(), dataStore, args[0], sources); err != nil {
				return err
			}
			return writeJSON(os.Stdout, map[string]any{"slug": args[0], "sources_added": sources})
		},
	}
	command.Flags().StringArrayVar(&sources, "source", nil, "Source feedstock ID (repeatable)")
	_ = command.MarkFlagRequired("source")
	return command
}

func newKnowledgeInvalidateCommand() *cobra.Command {
	var sources []string
	command := &cobra.Command{
		Use:   "invalidate <slug>",
		Short: "Invalidate a contradicted knowledge while preserving provenance",
		Args:  cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			dataStore, err := configuredStore()
			if err != nil {
				return err
			}
			if err := brew.Invalidate(command.Context(), dataStore, args[0], sources); err != nil {
				return err
			}
			return writeJSON(os.Stdout, map[string]any{"slug": args[0], "invalidated": true})
		},
	}
	command.Flags().StringArrayVar(&sources, "source", nil, "Contradicting source feedstock ID (repeatable)")
	_ = command.MarkFlagRequired("source")
	return command
}

func configuredStore() (*store.Store, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	return store.New(cfg.Root)
}

func loadRunner() (config.Config, llm.Runner, error) {
	cfg, err := config.Load()
	if err != nil {
		return config.Config{}, nil, err
	}
	executable, err := os.Executable()
	if err != nil {
		return config.Config{}, nil, err
	}
	runner, err := llm.New(cfg, executable, cfg.Root, progressWriter())
	return cfg, runner, err
}

func progressWriter() io.Writer {
	if term.IsTerminal(int(os.Stderr.Fd())) {
		return os.Stderr
	}
	return nil
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
