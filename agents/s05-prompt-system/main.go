package main

// main.go — CLI entry point for the prompt system.
//
// s02..s04 each had a hardcoded systemPrompt() function buried in their loop.
// s05 pulls those out into prompts.go and drives the choice from a flag. So this
// CLI has two jobs:
//
//   1. INSPECT (default): print the fully-assembled system prompt + few-shot
//      example turns for a chosen -format. This is the "show me what actually
//      steers the model" mode — no network, no API key. It's how you SEE that
//      the editblock prompt contains <<<<<<< SEARCH and the wholefile one does
//      not, which is exactly why s03 and s02 behave differently.
//
//   2. RUN (-file + -instruction): build a Provider, render the prompt for the
//      chosen format, prepend the example turns, send one round-trip, and print
//      the reply. This shows the prompt doing its job end to end.
//
// Usage:
//   s05 -format diff                      # print the diff system prompt
//   s05 -format whole -examples           # also print the few-shot turns
//   s05 -list                             # list known formats
//   s05 -format diff -file x.go -instruction "..."   # actually call the LLM
//
// The deliberate contrast with s03: there, switching format meant editing code.
// Here, switching format means changing one flag — the loop never changes.

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
)

// providerProfiles are convenient shortcuts for popular OpenAI-compatible
// endpoints (and Anthropic's native API). Pass `-provider <name>` and we fill in
// the base URL and a default model; override with `-base-url` and `-model`.
var providerProfiles = map[string]struct {
	BaseURL string
	Model   string
	APIKey  string // env var to read the key from
}{
	"anthropic":  {Model: "claude-sonnet-4-6", APIKey: "ANTHROPIC_API_KEY"},
	"openai":     {BaseURL: "https://api.openai.com/v1", Model: "gpt-4o-mini", APIKey: "OPENAI_API_KEY"},
	"deepseek":   {BaseURL: "https://api.deepseek.com/v1", Model: "deepseek-chat", APIKey: "DEEPSEEK_API_KEY"},
	"moonshot":   {BaseURL: "https://api.moonshot.cn/v1", Model: "moonshot-v1-8k", APIKey: "MOONSHOT_API_KEY"},
	"qwen":       {BaseURL: "https://dashscope.aliyuncs.com/compatible-mode/v1", Model: "qwen-plus", APIKey: "DASHSCOPE_API_KEY"},
	"groq":       {BaseURL: "https://api.groq.com/openai/v1", Model: "llama-3.3-70b-versatile", APIKey: "GROQ_API_KEY"},
	"openrouter": {BaseURL: "https://openrouter.ai/api/v1", Model: "openai/gpt-4o-mini", APIKey: "OPENROUTER_API_KEY"},
	"local":      {BaseURL: "http://localhost:8000/v1", Model: "local-model", APIKey: "OPENAI_API_KEY"},
}

func main() {
	format := flag.String("format", "diff", "edit format whose prompt to use: whole | diff | udiff")
	examples := flag.Bool("examples", false, "also print the few-shot example messages (inspect mode)")
	list := flag.Bool("list", false, "list known edit formats and exit")
	noLazy := flag.Bool("no-lazy", false, "omit the lazy ('finish the code') reminder")
	overeager := flag.Bool("overeager", false, "add the overeager ('do only what's asked') reminder")

	file := flag.String("file", "", "RUN mode: file to edit (requires -instruction)")
	instruction := flag.String("instruction", "", "RUN mode: change to request")
	provider := flag.String("provider", envOr("PROVIDER", "anthropic"),
		"provider profile: anthropic | openai | deepseek | moonshot | qwen | groq | openrouter | local")
	baseURL := flag.String("base-url", envOr("BASE_URL", ""), "override the OpenAI-compatible base URL")
	modelFlag := flag.String("model", envOr("MODEL", ""), "override the model id")
	verbose := flag.Bool("v", false, "print the request/response shape to stderr")

	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(),
			"usage: s05 [-format whole|diff|udiff] [-examples] [-list]\n"+
				"       s05 -format F -file PATH -instruction TEXT [provider flags]\n\n"+
				"  INSPECT (default): assemble and print the system prompt for -format.\n"+
				"  RUN: with -file and -instruction, render the prompt and call the LLM once.\n\n"+
				"  Examples:\n"+
				"    s05 -format diff                 # print the SEARCH/REPLACE system prompt\n"+
				"    s05 -format whole -examples      # whole-file prompt + few-shot turns\n"+
				"    s05 -list                        # list known formats\n"+
				"    s05 -format diff -file x.go -instruction \"rename foo to bar\"\n")
	}
	flag.Parse()

	if *list {
		fmt.Println("Known edit formats (each has its own prompt set):")
		for _, f := range KnownFormats() {
			fmt.Printf("  %-6s %s\n", f, formatBlurb(f))
		}
		return
	}

	ef := EditFormat(*format)
	vars := DefaultVars()
	vars.Lazy = !*noLazy
	vars.Overeager = *overeager

	// RUN mode: -file + -instruction => actually call the model with this prompt.
	if *file != "" || *instruction != "" {
		if *file == "" || *instruction == "" {
			log.Fatal("RUN mode needs BOTH -file and -instruction")
		}
		runWithPrompt(ef, vars, *provider, *baseURL, *modelFlag, *file, *instruction, *verbose)
		return
	}

	// INSPECT mode (default): just assemble and print the prompt. No network.
	sys, err := SystemPrompt(ef, vars)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("===== SYSTEM PROMPT (format=%s) =====\n", ef)
	fmt.Println(sys)
	if *examples {
		p, _ := PromptsFor(ef) // ef already validated by SystemPrompt above
		fmt.Printf("\n===== FEW-SHOT EXAMPLE MESSAGES (format=%s) =====\n", ef)
		for _, m := range p.RenderExamples(vars) {
			fmt.Printf("\n--- %s ---\n%s\n", strings.ToUpper(m.Role), firstText(m.Content))
		}
	}
}

// runWithPrompt renders the prompt for `format`, prepends the few-shot examples,
// sends one round-trip, and prints the reply. This is s02..s04's loop with the
// prompt now SELECTED instead of hardcoded — note how nothing below branches on
// the format; prompts.go absorbed that.
func runWithPrompt(format EditFormat, vars PromptVars, provider, baseURL, modelFlag, file, instruction string, verbose bool) {
	p, err := PromptsFor(format)
	if err != nil {
		log.Fatal(err)
	}

	prof, ok := providerProfiles[provider]
	if !ok {
		log.Fatalf("unknown -provider %q", provider)
	}
	apiKey := os.Getenv(prof.APIKey)
	if apiKey == "" {
		log.Fatalf("%s is not set (required by -provider=%s)", prof.APIKey, provider)
	}
	model := modelFlag
	if model == "" {
		model = prof.Model
	}
	url := baseURL
	if url == "" {
		url = prof.BaseURL
	}

	var prov Provider
	switch provider {
	case "anthropic":
		prov = NewAnthropicProvider(apiKey, model)
	default:
		prov = NewOpenAIProvider(apiKey, url, model)
	}

	contents, err := os.ReadFile(file)
	if err != nil {
		log.Fatalf("read %s: %v", file, err)
	}

	// Assemble the request: rendered system prompt + few-shot examples + the
	// real user turn. The examples come FIRST so the model reads them as prior
	// conversation, exactly as upstream format_chat_chunks does.
	msgs := p.RenderExamples(vars)
	msgs = append(msgs, Message{
		Role: "user",
		Content: []ContentBlock{{
			Type: "text",
			Text: fmt.Sprintf("File: %s\n\n%s%s%s\n\nInstruction: %s",
				file, vars.Fence[0]+"\n", string(contents), "\n"+vars.Fence[1], instruction),
		}},
	})
	req := CreateMessageRequest{System: p.Render(vars), Messages: msgs}

	if verbose {
		fmt.Fprintf(os.Stderr, "[s05] format=%s provider=%s model=%s\n", format, provider, model)
		fmt.Fprintf(os.Stderr, "[s05] system prompt: %d bytes, %d few-shot turns\n",
			len(req.System), len(msgs)-1)
	}

	resp, err := prov.CreateMessage(context.Background(), req)
	if err != nil {
		log.Fatalf("provider call: %v", err)
	}
	if verbose {
		fmt.Fprintf(os.Stderr, "[s05] stop_reason=%s in=%d out=%d tokens\n",
			resp.StopReason, resp.Usage.InputTokens, resp.Usage.OutputTokens)
	}
	fmt.Println(firstText(resp.Content))
	fmt.Fprintln(os.Stderr,
		"\n[s05] ^ the model replied in the format the prompt requested; feed this to the\n"+
			"      matching s02/s03/s04 parser to apply it.")
}

// ---- small helpers ----

func formatBlurb(f EditFormat) string {
	switch f {
	case FormatWhole:
		return "return the entire updated file (s02)"
	case FormatDiff:
		return "SEARCH/REPLACE blocks (s03, aider default)"
	case FormatUDiff:
		return "unified diff hunks (s04)"
	default:
		return ""
	}
}

// firstText returns the concatenation of all text blocks in a content list.
func firstText(content []ContentBlock) string {
	var sb strings.Builder
	for _, b := range content {
		if b.Type == "text" {
			sb.WriteString(b.Text)
		}
	}
	return sb.String()
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
