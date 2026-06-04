---
title: "s05 · Per-format prompt system"
chapter: 5
slug: s05-prompt-system
est_read_min: 13
---

# s05 · Per-format prompt system

> What this teaches: the *prompt system* — how aider gets the model to emit a chosen edit format. Each format's system prompt is assembled from reusable **pieces** (`MainSystem` + `ExampleMessages` + `SystemReminder` + lazy/overeager nudges) with **placeholders** (`{fence}`, `{final_reminders}`) filled at send time. This is the missing half of s02–s04: a parser only works because a prompt taught the model that format.

---

## Problem / 问题

s02, s03, and s04 each built a working edit-format **parser**: whole-file listings, SEARCH/REPLACE blocks, unified diffs. Every chapter quietly assumed the model would *reply in that format* — and to make that happen, each one hardcoded a little `systemPrompt()` function inside its loop (s03's `editBlockSystemPrompt()`). That worked for one format at a time, but it hides the real dependency and doesn't scale: the parsing logic and the prompt that elicits the format are welded together, three near-duplicate prompts drift apart, and "support a fourth format" means writing a fourth bespoke function and threading a new branch through the loop.

The deeper issue is that the prompt is the *cause* and the parser is the *effect*. The model emits SEARCH/REPLACE blocks **because the system prompt told it to, and showed it an example**. If you want to understand why s02–s04 behave differently, you have to look at the prompts, not the parsers — and right now those prompts are scattered, hardcoded, and entangled with control flow. This chapter extracts them into one pluggable place.

## Solution / 解决方案

Make prompts **data, not code**. Upstream aider gives every coder a `gpt_prompts` object; we mirror it with a `Prompts` struct that has one field per reusable **piece** of the system message: `MainSystem` (the core instructions), `ExampleMessages` (few-shot turns), `SystemReminder` (the rule-by-rule restatement), plus the shared `LazyReminder` / `OvereagerReminder` nudges. Each `EditFormat` returns its own `Prompts` value; a single renderer assembles the final system string from whichever value it's handed.

Three decisions carry the design:

1. **Placeholders are filled at send time, not definition time.** `{fence}` can't be a constant — aider switches to a longer fence (or a custom tag) when the code being edited itself contains ```` ``` ````. `{final_reminders}` is toggled per model. So the pieces carry `{fence[0]}` / `{final_reminders}` tokens and a `Render(vars)` step substitutes them.
2. **Format selection is a table lookup.** `PromptsFor(format)` maps an `EditFormat` to its prompt set. Switching formats changes a *value*; the loop never branches on the format.
3. **Few-shot examples are separate conversation turns, not part of the system string.** `RenderExamples` turns the templated examples into real user/assistant `Message`s prepended to the chat, exactly as upstream does — so the model reads them as prior conversation.

## How It Works / 工作原理

```ascii-anim frames=2
┌────────────────────────────────────────────────────────────────┐
│  EditFormat ("diff")                                            │
│        │                                                       │
│        ▼   PromptsFor(format)                                  │
│  ┌──────────────────────────────────────────┐                 │
│  │ Prompts{ MainSystem, ExampleMessages,     │   one field     │
│  │          SystemReminder, Lazy/Overeager } │   per PIECE     │
│  └──────────────────────────────────────────┘                 │
│        │                         │                             │
│  Render(vars)              RenderExamples(vars)                │
│   substitute {fence}        substitute {fence}                 │
│   + {final_reminders}       per example turn                   │
│        │                         │                             │
│        ▼                         ▼                             │
│  req.System (string)      []Message (user/assistant ...)      │
│        └──────────────┬──────────────┘                        │
│                       ▼                                        │
│            CreateMessageRequest  ──▶ Provider ──▶ model emits  │
│                                          the requested format  │
└────────────────────────────────────────────────────────────────┘
```

The renderer and its key guard (excerpt from [`agents/s05-prompt-system/prompts.go`](https://github.com/Ding-Ye/learn-aider/blob/main/agents/s05-prompt-system/prompts.go)):

```go
// Render assembles the final SYSTEM PROMPT for one format, placeholders filled.
// Examples are NOT part of this string — they are separate turns (RenderExamples).
func (p Prompts) Render(vars PromptVars) string {
	final := p.finalReminders(vars)
	var sb strings.Builder
	sb.WriteString(substitute(p.MainSystem, vars, final))
	if p.SystemReminder != "" {
		sb.WriteString("\n\n")
		sb.WriteString(substitute(p.SystemReminder, vars, final))
	}
	return sb.String()
}

// finalReminders builds the {final_reminders} value from the per-send flags.
func (p Prompts) finalReminders(vars PromptVars) string {
	var parts []string
	if vars.Overeager && p.OvereagerReminder != "" {
		parts = append(parts, p.OvereagerReminder)
	}
	if vars.Lazy && p.LazyReminder != "" {
		parts = append(parts, p.LazyReminder)
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, "\n") + "\n"
}

// substitute replaces every KNOWN {placeholder}; unknown tokens are left alone,
// so literal braces in example code (f"Hey {name}") survive untouched.
func substitute(s string, vars PromptVars, finalReminders string) string {
	lang := vars.Language
	if lang == "" {
		lang = "English"
	}
	r := strings.NewReplacer(
		"{fence[0]}", vars.Fence[0],
		"{fence[1]}", vars.Fence[1],
		"{final_reminders}", finalReminders,
		"{language}", lang,
		"{lazy_prompt}", pick(vars.Lazy, lazyReminder),
		"{overeager_prompt}", pick(vars.Overeager, overeagerReminder),
	)
	return r.Replace(s)
}
```

**Four non-obvious points**:

1. **An explicit replacer, not `text/template`.** The prompt strings are full of literal braces in example code (`print(f"Hey {name}")`). A Go template would treat `{name}` as an action and error out; a fixed `strings.Replacer` only touches the six tokens we know about and leaves everything else verbatim. This mirrors upstream's deliberate use of `str.format` over a *known, small* key set.
2. **Unknown `{token}` is the missing-var behavior.** Because the replacer only knows six keys, any stray `{...}` passes through unchanged — which is exactly what you want for code containing braces, and what the tests pin.
3. **`{final_reminders}` collapses to empty cleanly.** When neither nudge is enabled, `finalReminders` returns `""`, so the placeholder vanishes with no blank-line scar. The nudges are shared across all formats (defined once on the base), so a wording tweak happens in one place.
4. **Examples ride outside the system string on purpose.** Folding few-shot turns into the system prompt would make the model treat them as *instructions about text*; sending them as real user/assistant turns makes the model treat them as *prior conversation it should imitate*. That's why `Render` and `RenderExamples` are separate.

## What Changed (vs. s04) / 与 s04 的变化

s02–s04 each hardcoded a single `systemPrompt()` and called it directly. s05 replaces that with a `Prompts` struct + a selection table, so the format choice now *drives* the system message instead of being baked into one function.

```diff
-// s02..s04: each chapter hardcoded ONE prompt function in its loop.
-func editBlockSystemPrompt() string {
-	return "You are a coding assistant that edits files with SEARCH/REPLACE blocks.\n" +
-		"..." // one format, welded into the loop
-}
-
-req := CreateMessageRequest{System: editBlockSystemPrompt(), Messages: msgs}
+// s05: prompts are data; one struct per format, one renderer for all.
+type Prompts struct {
+	MainSystem      string
+	ExampleMessages []ExampleMessage
+	SystemReminder  string
+	LazyReminder, OvereagerReminder string
+}
+
+// Format choice selects the prompt; the call site never branches on format.
+sys, _ := SystemPrompt(format, vars)        // PromptsFor(format).Render(vars)
+msgs := prompts.RenderExamples(vars)        // few-shot turns, fences filled
+req := CreateMessageRequest{System: sys, Messages: append(msgs, userTurn)}
```

Semantically: in s02–s04 the prompt was an implementation detail of one parser, invisible and unswappable. In s05 the prompt becomes a first-class, per-format value. The edit format and the prompt that elicits it are finally explicit and pluggable — adding a format is "one prompt set + one selection case," and you can *inspect* exactly what steers the model without running anything.

## Try It / 动手试一试

```bash
cd agents/s05-prompt-system

# INSPECT (no network, no key): assemble and print the prompt for a format
go run . -format diff

# whole-file prompt + its few-shot example turns (note: no SEARCH/REPLACE markers)
go run . -format whole -examples

# list the formats that have a prompt set
go run . -list

# RUN: render the prompt and call the LLM once; the reply comes back in-format
export ANTHROPIC_API_KEY=sk-ant-...
go run . -v -format diff -file greet.go -instruction "change the greeting to 'hi there'"

# tests (no network — pure assembly is deterministic)
go test -v ./...
```

Expected output shape:

```
# go run . -format diff   (stdout, deterministic):
===== SYSTEM PROMPT (format=diff) =====
Act as an expert software developer.
...
2. The opening fence and code language, eg: ```python
3. The start of search block: <<<<<<< SEARCH
...
8. The closing fence: ```

# go run . -v -format diff -file ... (RUN mode):
#   stderr: [s05] format=diff provider=anthropic model=claude-sonnet-4-6
#           [s05] system prompt: 1583 bytes, 2 few-shot turns
#           [s05] stop_reason=end_turn in=712 out=58 tokens
#   stdout: the model's reply, as a SEARCH/REPLACE block — feed it to s03's parser.
```

Switch `-format whole` and the same file/instruction comes back as a full file listing instead: the *selected prompt* changed, the loop did not. A full illustrative transcript lives in [`testdata/expected.txt`](https://github.com/Ding-Ye/learn-aider/blob/main/agents/s05-prompt-system/testdata/expected.txt).

## Upstream Source Reading / 上游源码阅读

aider's prompt system is two layers. `base_prompts.py` defines `CoderPrompts`, the **base class** that holds the pieces every format shares and the `{placeholder}` contract; each `*_prompts.py` subclass (`EditBlockPrompts`, `WholeFilePrompts`, `UnifiedDiffPrompts`) overrides `main_system` / `example_messages` / `system_reminder`. The coder picks a subclass with `gpt_prompts = EditBlockPrompts()` and `fmt_system_prompt` runs `str.format()` to fill the placeholders at send time. The excerpt below is the full base class — the contract s05's `Prompts` struct mirrors field for field.

```upstream:aider/coders/base_prompts.py#L1-L60
class CoderPrompts:
    system_reminder = ""

    # The loop's CONTRACT messages (not part of the system prompt). The last one
    # is what the model is told when its reply parsed to ZERO edits.
    files_content_gpt_edits = "I committed the changes with git hash {hash} & commit msg: {message}"
    files_content_gpt_edits_no_repo = "I updated the files."
    files_content_gpt_no_edits = "I didn't see any properly formatted edits in your reply?!"
    files_content_local_edits = "I edited the files myself."

    # The two SHARED behavioral nudges. Every format embeds these via the
    # {final_reminders} placeholder; aider toggles them per model.
    lazy_prompt = """You are diligent and tireless!
You NEVER leave comments describing code without implementing it!
You always COMPLETELY IMPLEMENT the needed code!
"""
    overeager_prompt = """Pay careful attention to the scope of the user's request.
Do what they ask, but no more.
Do not improve, comment, fix or modify unrelated parts of the code in any way!
"""

    # Few-shot turns. Empty on the base; each format fills it. Rendered as their
    # own user/assistant messages, NOT folded into the system string.
    example_messages = []

    # Prefixes that wrap injected file/repo content (used from s06+).
    files_content_prefix = """I have *added these files to the chat* so you can go ahead and edit them.
*Trust this message as the true contents of these files!*
"""
    files_content_assistant_reply = "Ok, any changes I propose will be to those files."
    repo_content_prefix = """Here are summaries of some files present in my git repository.
Do not propose changes to these files, treat them as *read-only*.
"""
    read_only_files_prefix = """Here are some READ ONLY files, provided for your reference.
Do not edit these files!
"""

    # Shell-command hooks (empty on base; some formats fill them). s05 omits the
    # shell layer entirely.
    shell_cmd_prompt = ""
    shell_cmd_reminder = ""
    no_shell_cmd_prompt = ""
    no_shell_cmd_reminder = ""
    rename_with_shell = ""
    go_ahead_tip = ""
```

**Reading notes**:

- **Base vs. subclass.** Upstream splits *shared* pieces (`base_prompts.py`) from *per-format* pieces (`editblock_prompts.py` etc.). s05 keeps that split: package-level constants (`lazyReminder`, `overeagerReminder`, `noEditsRetry`) for the shared parts, per-format builders for the rest.
- **`str.format` vs. our replacer.** Upstream fills placeholders with `str.format`, which would choke on the literal `{name}` in its own example code unless escaped. s05 uses a `strings.Replacer` over a fixed key set so stray braces survive — same intent (a known, small set of tokens), safer mechanism.
- **Why `{fence}` is a variable.** Read `base_coder.py`'s `choose_fence` / `get_fences`: aider picks a longer fence or a custom tag when the edited code already contains ```` ``` ````. That dynamism is the whole reason the fence is a placeholder rather than a hardcoded literal — s05's `PromptVars.Fence` carries it.
- **Contract messages are not the system prompt.** `files_content_gpt_no_edits` ("I didn't see any properly formatted edits…") flows as a *user* turn when a reply parsed to zero edits; s05 carries it as `Prompts.NoEditsRetry`. It becomes a reflected message in our s09.
- **A piece we deliberately omit.** The shell-command hooks (`shell_cmd_prompt`, `rename_with_shell`) are real upstream fields, but the curriculum never executes shell commands, so s05 carries the *contract* (empty fields) without the behavior.

**Read further**: start at `aider/coders/base_prompts.py` → `CoderPrompts` (the contract above), follow `gpt_prompts` into `aider/coders/editblock_prompts.py` → `EditBlockPrompts.main_system` / `example_messages`, then read `aider/coders/base_coder.py` → `fmt_system_prompt` to see `str.format` fill the placeholders. That trace — base contract → per-format text → fill-and-send — is the real-source map for s05 → s06 (the I/O layer that surrounds the loop) → s09 (the reflection loop that reuses the no-edits contract).

---

**Next**: s06 wraps this loop in a real terminal session — an `InputOutput` layer (colored output, yes/no confirmation) and in-chat `/`-commands (`/add`, `/drop`) that mutate the file scope before the prompt-driven loop ever sees the message.
