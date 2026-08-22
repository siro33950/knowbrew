package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/siro33950/knowbrew/internal/adapters/fsutil"
)

const (
	ConfigEnvironment              = "KNOWBREW_CONFIG"
	APITokenEnvironment            = "KNOWBREW_API_KEY"
	APIURLEnvironment              = "KNOWBREW_API_URL"
	InvocationFeedstockEnvironment = "KNOWBREW_INVOCATION_FEEDSTOCK"
	InvocationIDEnvironment        = "KNOWBREW_INVOCATION_ID"
	InvocationTaskEnvironment      = "KNOWBREW_INVOCATION_TASK"
	DefaultLLMTimeout              = 5 * time.Minute
	DefaultContextMaxTokens        = 2000
	DefaultDrawConcurrency         = 5
	DefaultDrawContextTurns        = 3
	DefaultDrawMaxContextTurns     = 20
	DefaultDrawDraftEffort         = "low"
	DefaultDistillEffort           = "high"
	DefaultEmbeddingModel          = EmbeddingRuri
	EmbeddingDisabled              = "disabled"
	EmbeddingRuri                  = "ruri-v3-130m-int8-onnx"
	EmbeddingSnowflake             = "snowflake-arctic-embed-m-v1.5-int8-onnx"
	EmbeddingQwen                  = "qwen3-embedding-0.6b-q8_0"
	EmbeddingCustom                = "custom"
)

type LLM struct {
	Backend           string `toml:"backend"`
	DrawDraftModel    string `toml:"draw_draft_model"`
	DrawExtractModel  string `toml:"draw_extract_model"`
	BrewModel         string `toml:"brew_model"`
	DistillModel      string `toml:"distill_model"`
	DrawDraftEffort   string `toml:"draw_draft_effort"`
	DrawExtractEffort string `toml:"draw_extract_effort"`
	BrewEffort        string `toml:"brew_effort"`
	DistillEffort     string `toml:"distill_effort"`
	Timeout           string `toml:"timeout,omitempty"`
}

type Draw struct {
	Concurrency     int `toml:"concurrency"`
	ContextTurns    int `toml:"context_turns"`
	MaxContextTurns int `toml:"max_context_turns"`
}

type Context struct {
	MaxTokens int `toml:"max_tokens"`
}

type Embedding struct {
	Model string `toml:"model"`
	Path  string `toml:"path,omitempty"`
}

type Source struct {
	Agent      string   `toml:"agent"`
	Paths      []string `toml:"paths,omitempty"`
	Parser     string   `toml:"parser"`
	LegacyPath string   `toml:"path,omitempty"`
}

type Config struct {
	Root      string    `toml:"root"`
	LLM       LLM       `toml:"llm"`
	Draw      Draw      `toml:"draw"`
	Context   Context   `toml:"context"`
	Embedding Embedding `toml:"embedding"`
	Sources   []Source  `toml:"sources"`

	Path                 string `toml:"-"`
	contextMaxTokensSet  bool   `toml:"-"`
	drawConcurrencySet   bool   `toml:"-"`
	drawContextTurnsSet  bool   `toml:"-"`
	drawMaxContextSet    bool   `toml:"-"`
	drawDraftEffortSet   bool   `toml:"-"`
	drawExtractEffortSet bool   `toml:"-"`
	brewEffortSet        bool   `toml:"-"`
	distillModelSet      bool   `toml:"-"`
	distillEffortSet     bool   `toml:"-"`
	embeddingModelSet    bool   `toml:"-"`
}

type Locator struct {
	Config string `toml:"config"`
}

func DefaultConfigPath(root string) string {
	return filepath.Join(root, ".knowbrew", "config.toml")
}

func LocatorPath() (string, error) {
	base := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME"))
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("find home directory: %w", err)
		}
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "knowbrew", "location.toml"), nil
}

func ResolvePath() (string, error) {
	if explicit := strings.TrimSpace(os.Getenv(ConfigEnvironment)); explicit != "" {
		return expandPath(explicit)
	}
	locatorPath, err := LocatorPath()
	if err != nil {
		return "", err
	}
	var locator Locator
	if _, err := toml.DecodeFile(locatorPath, &locator); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", errors.New("configuration not found; run \"knowbrew init\"")
		}
		return "", fmt.Errorf("read locator %s: %w", locatorPath, err)
	}
	if strings.TrimSpace(locator.Config) == "" {
		return "", fmt.Errorf("locator %s does not define config", locatorPath)
	}
	return expandPath(locator.Config)
}

func Load() (Config, error) {
	path, err := ResolvePath()
	if err != nil {
		return Config{}, err
	}
	return LoadPath(path)
}

// LoadPath reads one explicit configuration file without consulting the
// process-wide locator. Setup uses it to preserve the root it is updating.
func LoadPath(path string) (Config, error) {
	return loadPath(path, false)
}

// LoadPathForSetup reads a configuration file that may still carry the retired
// single-stage draw keys, so that "knowbrew init" can migrate it instead of
// refusing to run. Retired values seed the equivalent two-stage keys.
func LoadPathForSetup(path string) (Config, error) {
	return loadPath(path, true)
}

type retiredDrawKeys struct {
	Model  string `toml:"draw_model"`
	Effort string `toml:"draw_effort"`
}

func loadPath(path string, migrate bool) (Config, error) {
	var cfg Config
	metadata, err := toml.DecodeFile(path, &cfg)
	if err != nil {
		return Config{}, fmt.Errorf("read configuration %s: %w", path, err)
	}
	if metadata.IsDefined("llm", "model") {
		return Config{}, fmt.Errorf(
			"invalid configuration %s: [llm] model is no longer supported; migrate it to draw_draft_model, draw_extract_model, brew_model, and distill_model",
			path,
		)
	}
	cfg.Path = path
	cfg.contextMaxTokensSet = metadata.IsDefined("context", "max_tokens")
	cfg.drawConcurrencySet = metadata.IsDefined("draw", "concurrency")
	cfg.drawContextTurnsSet = metadata.IsDefined("draw", "context_turns")
	cfg.drawMaxContextSet = metadata.IsDefined("draw", "max_context_turns")
	cfg.drawDraftEffortSet = metadata.IsDefined("llm", "draw_draft_effort")
	cfg.drawExtractEffortSet = metadata.IsDefined("llm", "draw_extract_effort")
	cfg.brewEffortSet = metadata.IsDefined("llm", "brew_effort")
	cfg.distillModelSet = metadata.IsDefined("llm", "distill_model")
	cfg.distillEffortSet = metadata.IsDefined("llm", "distill_effort")
	cfg.embeddingModelSet = metadata.IsDefined("embedding", "model")
	if err := resolveDrawStageKeys(path, &cfg, metadata, migrate); err != nil {
		return Config{}, err
	}
	if !cfg.distillModelSet {
		cfg.LLM.DistillModel = cfg.LLM.BrewModel
	}
	if !cfg.distillEffortSet {
		cfg.LLM.DistillEffort = DefaultDistillEffort
	}
	if err := cfg.Normalize(); err != nil {
		return Config{}, fmt.Errorf("invalid configuration %s: %w", path, err)
	}
	return cfg, nil
}

// resolveDrawStageKeys keeps the two Draw stages independently configured. A
// missing stage key is an error rather than a silent fallback to the other
// stage or to Brew, because a stage would then run on a model the operator
// never chose.
func resolveDrawStageKeys(
	path string,
	cfg *Config,
	metadata toml.MetaData,
	migrate bool,
) error {
	retired := metadata.IsDefined("llm", "draw_model") || metadata.IsDefined("llm", "draw_effort")
	if retired {
		if !migrate {
			return fmt.Errorf(
				"invalid configuration %s: [llm] draw_model and draw_effort are no longer supported; "+
					"migrate them to draw_draft_model, draw_draft_effort, draw_extract_model, "+
					"and draw_extract_effort",
				path,
			)
		}
		var previous struct {
			LLM retiredDrawKeys `toml:"llm"`
		}
		if _, err := toml.DecodeFile(path, &previous); err != nil {
			return fmt.Errorf("read configuration %s: %w", path, err)
		}
		if !metadata.IsDefined("llm", "draw_draft_model") {
			cfg.LLM.DrawDraftModel = previous.LLM.Model
		}
		if !cfg.drawDraftEffortSet {
			cfg.LLM.DrawDraftEffort = previous.LLM.Effort
			cfg.drawDraftEffortSet = metadata.IsDefined("llm", "draw_effort")
		}
		if !metadata.IsDefined("llm", "draw_extract_model") {
			cfg.LLM.DrawExtractModel = cfg.LLM.BrewModel
		}
		if !cfg.drawExtractEffortSet {
			cfg.LLM.DrawExtractEffort = cfg.LLM.BrewEffort
			cfg.drawExtractEffortSet = cfg.brewEffortSet
		}
		return nil
	}
	if migrate {
		return nil
	}
	missing := make([]string, 0, 4)
	for _, key := range []string{
		"draw_draft_model", "draw_draft_effort", "draw_extract_model", "draw_extract_effort",
	} {
		if !metadata.IsDefined("llm", key) {
			missing = append(missing, key)
		}
	}
	if len(missing) != 0 {
		return fmt.Errorf(
			"invalid configuration %s: [llm] %s must be set; run \"knowbrew init\" to write the Draw stage keys",
			path, strings.Join(missing, ", "),
		)
	}
	return nil
}

// FillInitDefaults adds defaults only for keys absent from an existing file.
// Explicit empty values remain user-owned and are preserved.
func (cfg *Config) FillInitDefaults() {
	if !cfg.drawDraftEffortSet {
		cfg.LLM.DrawDraftEffort = DefaultDrawDraftEffort
	}
	if !cfg.drawExtractEffortSet {
		cfg.LLM.DrawExtractEffort = ""
	}
	if !cfg.brewEffortSet {
		cfg.LLM.BrewEffort = ""
	}
	if !cfg.distillModelSet {
		cfg.LLM.DistillModel = cfg.LLM.BrewModel
	}
	if !cfg.distillEffortSet {
		cfg.LLM.DistillEffort = DefaultDistillEffort
	}
	if !cfg.embeddingModelSet {
		cfg.Embedding.Model = DefaultEmbeddingModel
	}
}

func (cfg *Config) Normalize() error {
	if strings.TrimSpace(cfg.Path) == "" {
		return errors.New("configuration path is unknown")
	}
	root := strings.TrimSpace(cfg.Root)
	if root == "" {
		root = ".."
	}
	if !filepath.IsAbs(root) {
		root = filepath.Join(filepath.Dir(cfg.Path), root)
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return fmt.Errorf("resolve root: %w", err)
	}
	cfg.Root = filepath.Clean(absolute)
	switch cfg.LLM.Backend {
	case "claude-cli", "codex-cli", "api", "ollama":
	default:
		return fmt.Errorf("unsupported LLM backend %q", cfg.LLM.Backend)
	}
	if (cfg.LLM.Backend == "api" || cfg.LLM.Backend == "ollama") &&
		(strings.TrimSpace(cfg.LLM.DrawDraftModel) == "" ||
			strings.TrimSpace(cfg.LLM.DrawExtractModel) == "" ||
			strings.TrimSpace(cfg.LLM.BrewModel) == "" ||
			strings.TrimSpace(cfg.LLM.DistillModel) == "") {
		return errors.New(
			"API and Ollama backends require draw_draft_model, draw_extract_model, brew_model, and distill_model",
		)
	}
	if cfg.Draw.Concurrency == 0 && !cfg.drawConcurrencySet {
		cfg.Draw.Concurrency = DefaultDrawConcurrency
	}
	if cfg.Draw.Concurrency < 1 {
		return errors.New("draw concurrency must be at least 1")
	}
	if cfg.Draw.ContextTurns == 0 && !cfg.drawContextTurnsSet {
		cfg.Draw.ContextTurns = DefaultDrawContextTurns
	}
	if cfg.Draw.ContextTurns < 0 {
		return errors.New("draw context_turns must be at least 0")
	}
	if cfg.Draw.MaxContextTurns == 0 && !cfg.drawMaxContextSet {
		cfg.Draw.MaxContextTurns = DefaultDrawMaxContextTurns
	}
	if cfg.Draw.MaxContextTurns < cfg.Draw.ContextTurns {
		return errors.New("draw max_context_turns must be at least context_turns")
	}
	if cfg.Context.MaxTokens == 0 && !cfg.contextMaxTokensSet {
		cfg.Context.MaxTokens = DefaultContextMaxTokens
	}
	if cfg.Context.MaxTokens < 1 {
		return errors.New("context max_tokens must be at least 1")
	}
	if strings.TrimSpace(cfg.LLM.Timeout) == "" {
		cfg.LLM.Timeout = DefaultLLMTimeout.String()
	}
	if _, err := cfg.LLM.TimeoutDuration(); err != nil {
		return err
	}
	cfg.Embedding.Model = strings.TrimSpace(cfg.Embedding.Model)
	switch cfg.Embedding.Model {
	case "", EmbeddingDisabled, EmbeddingRuri, EmbeddingSnowflake, EmbeddingQwen:
		cfg.Embedding.Path = ""
	case EmbeddingCustom:
		if strings.TrimSpace(cfg.Embedding.Path) == "" {
			return errors.New("custom embedding model requires path")
		}
		cfg.Embedding.Path, err = expandPath(cfg.Embedding.Path)
		if err != nil {
			return fmt.Errorf("resolve custom embedding model: %w", err)
		}
	default:
		return fmt.Errorf("unsupported embedding model %q", cfg.Embedding.Model)
	}
	normalizedSources := make([]Source, 0, len(cfg.Sources))
	sourceIndexes := make(map[string]int, len(cfg.Sources))
	for index := range cfg.Sources {
		source := &cfg.Sources[index]
		paths := append([]string(nil), source.Paths...)
		if strings.TrimSpace(source.LegacyPath) != "" {
			paths = append([]string{source.LegacyPath}, paths...)
		}
		source.LegacyPath = ""
		source.Paths = source.Paths[:0]
		seenPaths := make(map[string]struct{}, len(paths))
		for _, value := range paths {
			if strings.TrimSpace(value) == "" {
				continue
			}
			path, pathErr := expandPath(value)
			if pathErr != nil {
				return fmt.Errorf("resolve source %d: %w", index+1, pathErr)
			}
			path = filepath.Clean(path)
			if _, exists := seenPaths[path]; exists {
				continue
			}
			seenPaths[path] = struct{}{}
			source.Paths = append(source.Paths, path)
		}
		if len(source.Paths) == 0 {
			return fmt.Errorf("source %d requires at least one path", index+1)
		}
		if source.Agent != "claude" && source.Agent != "codex" {
			return fmt.Errorf("source %d has unsupported agent %q", index+1, source.Agent)
		}
		if source.Parser == "" {
			source.Parser = source.Agent
		}
		key := source.Agent + "\x00" + source.Parser
		if existingIndex, exists := sourceIndexes[key]; exists {
			existing := &normalizedSources[existingIndex]
			seen := make(map[string]struct{}, len(existing.Paths)+len(source.Paths))
			for _, path := range existing.Paths {
				seen[path] = struct{}{}
			}
			for _, path := range source.Paths {
				if _, exists := seen[path]; exists {
					continue
				}
				seen[path] = struct{}{}
				existing.Paths = append(existing.Paths, path)
			}
			continue
		}
		sourceIndexes[key] = len(normalizedSources)
		normalizedSources = append(normalizedSources, *source)
	}
	cfg.Sources = normalizedSources
	return nil
}

func (llm LLM) TimeoutDuration() (time.Duration, error) {
	value := strings.TrimSpace(llm.Timeout)
	if value == "" {
		return DefaultLLMTimeout, nil
	}
	timeout, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("invalid LLM timeout %q: %w", value, err)
	}
	if timeout <= 0 {
		return 0, errors.New("LLM timeout must be greater than zero")
	}
	return timeout, nil
}

func Save(root string, cfg Config) (string, error) {
	absoluteRoot, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("resolve root: %w", err)
	}
	path := DefaultConfigPath(absoluteRoot)
	onDisk := cfg
	if onDisk.Draw.Concurrency == 0 {
		onDisk.Draw.Concurrency = DefaultDrawConcurrency
	}
	if onDisk.Draw.MaxContextTurns == 0 {
		onDisk.Draw.MaxContextTurns = DefaultDrawMaxContextTurns
	}
	if onDisk.Context.MaxTokens == 0 {
		onDisk.Context.MaxTokens = DefaultContextMaxTokens
	}
	onDisk.Path = ""
	onDisk.Root = ".."
	var data bytes.Buffer
	if err := toml.NewEncoder(&data).Encode(onDisk); err != nil {
		return "", fmt.Errorf("encode configuration: %w", err)
	}
	if err := fsutil.AtomicWrite(path, data.Bytes(), 0o600); err != nil {
		return "", err
	}
	locatorPath, err := LocatorPath()
	if err != nil {
		return "", err
	}
	data.Reset()
	if err := toml.NewEncoder(&data).Encode(Locator{Config: path}); err != nil {
		return "", fmt.Errorf("encode locator: %w", err)
	}
	if err := fsutil.AtomicWrite(locatorPath, data.Bytes(), 0o600); err != nil {
		return "", err
	}
	return path, nil
}

func expandPath(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "~" || strings.HasPrefix(value, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		if value == "~" {
			value = home
		} else {
			value = filepath.Join(home, strings.TrimPrefix(value, "~/"))
		}
	}
	return filepath.Abs(value)
}
