---
title: "s01 · The minimum coder loop"
chapter: 1
slug: s01-minimum-coder-loop
est_read_min: 11
---

# s01 · The minimum coder loop

> What this teaches: the *coder loop* — the smallest cycle (read → prompt → call → extract → write) that lets an LLM change a file on disk. This is aider's spine; every later chapter is a layer on top of it, so it gets its own chapter.

---

## Problem / 问题

aider is an AI pair programmer: you describe a change in English, it edits your files and commits them. But the real codebase is ~13k lines spread across edit-format parsers, a tree-sitter repo map, git integration, a linter-and-reflect loop, token budgeting, and a 50-model provider layer. If you start reading there, you drown.

So ask the smallest possible question: **what is the least code that lets an LLM change one file on disk?** Not "how does aider do everything" — just the single load-bearing cycle. Strip away git, the repo map, multi-file edits, streaming, and reflection. What remains is one user instruction, one LLM call, and one applied edit. That irreducible cycle is the *coder loop*, and once you can see it clearly, every feature aider adds later has an obvious place to hang.

## Solution / 解决方案

A coder loop is five beats: **read the target file → build a prompt that pins an edit format → call the `Provider` → extract one edit from the reply → write the file back.** That's it. Upstream this is `Coder.run → run_one → send_message → apply_updates`; we collapse the whole chain into one method, `Coder.Run`.

The trick that makes it tiny is the **edit format**. Rather than teach the model a tool-call protocol, we *steer* it (via the system prompt) to reply with the entire new file inside one fenced code block. We then parse that block. This is aider's `whole` format — its simplest — and it is the lowest-common-denominator output that every model can produce, even ones without tool-calling.

Three design decisions worth naming:

1. **Whole-file, not diff.** Applying the edit is a single `os.WriteFile` — no text matching, no merging. All the intelligence lives in the parser, so the *apply* step is trivial. (Diffs and SEARCH/REPLACE come in s03–s04.)
2. **`Provider` is an interface from line one.** The loop never names a concrete HTTP client, so tests inject a fake and s10 can wrap retries + streaming behind the same interface.
3. **One block, one file.** s01 parses exactly the first fenced block. Multi-file replies (scanning for a filename above each fence) are deliberately deferred to s02 — keeping the parser honest about how small the baseline is.

## How It Works / 工作原理

```ascii-anim frames=2
┌──────────────────────────────────────────────────────────────┐
│                      Coder.Run(path, instruction)            │
│                                                              │
│  disk ──read──▶ [original] ──┐                               │
│                              ▼                               │
│  system prompt ─────▶ CreateMessageRequest ──▶ Provider      │
│  (whole-file rules)          ▲                    │          │
│                              │                    ▼          │
│  instruction ────────────────┘            CreateMessageResp  │
│                                                   │          │
│                                                   ▼          │
│                            extractCodeBlock(reply) → Edit    │
│                                                   │          │
│                              applyWholeFile ──────┘          │
│                                   │                          │
│                                   ▼                          │
│                              disk [rewritten]                │
└──────────────────────────────────────────────────────────────┘
```

The core ~40 lines (excerpt from [`agents/s01-minimum-coder-loop/coder.go`](https://github.com/Ding-Ye/learn-aider/blob/main/agents/s01-minimum-coder-loop/coder.go)):

```go
func (c *Coder) Run(ctx context.Context, path, instruction string) error {
	// 1. READ the target file (the model needs the current contents).
	original, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}

	// 2. BUILD the prompt. System prompt = the edit-format contract; user
	//    message = file + instruction.
	req := CreateMessageRequest{
		System: wholeFileSystemPrompt(path),
		Messages: []Message{{
			Role:    "user",
			Content: []ContentBlock{{Type: "text", Text: buildUserPrompt(path, string(original), instruction)}},
		}},
	}

	// 3. CALL the provider — the single network round-trip.
	resp, err := c.Provider.CreateMessage(ctx, req)
	if err != nil {
		return fmt.Errorf("provider call: %w", err)
	}
	reply := firstText(resp.Content)

	// 4. EXTRACT the edit: whole-file = the first fenced code block.
	edit, err := c.extractEdit(path, reply)
	if err != nil {
		fmt.Fprintf(c.errw, "[s01] could not parse an edit from the reply:\n%s\n", reply)
		return err // no reflection in s01 — surface and stop
	}

	// 5. WRITE it back. One edit, one file.
	if err := c.applyWholeFile(edit); err != nil {
		return fmt.Errorf("apply edit: %w", err)
	}
	fmt.Fprintf(c.out, "Applied edit to %s (%d bytes)\n", edit.Path, len(edit.Replace))
	return nil
}
```

**Four non-obvious points**:

1. **The fence is chat syntax, not file content.** The model writes ` ```go\n<code>\n``` ` but the file on disk must contain only `<code>`. `extractCodeBlock` strips both fence lines (and the language tag on the opening fence) — forget this and you write backticks into the user's source.
2. **We take only the *first* block.** Whole-file is "one file per reply." Taking the first block (rather than concatenating all of them) keeps s01's parser ~10 lines; s02 generalizes to many blocks by reading the filename on the line above each fence.
3. **An unterminated fence is malformed, not partial.** If there's an opening ` ``` ` but no closing one, `extractCodeBlock` returns `false`. s01 has no streaming, so a half-block is simply a bad reply.
4. **No reflection — on purpose.** Upstream a malformed reply becomes a `reflected_message` and the loop retries (bounded by `max_reflections`). s01 prints the raw reply and exits non-zero. That self-correction loop is s09; leaving it out here keeps the cycle to five beats.

## What Changed / 与上一节的变化

This is the **baseline chapter** — there is no previous chapter to diff against. Everything later extends the types introduced here. The core shapes are:

```go
// The generic LLM core (Anthropic wire shape), shared by every chapter:
type Message struct {
	Role    string         `json:"role"`
	Content []ContentBlock `json:"content"`
}
type Provider interface {
	CreateMessage(ctx context.Context, req CreateMessageRequest) (*CreateMessageResponse, error)
}

// The aider-specific pieces this chapter introduces:
type EditFormat string
const FormatWhole EditFormat = "whole" // s03 adds "diff", s04 adds "udiff"

type Edit struct {
	Path    string
	Search  string // "" means whole-file replace (s01 always leaves this empty)
	Replace string // the entire new file
}
```

Semantically: s01 establishes the `Provider` boundary and the `read → call → extract → write` skeleton. s02 turns the inline `extractCodeBlock` into a reusable multi-file `WholeFileCoder`; s03 makes `Edit.Search` non-empty (SEARCH/REPLACE); s07 adds a git auto-commit after the write; s09 wraps the whole thing in a reflection loop.

## Try It / 动手试一试

```bash
cd agents/s01-minimum-coder-loop

# set ONE provider's key
export ANTHROPIC_API_KEY=sk-ant-...

# read hello.go, ask the model to rewrite it, write it back
go run . hello.go "add a doc comment to main"

# -v prints the request/response shape on stderr
go run . -v hello.go "rename foo to bar"

# swap providers — only the transport changes
export DEEPSEEK_API_KEY=sk-...
go run . -provider deepseek hello.go "fix the typo"

# tests (no network — a fakeProvider stands in for the LLM)
go test -v ./...
```

Expected output shape:

```
# stderr, with -v:
[s01] provider=anthropic model=claude-sonnet-4-6 url=
[s01] sending 62 bytes of hello.go + instruction "add a doc comment to main"
[s01] stop_reason=end_turn in=512 out=78 tokens

# stdout:
Applied edit to hello.go (96 bytes)
```

If the model replies without a fenced block, you'll instead see `[s01] could not parse an edit from the reply:` followed by the raw text and a non-zero exit — the file is left untouched. (A full illustrative transcript lives in [`testdata/expected.txt`](https://github.com/Ding-Ye/learn-aider/blob/main/agents/s01-minimum-coder-loop/testdata/expected.txt).)

## Upstream Source Reading / 上游源码阅读

aider's equivalent is `WholeFileCoder.get_edits` (the parser) and `apply_edits` (the writer). The big difference: upstream parses **many** fenced blocks and recovers a **filename** from the line above each fence, with priority rules for which name wins. s01 already knows the path (from argv) and parses one block, so it skips all of that.

```upstream:aider/coders/wholefile_coder.py#L22-L128
class WholeFileCoder(Coder):
    edit_format = "whole"              # selects the prompt + this parser

    def get_edits(self, mode="update"):
        content = self.get_multi_response_content_in_progress()  # the raw reply
        chat_files = self.get_inchat_relative_files()            # editable files
        lines = content.splitlines(keepends=True)
        edits = []

        fname = None          # None => outside a block; set => inside, accumulating
        fname_source = None
        new_lines = []
        for i, line in enumerate(lines):
            if line.startswith(self.fence[0]) or line.startswith(self.fence[1]):
                if fname is not None:                 # closing fence -> emit block
                    edits.append((fname, fname_source, new_lines))
                    fname, fname_source, new_lines = None, None, []
                    continue
                if i > 0:                             # opening fence -> name is line above
                    fname_source = "block"
                    fname = lines[i - 1].strip().strip("*").rstrip(":").strip("`").lstrip("#").strip()
                    if len(fname) > 250:              # Issue #1232: absurd name -> drop
                        fname = ""
                    if fname and fname not in chat_files and Path(fname).name in chat_files:
                        fname = Path(fname).name      # collapse bogus path/to/ prefix
                if not fname:                         # bare ``` with no usable name
                    if len(chat_files) == 1:
                        fname, fname_source = chat_files[0], "chat"
                    else:
                        raise ValueError("No filename provided before fence")
            elif fname is not None:
                new_lines.append(line)                # inside a block: file body
        if fname:                                     # reply ended mid-block
            edits.append((fname, fname_source, new_lines))
        return edits  # (upstream then de-dupes by source priority: block > saw > chat)

    def apply_edits(self, edits):
        for path, fname_source, new_lines in edits:
            full_path = self.abs_root_path(path)
            self.io.write_text(full_path, "".join(new_lines))  # s01: os.WriteFile
```

**Reading notes**:

- **Filename recovery.** Upstream extracts the filename from the line above the fence (stripping `**`, backticks, `#`, trailing `:`). We pass the path on the CLI, so s01 needs none of this — that machinery is exactly what s02 rebuilds.
- **Many blocks vs. one.** Upstream's state machine loops over every fence to support multi-file replies; s01 returns after the first block. Same idea, smaller scope.
- **De-dupe priority.** Upstream ranks filename sources `block > saw > chat` so the most reliable name wins. With one known path there's no ambiguity to resolve.
- **`apply_edits` is trivial — by design.** It's a `join + write`. The whole reason whole-file is the baseline format is that *applying* an edit is a no-op once you've parsed it; the difficulty is all in the parse.
- **A deliberately imperfect choice we keep.** Like upstream, if the reply ends mid-block we still emit it (`if fname:` at the end). s01 mirrors this leniency but draws the line at a *missing opening* fence, which it rejects as malformed.

**Read further**: start at `aider/coders/base_coder.py` → `run_one` (L924), follow `send_message` (L1419) into `apply_updates` (L2296), which calls the `get_edits` / `apply_edits` above. That chain — `run → run_one → send_message → apply_updates → get_edits/apply_edits` — is the real-source map for s01 → s02 (whole-file parser) → s07 (auto-commit) → s09 (reflection).

---

**Next**: s02 turns this chapter's inline `extractCodeBlock` into a reusable `WholeFileCoder` that handles many files and recovers filenames from the line above each fence — aider's real whole-file format.
