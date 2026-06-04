package main

// main.go — CLI entry point for s02.
//
// Mirrors a tiny slice of aider/main.py: parse flags, build a Provider, build a
// Coder, run one cycle. The change from s01 is in the LOOP shape: s01 took ONE
// file and applied ONE edit. s02 takes MANY files, sends them all to the model,
// and applies every whole-file edit the reply contains — because the whole-file
// format is inherently multi-file.
//
// Usage:  s02 [flags] <file...> <instruction>
//   e.g.  s02 main.go util.go "extract the parser into util.go"

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
)

// providerProfiles are convenient shortcuts for popular endpoints. Pass
// `-provider <name>` and we fill in the base URL + default model; override with
// `-base-url` and `-model`. Anthropic uses its native API; the rest are reached
// as OpenAI-compatible Anthropic gateways (s10 builds the real litellm-style
// registry — here a static map is enough to show the seam).
var providerProfiles = map[string]struct {
	BaseURL string
	Model   string
	APIKey  string // env var to read the key from
}{
	"anthropic":  {Model: "claude-sonnet-4-6", APIKey: "ANTHROPIC_API_KEY"},
	"deepseek":   {BaseURL: "https://api.deepseek.com/anthropic/v1/messages", Model: "deepseek-chat", APIKey: "DEEPSEEK_API_KEY"},
	"openrouter": {BaseURL: "https://openrouter.ai/api/v1/messages", Model: "anthropic/claude-sonnet-4-6", APIKey: "OPENROUTER_API_KEY"},
	"local":      {BaseURL: "http://localhost:8000/v1/messages", Model: "local-model", APIKey: "ANTHROPIC_API_KEY"},
}

func main() {
	verbose := flag.Bool("v", false, "print the request/response shape to stderr")
	provider := flag.String("provider", envOr("PROVIDER", "anthropic"),
		"provider profile: anthropic | deepseek | openrouter | local")
	baseURL := flag.String("base-url", envOr("BASE_URL", ""),
		"override the messages endpoint URL")
	modelFlag := flag.String("model", envOr("MODEL", ""),
		"override the model id (defaults to the provider profile's default)")
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(),
			"usage: s02 [-v] [-provider P] [-base-url URL] [-model ID] <file...> <instruction>\n\n"+
				"  Read one or more <file>s, ask the model to rewrite them per the final\n"+
				"  <instruction>, and write back every whole-file edit in the reply.\n\n"+
				"  Provider profiles (set the matching API key env var first):\n"+
				"    anthropic  -> ANTHROPIC_API_KEY     (Claude, native API)\n"+
				"    deepseek   -> DEEPSEEK_API_KEY      (Anthropic-compatible endpoint)\n"+
				"    openrouter -> OPENROUTER_API_KEY    (any Claude model on OpenRouter)\n"+
				"    local      -> http://localhost:8000 (vLLM/SGLang Anthropic shim)\n\n"+
				"  Examples:\n"+
				"    s02 hello.go \"add a doc comment to main\"\n"+
				"    s02 -v main.go util.go \"move the parser into util.go\"\n")
	}
	flag.Parse()

	// Positional args: <file...> <instruction>. The LAST arg is the instruction;
	// everything before it is a file to put in chat. (We require >=2 args so
	// there is at least one file and one instruction.)
	if flag.NArg() < 2 {
		flag.Usage()
		os.Exit(2)
	}
	files := flag.Args()[:flag.NArg()-1]
	instruction := flag.Arg(flag.NArg() - 1)

	prof, ok := providerProfiles[*provider]
	if !ok {
		log.Fatalf("unknown -provider %q", *provider)
	}
	apiKey := os.Getenv(prof.APIKey)
	if apiKey == "" {
		log.Fatalf("%s is not set (required by -provider=%s)", prof.APIKey, *provider)
	}
	model := *modelFlag
	if model == "" {
		model = prof.Model
	}
	url := *baseURL
	if url == "" {
		url = prof.BaseURL
	}

	var p Provider
	if *provider == "anthropic" {
		p = NewAnthropicProvider(apiKey, model)
	} else {
		p = NewOpenAICompatProvider(apiKey, model, url)
	}
	if *verbose {
		fmt.Fprintf(os.Stderr, "[s02] provider=%s model=%s files=%v\n", *provider, model, files)
	}

	if err := run(context.Background(), p, files, instruction, *verbose); err != nil {
		log.Fatalf("coder error: %v", err)
	}
}

// run is the s02 loop: read every chat file, build one prompt carrying all of
// them, call the provider once, parse ALL whole-file edits, and write each one.
// Compare s01.Coder.Run: same five beats, but step 4 now yields many edits and
// step 5 iterates instead of writing a single file.
func run(ctx context.Context, p Provider, files []string, instruction string, verbose bool) error {
	// The chat-file set is just the basenames the user passed; the coder roots
	// relative paths at the current directory.
	chatFiles := make([]string, len(files))
	for i, f := range files {
		chatFiles[i] = f
	}
	coder := NewWholeFileCoder(".", chatFiles)

	// 1. READ every file the user put in chat so the model can rewrite them.
	var prompt strings.Builder
	prompt.WriteString("Files in chat:\n\n")
	for _, f := range files {
		content, err := os.ReadFile(f)
		if err != nil {
			return fmt.Errorf("read %s: %w", f, err)
		}
		fmt.Fprintf(&prompt, "%s\n```\n%s\n```\n\n", f, string(content))
	}
	fmt.Fprintf(&prompt, "Instruction: %s", instruction)

	// 2. BUILD the request. System prompt = the whole-file contract.
	req := CreateMessageRequest{
		System: coder.SystemPrompt(coder.fence),
		Messages: []Message{{
			Role:    "user",
			Content: []ContentBlock{{Type: "text", Text: prompt.String()}},
		}},
	}

	// 3. CALL the provider — one round-trip for all files.
	resp, err := p.CreateMessage(ctx, req)
	if err != nil {
		return fmt.Errorf("provider call: %w", err)
	}
	reply := firstText(resp.Content)
	if verbose {
		fmt.Fprintf(os.Stderr, "[s02] stop_reason=%s in=%d out=%d tokens\n",
			resp.StopReason, resp.Usage.InputTokens, resp.Usage.OutputTokens)
	}

	// 4. EXTRACT every whole-file edit (this is the chapter's mechanism).
	edits, err := coder.GetEdits(reply)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[s02] could not parse edits from the reply:\n%s\n", reply)
		return err
	}
	if len(edits) == 0 {
		fmt.Fprintf(os.Stderr, "[s02] no edits found in the reply:\n%s\n", reply)
		return fmt.Errorf("no edits parsed")
	}

	// 5. APPLY them all. ApplyEdits returns applied vs. failed for reporting.
	applied, failed, err := coder.ApplyEdits(edits)
	for _, e := range applied {
		fmt.Printf("Applied edit to %s (%d bytes)\n", filepath.Clean(e.Path), len(e.Replace))
	}
	for _, e := range failed {
		fmt.Fprintf(os.Stderr, "Failed to apply edit to %s\n", e.Path)
	}
	return err
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
