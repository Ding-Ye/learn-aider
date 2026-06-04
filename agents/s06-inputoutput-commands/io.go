package main

// io.go — the InputOutput layer.
//
// Upstream this is aider/io.py's InputOutput class (1191 lines of rich +
// prompt_toolkit). Its JOB, stripped of the terminal-rendering machinery, is a
// narrow contract: ask the user for a line, ask a yes/no question, print
// tool/error output, and append everything to a chat-history transcript. The
// coder never touches stdin/stdout directly — it goes through this object — which
// is the whole point: it makes the loop TESTABLE (inject scripted input) and
// makes "--yes" a one-line override instead of a flag threaded everywhere.
//
// We model that contract as an interface (Console) so tests can drive the loop
// with canned input and capture output, and provide two implementations:
//   - IO:       the real terminal (reads a bufio.Scanner, writes to stdout).
//   - ScriptIO: a fake for tests — a queue of input lines + a captured output
//               buffer + a forced ConfirmAsk answer (the "--yes" knob).

import (
	"bufio"
	"fmt"
	"io"
	"strings"
)

// Console is the I/O contract the rest of the program depends on. Compare
// upstream io.py: GetInput≈get_input (L523), ConfirmAsk≈confirm_ask (L807),
// ToolOutput≈tool_output (L995), ToolError≈tool_error (L988). AddToHistory≈
// append_chat_history. Programming against this interface (not a concrete IO) is
// what lets commands_test.go inject input without a real terminal.
type Console interface {
	// GetInput prompts the user for one line and returns it (without the newline).
	// `prefix` is the prompt shown, e.g. "diff> ". The bool is false at EOF.
	GetInput(prefix string) (string, bool)
	// ConfirmAsk asks a yes/no question and returns the user's choice. With --yes
	// set it returns true without reading input (upstream io.py L866).
	ConfirmAsk(question string) bool
	// ToolOutput prints normal status output (upstream tool_output).
	ToolOutput(format string, a ...interface{})
	// ToolError prints an error line (upstream tool_error). Tracked separately so
	// callers/tests can tell errors from normal output.
	ToolError(format string, a ...interface{})
	// AddToHistory appends a line to the chat-history transcript. Upstream writes
	// this to .aider.chat.history.md so a session can be resumed.
	AddToHistory(role, text string)
}

// HistoryLine is one entry in the chat transcript (upstream append_chat_history).
type HistoryLine struct {
	Role string // "user" | "assistant" | "tool" | "error"
	Text string
}

// IO is the real terminal implementation of Console.
type IO struct {
	in  *bufio.Scanner
	out io.Writer
	// Yes mirrors upstream io.py's `self.yes`. When true, ConfirmAsk auto-answers
	// "yes" and never blocks on input — this is the --yes / --yes-always flag.
	Yes bool

	History []HistoryLine // the in-memory chat transcript
}

// NewIO builds an IO over the given reader/writer. main.go passes os.Stdin /
// os.Stdout; tests use ScriptIO instead.
func NewIO(in io.Reader, out io.Writer, yes bool) *IO {
	return &IO{in: bufio.NewScanner(in), out: out, Yes: yes}
}

func (io *IO) GetInput(prefix string) (string, bool) {
	fmt.Fprint(io.out, prefix)
	if !io.in.Scan() {
		return "", false // EOF (Ctrl-D)
	}
	line := io.in.Text()
	return line, true
}

// ConfirmAsk mirrors upstream confirm_ask: with --yes we never read input, we
// just say yes. Otherwise we read a line and treat empty / y / yes as yes.
func (io *IO) ConfirmAsk(question string) bool {
	if io.Yes {
		io.ToolOutput("%s (Y)es/(N)o [Yes]: yes", question)
		return true
	}
	fmt.Fprintf(io.out, "%s (Y)es/(N)o [Yes]: ", question)
	if !io.in.Scan() {
		return true // EOF → default (yes)
	}
	answer := strings.TrimSpace(strings.ToLower(io.in.Text()))
	yes := answer == "" || strings.HasPrefix(answer, "y")
	io.AddToHistory("user", fmt.Sprintf("%s %s", strings.TrimSpace(question), answer))
	return yes
}

func (io *IO) ToolOutput(format string, a ...interface{}) {
	msg := fmt.Sprintf(format, a...)
	fmt.Fprintln(io.out, msg)
	io.AddToHistory("tool", msg)
}

func (io *IO) ToolError(format string, a ...interface{}) {
	msg := fmt.Sprintf(format, a...)
	fmt.Fprintln(io.out, msg)
	io.AddToHistory("error", msg)
}

func (io *IO) AddToHistory(role, text string) {
	io.History = append(io.History, HistoryLine{Role: role, Text: text})
}

// ---- ScriptIO: the test double ----

// ScriptIO is a fake Console for tests. It pops input from a queue, captures all
// output into a buffer, and answers ConfirmAsk from a fixed field — the same role
// upstream's `--yes` plays, but here also usable to force a "no".
type ScriptIO struct {
	Inputs  []string // queue of lines GetInput will return, in order
	cursor  int
	Out     strings.Builder // everything that was printed (output + errors)
	Errors  []string        // just the ToolError lines, for assertions
	Confirm bool            // the answer ConfirmAsk always returns
	History []HistoryLine
}

// NewScriptIO builds a ScriptIO that will return `inputs` from GetInput in order
// and answer every ConfirmAsk with `confirm`.
func NewScriptIO(confirm bool, inputs ...string) *ScriptIO {
	return &ScriptIO{Inputs: inputs, Confirm: confirm}
}

func (s *ScriptIO) GetInput(prefix string) (string, bool) {
	s.Out.WriteString(prefix)
	if s.cursor >= len(s.Inputs) {
		return "", false // queue exhausted == EOF
	}
	line := s.Inputs[s.cursor]
	s.cursor++
	return line, true
}

func (s *ScriptIO) ConfirmAsk(question string) bool {
	s.Out.WriteString(fmt.Sprintf("%s -> %v\n", question, s.Confirm))
	s.AddToHistory("user", fmt.Sprintf("%s %v", question, s.Confirm))
	return s.Confirm
}

func (s *ScriptIO) ToolOutput(format string, a ...interface{}) {
	msg := fmt.Sprintf(format, a...)
	s.Out.WriteString(msg + "\n")
	s.AddToHistory("tool", msg)
}

func (s *ScriptIO) ToolError(format string, a ...interface{}) {
	msg := fmt.Sprintf(format, a...)
	s.Out.WriteString(msg + "\n")
	s.Errors = append(s.Errors, msg)
	s.AddToHistory("error", msg)
}

func (s *ScriptIO) AddToHistory(role, text string) {
	s.History = append(s.History, HistoryLine{Role: role, Text: text})
}
