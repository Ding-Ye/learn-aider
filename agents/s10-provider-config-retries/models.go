package main

// models.go — the model/provider configuration layer.
//
// This is the registry that turns a short name a user types (`-model sonnet`)
// into everything the rest of the program needs: which backend speaks to it,
// what base URL, what context window, and which edit format that model is best
// driven with. Upstream aider calls this ModelSettings (aider/models.py L127)
// plus the MODEL_ALIASES map (L99) and resolves model metadata through litellm;
// we hardcode a small, readable registry instead of a 1300-line file.
//
// The key idea: configuration is DATA. Adding a backend is adding a row, not
// writing code. main.go builds a Provider from a ModelConfig; retries.go wraps
// whatever that build returns; nothing branches on the model name.

import (
	"fmt"
	"sort"
	"strings"
)

// EditFormat names the strategy the model is told to emit (upstream
// Coder.edit_format / ModelSettings.edit_format). Redeclared here because every
// session is a self-contained module.
type EditFormat string

const (
	FormatWhole EditFormat = "whole" // wholefile_coder.py — return the full file
	FormatDiff  EditFormat = "diff"  // editblock_coder.py — SEARCH/REPLACE blocks
	FormatUDiff EditFormat = "udiff" // udiff_coder.py — unified diff hunks
)

// backend selects the wire protocol / transport for a model.
type backend string

const (
	backendAnthropic backend = "anthropic" // native Messages API
	backendOpenAI    backend = "openai"    // any OpenAI-compatible Chat Completions API
)

// ModelConfig is one row of the registry: a model's identity plus the knobs the
// loop and the provider need. It is the Go analog of upstream's ModelSettings,
// trimmed to the load-bearing fields.
type ModelConfig struct {
	// Name is the canonical model id sent on the wire (e.g. "claude-sonnet-4-6").
	Name string
	// Backend picks the provider implementation (Anthropic native vs OpenAI-compat).
	Backend backend
	// BaseURL is the OpenAI-compatible endpoint; empty for the Anthropic backend.
	BaseURL string
	// APIKeyEnv is the environment variable the key is read from.
	APIKeyEnv string
	// ContextWindow is the model's max input tokens (used for budgeting upstream;
	// here it is metadata the CLI can print).
	ContextWindow int
	// DefaultFormat is the edit format this model is driven with by default
	// (upstream ModelSettings.edit_format).
	DefaultFormat EditFormat
	// ExtraParams are provider-specific defaults merged into every request
	// (upstream ModelSettings.extra_params). Kept as a generic bag.
	ExtraParams map[string]interface{}
	// Streaming reports whether the model supports SSE streaming
	// (upstream ModelSettings.streaming, default true).
	Streaming bool
}

// registry maps a canonical model id -> its config. This is the s10 analog of
// upstream's MODEL_SETTINGS list loaded from model-settings.yml.
var registry = map[string]ModelConfig{
	"claude-sonnet-4-6": {
		Name:          "claude-sonnet-4-6",
		Backend:       backendAnthropic,
		APIKeyEnv:     "ANTHROPIC_API_KEY",
		ContextWindow: 200_000,
		DefaultFormat: FormatDiff,
		Streaming:     true,
	},
	"claude-opus-4-7": {
		Name:          "claude-opus-4-7",
		Backend:       backendAnthropic,
		APIKeyEnv:     "ANTHROPIC_API_KEY",
		ContextWindow: 200_000,
		DefaultFormat: FormatDiff,
		Streaming:     true,
	},
	"claude-haiku-4-5": {
		Name:          "claude-haiku-4-5",
		Backend:       backendAnthropic,
		APIKeyEnv:     "ANTHROPIC_API_KEY",
		ContextWindow: 200_000,
		DefaultFormat: FormatWhole, // smaller model: whole-file is more reliable
		Streaming:     true,
	},
	"gpt-4o": {
		Name:          "gpt-4o",
		Backend:       backendOpenAI,
		BaseURL:       "https://api.openai.com/v1",
		APIKeyEnv:     "OPENAI_API_KEY",
		ContextWindow: 128_000,
		DefaultFormat: FormatDiff,
		Streaming:     true,
	},
	"gpt-4o-mini": {
		Name:          "gpt-4o-mini",
		Backend:       backendOpenAI,
		BaseURL:       "https://api.openai.com/v1",
		APIKeyEnv:     "OPENAI_API_KEY",
		ContextWindow: 128_000,
		DefaultFormat: FormatWhole,
		Streaming:     true,
	},
	"deepseek-chat": {
		Name:          "deepseek-chat",
		Backend:       backendOpenAI,
		BaseURL:       "https://api.deepseek.com/v1",
		APIKeyEnv:     "DEEPSEEK_API_KEY",
		ContextWindow: 64_000,
		DefaultFormat: FormatDiff,
		// DeepSeek's OpenAI-compat endpoint honors temperature; pin it low for
		// deterministic edits, mirroring aider's use_temperature default of 0.
		ExtraParams: map[string]interface{}{"temperature": 0.0},
		Streaming:   true,
	},
	"deepseek-reasoner": {
		Name:          "deepseek-reasoner",
		Backend:       backendOpenAI,
		BaseURL:       "https://api.deepseek.com/v1",
		APIKeyEnv:     "DEEPSEEK_API_KEY",
		ContextWindow: 64_000,
		DefaultFormat: FormatDiff,
		// R1 is a reasoning model: upstream disables streaming-temperature and
		// forces alternating roles. We keep the metadata; streaming stays on.
		Streaming: true,
	},
	"qwen-plus": {
		Name:          "qwen-plus",
		Backend:       backendOpenAI,
		BaseURL:       "https://dashscope.aliyuncs.com/compatible-mode/v1",
		APIKeyEnv:     "DASHSCOPE_API_KEY",
		ContextWindow: 131_072,
		DefaultFormat: FormatDiff,
		Streaming:     true,
	},
	"moonshot-v1-8k": {
		Name:          "moonshot-v1-8k",
		Backend:       backendOpenAI,
		BaseURL:       "https://api.moonshot.cn/v1",
		APIKeyEnv:     "MOONSHOT_API_KEY",
		ContextWindow: 8_192,
		DefaultFormat: FormatWhole,
		Streaming:     true,
	},
	"llama-3.3-70b-versatile": {
		Name:          "llama-3.3-70b-versatile",
		Backend:       backendOpenAI,
		BaseURL:       "https://api.groq.com/openai/v1",
		APIKeyEnv:     "GROQ_API_KEY",
		ContextWindow: 128_000,
		DefaultFormat: FormatWhole,
		Streaming:     true,
	},
}

// aliases maps a short, friendly name to a canonical model id, mirroring
// upstream's MODEL_ALIASES (aider/models.py L99). `-model sonnet` resolves here.
var aliases = map[string]string{
	"sonnet":   "claude-sonnet-4-6",
	"opus":     "claude-opus-4-7",
	"haiku":    "claude-haiku-4-5",
	"4o":       "gpt-4o",
	"4o-mini":  "gpt-4o-mini",
	"deepseek": "deepseek-chat",
	"r1":       "deepseek-reasoner",
	"qwen":     "qwen-plus",
	"kimi":     "moonshot-v1-8k",
	"moonshot": "moonshot-v1-8k",
	"groq":     "llama-3.3-70b-versatile",
}

// UnknownModelError is returned when a name resolves to no registered config.
// It lists the known names so the CLI can show a helpful message.
type UnknownModelError struct {
	Name string
}

func (e *UnknownModelError) Error() string {
	return fmt.Sprintf("unknown model %q; known: %s", e.Name, strings.Join(knownNames(), ", "))
}

// LookupModel resolves a user-supplied name (alias OR canonical id) to its
// ModelConfig. Resolution order: exact canonical id first, then alias, then a
// litellm-style "provider/model" tail (e.g. "deepseek/deepseek-chat" -> try
// "deepseek-chat"). Returns *UnknownModelError when nothing matches.
func LookupModel(name string) (ModelConfig, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return ModelConfig{}, &UnknownModelError{Name: name}
	}
	if cfg, ok := registry[name]; ok {
		return cfg, nil
	}
	if canon, ok := aliases[name]; ok {
		return registry[canon], nil // canon is guaranteed present by construction
	}
	// Accept "provider/model" by trying the part after the last slash, the way
	// upstream's get_model_info splits "litellm_provider/model".
	if i := strings.LastIndex(name, "/"); i >= 0 && i+1 < len(name) {
		if cfg, ok := registry[name[i+1:]]; ok {
			return cfg, nil
		}
	}
	return ModelConfig{}, &UnknownModelError{Name: name}
}

// knownNames returns the canonical ids plus aliases, sorted, for error messages
// and the -list CLI flag.
func knownNames() []string {
	seen := make(map[string]bool)
	var out []string
	for k := range registry {
		if !seen[k] {
			seen[k] = true
			out = append(out, k)
		}
	}
	for a := range aliases {
		if !seen[a] {
			seen[a] = true
			out = append(out, a)
		}
	}
	sort.Strings(out)
	return out
}
