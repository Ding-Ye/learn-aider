package main

// commands.go — the in-chat slash-command dispatcher.
//
// Upstream this is aider/commands.py's Commands class (1712 lines). The load-
// bearing idea, stripped of the 50+ real handlers, is tiny:
//
//   1. is_command(inp): a line is a command iff it starts with "/" (or "!").
//      (upstream L255-256)
//   2. run(inp): split off the first word, look up the handler named cmd_<word>,
//      call it with the rest of the line as args. (upstream L312-332 / do_run
//      L287-299) — note upstream finds the handler by REFLECTION over methods
//      named cmd_*; in Go we use an explicit name→handler map, same registry idea.
//   3. each handler mutates shared SESSION STATE — chiefly the set of files "in
//      the chat" — which is what scopes what the coder may edit. /add and /drop
//      (upstream cmd_add L799, cmd_drop L912) grow and shrink that set; /ls
//      (cmd_ls L1064) reports it.
//
// The key separation: a command NEVER becomes a Message to the LLM. ParseAndRun
// returns handled=true for a command (the loop then asks for the next line) and
// handled=false for a plain chat line (the loop sends it to the coder). That is
// upstream's preproc_user_input boundary, made explicit.

import (
	"fmt"
	"sort"
	"strings"
)

// Session is the mutable state commands act on. In full aider this is the Coder
// (abs_fnames, abs_read_only_fnames, repo, ...). s06 keeps just the parts the
// command layer owns: the in-chat file set and the read-only set. `AllFiles` is
// the universe of files known in the repo, used by /ls and /add validation.
type Session struct {
	InChat   map[string]bool // editable files currently in the chat
	ReadOnly map[string]bool // reference-only files in the chat
	AllFiles []string        // every file the repo knows about (for /ls + /add)
	Format   EditFormat      // shown in the prompt prefix, e.g. "diff> "
}

// NewSession seeds an empty chat over a known file universe.
func NewSession(format EditFormat, allFiles ...string) *Session {
	return &Session{
		InChat:   map[string]bool{},
		ReadOnly: map[string]bool{},
		AllFiles: allFiles,
		Format:   format,
	}
}

// knows reports whether `f` is part of the repo's known file universe.
func (s *Session) knows(f string) bool {
	for _, a := range s.AllFiles {
		if a == f {
			return true
		}
	}
	return false
}

// handler is one command implementation. It gets the args string (everything
// after the command word) and may print via io / mutate the session.
type handler struct {
	run  func(c *Commands, args string)
	help string // one-line description, shown by /help (upstream cmd docstrings)
}

// Commands is the dispatcher: a registry of name→handler over a Session + IO.
type Commands struct {
	io       Console
	session  *Session
	registry map[string]handler
}

// NewCommands wires the dispatcher to an IO + Session and registers the built-in
// commands. Add a command = add one registry entry (upstream: define a cmd_*
// method). s06 ships /add /drop /ls /diff /help (>=5).
func NewCommands(io Console, session *Session) *Commands {
	c := &Commands{io: io, session: session}
	c.registry = map[string]handler{
		"add":  {run: (*Commands).cmdAdd, help: "Add a file to the chat so it can be edited"},
		"drop": {run: (*Commands).cmdDrop, help: "Remove a file from the chat to free context"},
		"ls":   {run: (*Commands).cmdLs, help: "List repo files and mark which are in the chat"},
		"diff": {run: (*Commands).cmdDiff, help: "Show a (stubbed) diff of the in-chat files"},
		"help": {run: (*Commands).cmdHelp, help: "List the available commands"},
	}
	return c
}

// IsCommand reports whether a line is a slash command (upstream is_command
// L255-256: `inp[0] in "/!"`). Empty input is not a command.
func (c *Commands) IsCommand(inp string) bool {
	if inp == "" {
		return false
	}
	return inp[0] == '/' || inp[0] == '!'
}

// ParseAndRun is the loop's entry point. If `inp` is a command it dispatches it
// and returns handled=true; otherwise it returns handled=false so the caller
// knows to treat the line as a chat message for the coder. This is the explicit
// version of upstream's preproc_user_input → run boundary.
func (c *Commands) ParseAndRun(inp string) (handled bool) {
	inp = strings.TrimSpace(inp)
	if !c.IsCommand(inp) {
		return false // plain chat → flows on to the coder
	}

	// Strip the leading "/" and split the command word from its args. Upstream
	// also supports "!" as a shell-run alias; s06 omits the shell layer, so we
	// just report it as unknown rather than executing anything.
	body := inp[1:]
	word, args, _ := strings.Cut(body, " ")
	word = strings.TrimSpace(word)
	args = strings.TrimSpace(args)

	h, ok := c.registry[word]
	if !ok {
		// upstream do_run L291-293 / run L332: unknown command → an error line,
		// no crash, and (here) we still return handled=true so a typo'd command
		// is NOT silently sent to the LLM.
		c.io.ToolError("Invalid command: /%s (try /help)", word)
		return true
	}
	h.run(c, args)
	return true
}

// ---- the built-in handlers (each mutates the session and/or prints) ----

// cmdAdd mirrors upstream cmd_add (L799): bring a file into the chat so the model
// may edit it. We validate against the known file universe; an unknown path is
// offered as a create (gated by ConfirmAsk, like upstream's "create file?"
// prompt at L838).
func (c *Commands) cmdAdd(args string) {
	if args == "" {
		c.io.ToolError("Usage: /add <file>")
		return
	}
	for _, f := range strings.Fields(args) {
		if c.session.InChat[f] {
			c.io.ToolError("%s is already in the chat", f)
			continue
		}
		if !c.session.knows(f) {
			if !c.io.ConfirmAsk(fmt.Sprintf("No file %q in repo. Create it?", f)) {
				continue
			}
			c.session.AllFiles = append(c.session.AllFiles, f)
		}
		delete(c.session.ReadOnly, f) // promote read-only → editable, like upstream
		c.session.InChat[f] = true
		c.io.ToolOutput("Added %s to the chat", f)
	}
}

// cmdDrop mirrors upstream cmd_drop (L912): remove a file from the chat. With no
// args, drop everything (upstream _drop_all_files).
func (c *Commands) cmdDrop(args string) {
	if args == "" {
		c.session.InChat = map[string]bool{}
		c.session.ReadOnly = map[string]bool{}
		c.io.ToolOutput("Dropped all files from the chat")
		return
	}
	for _, f := range strings.Fields(args) {
		if c.session.InChat[f] || c.session.ReadOnly[f] {
			delete(c.session.InChat, f)
			delete(c.session.ReadOnly, f)
			c.io.ToolOutput("Removed %s from the chat", f)
		} else {
			c.io.ToolError("%s is not in the chat", f)
		}
	}
}

// cmdLs mirrors upstream cmd_ls (L1064): list known repo files, split into "in
// chat" vs "not in chat", so the user can see the current edit scope.
func (c *Commands) cmdLs(args string) {
	var inChat, other []string
	for _, f := range c.session.AllFiles {
		if c.session.InChat[f] {
			inChat = append(inChat, f)
		} else {
			other = append(other, f)
		}
	}
	sort.Strings(inChat)
	sort.Strings(other)

	if len(inChat) == 0 && len(other) == 0 {
		c.io.ToolOutput("No files in chat or repo.")
		return
	}
	if len(other) > 0 {
		c.io.ToolOutput("Repo files not in the chat:")
		for _, f := range other {
			c.io.ToolOutput("  %s", f)
		}
	}
	if len(inChat) > 0 {
		c.io.ToolOutput("Files in chat:")
		for _, f := range inChat {
			c.io.ToolOutput("  %s", f)
		}
	}
}

// cmdDiff is a stub for upstream cmd_diff (L657), which shells out to `git diff`.
// s06 has no git layer yet (that arrives in s07), so we just report the current
// in-chat scope — enough to show that a command can READ session state too.
func (c *Commands) cmdDiff(args string) {
	if len(c.session.InChat) == 0 {
		c.io.ToolOutput("No files in chat to diff.")
		return
	}
	var files []string
	for f := range c.session.InChat {
		files = append(files, f)
	}
	sort.Strings(files)
	c.io.ToolOutput("Would diff %d file(s): %s", len(files), strings.Join(files, ", "))
	c.io.ToolOutput("(git diff lands in s07; here /diff just reports scope)")
}

// cmdHelp mirrors upstream basic_help (L1103): list the registered commands and
// their one-line descriptions, sorted for stable output.
func (c *Commands) cmdHelp(args string) {
	names := make([]string, 0, len(c.registry))
	for name := range c.registry {
		names = append(names, name)
	}
	sort.Strings(names)
	c.io.ToolOutput("Available commands:")
	for _, name := range names {
		c.io.ToolOutput("  /%-5s %s", name, c.registry[name].help)
	}
}
