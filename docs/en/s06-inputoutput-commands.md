---
title: "s06 · InputOutput layer + in-chat commands"
chapter: 6
slug: s06-inputoutput-commands
est_read_min: 13
---

# s06 · InputOutput layer + in-chat commands

> What this teaches: the user-facing **shell** that wraps the coder loop. Two pieces — an **InputOutput** contract (`io.go`, upstream `aider/io.py`) that the coder talks through instead of touching stdin, and a **Commands** dispatcher (`commands.go`, upstream `aider/commands.py`) that intercepts `/`-lines, mutates the in-chat file scope, and never sends them to the model. The boundary `ParseAndRun` returns — handled vs. not — is the chapter.

---

## Problem

s01 through s05 were all the same skeleton: take one instruction (a CLI flag or arg), make one LLM call, apply the result, exit. That is enough to *demonstrate* an edit format, but it is not a session. A real aider run is an interactive loop where you talk to the model for many turns — and where most of what you type is *not* a message to the model at all. "Add this file to the chat", "drop that one", "show me what's in scope", "commit", "what can I do?" are commands to the *tool*, not prompts for the *LLM*. If every line were forwarded to the model, half of them would be wasted tokens and the model would have no way to actually change which files it can edit.

There are two missing layers. First, the coder currently reads and writes the terminal directly, which means it can't be tested without a real TTY and can't honor a `--yes` flag without threading it everywhere. Second, there is no notion of an in-chat **file scope** that the user can grow and shrink, and no place to put the `/`-commands that manage it. This chapter adds both: a narrow I/O contract, and a command dispatcher sitting in front of the loop.

## Solution

Put a thin shell in front of the coder. **InputOutput** becomes a small interface — `GetInput`, `ConfirmAsk`, `ToolOutput`, `ToolError`, `AddToHistory` — that everything else depends on. The coder no longer knows about stdin; it asks the `Console` for a line. That one indirection makes the loop testable (inject scripted input) and makes `--yes` a single branch inside `ConfirmAsk` instead of a flag passed through ten call sites. **Commands** becomes a registry mapping a command name to a handler over shared session state. A line is dispatched if it starts with `/`; the handler mutates the **in-chat file set** (the edit scope) and prints through the same `Console`.

Three decisions carry the design:

1. **Commands are intercepted before the model.** `ParseAndRun(line)` returns `handled=true` for a `/`-line and `false` for plain text. Only the `false` path ever becomes a `Message`. A `/add` changes state with zero network — which is exactly why this whole chapter runs with no API key.
2. **The dispatcher is a name→handler map.** Upstream finds `cmd_<word>` by reflection over methods; we use an explicit `map[string]handler`. Adding a command is adding one entry. An unknown command produces an error line (not a crash) and is still "handled" so a typo is never sent to the LLM.
3. **The in-chat file set IS the edit scope.** `/add`/`/drop` grow and shrink it; `/ls` reports it. In a full build that set becomes the file-content messages the coder sends — so this is the chapter where what the model can edit becomes dynamic and user-driven.

## How It Works

```ascii-anim frames=2
┌──────────────────────────────────────────────────────────────────┐
│  user types a line                                                │
│        │                                                         │
│        ▼   io.GetInput("diff> ")        ── Console (io.go) ──     │
│  ┌───────────────┐                                               │
│  │ "/add a.go"   │  starts with "/"?                             │
│  └───────────────┘                                               │
│        │ yes                         │ no                        │
│        ▼                             ▼                           │
│  Commands.ParseAndRun           sendToCoder(line)                │
│   look up cmd in registry        (becomes a Message → LLM)       │
│        │                                                         │
│        ▼  handler mutates Session.InChat  ── the EDIT SCOPE ──   │
│  /add → InChat[a.go]=true   /drop → delete   /ls → print buckets │
│        │                                                         │
│        ▼  handled=true → loop reads the next line (no LLM call)  │
└──────────────────────────────────────────────────────────────────┘
```

The core dispatch (excerpt from [`agents/s06-inputoutput-commands/commands.go`](https://github.com/Ding-Ye/learn-aider/blob/main/agents/s06-inputoutput-commands/commands.go)):

```go
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
// knows to treat the line as a chat message for the coder.
func (c *Commands) ParseAndRun(inp string) (handled bool) {
	inp = strings.TrimSpace(inp)
	if !c.IsCommand(inp) {
		return false // plain chat → flows on to the coder
	}

	body := inp[1:]
	word, args, _ := strings.Cut(body, " ")
	word = strings.TrimSpace(word)
	args = strings.TrimSpace(args)

	h, ok := c.registry[word]
	if !ok {
		// unknown command → an error line, no crash, and still handled=true so a
		// typo'd command is NOT silently sent to the LLM.
		c.io.ToolError("Invalid command: /%s (try /help)", word)
		return true
	}
	h.run(c, args)
	return true
}
```

**Four non-obvious points**:

1. **`handled` is the whole boundary.** The loop does `if commands.ParseAndRun(line) { continue }` and only otherwise calls the coder. That single bool is what keeps commands off the wire and chat on it — it's the explicit version of upstream's `preproc_user_input`.
2. **Unknown command returns `true`, not `false`.** It would be a subtle bug to forward `/frobnicate` to the LLM as a prompt. Reporting an error and staying "handled" is the safe choice (upstream `do_run` L291-293).
3. **The registry replaces reflection.** Upstream auto-registers any `cmd_*` method via `getattr`. A `map[string]handler` is the same ergonomics without the magic, and `/help` just ranges over the map.
4. **`ConfirmAsk(--yes)` never reads input.** The create-file prompt in `/add` goes through `ConfirmAsk`; with `--yes` it returns true without touching the reader (upstream `io.py` L866). Tests use the same knob (`ScriptIO.Confirm`) to force yes or no deterministically.

## What Changed (vs. s05)

```diff
 // s05: one-shot. Build a prompt, send one message, print the reply, exit.
-resp, err := prov.CreateMessage(ctx, req)
-fmt.Println(firstText(resp.Content))

 // s06: a REPL with a command shell in front of the coder.
+for {
+	line, ok := io.GetInput(promptPrefix(session.Format))   // Console contract
+	if !ok { return }                                       // Ctrl-D / EOF
+	if commands.ParseAndRun(line) { continue }              // "/..." handled locally
+	sendToCoder(io, session, line)                          // plain text → the LLM
+}
```

The change is structural, not just additive. s05's `main` was a straight line: assemble → send → print. s06's `main` is a loop with a fork in it, and the fork is the lesson. Two new long-lived objects appear — a `Console` (the I/O contract) and a `Commands` dispatcher over a mutable `Session` — and the coder call shrinks to one branch of the loop. The file scope, which was a fixed CLI argument in every previous chapter, is now something the user edits at runtime with `/add` and `/drop`.

## Try It

```bash
cd agents/s06-inputoutput-commands

# interactive REPL — no network, no API key (this chapter has no LLM call)
go run .

# scriptable: pipe a sequence of commands + one chat line on stdin
printf '/help\n/add main.go\n/ls\nrename foo to bar\n/quit\n' | go run .

# seed a different repo file universe; change the prompt prefix; auto-confirm
go run . -files a.go,b.go,c.go
go run . -format whole
go run . -yes

# tests (these run in CI, no network)
go test -v ./...
```

Expected output shape:

```
diff> Added main.go to the chat
diff> Repo files not in the chat:
  README.md
  util.go
Files in chat:
  main.go
diff> [coder] would send to the LLM with 1 file(s) in chat: "rename foo to bar"
diff> Bye.
```

The key things to notice: `/add` and `/ls` print and mutate state but the line `rename foo to bar` (no leading `/`) is the only one that reaches the coder stub — and the stub reports the file scope it *would* send. An unknown `/command` prints `Invalid command: ...` and is not forwarded.

## Upstream Source Reading

In aider the equivalent shell is `aider/commands.py` (the `Commands` class) plus `aider/io.py` (the `InputOutput` class). The dispatcher below is the load-bearing core; the real file has 50+ `cmd_*` handlers and prefix-matching, but the shape is exactly this: decide if the line is a command, split the first word, look up the handler by name, call it.

```upstream:aider/commands.py#L312-L332
    def run(self, inp):
        if inp.startswith("!"):
            self.coder.event("command_run")
            return self.do_run("run", inp[1:])

        res = self.matching_commands(inp)
        if res is None:
            return
        matching_commands, first_word, rest_inp = res
        if len(matching_commands) == 1:
            command = matching_commands[0][1:]
            self.coder.event(f"command_{command}")
            return self.do_run(command, rest_inp)
        elif first_word in matching_commands:
            command = first_word[1:]
            self.coder.event(f"command_{command}")
            return self.do_run(command, rest_inp)
        elif len(matching_commands) > 1:
            self.io.tool_error(f"Ambiguous command: {', '.join(matching_commands)}")
        else:
            self.io.tool_error(f"Invalid command: {first_word}")
```

**Reading notes**:

- **Reflection vs. map**: upstream `do_run` (L287-299) resolves a handler with `getattr(self, "cmd_" + name)`, so *any* method named `cmd_x` is automatically a command. We use an explicit `map[string]handler` — the same "add one thing = add one command" ergonomics, but visible and type-checked.
- **Prefix matching**: upstream resolves `/dr` to `/drop` via `matching_commands` and reports an "Ambiguous command" when a prefix matches several. s06 requires the exact command word; the dispatch *shape* is what the chapter is about, so we skip prefix resolution.
- **The `!` shell alias**: upstream's `run` runs a shell command when the line starts with `!` (L313-315). s06 has no command-execution layer in the curriculum, so `!` falls through to "Invalid command" rather than executing anything.
- **The `--yes` fast-path**: upstream `confirm_ask` (io.py L866-869) returns without reading input when `self.yes` is set. That single branch is what makes the loop scriptable; s06 reproduces it in `IO.ConfirmAsk` and in `ScriptIO.Confirm`.
- **A correct-but-imperfect choice we keep**: an unknown command is reported and still treated as *handled*, so a typo like `/lst` is never forwarded to the model as a prompt — matching upstream, which prints an error and returns rather than falling through to the LLM.

**Read further**: start at `aider/coders/base_coder.py` → `run()`, follow `preproc_user_input()` into `aider/commands.py` `run`/`do_run`, then see how the in-chat set turns into prompt content in `base_coder.py` `get_files_messages()`. That trace is the real-source map for s06 → s07 (git) → the message-assembly chapters.

---

**Next**: s07 gives the file scope teeth — every applied edit becomes an atomic, attributed **git commit** (`aider/repo.py`), so the loop gains a post-apply step and edits become reversible.
