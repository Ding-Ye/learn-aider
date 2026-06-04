---
title: "s04 · Unified diff edit format"
chapter: 4
slug: s04-unified-diff-format
est_read_min: 14
---

# s04 · Unified diff edit format

> What this teaches: the *unified-diff edit format* — the `git diff` shape (`--- / +++ / @@` with `-`/`+` lines) as a third way for the model to express edits. We parse the hunks, derive each hunk's before/after text, and reuse s03's fuzzy matcher to apply it — **locating the hunk by its context lines and deliberately ignoring the `@@` line numbers**, because that is the part LLMs get wrong.

---

## Problem / 问题

s03 gave us SEARCH/REPLACE blocks and the fuzzy matcher that makes them apply. But SEARCH/REPLACE is aider's *own* fence syntax (`<<<<<<< SEARCH` … `>>>>>>> REPLACE`). Many models — especially ones heavily trained on GitHub — are far more fluent in the **unified diff** format, the thing `git diff` prints and `patch(1)` consumes. Asking those models for a format they already "speak" yields cleaner, more reliable edits. So aider ships a `udiff` coder too.

The catch is the part of a unified diff that looks most authoritative: the hunk header `@@ -12,4 +13,5 @@`, which claims the change starts at line 12 and spans 4 old / 5 new lines. An LLM computes those numbers *from memory*, by counting — and counting is exactly what language models are worst at. The line numbers are almost always slightly wrong. A strict patcher like `patch(1)` would reject the hunk outright. This chapter's real work is applying a model-generated diff *despite* its bogus line numbers, by throwing the numbers away and anchoring on something the model actually quotes correctly: the surrounding context lines.

## Solution / 解决方案

A unified-diff hunk is a list of lines, each tagged by its first character: a leading space for an unchanged **context** line, `-` for a removed line, `+` for an added line. **Parsing** scans the reply for a ```diff fence and splits its body into `(path, hunk)` pairs at `@@` boundaries. The path comes from the `--- a/x` / `+++ b/x` header (git's `a/`,`b/` prefixes stripped).

The key insight is that a hunk reduces to s03's problem. **Deriving** before/after text (`hunkToBeforeAfter`) gives you: `before` = context + `-` lines (what the file holds now), `after` = context + `+` lines (what it should hold). From there, applying a hunk *is* search/replace — locate `before`, splice `after` — so we reuse s03's tiered matcher untouched. Three decisions follow:

1. **Ignore the `@@` line numbers entirely.** We never read `-a,b +c,d`. The hunk is located by matching its `before` text (context + removals) in the file, wherever that text actually sits.
2. **Make the context match whitespace-flexible.** The model re-indents the context it quotes, exactly the s03 mistake — so the same exact → whitespace-tolerant tiers apply.
3. **Drop pure-context hunks; keep failures soft.** A hunk with no `-`/`+` changes nothing and is discarded. A hunk whose context can't be found leaves the file untouched and is reported in `failed`.

## How It Works / 工作原理

```ascii-anim frames=2
┌──────────────────────────────────────────────────────────────┐
│              reply text  ──►  ```diff … ``` fence             │
│                          │                                   │
│         findDiffs / processFencedBlock                       │
│                          │   path from --- a/x +++ b/x        │
│                          ▼   hunks split at @@ ... @@         │
│            []Hunk{Path, Lines: " ctx" "-old" "+new"}         │
│                          │                                   │
│         hunkToBeforeAfter │  ' '→both  '-'→before  '+'→after  │
│                          ▼   before = ctx+removed             │
│                              after  = ctx+added               │
│              ┌───────────────────────────┐                  │
│   ApplyEdits │ locate `before` by CONTEXT │  (@@ nums ignored)│
│              │ via s03 tiered matcher:    │                  │
│              │  exact → whitespace-flex   │── hit ──▶ splice │
│              │  else ─────────────────────┼── miss ─▶ failed │
│              └───────────────────────────┘     (file kept)   │
└──────────────────────────────────────────────────────────────┘
```

The derive-then-reuse step (excerpt from [`agents/s04-unified-diff-format/udiff.go`](https://github.com/Ding-Ye/learn-aider/blob/main/agents/s04-unified-diff-format/udiff.go)):

```go
// hunkToBeforeAfter splits a hunk's diff lines into BEFORE (context + removed)
// and AFTER (context + added). This turns a unified-diff hunk into the s03
// search/replace problem. Upstream: hunk_to_before_after (L403-429).
func hunkToBeforeAfter(h Hunk) (before, after string) {
	var b, a strings.Builder
	for _, line := range h.Lines {
		op := byte(' ')
		body := line
		if len(line) >= 2 {
			op, body = line[0], line[1:]
		}
		switch op {
		case ' ': // context: an anchor present on BOTH sides
			b.WriteString(body)
			a.WriteString(body)
		case '-': // removed: BEFORE only
			b.WriteString(body)
		case '+': // added: AFTER only
			a.WriteString(body)
		}
	}
	return b.String(), a.String()
}

// applyOne locates the hunk by its context (NOT the @@ numbers) and splices.
func (c *Coder) applyOne(h Hunk) (bool, error) {
	full := c.absPath(h.Path)
	before, after := hunkToBeforeAfter(h)

	// A hunk with only `+` lines has empty before-text: create / append.
	if strings.TrimSpace(before) == "" {
		existing, _ := os.ReadFile(full)
		return true, os.WriteFile(full, []byte(string(existing)+after), 0o644)
	}
	content, err := os.ReadFile(full)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil // referenced a file we don't have -> soft failure
		}
		return false, err
	}
	// THE KEY STEP: reuse s03's tiered, whitespace-flexible matcher on the
	// hunk's context. The @@ -a,b +c,d numbers are never consulted.
	updated, ok := replaceMostSimilarChunk(string(content), before, after)
	if !ok {
		return false, nil // context not found -> soft failure, file untouched
	}
	return true, os.WriteFile(full, []byte(updated), 0o644)
}
```

**Four non-obvious points**:

1. **The `@@` numbers are never read.** `applyOne` calls `replaceMostSimilarChunk(content, before, after)` — it searches the whole file for the `before` text. The header's line numbers (`-a,b +c,d`) are parsed past and discarded in `processFencedBlock`. This is the whole point: the model's line arithmetic is unreliable, its quoted context is not.
2. **Context lines are load-bearing, so the model must emit some.** A hunk that is *only* `-`/`+` with no surrounding ` ` lines has a thin `before` text; if it's short and non-unique, the match is ambiguous and fails. That is *why* the system prompt asks for a few context lines — they are the anchor, not decoration.
3. **A pure-`+` hunk means create/append.** When `before` is blank (no context, no removals), there is nothing to locate, so `applyOne` short-circuits to write/append — the same create-file path as s03's empty SEARCH and upstream `do_replace`.
4. **We reuse s03's matcher instead of upstream's partial-hunk machinery.** Upstream `apply_hunk` has an elaborate fallback (`apply_partial_hunk`) that progressively drops context lines and retries. s04 deliberately stops at s03's exact → whitespace tiers: the load-bearing idea (match by context, ignore numbers, tolerate indent drift) is identical, and the extra machinery isn't needed to teach it.

## What Changed (vs. s03) / 与 s03 的变化

s03 parsed an explicit `(path, search, replace)` from SEARCH/REPLACE fences. s04 parses `(path, hunk)` from a `git diff`, then **derives** search/replace from the hunk's `-`/`+`/context lines. The *applier* — the tiered fuzzy matcher — is the same code; only how before/after are obtained changed.

```diff
-// s03: the model writes SEARCH and REPLACE explicitly; we parse them directly.
-func (c *Coder) GetEdits(reply string) ([]Edit, error) {
-	// scan for <<<<<<< SEARCH ... ======= ... >>>>>>> REPLACE
-	// each block IS an Edit{Path, Search, Replace}
-}
+// s04: the model writes a unified diff; we parse hunks, then DERIVE before/after.
+func (c *Coder) GetEdits(reply string) ([]Hunk, error) {
+	hunks := findDiffs(reply)          // ```diff fences -> []Hunk{Path, Lines}
+	// (filename made sticky across hunks)
+	return hunks, nil
+}
+
+// before = context + '-' lines ; after = context + '+' lines
+func hunkToBeforeAfter(h Hunk) (before, after string) { /* ... */ }

 // UNCHANGED from s03: locate `before` in the file, splice `after`.
 updated, ok := replaceMostSimilarChunk(string(content), before, after)
```

Semantically: s03's SEARCH section *is* the before-text, stated outright. In s04 the before-text is implicit — it's whatever the context and `-` lines add up to — so there's an extra derivation step, but the downstream apply is identical. The chapter shows that "edit format" is a thin parsing concern layered over one shared matching engine.

## Try It / 动手试一试

```bash
cd agents/s04-unified-diff-format

# set ONE provider's key
export ANTHROPIC_API_KEY=sk-ant-...

# read greet.go, ask for a change, apply the ```diff hunks
go run . greet.go "change the greeting to 'hi there'"

# -v prints the request/response shape on stderr
go run . -v greet.go "add error handling to readConfig"

# -root resolves parsed file paths under a directory
go run . -root /tmp greet.go "rename foo to bar"

# tests (no network — a fakeProvider stands in for the LLM)
go test -v ./...
```

Expected output shape:

```
# stderr, with -v:
[s04] provider=anthropic model=claude-sonnet-4-6 url=
[s04] sending 118 bytes of greet.go + instruction "change the greeting to 'hi there'"
[s04] stop_reason=end_turn in=611 out=72 tokens

# stdout:
Applied hunk to greet.go
```

If a hunk's context matches nothing, you'll see `FAILED to apply hunk to greet.go — its context did not match (file left untouched)` and a non-zero exit — the file is never half-edited, and a wrong `@@` number never matters because it's ignored. (A full illustrative transcript lives in [`testdata/expected.txt`](https://github.com/Ding-Ye/learn-aider/blob/main/agents/s04-unified-diff-format/testdata/expected.txt).)

## Upstream Source Reading / 上游源码阅读

aider's parser is `find_diffs` → `process_fenced_block`, and the derive step is `hunk_to_before_after`. The apply step (`do_replace` → `apply_hunk`) feeds the derived before/after into the same `flexible_search_and_replace` engine that the editblock coder uses. The big differences from s04: upstream `apply_hunk` adds a `apply_partial_hunk` fallback that progressively drops context lines to retry a stubborn hunk, dedups identical hunks, and emits `UnifiedDiffNoMatch` / `UnifiedDiffNotUnique` messages tailored for the model. s04 keeps the load-bearing parse + derive + context-match and drops the partial-hunk retries.

```upstream:aider/coders/udiff_coder.py#L337-L400
def process_fenced_block(lines, start_line_num):
    # Find the closing ``` fence.
    for line_num in range(start_line_num, len(lines)):
        if lines[line_num].startswith("```"):
            break

    block = lines[start_line_num:line_num]
    block.append("@@ @@")              # sentinel so the LAST hunk gets flushed

    # A leading `--- a/x` / `+++ b/x` pair names the file. Strip git's a//b/ (or
    # /dev/null) prefixes to recover the real path.
    if block[0].startswith("--- ") and block[1].startswith("+++ "):
        a_fname = block[0][4:].strip()
        b_fname = block[1][4:].strip()
        if (a_fname.startswith("a/") or a_fname == "/dev/null") and b_fname.startswith("b/"):
            fname = b_fname[2:]
        else:
            fname = b_fname
        block = block[2:]
    else:
        fname = None                   # sticky filename, filled in by get_edits

    edits = []
    keeper = False                     # does this hunk contain a -/+ change?
    hunk = []
    for line in block:
        hunk.append(line)
        if len(line) < 2:
            continue

        # A fresh `--- / +++` header mid-block: flush the hunk, switch file.
        if line.startswith("+++ ") and hunk[-2].startswith("--- "):
            hunk = hunk[:-3] if hunk[-3] == "\n" else hunk[:-2]
            edits.append((fname, hunk))
            hunk = []
            keeper = False
            fname = line[4:].strip()
            continue

        op = line[0]
        if op in "-+":
            keeper = True              # this hunk actually changes something
            continue
        if op != "@":
            continue                   # a normal context line — accumulate
        if not keeper:
            hunk = []                  # `@@` but no change yet: drop pure context
            continue

        hunk = hunk[:-1]               # drop the `@@` line, flush the hunk
        edits.append((fname, hunk))
        hunk = []
        keeper = False

    return line_num + 1, edits
```

**Reading notes**:

- **The `@@` line is a *separator*, not data.** Upstream treats `@@` only as the signal to flush the current hunk (`hunk = hunk[:-1]` drops the `@@` line itself). The `-a,b +c,d` numbers inside it are never parsed. s04 does the same — that's why a wrong header can't break an edit.
- **The `"@@ @@"` sentinel.** Both append a fake `@@ @@` to the block so the final hunk is flushed by the same code path as interior ones, instead of needing special end-of-block handling. s04 ports this trick verbatim.
- **`keeper` drops pure-context hunks.** A hunk reaches `@@` with `keeper == False` (no `-`/`+` seen) when it's only context; upstream resets `hunk = []` and skips it. s04's `hunkChanges` + the `keeper` check in `processFencedBlock` mirror this — a no-op hunk never reaches the file.
- **Where the fuzziness lives.** This excerpt is just the parse. The apply path is `do_replace` (L121-149) → `apply_hunk` (L151-199) → `flexible_search_and_replace` (search_replace.py). s04 swaps that last call for the s03 `replaceMostSimilarChunk` we already ported — same job (context match, whitespace-tolerant), less code.
- **A deliberate simplification we keep.** Upstream `apply_partial_hunk` (L282-309) retries a failing hunk by shedding context lines one at a time. s04 omits it: the exact → whitespace tiers cover the cases this chapter exists to teach, and adding the retry loop would obscure the core idea (match by context, ignore numbers).

**Read further**: start at `aider/coders/udiff_coder.py` → `find_diffs` (the scan), follow `process_fenced_block` (the excerpt above) into `hunk_to_before_after` (L403), then `do_replace` (L121) → `apply_hunk` (L151). That trace — parse → derive → context-match — is the real-source map for s03 (the shared matcher) → s04 → s05 (per-format prompts, where `udiff_prompts.py` defines what the model is told).

---

**Next**: s05 stops hard-coding each format's instructions inline and extracts them into a per-format prompt table, so the same loop can drive whole-file, SEARCH/REPLACE, and unified-diff just by swapping the system prompt.
