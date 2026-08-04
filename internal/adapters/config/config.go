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
	InvocationAssertionEnvironment = "KNOWBREW_INVOCATION_ASSERTION"
	InvocationIDEnvironment        = "KNOWBREW_INVOCATION_ID"
	DefaultLLMTimeout              = 5 * time.Minute
	DefaultDrawConcurrency         = 5
	DefaultDrawContextTurns        = 3
	DefaultDrawMaxContextTurns     = 20
	DefaultDrawEffort              = "low"
	DefaultDistillEffort           = "high"
	DefaultEmbeddingModel          = EmbeddingRuri
	EmbeddingDisabled              = "disabled"
	EmbeddingRuri                  = "ruri-v3-130m-int8-onnx"
	EmbeddingSnowflake             = "snowflake-arctic-embed-m-v1.5-int8-onnx"
	EmbeddingQwen                  = "qwen3-embedding-0.6b-q8_0"
	EmbeddingCustom                = "custom"
)

type LLM struct {
	Backend       string `toml:"backend"`
	DrawModel     string `toml:"draw_model"`
	BrewModel     string `toml:"brew_model"`
	DistillModel  string `toml:"distill_model"`
	DrawEffort    string `toml:"draw_effort"`
	BrewEffort    string `toml:"brew_effort"`
	DistillEffort string `toml:"distill_effort"`
	Timeout       string `toml:"timeout,omitempty"`
}

type Draw struct {
	Concurrency     int `toml:"concurrency"`
	ContextTurns    int `toml:"context_turns"`
	MaxContextTurns int `toml:"max_context_turns"`
}

type Embedding struct {
	Model string `toml:"model"`
	Path  string `toml:"path,omitempty"`
}

type Source struct {
	Agent  string `toml:"agent"`
	Path   string `toml:"path"`
	Parser string `toml:"parser"`
}

type Config struct {
	Root      string    `toml:"root"`
	LLM       LLM       `toml:"llm"`
	Draw      Draw      `toml:"draw"`
	Embedding Embedding `toml:"embedding"`
	Sources   []Source  `toml:"sources"`

	Path                string `toml:"-"`
	drawConcurrencySet  bool   `toml:"-"`
	drawContextTurnsSet bool   `toml:"-"`
	drawMaxContextSet   bool   `toml:"-"`
	drawEffortSet       bool   `toml:"-"`
	brewEffortSet       bool   `toml:"-"`
	distillModelSet     bool   `toml:"-"`
	distillEffortSet    bool   `toml:"-"`
	embeddingModelSet   bool   `toml:"-"`
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
	var cfg Config
	metadata, err := toml.DecodeFile(path, &cfg)
	if err != nil {
		return Config{}, fmt.Errorf("read configuration %s: %w", path, err)
	}
	if metadata.IsDefined("llm", "model") {
		return Config{}, fmt.Errorf(
			"invalid configuration %s: [llm] model is no longer supported; migrate it to draw_model, brew_model, and distill_model",
			path,
		)
	}
	cfg.Path = path
	cfg.drawConcurrencySet = metadata.IsDefined("draw", "concurrency")
	cfg.drawContextTurnsSet = metadata.IsDefined("draw", "context_turns")
	cfg.drawMaxContextSet = metadata.IsDefined("draw", "max_context_turns")
	cfg.drawEffortSet = metadata.IsDefined("llm", "draw_effort")
	cfg.brewEffortSet = metadata.IsDefined("llm", "brew_effort")
	cfg.distillModelSet = metadata.IsDefined("llm", "distill_model")
	cfg.distillEffortSet = metadata.IsDefined("llm", "distill_effort")
	cfg.embeddingModelSet = metadata.IsDefined("embedding", "model")
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

// FillInitDefaults adds defaults only for keys absent from an existing file.
// Explicit empty values remain user-owned and are preserved.
func (cfg *Config) FillInitDefaults() {
	if !cfg.drawEffortSet {
		cfg.LLM.DrawEffort = DefaultDrawEffort
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
		(strings.TrimSpace(cfg.LLM.DrawModel) == "" || strings.TrimSpace(cfg.LLM.BrewModel) == "" ||
			strings.TrimSpace(cfg.LLM.DistillModel) == "") {
		return errors.New("API and Ollama backends require draw_model, brew_model, and distill_model")
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
	for index := range cfg.Sources {
		source := &cfg.Sources[index]
		source.Path, err = expandPath(source.Path)
		if err != nil {
			return fmt.Errorf("resolve source %d: %w", index+1, err)
		}
		if source.Agent != "claude" && source.Agent != "codex" {
			return fmt.Errorf("source %d has unsupported agent %q", index+1, source.Agent)
		}
		if source.Parser == "" {
			source.Parser = source.Agent
		}
	}
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
