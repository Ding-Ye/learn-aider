---
title: "s03 · Search/replace edit blocks"
chapter: 3
slug: s03-editblock-searchreplace
est_read_min: 14
---

# s03 · Search/replace edit blocks

> What this teaches: the *SEARCH/REPLACE edit format* and its **fuzzy matcher** — the mechanism at the heart of aider. The model emits only the lines to find and the lines to replace them with; we locate the SEARCH text in the file (tolerating whitespace drift) and substitute. This is aider's default `diff` format and gets its own chapter because the tiered matcher is the single most load-bearing piece of cleverness in the project.

---

## Problem / 问题

s02 taught the **whole-file** format: to change one line, the model re-emits the entire file inside a fenced block, and we overwrite the file wholesale. That is simple to apply (`os.WriteFile`) but it has two real costs. It is **token-expensive** — a one-line fix in a 500-line file means the model reads and rewrites all 500 lines, every turn — and it is **destructive** — any line the model fumbles while re-typing the file (a dropped import, a mangled comment) silently clobbers correct code.

aider's answer, and its most distinctive mechanism, is the **SEARCH/REPLACE block**: the model writes only the lines it wants to find and the lines to put in their place. That's cheaper and safer — but it introduces a hard problem. The model generates the SEARCH text *from memory* of what it read, not by copy-pasting bytes, so it constantly re-indents, swaps tabs for spaces, or drops a blank line. If "apply" demanded a byte-exact match, a large fraction of otherwise-correct edits would fail. The chapter's real work is the **fuzzy matcher** that makes SEARCH/REPLACE usable.

## Solution / 解决方案

A SEARCH/REPLACE edit is a triple `(path, search, replace)`. The model emits, per change: the file path on its own line, then `<<<<<<< SEARCH`, the old lines, `=======`, the new lines, `>>>>>>> REPLACE`. **Parsing** walks the reply line by line and pulls out those triples. **Applying** locates the `search` lines in the file and splices `replace` in their place.

The trick that makes it work is a **tiered matcher**: try the cheapest, strictest match first, and only fall back to more tolerant matches if it fails. Concretely:

1. **Exact match** (`perfectReplace`) — find a contiguous run of file lines equal to SEARCH, byte for byte. The happy path.
2. **Whitespace-flexible** (`replacePartWithMissingLeadingWhitespace`) — the same lines, but the model used the wrong (usually uniformly-shifted) indentation. Outdent both sides, slide SEARCH over the file asking "do these match except for one *consistent* leading-whitespace prefix?", and if so re-apply the file's true indent to REPLACE. This tier is what tolerates the model's #1 mistake.
3. **Elision** (`tryDotDotDots`) — the model wrote `...` to stand in for unchanged middle code; splice the non-elided pieces individually.

Failures are **soft**: an unmatched SEARCH leaves the file untouched and is reported, never silently half-applied.

## How It Works / 工作原理

```ascii-anim frames=2
┌──────────────────────────────────────────────────────────────┐
│                  reply text from the model                   │
│                          │                                   │
│                          ▼                                   │
│   GetEdits ── scan lines, on <<<<<<< SEARCH pull:            │
│       filename (line above) · SEARCH body · REPLACE body     │
│                          │                                   │
│                          ▼   []Edit{Path, Search, Replace}   │
│              ┌───────────────────────────┐                  │
│   ApplyEdits │ per edit: locate Search in │                  │
│              │ the file via TIERED match: │                  │
│              │  1 exact (perfectReplace)  │── hit ──▶ splice │
│              │  2 whitespace-flexible     │── hit ──▶ splice │
│              │  3 "..." elision           │── hit ──▶ splice │
│              │  else ─────────────────────┼── miss ─▶ failed │
│              └───────────────────────────┘     (file kept)   │
└──────────────────────────────────────────────────────────────┘
```

The load-bearing matcher (excerpt from [`agents/s03-editblock-searchreplace/editblock.go`](https://github.com/Ding-Ye/learn-aider/blob/main/agents/s03-editblock-searchreplace/editblock.go)):

```go
// replaceMostSimilarChunk is the TIERED matcher — the load-bearing fuzzy logic.
func replaceMostSimilarChunk(whole, part, replace string) (string, bool) {
	wholeLines := splitKeepNL(whole)
	partLines := splitKeepNL(part)
	replaceLines := splitKeepNL(replace)

	// Tier 1+2: exact match, then whitespace-flexible match.
	if res, ok := perfectOrWhitespace(wholeLines, partLines, replaceLines); ok {
		return res, true
	}

	// GPT sometimes prepends a spurious blank line to SEARCH; retry without it.
	if len(partLines) > 2 && strings.TrimSpace(partLines[0]) == "" {
		if res, ok := perfectOrWhitespace(wholeLines, partLines[1:], replaceLines); ok {
			return res, true
		}
	}

	// Tier 3: the model elided unchanged code with "..." lines.
	if res, ok := tryDotDotDots(whole, part, replace); ok {
		return res, true
	}
	return "", false
}

// matchButForLeadingWhitespace: do these lines match once you ignore leading
// whitespace AND the added indent is the SAME single prefix on every non-blank
// line? If so, return that prefix — the file's true indentation for this block.
func matchButForLeadingWhitespace(wholeLines, partLines []string) (string, bool) {
	if len(wholeLines) != len(partLines) {
		return "", false
	}
	for i := range wholeLines { // non-whitespace content must agree
		if strings.TrimLeft(wholeLines[i], " \t") != strings.TrimLeft(partLines[i], " \t") {
			return "", false
		}
	}
	adds := map[string]struct{}{} // the added prefix must be ONE consistent value
	for i := range wholeLines {
		if strings.TrimSpace(wholeLines[i]) == "" {
			continue
		}
		adds[wholeLines[i][:len(wholeLines[i])-len(partLines[i])]] = struct{}{}
	}
	if len(adds) != 1 {
		return "", false
	}
	for p := range adds {
		return p, true
	}
	return "", false
}
```

**Four non-obvious points**:

1. **The whitespace tier requires a *uniform* offset.** `matchButForLeadingWhitespace` rejects a candidate when the indentation difference isn't a single consistent prefix (`len(adds) != 1`). That guard is what stops the matcher from "successfully" applying an edit to lines that merely *look* similar — it only forgives the mistake the model actually makes (shifting the whole block by the same amount).
2. **Lines keep their trailing newline.** `splitKeepNL` mirrors Python's `splitlines(keepends=True)`: each line carries its `\n`, so splicing slices back with `strings.Join(..., "")` can't accidentally merge or drop line boundaries.
3. **Empty SEARCH is a create/append, not a match.** `applyOne` short-circuits when `Search` is blank: it appends to an existing file or creates a new one, exactly like upstream `do_replace`'s `not before_text.strip()` branch. No locating happens.
4. **We stop at the dotdotdots tier — on purpose.** Upstream `replace_most_similar_chunk` has an edit-distance tier after it, but `return`s before reaching it (L183); that code is dead upstream too. The whitespace tier is the one that does the work, so we port the live tiers and skip the dead one.

## What Changed (vs. s02) / 与 s02 的变化

The format flips from "rewrite the whole file" to "find these lines, replace them." `Edit.Search` becomes non-empty, and *applying* an edit stops being a trivial `os.WriteFile` and becomes the tiered matcher.

```diff
 // Edit is one parsed file change.
 type Edit struct {
 	Path    string
-	Search  string // s02: always "" — whole-file replace
-	Replace string // s02: the ENTIRE new file
+	Search  string // s03: the lines to LOCATE in the file ("" => create/append)
+	Replace string // s03: the lines to splice in their place
 }

-// s02 apply: overwrite the whole file. All intelligence is in the parser.
-func (c *Coder) applyWholeFile(e Edit) error {
-	return os.WriteFile(e.Path, []byte(e.Replace), 0o644)
-}
+// s03 apply: LOCATE e.Search (tolerating whitespace drift), then splice.
+func (c *Coder) applyOne(e Edit) (bool, error) {
+	content, _ := os.ReadFile(c.absPath(e.Path))
+	updated, ok := replaceMostSimilarChunk(string(content), e.Search, e.Replace)
+	if !ok {
+		return false, nil // soft failure: file left untouched, edit -> `failed`
+	}
+	return true, os.WriteFile(c.absPath(e.Path), []byte(updated), 0o644)
+}
```

Semantically: in s02 all the difficulty lived in the *parser* (recover the filename, handle many blocks) and applying was a no-op. In s03 the parser is comparably simple, but applying is now the hard part — because the model's SEARCH text and the file's bytes won't match exactly. The chapter's center of gravity moves from "parse" to "match."

## Try It / 动手试一试

```bash
cd agents/s03-editblock-searchreplace

# set ONE provider's key
export ANTHROPIC_API_KEY=sk-ant-...

# read greet.go, ask for a surgical change, apply the SEARCH/REPLACE blocks
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
[s03] provider=anthropic model=claude-sonnet-4-6 url=
[s03] sending 118 bytes of greet.go + instruction "change the greeting to 'hi there'"
[s03] stop_reason=end_turn in=611 out=64 tokens

# stdout:
Applied edit to greet.go
```

If the SEARCH text matches nothing, you'll see `FAILED to match SEARCH in greet.go (file left untouched)` and a non-zero exit — the file is never half-edited. (A full illustrative transcript lives in [`testdata/expected.txt`](https://github.com/Ding-Ye/learn-aider/blob/main/agents/s03-editblock-searchreplace/testdata/expected.txt).)

## Upstream Source Reading / 上游源码阅读

aider's parser is `find_original_update_blocks` (a generator) and its matcher is `replace_most_similar_chunk` + the whitespace helpers. The big differences from s03: upstream also special-cases ```` ```bash ```` shell blocks, fuzzy-matches the filename against the set of in-chat files (`difflib.get_close_matches`), and re-raises parse errors with the processed prefix so the model sees exactly where it broke. s03 keeps the load-bearing parse + match and drops those extras.

```upstream:aider/coders/editblock_coder.py#L439-L535
def find_original_update_blocks(content, fence=DEFAULT_FENCE, valid_fnames=None):
    lines = content.splitlines(keepends=True)
    i = 0
    current_filename = None  # sticky: a 2nd block can omit the filename

    head_pattern = re.compile(HEAD)        # ^<{5,9} SEARCH>?\s*$  (lenient fence)
    divider_pattern = re.compile(DIVIDER)  # ^={5,9}\s*$
    updated_pattern = re.compile(UPDATED)  # ^>{5,9} REPLACE\s*$

    while i < len(lines):
        line = lines[i]
        # (shell ```bash handling omitted — s03 doesn't run shell commands)

        if head_pattern.match(line.strip()):              # a SEARCH marker
            try:
                # filename is on one of the up-to-3 lines ABOVE the marker;
                # if the next line is already the DIVIDER, SEARCH is empty =>
                # "create a new file", so don't require a known name.
                if i + 1 < len(lines) and divider_pattern.match(lines[i + 1].strip()):
                    filename = find_filename(lines[max(0, i - 3) : i], fence, None)
                else:
                    filename = find_filename(lines[max(0, i - 3) : i], fence, valid_fnames)
                if not filename:
                    filename = current_filename or _raise_missing_filename()
                current_filename = filename

                original_text = []                        # SEARCH body up to =======
                i += 1
                while i < len(lines) and not divider_pattern.match(lines[i].strip()):
                    original_text.append(lines[i]); i += 1
                if i >= len(lines) or not divider_pattern.match(lines[i].strip()):
                    raise ValueError(f"Expected `{DIVIDER_ERR}`")

                updated_text = []                          # REPLACE body up to >>>>>>>
                i += 1
                while i < len(lines) and not (
                    updated_pattern.match(lines[i].strip())
                    or divider_pattern.match(lines[i].strip())
                ):
                    updated_text.append(lines[i]); i += 1
                if i >= len(lines) or not (...):
                    raise ValueError(f"Expected `{UPDATED_ERR}` or `{DIVIDER_ERR}`")

                yield filename, "".join(original_text), "".join(updated_text)
            except ValueError as e:
                processed = "".join(lines[: i + 1])
                raise ValueError(f"{processed}\n^^^ {e.args[0]}")
        i += 1
```

**Reading notes**:

- **Lenient fences.** Upstream's `HEAD`/`DIVIDER`/`UPDATED` regexes accept 5-9 brackets/equals so a slightly-off fence still parses. s03 gets the same forgiveness by trimming and prefix-checking (`<<<<<<<`, `=======`, `>>>>>>>`) instead of a regex — no dependency, same real coverage.
- **Filename recovery.** Upstream `find_filename` (L538) fuzzy-matches the recovered name against `valid_fnames` with `difflib.get_close_matches(cutoff=0.8)`. s03 trusts the name as written (extension-or-slash heuristic) because we don't carry an in-chat file set yet — that arrives with the command layer in s06.
- **Empty SEARCH = create-file.** Both detect "DIVIDER immediately after HEAD" and route to a create path (upstream `do_replace` touches the file; s03's `applyOne` writes/appends). This is why `Edit.Search == ""` is meaningful.
- **The matcher is where the cleverness is.** The excerpt above is just the parse. The fuzzy work is `replace_most_similar_chunk` (L157-188) → `perfect_or_whitespace` (L134-144) → `replace_part_with_missing_leading_whitespace` (L243-273) → `match_but_for_leading_whitespace` (L276-293). s03 ports all four.
- **A deliberately imperfect choice we keep.** Upstream `replace_most_similar_chunk` `return`s at L183 *before* its edit-distance tier — that tier is dead code. s03 mirrors this: we stop at the dotdotdots tier, because the whitespace tier is the one that actually earns its keep.

**Read further**: start at `aider/coders/editblock_coder.py` → `find_original_update_blocks` (the parse above), follow `do_replace` (L364) into `replace_most_similar_chunk` (L157), then read `match_but_for_leading_whitespace` (L276). That trace — parse → do_replace → tiered match — is the real-source map for s03 → s04 (unified diff reuses the same matcher) → s05 (per-format prompts).

---

**Next**: s04 evolves this chapter's format further by parsing standard `@@`-hunk unified diffs, deriving the before/after text from the hunk, and reusing s03's search/replace engine to apply it despite line-number skew.
