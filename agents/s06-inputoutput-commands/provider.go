package main

// provider.go — the shared type catalog (canonical, from .learn/plan.md).
//
// Every session (s01..s10) re-pastes these same types verbatim and extends them,
// so each chapter is a self-contained Go module with NO cross-session imports.
// The shape is Anthropic's Messages API wire format: a clean tagged-union model
// that maps onto every other provider.
//
// s06 teaches the InputOutput layer + in-chat slash commands — the user-facing
// shell that wraps the coder loop. Crucially, that shell lives ENTIRELY ABOVE the
// Provider: a /add or /drop never touches the network, and a plain chat line is
// the only thing that ever becomes a Message and flows to CreateMessage. So these
// transport types are unchanged from s01..s05 and are carried here only so the
// module compiles on its own; the new mechanism is in io.go + commands.go. The
// one connecting idea: Commands mutate the in-chat file set, and THAT set is what
// a later chapter turns into the file-content Messages sent to the Provider.

import "context"

// ---- Generic LLM core (Anthropic wire shape) ----

// Message is a single turn in the conversation. The API takes a list of these
// (alternating user / assistant) plus an optional system prompt. In s06 only the
// non-command user input ever becomes a Message; commands are intercepted first.
type Message struct {
	Role    string         `json:"role"`
	Content []ContentBlock `json:"content"`
}

// ContentBlock is one item inside a message. The wire format is a tagged union
// over Type ("text" | "tool_use" | "tool_result"); the JSON encoder relies on
// `omitempty` to suppress fields that don't apply to a given block type. We carry
// the full union for catalog fidelity even though s06 only uses Type=="text".
type ContentBlock struct {
	Type  string                 `json:"type"`
	Text  string                 `json:"text,omitempty"`
	ID    string                 `json:"id,omitempty"`
	Name  string                 `json:"name,omitempty"`
	Input map[string]interface{} `json:"input,omitempty"`

	ToolUseID   string      `json:"tool_use_id,omitempty"`
	ToolContent interface{} `json:"content,omitempty"`
}

// ToolSchema describes a callable tool to the model. Carried for catalog
// fidelity; aider steers the model with a system prompt rather than tools.
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

// Provider abstracts the single LLM call. This is litellm's job upstream. s06's
// interactive shell sits entirely above this interface — it never calls it
// directly — which is exactly why this chapter needs no network and no API key.
type Provider interface {
	CreateMessage(ctx context.Context, req CreateMessageRequest) (*CreateMessageResponse, error)
}

// Tool is the contract for built-in tools (bash, edit-file). Carried for
// catalog fidelity; unused by s06.
type Tool interface {
	Schema() ToolSchema
	Execute(ctx context.Context, input map[string]interface{}) (string, error)
}

// ---- aider-specific shared types (canonical, from .learn/plan.md) ----

// EditFormat names the strategy the LLM is told to emit (upstream
// Coder.edit_format). Carried here so the module matches the catalog; s06 prints
// it in the prompt prefix (e.g. "diff> ") exactly as upstream get_input does.
type EditFormat string

const (
	FormatWhole EditFormat = "whole" // wholefile_coder.py  (s02)
	FormatDiff  EditFormat = "diff"  // editblock_coder.py  (s03)
	FormatUDiff EditFormat = "udiff" // udiff_coder.py      (s04)
)

// Edit is one parsed file change (upstream tuples: (path, original, updated)).
// Carried for catalog fidelity; s06 manages WHICH files are in scope, not how
// they are edited.
type Edit struct {
	Path    string
	Search  string // "" means whole-file replace / create-file
	Replace string
}
