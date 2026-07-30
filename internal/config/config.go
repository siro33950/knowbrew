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
	"github.com/siro33950/knowbrew/internal/fsutil"
)

const (
	ConfigEnvironment              = "KNOWBREW_CONFIG"
	APITokenEnvironment            = "KNOWBREW_API_KEY"
	APIURLEnvironment              = "KNOWBREW_API_URL"
	InvocationFeedstockEnvironment = "KNOWBREW_INVOCATION_FEEDSTOCK"
	InvocationIDEnvironment        = "KNOWBREW_INVOCATION_ID"
	DefaultLLMTimeout              = 5 * time.Minute
)

type LLM struct {
	Backend string `toml:"backend"`
	Model   string `toml:"model"`
	Timeout string `toml:"timeout,omitempty"`
}

type Source struct {
	Agent  string `toml:"agent"`
	Path   string `toml:"path"`
	Parser string `toml:"parser"`
}

type Config struct {
	Root    string   `toml:"root"`
	LLM     LLM      `toml:"llm"`
	Sources []Source `toml:"sources"`

	Path string `toml:"-"`
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
	var cfg Config
	if _, err := toml.DecodeFile(path, &cfg); err != nil {
		return Config{}, fmt.Errorf("read configuration %s: %w", path, err)
	}
	cfg.Path = path
	if err := cfg.Normalize(); err != nil {
		return Config{}, fmt.Errorf("invalid configuration %s: %w", path, err)
	}
	return cfg, nil
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
	if strings.TrimSpace(cfg.LLM.Timeout) == "" {
		cfg.LLM.Timeout = DefaultLLMTimeout.String()
	}
	if _, err := cfg.LLM.TimeoutDuration(); err != nil {
		return err
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
