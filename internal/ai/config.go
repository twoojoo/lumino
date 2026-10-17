package ai

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"

	"github.com/twoojoo/lumino/internal/prefs"
	"github.com/twoojoo/lumino/internal/secrets"
)

// Kind of provider implementation.
const (
	KindAnthropic = "anthropic"
	KindOpenAI    = "openai-compatible"
)

// Preset is a known provider endpoint.
type Preset struct {
	ID      string
	Name    string
	Kind    string
	BaseURL string
	KeyEnv  string // env var holding the API key ("" = none needed)
	Model   string // suggested default ("" = pick from list)
	Local   bool
}

// Presets lists supported providers. Any other OpenAI-compatible server
// works through "custom".
var Presets = []Preset{
	{ID: "anthropic", Name: "Anthropic (Claude)", Kind: KindAnthropic, KeyEnv: "ANTHROPIC_API_KEY", Model: DefaultAnthropicModel},
	{ID: "openai", Name: "OpenAI", Kind: KindOpenAI, BaseURL: "https://api.openai.com/v1", KeyEnv: "OPENAI_API_KEY"},
	{ID: "gemini", Name: "Google Gemini", Kind: KindOpenAI, BaseURL: "https://generativelanguage.googleapis.com/v1beta/openai/", KeyEnv: "GEMINI_API_KEY"},
	{ID: "mistral", Name: "Mistral", Kind: KindOpenAI, BaseURL: "https://api.mistral.ai/v1", KeyEnv: "MISTRAL_API_KEY"},
	{ID: "groq", Name: "Groq", Kind: KindOpenAI, BaseURL: "https://api.groq.com/openai/v1", KeyEnv: "GROQ_API_KEY"},
	{ID: "openrouter", Name: "OpenRouter", Kind: KindOpenAI, BaseURL: "https://openrouter.ai/api/v1", KeyEnv: "OPENROUTER_API_KEY"},
	{ID: "deepseek", Name: "DeepSeek", Kind: KindOpenAI, BaseURL: "https://api.deepseek.com/v1", KeyEnv: "DEEPSEEK_API_KEY"},
	{ID: "xai", Name: "xAI (Grok)", Kind: KindOpenAI, BaseURL: "https://api.x.ai/v1", KeyEnv: "XAI_API_KEY"},
	{ID: "ollama", Name: "Ollama (local)", Kind: KindOpenAI, BaseURL: "http://localhost:11434/v1", Local: true},
	{ID: "lmstudio", Name: "LM Studio (local)", Kind: KindOpenAI, BaseURL: "http://localhost:1234/v1", Local: true},
	{ID: "custom", Name: "Custom OpenAI-compatible (vLLM, llama.cpp, LocalAI…)", Kind: KindOpenAI, BaseURL: "http://localhost:8000/v1", Local: true},
}

// PresetByID returns the preset with id.
func PresetByID(id string) (Preset, bool) {
	for _, p := range Presets {
		if p.ID == id {
			return p, true
		}
	}
	return Preset{}, false
}

// ProviderConfig is the per-provider user configuration.
type ProviderConfig struct {
	Model   string `json:"model,omitempty"`
	BaseURL string `json:"baseURL,omitempty"`
	// APIKey lives in the OS keyring (never in ai.json).
	APIKey string `json:"-"`
}

// Config is persisted in ~/.config/lumino/ai.json.
type Config struct {
	Provider            string                    `json:"provider"`
	Providers           map[string]ProviderConfig `json:"providers,omitempty"`
	DisabledConnections []string                  `json:"disabledConnections,omitempty"`
	// AllowQueries lets the assistant run read-only queries itself.
	AllowQueries *bool `json:"allowQueries,omitempty"`
	path         string
	// cleared: providers whose key the user removed (only these are
	// deleted from the keyring: a key that failed to load is kept)
	cleared map[string]bool
	// LoadError: ai.json exists but could not be read (EACCES, EIO…);
	// Save refuses to replace it with the defaults.
	LoadError error `json:"-"`
}

// QueriesAllowed reports whether the model may run read-only queries
// (default on).
func (c *Config) QueriesAllowed() bool { return c.AllowQueries == nil || *c.AllowQueries }

// ConfigPath returns the config file location.
func ConfigPath() string { return filepath.Join(prefs.Dir(), "ai.json") }

// LoadConfig reads the config (missing file → defaults).
func LoadConfig() *Config {
	c := Config{Provider: "anthropic", Providers: map[string]ProviderConfig{}, path: ConfigPath()}
	b, err := os.ReadFile(c.path)
	switch {
	case err == nil:
		if err := json.Unmarshal(b, &c); err != nil {
			prefs.Quarantine(c.path, err)
			c = Config{Provider: "anthropic", path: c.path}
		}
	case !errors.Is(err, fs.ErrNotExist):
		c.LoadError = fmt.Errorf("cannot read %s (%v): settings won't be saved until it is readable", c.path, err)
	}
	if c.Providers == nil {
		c.Providers = map[string]ProviderConfig{}
	}
	if _, ok := PresetByID(c.Provider); !ok {
		c.Provider = "anthropic"
	}
	for id, pc := range c.Providers {
		if k, ok := secrets.Get("ai:"+id, secrets.APIKey); ok {
			pc.APIKey = k
			c.Providers[id] = pc
		}
	}
	return &c
}

// Save writes the config with 0600 permissions (API keys go to the keyring).
func (c *Config) Save() error {
	if c.LoadError != nil {
		return c.LoadError
	}
	if c.path == "" {
		c.path = ConfigPath()
	}
	for id, pc := range c.Providers {
		if pc.APIKey != "" {
			if err := secrets.Set("ai:"+id, secrets.APIKey, pc.APIKey, true); err != nil {
				return err
			}
		} else if c.cleared[id] {
			secrets.Delete("ai:"+id, secrets.APIKey)
			delete(c.cleared, id)
		}
	}
	b, _ := json.MarshalIndent(c, "", "  ")
	return prefs.WriteFileAtomic(c.path, b)
}

// Current returns the active preset and its effective settings.
func (c *Config) Current() (Preset, ProviderConfig) {
	p, _ := PresetByID(c.Provider)
	pc := c.Providers[p.ID]
	if pc.BaseURL == "" {
		pc.BaseURL = p.BaseURL
	}
	if pc.Model == "" {
		pc.Model = p.Model
	}
	return p, pc
}

// Set updates the active provider settings.
func (c *Config) Set(id string, pc ProviderConfig) {
	c.Provider = id
	if pc.APIKey == "" && c.Providers[id].APIKey != "" {
		if c.cleared == nil {
			c.cleared = map[string]bool{}
		}
		c.cleared[id] = true
	}
	c.Providers[id] = pc
}

// ConnectionEnabled reports whether the assistant may see a connection.
func (c *Config) ConnectionEnabled(id string) bool {
	return !slices.Contains(c.DisabledConnections, id)
}

// SetConnectionEnabled toggles the assistant for a connection.
func (c *Config) SetConnectionEnabled(id string, on bool) {
	c.DisabledConnections = slices.DeleteFunc(c.DisabledConnections, func(s string) bool { return s == id })
	if !on {
		c.DisabledConnections = append(c.DisabledConnections, id)
	}
}

// KeySource describes where the API key comes from (for the UI).
func KeySource(p Preset, pc ProviderConfig) string {
	switch {
	case pc.APIKey != "":
		return "key in the system keyring"
	case p.KeyEnv != "" && os.Getenv(p.KeyEnv) != "":
		return "$" + p.KeyEnv
	case p.KeyEnv == "":
		return "no key needed"
	default:
		return "missing — set $" + p.KeyEnv
	}
}

// Build creates the Provider for the active configuration.
func (c *Config) Build() (Provider, string, error) {
	p, pc := c.Current()
	key := pc.APIKey
	if key == "" && p.KeyEnv != "" {
		key = os.Getenv(p.KeyEnv)
	}
	if p.Kind == KindOpenAI && pc.Model == "" {
		return nil, "", fmt.Errorf("choose a model for %s", p.Name)
	}
	if !p.Local && p.KeyEnv != "" && key == "" && p.Kind == KindOpenAI {
		return nil, "", fmt.Errorf("%s needs an API key: set $%s or store one in settings", p.Name, p.KeyEnv)
	}
	id := p.ID + "|" + pc.BaseURL + "|" + pc.Model
	switch p.Kind {
	case KindAnthropic:
		return NewAnthropic(id, key, pc.BaseURL), pc.Model, nil
	default:
		return NewOpenAICompat(id, key, pc.BaseURL), pc.Model, nil
	}
}
