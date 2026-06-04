package main

// main.go — CLI entry point for the linter + reflection loop.
//
// s01..s08 ended their loop at "apply the edit". s09 adds the step that makes
// aider a *pair programmer* instead of a one-shot code generator: after applying
// edits it lints the changed files, and if the linter complains it feeds the
// errors back to the model and lets it try again (bounded by -max-reflections).
//
// Two modes:
//
//   1. LINT (default, no network): -lint FILE [FILE...] runs just the Linter and
//      prints the report. This is "show me what the loop would reflect back" —
//      handy for seeing exactly what text the model receives. Try it on a Go
//      file with a syntax error.
//
//   2. RUN: -file PATH -instruction TEXT builds a Provider + a whole-file Coder +
//      the Linter, then runs the reflection loop. The model edits the file; if
//      the edit doesn't lint clean, it gets the errors and tries again, up to
//      the cap. Requires an API key.
//
// The demo Coder is the simplest one (whole-file, ported from s02's idea) so the
// chapter stays focused on the LOOP, not on edit-format parsing.

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
	lintPaths := flag.Bool("lint", false, "LINT mode: lint the FILE args and print the report, no LLM")
	file := flag.String("file", "", "RUN mode: file to edit (requires -instruction)")
	instruction := flag.String("instruction", "", "RUN mode: change to request")
	lintCmd := flag.String("lint-cmd", "", "override the lint command for the edited file's extension (e.g. \"go vet\")")
	maxReflections := flag.Int("max-reflections", DefaultMaxReflections, "max self-correction passes after the first try")
	provider := flag.String("provider", envOr("PROVIDER", "anthropic"),
		"provider profile: anthropic | openai | deepseek | moonshot | qwen | groq | openrouter | local")
	baseURL := flag.String("base-url", envOr("BASE_URL", ""), "override the OpenAI-compatible base URL")
	modelFlag := flag.String("model", envOr("MODEL", ""), "override the model id")
	verbose := flag.Bool("v", false, "print the loop's progress to stderr")

	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(),
			"usage: s09 -lint FILE [FILE...]\n"+
				"       s09 -file PATH -instruction TEXT [-max-reflections N] [provider flags]\n\n"+
				"  LINT (default): run only the Linter over the files and print the report.\n"+
				"  RUN: edit -file via the LLM, then lint+reflect until clean or the cap.\n\n"+
				"  Examples:\n"+
				"    s09 -lint broken.go                 # print what the loop would reflect\n"+
				"    s09 -file x.go -instruction \"add a divide func\"\n"+
				"    s09 -file x.go -instruction \"...\" -lint-cmd \"go vet\" -max-reflections 2\n")
	}
	flag.Parse()

	// LINT mode: just run the linter over the named files.
	if *lintPaths || (flag.NArg() > 0 && *file == "") {
		paths := flag.Args()
		if len(paths) == 0 {
			log.Fatal("LINT mode needs at least one FILE argument")
		}
		linter := NewLinter("")
		if *lintCmd != "" {
			linter.SetLinter("", *lintCmd)
		}
		text, ok := linter.Lint(paths)
		if ok {
			fmt.Println("lint clean: nothing to reflect")
			return
		}
		fmt.Print(text)
		if !strings.HasSuffix(text, "\n") {
			fmt.Println()
		}
		fmt.Fprintln(os.Stderr,
			"\n[s09] ^ in RUN mode this exact text becomes a synthetic user message,\n"+
				"      handed back to the model so it can fix the code.")
		os.Exit(1) // non-zero so scripts can tell lint failed
	}

	// RUN mode: -file + -instruction => drive the reflection loop.
	if *file == "" || *instruction == "" {
		flag.Usage()
		log.Fatal("RUN mode needs BOTH -file and -instruction (or use -lint FILE)")
	}
	runReflection(*provider, *baseURL, *modelFlag, *file, *instruction, *lintCmd, *maxReflections, *verbose)
}

// runReflection wires a Provider + whole-file Coder + Linter into the loop and
// runs it for one instruction. Note how the loop body in reflect.go never
// mentions a provider or a format — this function fills those holes and the loop
// stays generic.
func runReflection(provider, baseURL, modelFlag, file, instruction, lintCmd string, maxReflections int, verbose bool) {
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

	coder := NewWholeFileCoder(file)
	linter := NewLinter("")
	if lintCmd != "" {
		linter.SetLinter("", lintCmd)
	}

	// The SendFunc is the only provider-aware piece: build the request (system
	// prompt from the coder + the running conversation) and return the reply text.
	fence := [2]string{"```", "```"}
	send := func(ctx context.Context, messages []Message) (string, error) {
		req := CreateMessageRequest{System: coder.SystemPrompt(fence), Messages: messages}
		resp, err := prov.CreateMessage(ctx, req)
		if err != nil {
			return "", err
		}
		return replyText(resp.Content), nil
	}

	loop := NewReflectLoop(coder, linter, send)
	loop.MaxReflections = maxReflections

	if verbose {
		fmt.Fprintf(os.Stderr, "[s09] provider=%s model=%s file=%s max-reflections=%d\n",
			provider, model, file, maxReflections)
	}

	res, err := loop.Run(context.Background(), instruction)
	if err != nil {
		log.Fatalf("reflection loop: %v", err)
	}

	// Report what happened. The interesting number is Reflections: 0 means the
	// model nailed it first try; >0 means the linter caught a problem and the
	// model fixed it on a later pass.
	switch {
	case res.Clean && res.Reflections == 0:
		fmt.Fprintln(os.Stderr, "[s09] clean on the first try — no reflection needed.")
	case res.Clean:
		fmt.Fprintf(os.Stderr, "[s09] converged after %d reflection(s) — lint is now clean.\n", res.Reflections)
	case res.HitCap:
		fmt.Fprintf(os.Stderr, "[s09] stopped after %d reflection(s): cap reached, still not clean:\n%s\n",
			res.Reflections, res.LintText)
	}
	fmt.Printf("applied %d edit(s) to %s across %d pass(es)\n",
		len(res.Applied), file, len(res.Replies))
}

// ---- small helpers ----

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
