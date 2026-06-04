package main

import (
	"strings"
	"testing"
)

// TestIsCommand: a line is a command iff it starts with "/" or "!" (upstream
// is_command L255-256). Plain text and empty input are not commands.
func TestIsCommand(t *testing.T) {
	c := NewCommands(NewScriptIO(true), NewSession(FormatDiff))
	cases := map[string]bool{
		"/add x": true,
		"/ls":    true,
		"!ls":    true,
		"hi":     false,
		"":       false,
		" /add":  false, // leading space: ParseAndRun trims, but raw IsCommand is literal
	}
	for inp, want := range cases {
		if got := c.IsCommand(inp); got != want {
			t.Errorf("IsCommand(%q) = %v, want %v", inp, got, want)
		}
	}
}

// TestDispatch: a known command runs its handler and is reported handled=true.
func TestDispatch(t *testing.T) {
	io := NewScriptIO(true)
	c := NewCommands(io, NewSession(FormatDiff, "a.go"))
	if !c.ParseAndRun("/add a.go") {
		t.Fatal("ParseAndRun(/add a.go) returned handled=false, want true")
	}
	if !strings.Contains(io.Out.String(), "Added a.go to the chat") {
		t.Errorf("expected /add to print confirmation, got:\n%s", io.Out.String())
	}
}

// TestUnknownCommand: an unknown command produces an error line, does NOT crash,
// and is still treated as handled (so a typo isn't sent to the LLM). Upstream
// run L332 / do_run L291-293.
func TestUnknownCommand(t *testing.T) {
	io := NewScriptIO(true)
	c := NewCommands(io, NewSession(FormatDiff))
	handled := c.ParseAndRun("/frobnicate")
	if !handled {
		t.Error("unknown command should be handled=true, not forwarded to the coder")
	}
	if len(io.Errors) == 0 || !strings.Contains(io.Errors[0], "Invalid command") {
		t.Errorf("expected an 'Invalid command' error, got errors=%v", io.Errors)
	}
}

// TestAddThenLsReflectsState: /add mutates the session, and a later /ls reflects
// it — the file moves from "not in chat" to "Files in chat". This is the whole
// point of commands: they mutate shared scope.
func TestAddThenLsReflectsState(t *testing.T) {
	io := NewScriptIO(true)
	session := NewSession(FormatDiff, "a.go", "b.go")
	c := NewCommands(io, session)

	c.ParseAndRun("/add a.go")
	if !session.InChat["a.go"] {
		t.Fatal("after /add a.go, session.InChat[a.go] should be true")
	}

	io.Out.Reset()
	c.ParseAndRun("/ls")
	out := io.Out.String()
	// a.go must appear under "Files in chat"; b.go under "not in the chat".
	chatIdx := strings.Index(out, "Files in chat:")
	if chatIdx < 0 {
		t.Fatalf("/ls missing 'Files in chat:' section:\n%s", out)
	}
	if !strings.Contains(out[chatIdx:], "a.go") {
		t.Errorf("/ls should list a.go in chat:\n%s", out)
	}
	if strings.Contains(out[chatIdx:], "b.go") {
		t.Errorf("/ls should NOT list b.go in chat (it was never added):\n%s", out)
	}
}

// TestDropMutatesState: /add then /drop returns the file to out-of-chat scope.
func TestDropMutatesState(t *testing.T) {
	io := NewScriptIO(true)
	session := NewSession(FormatDiff, "a.go")
	c := NewCommands(io, session)

	c.ParseAndRun("/add a.go")
	c.ParseAndRun("/drop a.go")
	if session.InChat["a.go"] {
		t.Error("after /drop a.go, the file should no longer be in chat")
	}
	io2 := NewScriptIO(true)
	c2 := NewCommands(io2, session)
	c2.ParseAndRun("/drop a.go")
	if len(io2.Errors) == 0 {
		t.Error("dropping a file not in chat should report an error")
	}
}

// TestNonCommandPassesThrough: plain text is NOT handled by the dispatcher, so
// the caller knows to send it to the coder. This is the loop's key boundary.
func TestNonCommandPassesThrough(t *testing.T) {
	io := NewScriptIO(true)
	c := NewCommands(io, NewSession(FormatDiff))
	if c.ParseAndRun("please add docstrings") {
		t.Error("a plain message must return handled=false so the loop forwards it to the coder")
	}
	if len(io.Errors) != 0 {
		t.Errorf("a plain message should not produce errors, got %v", io.Errors)
	}
}

// TestAddUnknownFileGatedByConfirm: /add on an unknown path asks ConfirmAsk; with
// the injected answer = false, nothing is added (upstream cmd_add create-prompt
// at L838). With answer = true, the file is created in scope.
func TestAddUnknownFileGatedByConfirm(t *testing.T) {
	// confirm = false → declined → not added
	ioNo := NewScriptIO(false)
	sNo := NewSession(FormatDiff, "known.go")
	NewCommands(ioNo, sNo).ParseAndRun("/add brand_new.go")
	if sNo.InChat["brand_new.go"] {
		t.Error("declined create should NOT add the file")
	}

	// confirm = true → accepted → added to scope
	ioYes := NewScriptIO(true)
	sYes := NewSession(FormatDiff, "known.go")
	NewCommands(ioYes, sYes).ParseAndRun("/add brand_new.go")
	if !sYes.InChat["brand_new.go"] {
		t.Error("accepted create should add the file to the chat")
	}
}

// TestHelpListsCommands: /help lists every registered command.
func TestHelpListsCommands(t *testing.T) {
	io := NewScriptIO(true)
	NewCommands(io, NewSession(FormatDiff)).ParseAndRun("/help")
	out := io.Out.String()
	for _, name := range []string{"/add", "/drop", "/ls", "/diff", "/help"} {
		if !strings.Contains(out, name) {
			t.Errorf("/help should mention %s, got:\n%s", name, out)
		}
	}
}
