---
title: "s02 · Whole-file edit format"
chapter: 2
slug: s02-wholefile-format
est_read_min: 12
---

# s02 · Whole-file edit format

> What this teaches: aider's simplest *edit format*, the `whole` format — the LLM returns the complete rewritten file(s), each as a fenced block with its path on the line above. This chapter turns s01's one-block toy parser into the real multi-file `WholeFileCoder` and formalizes the `Coder` interface, so it earns its own chapter.

---

## Problem / 问题

s01 proved the coder loop works, but its parser is a toy. It assumed exactly one file (the path came from `argv`) and grabbed the first fenced code block, fences stripped. That is enough to demo "rewrite this one file," and nothing more.

The real whole-file format does three things s01 cannot. First, a single reply can rewrite **several files** at once — a refactor that moves code from `main.go` into `util.go` is two files in one response. Second, the only place the **filename** appears is the line *above* each fence, and the model dresses it up: `**foo.go**`, `` `foo.go` ``, `# foo.go`, or `foo.go:`. Third, the model sometimes emits a bare fence with no name at all, or echoes a bogus `path/to/` prefix copied from the prompt's example. A toy that ignores all this will write the wrong file, write fences into your source, or silently drop edits. s02's job is to handle the format the way aider actually does.

## Solution / 解决方案

Promote s01's inline `extractCodeBlock` into a reusable `WholeFileCoder` that satisfies the curriculum's `Coder` interface (`Format`, `SystemPrompt`, `GetEdits`, `ApplyEdits`). The parsing lives in `GetEdits`, a small line-by-line **state machine**: walking the reply, a fence line toggles between "outside a block" and "inside a block accumulating the file body," and each *closing* fence emits one parsed file. Applying stays a trivial write-per-file, because in whole-file the parsed `Replace` already *is* the entire new file.

Three design decisions worth naming:

1. **The filename comes from the line above the fence.** On every *opening* fence we read the previous line and clean it (`cleanFilename` strips `**`, backticks, `#`, trailing `:`). This single rule is the whole mechanism — get it right and multi-file "just works."
2. **Fallbacks in reliability order.** A bare fence with no usable name falls back to a filename mentioned earlier in prose (`saw`), then the sole chat file (`chat`), then an error. When two blocks name the same file, the most reliable *source* wins (`block` > `saw` > `chat`). This mirrors upstream exactly.
3. **Parse is hard, apply is trivial.** All the intelligence is in `GetEdits`; `ApplyEdits` is `os.WriteFile` per file. That asymmetry is the defining property of whole-file and the reason it's the baseline before SEARCH/REPLACE (s03) and unified diff (s04).

## How It Works / 工作原理

```ascii-anim frames=2
┌──────────────────────────────────────────────────────────────┐
│  GetEdits(reply)  — a line-by-line state machine             │
│                                                              │
│   reply lines                                                │
│      │                                                       │
│      ▼      fence? ──no──▶ inside block? ──yes──▶ body += ln │
│   ┌──────┐                      │ no                         │
│   │ scan │                      ▼                            │
│   └──────┘            prose: note `chat-file` → saw_fname    │
│      │ fence?                                                │
│      ▼ yes                                                   │
│   inside? ──yes──▶ EMIT (fname, body); reset ───┐           │
│      │ no                                         │           │
│      ▼                                            │           │
│   open: fname = cleanFilename(prev line)          │           │
│         └─ empty? → saw_fname / sole chat / ERR   │           │
│                                                   ▼           │
│                                  refine: block > saw > chat   │
│                                          → []Edit{Path,Replace}│
└──────────────────────────────────────────────────────────────┘
```

The core ~45 lines (excerpt from [`agents/s02-wholefile-format/wholefile.go`](https://github.com/Ding-Ye/learn-aider/blob/main/agents/s02-wholefile-format/wholefile.go)):

```go
for i, raw := range lines {
	line := raw
	isFence := strings.HasPrefix(line, c.fence[0]) || strings.HasPrefix(line, c.fence[1])

	switch {
	case isFence && inBlock:
		// Closing fence: emit the block we were accumulating and reset.
		edits = append(edits, blockEdit{fname: fname, source: source, body: strings.Join(body, "\n")})
		fname, source, body, inBlock = "", "", nil, false

	case isFence && !inBlock:
		// Opening fence: the filename is on the PREVIOUS line.
		if i > 0 {
			fname = cleanFilename(lines[i-1])
			source = "block"
			if len(fname) > 250 { // Issue #1232: absurd name -> drop
				fname = ""
			}
			// Collapse a bogus "path/to/" prefix to the basename.
			if fname != "" && !c.isChatFile(fname) && c.isChatFile(filepath.Base(fname)) {
				fname = filepath.Base(fname)
			}
		}
		if fname == "" { // bare fence: recover in reliability order
			switch {
			case sawFname != "":
				fname, source = sawFname, "saw"
			case len(c.chatFiles) == 1:
				fname, source = c.chatFiles[0], "chat"
			default:
				return nil, fmt.Errorf("no filename provided before %s in reply", c.fence[0])
			}
		}
		inBlock = true

	case inBlock:
		body = append(body, line) // inside a block: file body

	default:
		// Prose: note a backtick-quoted chat file so a later bare fence can use it.
		for _, word := range strings.Fields(line) {
			word = strings.TrimRight(word, ".:,;!")
			for _, cf := range c.chatFiles {
				if word == "`"+cf+"`" {
					sawFname = cf
				}
			}
		}
	}
}
```

**Four non-obvious points**:

1. **The filename is the line *before* the fence, not after.** Markdown convention puts the path as a heading/label above the code block. We read `lines[i-1]` on the opening fence — and clean it, because the model wraps it in `**`, backticks, `#`, or a trailing `:`. Miss the cleanup and `**foo.go**` is treated as a different file than `foo.go`.
2. **`saw_fname` is set from *prose*, not blocks.** Before any fence, we scan plain text for a backtick-quoted chat file (`update `foo.go`:`). That's the only way a *bare* fence later in the reply can still find its target. It's a fallback, so it loses to a real filename above the fence.
3. **A reply that ends mid-block still emits.** If the model runs out of tokens with the closing fence missing, we keep the partial file (`if inBlock && fname != ""` after the loop). Upstream does the same — a partial file beats discarding the edit. Deliberately lenient.
4. **De-dupe by source priority, not by order.** `refine` processes sources `block > saw > chat` so a confidently-named block always beats a fallback for the same path. The last-seen block does *not* automatically win; reliability does.

## What Changed / 与上一节的变化

s01 had the parser inline in `Coder.Run`, returned one `Edit`, and wrote one file. s02 extracts a reusable `WholeFileCoder` behind the `Coder` interface and goes multi-file end to end:

```diff
-// s01: inline, single block, path already known from argv
-func (c *Coder) extractEdit(path, reply string) (Edit, error) {
-	body, ok := extractCodeBlock(reply) // FIRST fenced block only
-	if !ok {
-		return Edit{}, fmt.Errorf("no fenced code block found in reply")
-	}
-	return Edit{Path: path, Search: "", Replace: body}, nil
-}
+// s02: a Coder implementation that parses MANY files and recovers each filename
+type WholeFileCoder struct {
+	root      string
+	chatFiles []string
+	fence     [2]string
+}
+func (c *WholeFileCoder) GetEdits(response string) ([]Edit, error) { /* state machine */ }
+func (c *WholeFileCoder) ApplyEdits(edits []Edit) (applied, failed []Edit, err error)
```

Semantically: the parser stops being a one-shot helper and becomes a *strategy object*. The loop in `main.go` now calls `GetEdits` (many edits) and iterates `ApplyEdits` instead of writing a single file. `ApplyEdits` also returns `applied`/`failed` slices so a later chapter (s09's reflection) can report partial success. The `Edit` shape is unchanged — whole-file still leaves `Search` empty; s03 is where `Search` finally becomes non-empty.

## Try It / 动手试一试

```bash
export ANTHROPIC_API_KEY=sk-ant-...
cd agents/s02-wholefile-format

# rewrite a single file (same shape as s01)
go run . hello.go "add a doc comment to main"

# the new trick: rewrite TWO files from one reply; -v shows token usage
go run . -v main.go util.go "move the parser into util.go"

# tests (no network — a fakeProvider stands in for the LLM)
go test -v ./...
```

Expected output shape:

```
# stderr, with -v:
[s02] provider=anthropic model=claude-sonnet-4-6 files=[main.go util.go]
[s02] stop_reason=end_turn in=412 out=96 tokens

# stdout:
Applied edit to main.go (32 bytes)
Applied edit to util.go (33 bytes)
```

Two files written from one reply — that's the whole point. If the reply contains a bare ```` ``` ```` with no filename and you have more than one file in chat, you'll instead see `no filename provided before ``` in reply` and nothing is written. A full illustrative transcript lives in [`testdata/expected.txt`](https://github.com/Ding-Ye/learn-aider/blob/main/agents/s02-wholefile-format/testdata/expected.txt).

## Upstream Source Reading / 上游源码阅读

aider's equivalent is the whole 144-line `wholefile_coder.py`. `WholeFileCoder.get_edits` (the state machine) and `apply_edits` (the writer) are what `wholefile.go` mirrors line for line; we drop only `do_live_diff` (streaming diff display, our s10's concern) and the de-dupe is identical.

```upstream:aider/coders/wholefile_coder.py#L22-L128
class WholeFileCoder(Coder):
    edit_format = "whole"              # selects the prompt + this parser

    def get_edits(self, mode="update"):
        content = self.get_multi_response_content_in_progress()  # the raw reply
        chat_files = self.get_inchat_relative_files()            # editable files
        lines = content.splitlines(keepends=True)
        edits = []

        saw_fname = None      # a filename mentioned in prose
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
                    if saw_fname:
                        fname, fname_source = saw_fname, "saw"
                    elif len(chat_files) == 1:
                        fname, fname_source = chat_files[0], "chat"
                    else:
                        raise ValueError("No filename provided before fence")
            elif fname is not None:
                new_lines.append(line)                # inside a block: file body
            else:
                for word in line.strip().split():     # prose: note backtick-quoted chat files
                    word = word.rstrip(".:,;!")
                    for chat_file in chat_files:
                        if word == f"`{chat_file}`":
                            saw_fname = chat_file
        if fname:                                     # reply ended mid-block -> still emit
            edits.append((fname, fname_source, new_lines))

        seen = set()                                  # de-dupe by source priority
        refined_edits = []
        for source in ("block", "saw", "chat"):
            for fname, fname_source, new_lines in edits:
                if fname_source != source or fname in seen:
                    continue
                seen.add(fname)
                refined_edits.append((fname, fname_source, new_lines))
        return refined_edits

    def apply_edits(self, edits):
        for path, fname_source, new_lines in edits:
            full_path = self.abs_root_path(path)
            self.io.write_text(full_path, "".join(new_lines))  # our s02: os.WriteFile
```

**Reading notes**:

- **Filename cleanup is byte-identical.** Upstream chains `.strip("*").rstrip(":").strip("`").lstrip("#")`; our `cleanFilename` does the same trims in the same order. We split this onto its own named function only for readability.
- **`keepends=True` vs. our split-on-`\n`.** Upstream keeps line terminators so `"".join(new_lines)` reproduces the file exactly. We `strings.Split` on `\n` and `strings.Join` back with `\n`, which is equivalent for `\n`-terminated files (the common case); a real port would preserve `\r\n` like upstream.
- **`do_live_diff` omitted.** Upstream's `get_edits(mode="diff")` renders an incremental diff while the response streams. That belongs to streaming output (our s10), so s02 only implements `mode="update"`.
- **The error case is preserved, not smoothed.** With multiple chat files and a nameless fence, upstream `raise ValueError` and we `return ...err`. We could guess by diff size (upstream's own TODO at L81), but mirroring the honest failure keeps the lesson clear.
- **`apply_edits` is deliberately a no-op-grade write.** Both upstream and ours just join + write. That's not laziness — it's the thesis of the whole-file format, and it's exactly what stops being true in s03.

**Read further**: start at `aider/coders/wholefile_coder.py` → `get_edits`, then compare `aider/coders/editblock_coder.py` → `find_original_update_blocks` (L439-560), follow it into `do_replace` (L364) and `replace_most_similar_chunk` (L157). That trace is the real-source map for s02 → s03 (SEARCH/REPLACE) → s04 (unified diff), where parsing gets harder and "apply" stops being a plain write.

---

**Next**: s03 swaps whole-file rewrites for SEARCH/REPLACE blocks, so the model touches only the lines that change — and `Edit.Search` becomes non-empty, forcing a fuzzy matcher to apply edits despite whitespace drift.
