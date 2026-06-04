package main

// main.go — the interactive shell that wraps the coder loop.
//
// s01..s05 took one instruction on the command line, made one LLM call, and
// exited. A real session is a REPL: read a line, and either (a) it's a slash
// command that mutates session state without ever hitting the model, or (b) it's
// a chat message that flows to the coder. This file is that loop, and it shows
// the single most important boundary of the chapter:
//
//     line := io.GetInput(prefix)
//     if commands.ParseAndRun(line) { continue }   // (a) handled locally
//     coder.send(line)                              // (b) goes to the LLM
//
// There is NO network here — the chapter's mechanism is the I/O + command layer
// itself, so the "coder" is a stub that just echoes what it WOULD send. That is
// deliberate: it keeps the lesson on the shell, and it means `go run .` needs no
// API key.
//
// Usage:
//   s06                          # interactive REPL over a demo file set
//   s06 -files a.go,b.go         # seed the known repo file universe
//   s06 -format whole            # change the prompt prefix (whole> / diff> ...)
//   s06 -yes                     # auto-answer every confirmation (the --yes flag)
//   echo "/add a.go" | s06       # scriptable: stdin drives the REPL
//
// Try: /add a.go, then /ls (a.go now shows under "Files in chat"), then type a
// plain sentence and watch it go to the coder stub, then /help, then /quit.

import (
	"flag"
	"fmt"
	"os"
	"strings"
)

func main() {
	filesFlag := flag.String("files", "main.go,util.go,README.md",
		"comma-separated repo file universe (what /ls and /add validate against)")
	formatFlag := flag.String("format", "diff", "edit format shown in the prompt prefix: whole | diff | udiff")
	yes := flag.Bool("yes", false, "auto-answer every confirmation as yes (upstream --yes)")
	flag.Parse()

	// The known file universe. In real aider this comes from GitRepo
	// get_tracked_files (s07); here it's just a flag so the demo is self-contained.
	var allFiles []string
	for _, f := range strings.Split(*filesFlag, ",") {
		if f = strings.TrimSpace(f); f != "" {
			allFiles = append(allFiles, f)
		}
	}

	io := NewIO(os.Stdin, os.Stdout, *yes)
	session := NewSession(EditFormat(*formatFlag), allFiles...)
	commands := NewCommands(io, session)

	fmt.Fprintln(os.Stderr, "[s06] interactive shell. Lines starting with / are commands; "+
		"anything else is a chat message. Try /help, /add, /ls, /quit, or Ctrl-D to exit.")

	runREPL(io, session, commands)
}

// runREPL is the read–dispatch–or–send loop. It is intentionally tiny: all the
// mechanism lives in Commands.ParseAndRun and the IO contract.
func runREPL(io Console, session *Session, commands *Commands) {
	for {
		prefix := promptPrefix(session.Format)
		line, ok := io.GetInput(prefix)
		if !ok {
			io.ToolOutput("Bye.") // EOF / Ctrl-D
			return
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		// /quit and /exit aren't in the registry (they control the loop, not the
		// session), so the loop handles them before dispatching. Upstream cmd_exit
		// raises SystemExit; we just return.
		if line == "/quit" || line == "/exit" {
			io.ToolOutput("Bye.")
			return
		}

		// THE BOUNDARY: a command is handled locally and we loop for the next
		// line; a plain message is forwarded to the coder.
		if commands.ParseAndRun(line) {
			continue
		}
		sendToCoder(io, session, line)
	}
}

// sendToCoder stands in for s01..s05's loop. In a full build this assembles the
// in-chat file contents + the message into a CreateMessageRequest and calls the
// Provider. Here it just records the user turn and echoes what WOULD be sent,
// so the passthrough is visible without a network call.
func sendToCoder(io Console, session *Session, message string) {
	io.AddToHistory("user", message)
	n := len(session.InChat)
	io.ToolOutput("[coder] would send to the LLM with %d file(s) in chat: %q", n, message)
	if n == 0 {
		io.ToolOutput("[coder] (no files in chat yet — use /add <file> to give the model edit scope)")
	}
}

// promptPrefix builds the input prompt, e.g. "diff> ". Upstream get_input
// (io.py L545-552) prepends the edit format to the "> " for exactly this.
func promptPrefix(format EditFormat) string {
	if format == "" {
		return "> "
	}
	return string(format) + "> "
}
