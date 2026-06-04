package main

import (
	"errors"
	"testing"
)

// TestLookupModel_AliasResolvesToCanonical pins the alias table: the friendly
// name a user types resolves to the full model id sent on the wire, and carries
// the right backend + default edit format (upstream MODEL_ALIASES + ModelSettings).
func TestLookupModel_AliasResolvesToCanonical(t *testing.T) {
	cfg, err := LookupModel("sonnet")
	if err != nil {
		t.Fatalf("sonnet should resolve: %v", err)
	}
	if cfg.Name != "claude-sonnet-4-6" {
		t.Fatalf("alias sonnet -> %q, want claude-sonnet-4-6", cfg.Name)
	}
	if cfg.Backend != backendAnthropic {
		t.Fatalf("sonnet backend = %q, want anthropic", cfg.Backend)
	}
	if cfg.DefaultFormat != FormatDiff {
		t.Fatalf("sonnet default format = %q, want diff", cfg.DefaultFormat)
	}
}

// TestLookupModel_Defaults checks per-model defaults differ as configured: a big
// model defaults to SEARCH/REPLACE, a small one to whole-file, and extra params
// ride along where set.
func TestLookupModel_Defaults(t *testing.T) {
	haiku, err := LookupModel("haiku")
	if err != nil {
		t.Fatalf("haiku: %v", err)
	}
	if haiku.DefaultFormat != FormatWhole {
		t.Fatalf("haiku default format = %q, want whole", haiku.DefaultFormat)
	}
	if haiku.ContextWindow != 200_000 {
		t.Fatalf("haiku context window = %d, want 200000", haiku.ContextWindow)
	}

	ds, err := LookupModel("deepseek")
	if err != nil {
		t.Fatalf("deepseek: %v", err)
	}
	if ds.Backend != backendOpenAI {
		t.Fatalf("deepseek backend = %q, want openai", ds.Backend)
	}
	if ds.BaseURL == "" {
		t.Fatal("deepseek must carry an OpenAI-compatible base URL")
	}
	if temp, ok := ds.ExtraParams["temperature"]; !ok || temp != 0.0 {
		t.Fatalf("deepseek extra param temperature = %v (ok=%v), want 0.0", temp, ok)
	}
}

// TestLookupModel_CanonicalIDAlsoWorks: passing the full id (not just an alias)
// resolves too — users may type either.
func TestLookupModel_CanonicalIDAlsoWorks(t *testing.T) {
	cfg, err := LookupModel("gpt-4o")
	if err != nil {
		t.Fatalf("canonical id gpt-4o should resolve: %v", err)
	}
	if cfg.Name != "gpt-4o" || cfg.Backend != backendOpenAI {
		t.Fatalf("gpt-4o resolved wrong: %+v", cfg)
	}
}

// TestLookupModel_ProviderSlashTail accepts a litellm-style "provider/model".
func TestLookupModel_ProviderSlashTail(t *testing.T) {
	cfg, err := LookupModel("deepseek/deepseek-chat")
	if err != nil {
		t.Fatalf("provider/model tail should resolve: %v", err)
	}
	if cfg.Name != "deepseek-chat" {
		t.Fatalf("got %q, want deepseek-chat", cfg.Name)
	}
}

// TestLookupModel_Unknown returns a typed *UnknownModelError whose message lists
// the known names, so the CLI can guide the user.
func TestLookupModel_Unknown(t *testing.T) {
	_, err := LookupModel("gpt-9-ultra")
	if err == nil {
		t.Fatal("unknown model must error")
	}
	var ue *UnknownModelError
	if !errors.As(err, &ue) {
		t.Fatalf("error type = %T, want *UnknownModelError", err)
	}
	if ue.Name != "gpt-9-ultra" {
		t.Fatalf("error carried name %q", ue.Name)
	}
	// Empty input is also unknown, not a panic.
	if _, err := LookupModel("   "); err == nil {
		t.Fatal("blank model must error")
	}
}
