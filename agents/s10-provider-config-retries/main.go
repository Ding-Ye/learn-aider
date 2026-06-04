package main

// main.go — CLI entry point for s10.
//
// Mirrors the model-selection slice of aider/main.py + models.py: take a
// `-model` name, resolve it through the registry (alias -> canonical id ->
// config), build the matching Provider, wrap it in retries, and (optionally)
// stream the reply. The point of this chapter is that ALL of that is driven by a
// ModelConfig row — selecting deepseek vs sonnet vs a local model changes data,
// not control flow.
//
// Usage:  s10 [flags] [instruction]
//   e.g.  s10 -model sonnet "say hello"
//         s10 -list
//         s10 -model deepseek -stream "explain goroutines in one line"

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
)

func main() {
	model := flag.String("model", envOr("MODEL", "sonnet"),
		"model name or alias (try -list to see them all)")
	stream := flag.Bool("stream", false, "stream the reply token-by-token (Anthropic backend)")
	list := flag.Bool("list", false, "list known models/aliases and exit")
	verbose := flag.Bool("v", false, "print the resolved config + retry shape to stderr")
	noRetry := flag.Bool("no-retry", false, "disable the retry decorator")
	maxTokens := flag.Int("max-tokens", 1024, "max output tokens")
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(),
			"usage: s10 [-model NAME] [-stream] [-v] [instruction]\n\n"+
				"  Resolve NAME through the model registry, build the matching provider,\n"+
				"  wrap it in retries, and send one message.\n\n"+
				"  -list                 show every model id and alias\n"+
				"  -model sonnet         alias -> claude-sonnet-4-6 (Anthropic native)\n"+
				"  -model deepseek       alias -> deepseek-chat (OpenAI-compatible)\n"+
				"  -stream               live tokens (Anthropic backend only)\n\n"+
				"  Set the API key the chosen model needs, e.g. ANTHROPIC_API_KEY,\n"+
				"  OPENAI_API_KEY, DEEPSEEK_API_KEY, DASHSCOPE_API_KEY, ...\n")
	}
	flag.Parse()

	if *list {
		printModels(os.Stdout)
		return
	}

	cfg, err := LookupModel(*model)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}

	if *verbose {
		fmt.Fprintf(os.Stderr,
			"[s10] model=%s backend=%s url=%s ctx=%d format=%s stream=%v\n",
			cfg.Name, cfg.Backend, orDash(cfg.BaseURL), cfg.ContextWindow, cfg.DefaultFormat, *stream && cfg.Streaming)
	}

	apiKey := os.Getenv(cfg.APIKeyEnv)
	if apiKey == "" {
		fmt.Fprintf(os.Stderr, "%s is not set (required by model %q)\n", cfg.APIKeyEnv, cfg.Name)
		os.Exit(1)
	}

	// Build the concrete provider from the config, then wrap it. The retry
	// decorator and the base provider both satisfy Provider, so the rest of the
	// program is oblivious to which backend or whether retries are on.
	base := buildProvider(cfg, apiKey)
	var provider Provider = base
	if !*noRetry {
		rc := DefaultRetryConfig()
		provider = NewRetryProvider(base, rc)
		if *verbose {
			fmt.Fprintf(os.Stderr, "[s10] retries on: base=%s cap=%s maxRetries=%d\n",
				rc.BaseDelay, rc.MaxDelay, rc.MaxRetries)
		}
	}

	instruction := strings.TrimSpace(strings.Join(flag.Args(), " "))
	if instruction == "" {
		instruction = "Reply with a single short sentence confirming you are online."
	}

	req := CreateMessageRequest{
		Model:     cfg.Name,
		MaxTokens: *maxTokens,
		Messages: []Message{
			{Role: "user", Content: []ContentBlock{{Type: "text", Text: instruction}}},
		},
	}

	ctx := context.Background()
	var resp *CreateMessageResponse

	// Streaming path: only the Anthropic backend implements StreamingProvider in
	// s10. We type-assert through whatever wrappers are in front (RetryProvider
	// only forwards unary), so streaming uses the bare base provider.
	if *stream && cfg.Streaming {
		if sp, ok := base.(StreamingProvider); ok {
			resp, err = sp.StreamMessage(ctx, req, func(tok string) {
				fmt.Print(tok) // live output
			})
			fmt.Println()
		} else {
			fmt.Fprintln(os.Stderr, "[s10] this backend has no streaming; falling back to a unary call")
			resp, err = provider.CreateMessage(ctx, req)
			if err == nil {
				fmt.Println(firstText(resp.Content))
			}
		}
	} else {
		resp, err = provider.CreateMessage(ctx, req)
		if err == nil {
			fmt.Println(firstText(resp.Content))
		}
	}

	if err != nil {
		fmt.Fprintf(os.Stderr, "request failed: %v\n", err)
		os.Exit(1)
	}
	if *verbose && resp != nil {
		fmt.Fprintf(os.Stderr, "[s10] stop_reason=%s in=%d out=%d tokens\n",
			resp.StopReason, resp.Usage.InputTokens, resp.Usage.OutputTokens)
	}
}

// buildProvider is the registry's payoff: a ModelConfig row -> a concrete
// Provider. This is the only place that knows about specific backend types.
func buildProvider(cfg ModelConfig, apiKey string) Provider {
	switch cfg.Backend {
	case backendAnthropic:
		return NewAnthropicProvider(apiKey, cfg.Name)
	default: // backendOpenAI and anything OpenAI-compatible
		return NewOpenAIProvider(apiKey, cfg.BaseURL, cfg.Name)
	}
}

func printModels(w *os.File) {
	fmt.Fprintln(w, "Known models and aliases (resolve with -model):")
	for _, name := range knownNames() {
		if cfg, ok := registry[name]; ok {
			fmt.Fprintf(w, "  %-26s backend=%-9s ctx=%-7d format=%s\n",
				name, cfg.Backend, cfg.ContextWindow, cfg.DefaultFormat)
		} else if canon, ok := aliases[name]; ok {
			fmt.Fprintf(w, "  %-26s -> %s\n", name, canon)
		}
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
