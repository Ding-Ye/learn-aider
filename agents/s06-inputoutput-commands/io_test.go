package main

import (
	"strings"
	"testing"
)

// TestIOGetInput: the real IO reads lines from its reader in order and reports
// EOF (ok=false) when the stream is exhausted.
func TestIOGetInput(t *testing.T) {
	var out strings.Builder
	io := NewIO(strings.NewReader("first\nsecond\n"), &out, false)

	if line, ok := io.GetInput("> "); !ok || line != "first" {
		t.Fatalf("GetInput #1 = (%q, %v), want (\"first\", true)", line, ok)
	}
	if line, ok := io.GetInput("> "); !ok || line != "second" {
		t.Fatalf("GetInput #2 = (%q, %v), want (\"second\", true)", line, ok)
	}
	if _, ok := io.GetInput("> "); ok {
		t.Fatal("GetInput #3 should report EOF (ok=false)")
	}
	if !strings.Contains(out.String(), "> ") {
		t.Errorf("GetInput should write the prompt prefix, got %q", out.String())
	}
}

// TestConfirmAskYes: with --yes set, ConfirmAsk returns true WITHOUT reading the
// reader at all (upstream io.py L866). We prove the reader is untouched by giving
// it a "no" that must be ignored.
func TestConfirmAskYes(t *testing.T) {
	var out strings.Builder
	io := NewIO(strings.NewReader("no\n"), &out, true /* Yes */)
	if !io.ConfirmAsk("Create file?") {
		t.Error("with Yes=true, ConfirmAsk must return true regardless of input")
	}
	// The "no" line must still be available — it was never consumed.
	if line, ok := io.GetInput("> "); !ok || line != "no" {
		t.Errorf("ConfirmAsk(--yes) must not consume input; next line = (%q, %v)", line, ok)
	}
}

// TestConfirmAskReadsInput: without --yes, ConfirmAsk reads a line. Empty and
// "y"/"yes" mean yes; "n"/"no" mean no.
func TestConfirmAskReadsInput(t *testing.T) {
	cases := map[string]bool{
		"y\n":   true,
		"yes\n": true,
		"\n":    true, // empty == default (yes)
		"n\n":   false,
		"no\n":  false,
	}
	for input, want := range cases {
		var out strings.Builder
		io := NewIO(strings.NewReader(input), &out, false)
		if got := io.ConfirmAsk("ok?"); got != want {
			t.Errorf("ConfirmAsk with input %q = %v, want %v", input, got, want)
		}
	}
}

// TestToolOutputAndHistory: ToolOutput / ToolError print AND append to the chat
// history transcript, tagged by role (upstream append_chat_history).
func TestToolOutputAndHistory(t *testing.T) {
	var out strings.Builder
	io := NewIO(strings.NewReader(""), &out, false)
	io.ToolOutput("hello %s", "world")
	io.ToolError("boom %d", 42)

	if !strings.Contains(out.String(), "hello world") || !strings.Contains(out.String(), "boom 42") {
		t.Errorf("output missing formatted messages:\n%s", out.String())
	}
	if len(io.History) != 2 {
		t.Fatalf("expected 2 history lines, got %d", len(io.History))
	}
	if io.History[0].Role != "tool" || io.History[1].Role != "error" {
		t.Errorf("history roles = %q,%q; want tool,error", io.History[0].Role, io.History[1].Role)
	}
}

// TestScriptIOConfirmInjection: the test double answers ConfirmAsk from its
// Confirm field — this is how commands_test injects yes/no without a terminal.
func TestScriptIOConfirmInjection(t *testing.T) {
	if !NewScriptIO(true).ConfirmAsk("q") {
		t.Error("ScriptIO{Confirm:true}.ConfirmAsk should return true")
	}
	if NewScriptIO(false).ConfirmAsk("q") {
		t.Error("ScriptIO{Confirm:false}.ConfirmAsk should return false")
	}
}

// TestREPLEndToEnd: drive the whole loop through a ScriptIO. /add then /ls then a
// chat line then /quit. Asserts: command mutated state, plain line reached the
// coder stub, and the loop exits on /quit.
func TestREPLEndToEnd(t *testing.T) {
	io := NewScriptIO(true,
		"/add a.go",
		"/ls",
		"add a docstring", // plain message → coder stub
		"/quit",
	)
	session := NewSession(FormatDiff, "a.go", "b.go")
	commands := NewCommands(io, session)

	runREPL(io, session, commands)

	if !session.InChat["a.go"] {
		t.Error("/add a.go in the REPL should have left a.go in chat")
	}
	out := io.Out.String()
	if !strings.Contains(out, "[coder] would send to the LLM") {
		t.Errorf("the plain message should reach the coder stub:\n%s", out)
	}
	if !strings.Contains(out, "Bye.") {
		t.Errorf("/quit should end the loop with a goodbye:\n%s", out)
	}
	// The prompt prefix reflects the format.
	if !strings.Contains(out, "diff> ") {
		t.Errorf("REPL prompt should carry the format prefix 'diff> ':\n%s", out)
	}
}
