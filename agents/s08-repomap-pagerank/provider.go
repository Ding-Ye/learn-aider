package main

// provider.go — the LLM transport layer + the shared type catalog.
//
// This is the curriculum's "generic agent core" (see .learn/plan.md). Every
// session (s01..s10) re-pastes these same types verbatim and extends them, so
// that each chapter is a self-contained Go module with NO cross-session imports.
// The shape is Anthropic's Messages API wire format: a clean tagged-union model
// that maps onto every other provider.
//
// Upstream aider hides all of this behind litellm (aider/llm.py,
// aider/models.py send_completion). We expose it directly so a learner can see
// exactly what bytes go over the wire.
//
// s08 changes NOTHING in the transport layer vs s01..s07. The new mechanism this
// chapter teaches lives entirely in repomap.go: the RepoMap — a ranked,
// token-budgeted summary of a whole repo's symbols, produced by running PageRank
// over a definition->reference graph. The connection to this file is that the
// RepoMap's output is injected as a read-only text block inside a Message before
// the loop ever calls the model: it is what lets the model "see" a codebase that
// is far too large to paste in full.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// ---- Generic LLM core (Anthropic wire shape) ----

// Message is a single turn in the conversation. The API takes a list of these
// (alternating user / assistant) plus an optional system prompt. s08's RepoMap
// renders into the Text of a read-only user turn prepended to the conversation.
type Message struct {
	Role    string         `json:"role"`
	Content []ContentBlock `json:"content"`
}

// ContentBlock is one item inside a message. The wire format is a tagged union
// over Type ("text" | "tool_use" | "tool_result"); the JSON encoder relies on
// `omitempty` to suppress fields that don't apply to a given block type. s08
// only ever uses Type=="text", but we carry the full union so the type matches
// the catalog verbatim and later sessions don't have to rewrite it.
type ContentBlock struct {
	Type  string                 `json:"type"`
	Text  string                 `json:"text,omitempty"`
	ID    string                 `json:"id,omitempty"`
	Name  string                 `json:"name,omitempty"`
	Input map[string]interface{} `json:"input,omitempty"`

	ToolUseID   string      `json:"tool_use_id,omitempty"`
	ToolContent interface{} `json:"content,omitempty"`
}

// ToolSchema describes a callable tool to the model. aider (and s08) steer the
// model with a system PROMPT and an injected repo map rather than tool calls, so
// this type is carried for catalog fidelity but unused by this chapter.
type ToolSchema struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	InputSchema map[string]interface{} `json:"input_schema"`
}

type CreateMessageRequest struct {
	Model     string       `json:"model"`
	MaxTokens int          `json:"max_tokens"`
	Messages  []Message    `json:"messages"`
	Tools     []ToolSchema `json:"tools,omitempty"`
	System    string       `json:"system,omitempty"`
	Stream    bool         `json:"stream,omitempty"`
}

type Usage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

type CreateMessageResponse struct {
	ID         string         `json:"id"`
	Role       string         `json:"role"`
	Content    []ContentBlock `json:"content"`
	StopReason string         `json:"stop_reason"`
	Usage      Usage          `json:"usage"`
}

// Provider abstracts the single LLM call. This is litellm's job upstream. s10
// will add retries + streaming + a model registry behind this exact interface,
// which is why even s08 programs against the interface rather than a concrete
// client (tests inject a fakeProvider).
type Provider interface {
	CreateMessage(ctx context.Context, req CreateMessageRequest) (*CreateMessageResponse, error)
}

// ---- AnthropicProvider: native /v1/messages ----

// AnthropicProvider talks to Anthropic's native /v1/messages endpoint. The wire
// format IS our internal format, so there is no translation to do here (compare
// OpenAIProvider below, which translates both ways).
type AnthropicProvider struct {
	apiKey string
	model  string
	client *http.Client
}

func NewAnthropicProvider(apiKey, model string) *AnthropicProvider {
	return &AnthropicProvider{
		apiKey: apiKey,
		model:  model,
		client: &http.Client{Timeout: 120 * time.Second},
	}
}

func (a *AnthropicProvider) CreateMessage(ctx context.Context, req CreateMessageRequest) (*CreateMessageResponse, error) {
	if req.Model == "" {
		req.Model = a.model
	}
	if req.MaxTokens == 0 {
		req.MaxTokens = 4096
	}

	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("encode request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST",
		"https://api.anthropic.com/v1/messages", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-api-key", a.apiKey)
	httpReq.Header.Set("anthropic-version", "2023-06-01")

	resp, err := a.client.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("anthropic API %d: %s", resp.StatusCode, string(respBody))
	}

	var out CreateMessageResponse
	if err := json.Unmarshal(respBody, &out); err != nil {
		return nil, fmt.Errorf("decode response: %w (body=%s)", err, string(respBody))
	}
	return &out, nil
}

// ---- OpenAIProvider: any OpenAI-compatible Chat Completions API ----

// OpenAIProvider works with any OpenAI-compatible endpoint (OpenAI, DeepSeek,
// Moonshot/Kimi, Qwen via DashScope, Groq, OpenRouter, self-hosted vLLM/SGLang).
// We keep the program's INTERNAL types Anthropic-shaped and translate at this
// boundary, so repomap.go and main.go are identical no matter which model the
// user picked.
type OpenAIProvider struct {
	apiKey  string
	baseURL string
	model   string
	client  *http.Client
}

func NewOpenAIProvider(apiKey, baseURL, model string) *OpenAIProvider {
	if baseURL == "" {
		baseURL = "https://api.openai.com/v1"
	}
	return &OpenAIProvider{
		apiKey:  apiKey,
		baseURL: strings.TrimRight(baseURL, "/"),
		model:   model,
		client:  &http.Client{Timeout: 120 * time.Second},
	}
}

func (o *OpenAIProvider) CreateMessage(ctx context.Context, req CreateMessageRequest) (*CreateMessageResponse, error) {
	model := req.Model
	if model == "" {
		model = o.model
	}
	maxTokens := req.MaxTokens
	if maxTokens == 0 {
		maxTokens = 4096
	}

	// Translate our Anthropic-shaped request into OpenAI chat messages.
	var msgs []map[string]interface{}
	if req.System != "" {
		msgs = append(msgs, map[string]interface{}{"role": "system", "content": req.System})
	}
	for _, m := range req.Messages {
		var sb strings.Builder
		for _, b := range m.Content {
			if b.Type == "text" {
				sb.WriteString(b.Text)
			}
		}
		msgs = append(msgs, map[string]interface{}{"role": m.Role, "content": sb.String()})
	}
	payload := map[string]interface{}{
		"model":      model,
		"messages":   msgs,
		"max_tokens": maxTokens,
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("encode openai request: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, "POST",
		o.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+o.apiKey)

	resp, err := o.client.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("openai-compat API %d: %s", resp.StatusCode, string(respBody))
	}

	var oaiResp struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(respBody, &oaiResp); err != nil {
		return nil, fmt.Errorf("decode openai response: %w (body=%s)", err, string(respBody))
	}

	out := &CreateMessageResponse{
		Role:       "assistant",
		StopReason: "end_turn",
		Usage: Usage{
			InputTokens:  oaiResp.Usage.PromptTokens,
			OutputTokens: oaiResp.Usage.CompletionTokens,
		},
	}
	if len(oaiResp.Choices) > 0 {
		out.Content = append(out.Content, ContentBlock{Type: "text", Text: oaiResp.Choices[0].Message.Content})
		if oaiResp.Choices[0].FinishReason == "length" {
			out.StopReason = "max_tokens"
		}
	}
	return out, nil
}

// ---- aider-specific shared types (canonical, from .learn/plan.md) ----

// EditFormat names the strategy the LLM is told to emit (upstream
// Coder.edit_format). Carried for catalog fidelity; s08 does not parse edits.
type EditFormat string

const (
	FormatWhole EditFormat = "whole" // wholefile_coder.py  (s02)
	FormatDiff  EditFormat = "diff"  // editblock_coder.py  (s03)
	FormatUDiff EditFormat = "udiff" // udiff_coder.py      (s04)
)

// Edit is one parsed file change. Carried for catalog fidelity; s08 produces a
// read-only repo map, not edits.
type Edit struct {
	Path    string
	Search  string // "" means whole-file replace / create-file
	Replace string
}

// Tag is one symbol extracted from a source file. Upstream defines it as a
// namedtuple "rel_fname fname line name kind" (repomap.py L29) populated by a
// tree-sitter query. s08 keeps the same five fields but fills them with a
// heuristic regex extractor (see repomap.go extractTags) instead of a real
// parser. Kind is "def" (a definition: func/type/const/var) or "ref" (a use).
type Tag struct {
	RelFname string // path relative to the repo root, used as the graph node id
	Fname    string // absolute path on disk
	Line     int    // 1-based line number of the symbol
	Name     string // the identifier
	Kind     string // "def" or "ref"
}
