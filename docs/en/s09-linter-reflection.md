---
title: "s09 · Linter + reflection loop"
chapter: 9
slug: s09-linter-reflection
est_read_min: 11
---

# s09 · Linter + reflection loop

> What this teaches: how aider self-corrects. After applying an edit it runs a **linter** on the changed files, and if the linter complains it feeds the errors back to the model as a new user turn and lets it try again — a **reflection loop** bounded by a small cap. This is the first time the control flow becomes multi-turn beyond a single send, which is why it gets its own chapter.

---

## Problem / 问题

s01..s08 built a complete single-shot pipeline: a user instruction becomes a prompt, the model replies, we parse the edit, apply it, and (s07) auto-commit it. RepoMap (s08) even gives the model a ranked view of the repo so it edits with context. But every one of those chapters ends at the same place — whatever the model produced lands on disk and the turn is over.

The problem is that models are not perfect. They drop a brace, leave a function half-written, reference a symbol that doesn't exist. In a single-shot loop that broken code is committed and the human discovers it later, by hand. A real pair programmer doesn't work that way: they notice the compiler error and fix it *before* handing the change over. s09's job is to add exactly that reflex — run a checker on what we just wrote, and if it's broken, give the model the errors and a chance to fix them, without a human round-trip.

## Solution / 解决方案

The mental model is a **feedback loop wrapped around the existing send-and-apply turn**. After applying edits we run a `Linter` over the changed files. A clean result ends the turn (nothing to fix). A non-clean result is turned into a synthetic *user* message — the linter's own output, prefixed with `# Fix any errors below` — and that message re-enters the loop as if the human had pasted a compiler error into the chat. We repeat until the lint is clean or we hit a small **reflection cap**.

Three design decisions carry the chapter:

1. **The lint report becomes the next message.** The single assignment `message = lintText` is the whole mechanism. The model sees its own broken output followed by the failure, which is the most natural possible "fix this" signal.
2. **The cap is mandatory, not a nicety.** A model that keeps emitting broken code would loop forever. Upstream caps at three reflections; we default to the same and stop with a warning rather than spin — better to hand a still-broken file to a human than burn tokens indefinitely.
3. **The loop is format- and provider-agnostic.** It talks to a `Coder` (for parse/apply) and a `SendFunc` (for the model call). The linter is just "run a command, capture output, label it." Swap the coder or the model and the loop is unchanged.

## How It Works / 工作原理

```ascii-anim frames=2
┌─────────────────────────────────────────────────────────────┐
│  user instruction                                            │
│        │                                                     │
│        ▼                                                     │
│   ┌─────────┐   reply   ┌──────────┐  edits  ┌───────────┐  │
│   │  Send   │ ────────▶ │ GetEdits │ ──────▶ │ ApplyEdits│  │
│   └─────────┘           └──────────┘         └─────┬─────┘  │
│        ▲                                            ▼        │
│        │                                      ┌───────────┐  │
│        │  message = lintText                  │  Linter   │  │
│        │  (reflections++)                     │  .Lint    │  │
│        │                                      └─────┬─────┘  │
│        │           not clean & under cap            │        │
│        └────────────────◀──────────────────────────┤        │
│                                       clean OR cap  ▼        │
│                                                   DONE       │
└─────────────────────────────────────────────────────────────┘
```

The loop body is a near-literal transcription of upstream `run_one` (excerpt from [`agents/s09-linter-reflection/reflect.go`](https://github.com/Ding-Ye/learn-aider/blob/main/agents/s09-linter-reflection/reflect.go)):

```go
func (r *ReflectLoop) Run(ctx context.Context, userInstruction string) (*ReflectResult, error) {
	res := &ReflectResult{}
	messages := []Message{userMsg(userInstruction)}
	message := userInstruction

	for message != "" {
		// 1. One model round-trip for the current message.
		reply, err := r.Send(ctx, messages)
		if err != nil {
			return res, fmt.Errorf("send (reflection %d): %w", res.Reflections, err)
		}
		res.Replies = append(res.Replies, reply)
		messages = append(messages, assistantMsg(reply))

		// 2. Realise the edits, then lint what changed.
		edits, err := r.Coder.GetEdits(reply)
		if err != nil {
			return res, fmt.Errorf("parse edits (reflection %d): %w", res.Reflections, err)
		}
		applied, _, err := r.Coder.ApplyEdits(edits)
		if err != nil {
			return res, fmt.Errorf("apply edits (reflection %d): %w", res.Reflections, err)
		}
		res.Applied = append(res.Applied, applied...)
		lintText, ok := r.Linter.Lint(r.pathsToLint(applied))

		// 3. Converged: the linter is happy.
		if ok {
			res.Clean = true
			return res, nil
		}
		// 4. Not clean. Stop if we've used our budget.
		if res.Reflections >= r.MaxReflections {
			res.HitCap = true
			res.LintText = lintText
			return res, nil
		}
		// 5. Reflect: the lint report BECOMES the next user message.
		res.Reflections++
		message = lintText
		messages = append(messages, userMsg(message))
	}
	res.Clean = true
	return res, nil
}
```

**Four non-obvious points**:

1. **`messages` and `message` are not the same thing** — `messages` is the full conversation sent to the model every pass (it grows by an assistant turn + a user turn each reflection); `message` is just the loop's "is there anything left to do" sentinel. When it goes empty the loop ends.
2. **The cap counts *extra* passes, not total calls.** `Reflections == 0` means the model got it right on the first try; `MaxReflections == 3` allows up to four model calls total. The check is `>=` *before* incrementing, so we never exceed the budget.
3. **No edits is a clean exit, not an error.** If the model replies with prose and no fenced block, `GetEdits` returns nothing, there's nothing to lint, and the loop ends clean. A reflection loop must not treat "the model declined" as a failure to retry.
4. **The linter decides "clean," not the absence of edits.** Even a successfully-applied edit is re-checked; convergence means *the checker passed*, which is what guards that the file on disk actually compiles.

## What Changed (vs. s08) / 与 s08 的变化

```diff
  // s08 (and s01..s07): the turn ends at apply.
- reply := send(messages)
- edits, _ := coder.GetEdits(reply)
- coder.ApplyEdits(edits)
- // ...auto-commit, then wait for the next user message.

  // s09: apply is followed by lint, and lint can re-enter the loop.
+ for message != "" {
+     reply := send(messages)
+     edits, _ := coder.GetEdits(reply)
+     applied, _, _ := coder.ApplyEdits(edits)
+     lintText, ok := linter.Lint(pathsToLint(applied))
+     if ok { break }                          // clean -> done
+     if reflections >= maxReflections { break } // give up at the cap
+     reflections++
+     message = lintText                        // errors become the next turn
+ }
```

The change is structural, not cosmetic. Up to s08 the loop was a straight line: one input, one output. s09 closes it into a cycle gated by the linter — the program now decides on its own whether to call the model again. That self-driven re-entry, with a hard cap so it always terminates, is the new mechanism.

## Try It / 动手试一试

```bash
cd agents/s09-linter-reflection

# LINT mode (no network, no key): print exactly what the loop would reflect back.
go run . -lint broken.go

# A clean file => "nothing to reflect"
go run . -lint provider.go

# RUN mode: edit a file via the LLM, then lint + reflect until clean or the cap.
export ANTHROPIC_API_KEY=sk-ant-...
go run . -v -file calc.go -instruction "add a Divide(a, b int) int function"

# Cap the self-correction at 2 extra passes
go run . -v -file calc.go -instruction "..." -max-reflections 2

# Tests (no network)
go test -v ./...
```

Expected output shape:

```
# LINT mode on a broken file:
# Fix any errors below, if possible.

## Running: gofmt broken.go

broken.go:4:9: expected operand, found '}'

# RUN mode, model fixes its own mistake on the second pass:
[s09] converged after 1 reflection(s) — lint is now clean.
applied 2 edit(s) to calc.go across 2 pass(es)
```

## Upstream Source Reading / 上游源码阅读

In aider the linter and the reflection loop live in two files. The linter is `aider/linter.py`; the loop is the `while message:` block inside `run_one` in `aider/coders/base_coder.py` (L924-944, read in s01). The excerpt below is the linter's dispatcher — the method our Go `Linter.Lint`/`lintOne`/`runCmd` port directly.

```upstream:aider/linter.py#L82-L116
    def lint(self, fname, cmd=None):
        rel_fname = self.get_rel_fname(fname)
        try:
            code = Path(fname).read_text(encoding=self.encoding, errors="replace")
        except OSError as err:
            print(f"Unable to read {fname}: {err}")
            return

        if cmd:
            cmd = cmd.strip()
        if not cmd:
            lang = filename_to_lang(fname)
            if not lang:
                return                       # no checker for this language -> skip
            if self.all_lint_cmd:
                cmd = self.all_lint_cmd      # --lint-cmd override wins
            else:
                cmd = self.languages.get(lang)

        if callable(cmd):
            lintres = cmd(fname, rel_fname, code)   # py_lint path
        elif cmd:
            lintres = self.run_cmd(cmd, rel_fname, code)
        else:
            lintres = basic_lint(rel_fname, code)   # built-in tree-sitter pass

        if not lintres:
            return                            # clean -> None (loop sees "no errors")

        res = "# Fix any errors below, if possible.\n\n"  # the header we copy verbatim
        res += lintres.text
        res += "\n"
        res += tree_context(rel_fname, code, lintres.lines)  # pretty source; we drop it

        return res
```

**Reading notes**:

- **Same precedence, different registry key.** Upstream keys checkers by tree-sitter *language* (`self.languages[lang]`); we key by file *extension* (`.go`). Both fall through to a built-in pass when nothing is configured. The `all_lint_cmd` override (our `allCmd`) sits above both.
- **`return None` == clean.** The whole loop hinges on this convention: a checker that finds nothing returns `None`, the dispatcher returns `None`, and `run_one` sees no `reflected_message` and stops. Our Go version returns `("", true)` for the same meaning.
- **The header is load-bearing, the pretty-print is not.** `# Fix any errors below, if possible.` tells the model the following text is a problem report; we keep it byte-for-byte. `tree_context` renders the offending source with a `█` marker — nice, but not essential, so we drop it and keep the raw `file:line` errors.
- **The cap lives in the *other* file.** `lint` only produces text; the bound is in `run_one`'s `if self.num_reflections >= self.max_reflections`. We fold both into one package (`linter.go` + `reflect.go`) but keep the same split of responsibilities.
- **A deliberately imperfect simplification:** our built-in checker is `gofmt -l -e`, which catches parse errors and formatting drift but not type errors. Upstream's `py_lint` additionally runs `compile()` and flake8. The `-lint-cmd "go vet"` flag is how you opt into deeper checking — correct, but lighter than upstream by default.

**Read further**: start at `aider/linter.py` → `Linter.lint`, follow the `LintResult` it returns into `aider/coders/base_coder.py` → `lint_edited` (which sets `self.lint_outcome`), then into `run_one`'s reflection loop where that outcome becomes `reflected_message`. That trace is the real-source map for s09 → s10 (where the model call behind `send` gains retries and streaming).

---

**Next**: s10 evolves the `Provider` behind `send` into a configurable, resilient stack — a model registry, retries with exponential backoff, and streaming — so the reflection loop keeps working across transient failures and many model families.
