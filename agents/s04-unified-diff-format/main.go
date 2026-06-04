package main

// main.go — CLI entry point.
//
// Mirrors a tiny slice of aider/main.py: parse flags, build a Provider, build a
// Coder, run one cycle. The difference from s03 is the cycle's middle: the model
// is steered to emit a ```diff block of unified-diff hunks (the "udiff" edit
// format), which GetEdits parses into hunks and ApplyEdits applies by locating
// each hunk's context — ignoring the unreliable @@ line numbers.
//
// Usage:  s04 [flags] <file> <instruction>
//   e.g.  s04 hello.go "rename the greeting to 'hi'"
//
// You pass ONE file on the CLI for convenience, but the model may emit hunks for
// any path (resolved under -root); applied/failed are reported per hunk.

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
	verbose := flag.Bool("v", false, "print the request/response shape to stderr")
	provider := flag.String("provider", envOr("PROVIDER", "anthropic"),
		"provider profile: anthropic | openai | deepseek | moonshot | qwen | groq | openrouter | local")
	baseURL := flag.String("base-url", envOr("BASE_URL", ""),
		"override the OpenAI-compatible base URL (e.g. http://localhost:8000/v1)")
	modelFlag := flag.String("model", envOr("MODEL", ""),
		"override the model id (defaults to the provider profile's default)")
	root := flag.String("root", ".", "directory that parsed file paths are resolved against")
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(),
			"usage: s04 [-v] [-provider P] [-base-url URL] [-model ID] [-root DIR] <file> <instruction>\n\n"+
				"  Read <file>, ask the model to edit it via a ```diff unified diff, apply the hunks.\n\n"+
				"  Provider profiles (set the matching API key env var first):\n"+
				"    anthropic  -> ANTHROPIC_API_KEY     (Claude, native API)\n"+
				"    openai     -> OPENAI_API_KEY        (gpt-4o-mini default)\n"+
				"    deepseek   -> DEEPSEEK_API_KEY      (deepseek-chat / deepseek-reasoner)\n"+
				"    moonshot   -> MOONSHOT_API_KEY      (Kimi / moonshot-v1-8k)\n"+
				"    qwen       -> DASHSCOPE_API_KEY     (Qwen via DashScope OpenAI-compat)\n"+
				"    groq       -> GROQ_API_KEY          (llama-3.3-70b default)\n"+
				"    openrouter -> OPENROUTER_API_KEY    (any model on OpenRouter)\n"+
				"    local      -> http://localhost:8000/v1 (vLLM/SGLang etc.)\n\n"+
				"  Examples:\n"+
				"    s04 hello.go \"rename the greeting to 'hi'\"\n"+
				"    s04 -v -provider deepseek hello.go \"add error handling to readConfig\"\n")
	}
	flag.Parse()

	if flag.NArg() < 2 {
		flag.Usage()
		os.Exit(2)
	}
	path := flag.Arg(0)
	instruction := strings.Join(flag.Args()[1:], " ")

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
	switch *provider {
	case "anthropic":
		p = NewAnthropicProvider(apiKey, model)
	default:
		p = NewOpenAIProvider(apiKey, url, model)
	}
	if *verbose {
		fmt.Fprintf(os.Stderr, "[s04] provider=%s model=%s url=%s\n", *provider, model, url)
	}

	coder := NewCoder(p, *root, *verbose)
	if err := coder.Run(context.Background(), path, instruction); err != nil {
		log.Fatalf("coder error: %v", err)
	}
}

// Run executes one read -> prompt -> call -> parse -> apply cycle for the
// unified-diff format. Compare s03's Run: the shape is identical, only the parser
// (GetEdits now yields hunks) and the application (locate by hunk context, not by
// an explicit SEARCH fence) changed.
func (c *Coder) Run(ctx context.Context, path, instruction string) error {
	// 1. READ the target file so the model can quote exact context into the hunk.
	original, err := os.ReadFile(c.absPath(path))
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}

	// 2. BUILD the prompt. The system prompt defines the unified-diff contract.
	req := CreateMessageRequest{
		System: uDiffSystemPrompt(),
		Messages: []Message{{
			Role: "user",
			Content: []ContentBlock{{
				Type: "text",
				Text: buildUserPrompt(path, string(original), instruction),
			}},
		}},
	}
	if c.Verbose {
		fmt.Fprintf(c.errw, "[s04] sending %d bytes of %s + instruction %q\n",
			len(original), path, instruction)
	}

	// 3. CALL the provider — the single network round-trip.
	resp, err := c.Provider.CreateMessage(ctx, req)
	if err != nil {
		return fmt.Errorf("provider call: %w", err)
	}
	reply := firstText(resp.Content)
	if c.Verbose {
		fmt.Fprintf(c.errw, "[s04] stop_reason=%s in=%d out=%d tokens\n",
			resp.StopReason, resp.Usage.InputTokens, resp.Usage.OutputTokens)
	}

	// 4. PARSE the unified-diff hunks out of the reply.
	hunks, err := c.GetEdits(reply)
	if err != nil {
		fmt.Fprintf(c.errw, "[s04] could not parse diff:\n%s\n", reply)
		return err
	}
	if len(hunks) == 0 {
		fmt.Fprintf(c.errw, "[s04] no ```diff hunks in the reply:\n%s\n", reply)
		return fmt.Errorf("no edits found")
	}

	// 5. APPLY them by locating each hunk's context; report what landed and what
	//    missed.
	applied, failed, err := c.ApplyEdits(hunks)
	if err != nil {
		return fmt.Errorf("apply edits: %w", err)
	}
	for _, h := range applied {
		fmt.Fprintf(c.out, "Applied hunk to %s\n", h.Path)
	}
	for _, h := range failed {
		fmt.Fprintf(c.errw, "FAILED to apply hunk to %s — its context did not match (file left untouched)\n", h.Path)
	}
	if len(failed) > 0 {
		// Upstream turns this into a reflected_message asking the model to retry
		// with corrected context (our s09). s04 just reports it.
		return fmt.Errorf("%d of %d hunks did not apply", len(failed), len(applied)+len(failed))
	}
	return nil
}

// ---- prompt construction ----

// uDiffSystemPrompt is the unified-diff format contract. It mirrors the
// load-bearing rules of aider's UnifiedDiffPrompts.main_system: return edits as a
// ```diff block; mark removed lines with `-` and added lines with `+`; include a
// few unchanged ` ` context lines so the hunk can be located; and DON'T worry
// about getting the @@ line numbers right (we ignore them). (s05 extracts these
// prompts into a per-format table.)
func uDiffSystemPrompt() string {
	return "You are a coding assistant that edits files using the unified diff format.\n" +
		"Return ALL changes as a single fenced ```diff block, like `git diff`:\n\n" +
		"```diff\n" +
		"--- a/path/to/file\n" +
		"+++ b/path/to/file\n" +
		"@@ ... @@\n" +
		" an unchanged context line (leading space)\n" +
		"-a line to remove (leading minus)\n" +
		"+a line to add (leading plus)\n" +
		" another context line\n" +
		"```\n\n" +
		"Rules:\n" +
		"- Mark each line: a leading space for context, `-` to remove, `+` to add.\n" +
		"- Include a few unchanged context lines around each change so the hunk can\n" +
		"  be located in the file. Quote them EXACTLY as they appear.\n" +
		"- You do NOT need correct `@@ -a,b +c,d @@` line numbers; `@@ ... @@` is\n" +
		"  fine. The hunk is located by its context lines, not the numbers.\n" +
		"- To create a new file, emit a hunk with only `+` lines and no context."
}

// buildUserPrompt packs the file contents and the instruction into one user turn.
func buildUserPrompt(path, contents, instruction string) string {
	return fmt.Sprintf("File: %s\n\n```\n%s\n```\n\nInstruction: %s", path, contents, instruction)
}

// firstText returns the concatenation of all text blocks in a response.
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
