package main

// udiff.go — the UNIFIED-DIFF edit format: parse ```diff hunks, apply them with
// whitespace-flexible context matching.
//
// s03 taught SEARCH/REPLACE blocks. s04 is a THIRD edit format for the same
// loop. Some models would rather emit a standard unified diff — the `git diff`
// shape — than aider's bespoke fences:
//
//	```diff
//	--- a/path/to/file.go
//	+++ b/path/to/file.go
//	@@ ... @@
//	 unchanged context line     (leading space)
//	-line to remove             (leading minus)
//	+line to add                (leading plus)
//	 more context               (leading space)
//	```
//
// Three jobs live here:
//
//  1. PARSE the reply into (path, hunk) pairs. A "hunk" is just the list of
//     ` `/`-`/`+` lines. Upstream: udiff_coder.py find_diffs (L312-401),
//     process_fenced_block (L337-400).
//
//  2. DERIVE before/after text from a hunk. The "before" text is every context
//     line plus every `-` line (i.e. what the file looked like). The "after"
//     text is every context line plus every `+` line (what it should look like).
//     Once you have (before, after) a unified-diff hunk reduces to *exactly* the
//     s03 search/replace problem: find `before` in the file, splice in `after`.
//     Upstream: hunk_to_before_after (L403-429).
//
//  3. APPLY the hunk by locating `before` in the file and substituting `after`.
//     We reuse s03's TIERED fuzzy matcher (exact -> whitespace-flexible), because
//     the same problem bites here, harder: a unified diff carries `@@ -12,4 +12,5
//     @@` line numbers that the model computes from memory and routinely gets
//     wrong. So we IGNORE the @@ line numbers entirely and locate the hunk by its
//     CONTEXT lines instead. Upstream: do_replace (L121-149), apply_hunk (L151).
//
// WHY context matching (and why flexible): the `@@ -start,count +start,count @@`
// header is the most error-prone part of a model-generated diff — it has to count
// lines exactly, which LLMs are bad at. The context lines (the unchanged ` `
// lines surrounding the change) are far more reliable, because the model is
// quoting real code. So we throw away the line numbers and anchor on context.
// And because the model still re-indents / normalizes whitespace when quoting
// that context (the same s03 problem), the context match must be whitespace-
// flexible, not byte-exact. Context matching + whitespace flexibility together
// are what make a model-emitted unified diff actually apply. This mirrors aider's
// udiff flexibility, which is more forgiving than `patch(1)`.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ---- aider-specific types (from the curriculum's shared catalog) ----

// EditFormat names the strategy the LLM is told to emit. Upstream this is
// Coder.edit_format, and it selects both the system prompt and the parser.
type EditFormat string

const (
	FormatWhole EditFormat = "whole" // wholefile_coder.py (s02) — return the entire new file
	FormatDiff  EditFormat = "diff"  // editblock_coder.py (s03) — SEARCH/REPLACE blocks
	FormatUDiff EditFormat = "udiff" // udiff_coder.py (s04) — unified diff
)

// Edit is one parsed file change, in the curriculum's canonical shape. For
// whole-file (s02) Search is "" and Replace holds the entire new file; for
// SEARCH/REPLACE (s03) Search/Replace are the find/substitute texts. For the
// unified-diff format introduced HERE, we DERIVE Search/Replace from a hunk:
// Search is the hunk's before-text (context + `-` lines) and Replace is its
// after-text (context + `+` lines). That conversion (hunkToBeforeAfter) is what
// lets s04 reuse s03's matcher unchanged — upstream's (path, original, updated)
// tuples are produced the same way.
//
// Special case: an empty Search with a real Path means "create this file" (or
// append to it), mirroring upstream do_replace's `not before_text.strip()` path.
type Edit struct {
	Path    string
	Search  string // before-text: "" means create-file / append (no chunk to locate)
	Replace string // after-text
}

// Hunk is the raw, un-derived form a diff parser yields: a file path plus the
// list of diff lines (each still carrying its leading ' ', '-' or '+'). We keep
// this intermediate shape (rather than going straight to Edit) because the diff
// grammar — `@@`-delimited, multi-hunk-per-file — is most naturally parsed into
// (path, []line) and only THEN reduced to before/after. Upstream's get_edits
// yields exactly this `(path, hunk)` pair (udiff_coder.py L52-67).
type Hunk struct {
	Path  string
	Lines []string // diff lines, e.g. " ctx\n", "-old\n", "+new\n"
}

// Coder ties a Provider to an edit format and runs the loop. We collapse
// upstream's per-format Coder subclasses into one struct with a Format field;
// s05 reintroduces the per-format split. UnifiedDiffCoder upstream sets
// edit_format = "udiff".
type Coder struct {
	Provider Provider
	Format   EditFormat
	Verbose  bool

	// root is the base directory edits are resolved against (so a parsed path
	// like "pkg/foo.go" lands under root). Defaults to "." in NewCoder.
	root string

	// out/errw are injectable so tests can capture the transcript instead of
	// writing to the real terminal.
	out  *os.File
	errw *os.File
}

// NewCoder builds a unified-diff (udiff-format) coder writing to stdout/stderr.
func NewCoder(p Provider, root string, verbose bool) *Coder {
	if root == "" {
		root = "."
	}
	return &Coder{
		Provider: p,
		Format:   FormatUDiff,
		Verbose:  verbose,
		root:     root,
		out:      os.Stdout,
		errw:     os.Stderr,
	}
}

// ============================================================================
// PART 1 — PARSING: reply text -> []Hunk
// ============================================================================

// GetEdits parses an LLM reply into unified-diff hunks. It is the Go analog of
// UnifiedDiffCoder.get_edits + find_diffs (udiff_coder.py L52-67, L312-401): scan
// the reply for ```diff fences, and inside each fence split the body into hunks
// at `@@ ... @@` boundaries (and at new `--- / +++` file headers).
//
// The filename is sticky: a `--- a/x` / `+++ b/x` header sets the current path,
// and subsequent hunks without their own header inherit it. A reply with a hunk
// but no filename anywhere is a hard error — there is nowhere to apply it.
func (c *Coder) GetEdits(reply string) ([]Hunk, error) {
	hunks := findDiffs(reply)

	// Make the filename sticky across hunks (upstream get_edits L58-65).
	lastPath := ""
	var out []Hunk
	for _, h := range hunks {
		if h.Path != "" {
			lastPath = h.Path
		} else {
			h.Path = lastPath
		}
		out = append(out, h)
	}

	// A hunk with no path anywhere can't be applied.
	for _, h := range out {
		if h.Path == "" {
			return nil, fmt.Errorf("unified diff hunk has no `--- a/<file>` / `+++ b/<file>` header; cannot tell which file to patch")
		}
	}
	return out, nil
}

// findDiffs walks the reply and collects every hunk inside a ```diff fence.
// Upstream: find_diffs (L312-334). We can fence on triple-backticks because all
// udiff content is line-prefixed with +/-/space, so the fence is unambiguous.
func findDiffs(content string) []Hunk {
	if !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	lines := splitKeepNL(content)

	var hunks []Hunk
	i := 0
	for i < len(lines) {
		if strings.HasPrefix(lines[i], "```diff") {
			var these []Hunk
			i, these = processFencedBlock(lines, i+1)
			hunks = append(hunks, these...)
			continue
		}
		i++
	}
	return hunks
}

// processFencedBlock parses one ```diff ... ``` block starting at startLine
// (the line AFTER the opening fence) and returns (indexAfterClosingFence, hunks).
// Upstream: process_fenced_block (L337-400).
//
// The grammar inside a fence:
//   - an optional `--- a/<file>` / `+++ b/<file>` pair sets the file path;
//   - then one or more hunks, each introduced by an `@@ ... @@` line and made of
//     ` `/`-`/`+` body lines.
//
// We append a sentinel "@@ @@" so the final hunk is flushed by the same code
// path as the interior ones (upstream does the identical trick at L344).
func processFencedBlock(lines []string, startLine int) (int, []Hunk) {
	// Find the closing fence.
	end := len(lines)
	for j := startLine; j < len(lines); j++ {
		if strings.HasPrefix(lines[j], "```") {
			end = j
			break
		}
	}

	block := append([]string{}, lines[startLine:end]...)
	block = append(block, "@@ @@") // sentinel: flush the last hunk

	// A leading `--- a/x` / `+++ b/x` pair names the file. Strip git's a//b/ (and
	// /dev/null) prefixes to recover the real path. Upstream L346-358.
	fname := ""
	if len(block) >= 2 && strings.HasPrefix(block[0], "--- ") && strings.HasPrefix(block[1], "+++ ") {
		aName := strings.TrimSpace(block[0][4:])
		bName := strings.TrimSpace(block[1][4:])
		if (strings.HasPrefix(aName, "a/") || aName == "/dev/null") && strings.HasPrefix(bName, "b/") {
			fname = bName[2:]
		} else {
			fname = bName
		}
		block = block[2:]
	}

	var hunks []Hunk
	keeper := false // does the current hunk contain at least one -/+ change?
	var cur []string
	for _, line := range block {
		cur = append(cur, line)
		if len(line) < 2 {
			continue
		}

		// A new `+++ ` right after a `--- ` means a fresh file header appeared
		// mid-block. Flush the hunk accumulated so far, then switch files.
		// Upstream L372-383.
		if strings.HasPrefix(line, "+++ ") && len(cur) >= 2 && strings.HasPrefix(cur[len(cur)-2], "--- ") {
			if len(cur) >= 3 && cur[len(cur)-3] == "\n" {
				cur = cur[:len(cur)-3]
			} else {
				cur = cur[:len(cur)-2]
			}
			if keeper {
				hunks = append(hunks, Hunk{Path: fname, Lines: cur})
			}
			cur = nil
			keeper = false
			fname = strings.TrimSpace(line[4:])
			continue
		}

		op := line[0]
		switch op {
		case '-', '+':
			keeper = true // this hunk actually changes something
			continue
		case '@':
			// `@@ ... @@` ends the previous hunk and starts the next. Drop the
			// `@@` line itself (cur[:-1]) and flush — but only if the hunk had a
			// real change; a pure-context hunk is discarded (it edits nothing).
			if !keeper {
				cur = nil
				continue
			}
			cur = cur[:len(cur)-1]
			hunks = append(hunks, Hunk{Path: fname, Lines: cur})
			cur = nil
			keeper = false
		default:
			// A normal context line (leading space) or blank line — accumulate.
			continue
		}
	}

	return end + 1, hunks
}

// ============================================================================
// PART 2 — DERIVE before/after from a hunk
// ============================================================================

// hunkToBeforeAfter splits a hunk's diff lines into the BEFORE text (what the
// file currently contains: context + removed lines) and the AFTER text (what it
// should contain: context + added lines). This is the conversion that turns a
// unified-diff hunk into the s03 search/replace problem. Upstream:
// hunk_to_before_after (L403-429).
//
//	" ctx\n"  -> appears in BOTH before and after (a context anchor)
//	"-old\n"  -> appears only in BEFORE (it is being removed)
//	"+new\n"  -> appears only in AFTER  (it is being added)
func hunkToBeforeAfter(h Hunk) (before, after string) {
	var b, a strings.Builder
	for _, line := range h.Lines {
		op := byte(' ')
		body := line
		if len(line) >= 2 {
			op = line[0]
			body = line[1:]
		} else if len(line) == 1 {
			// A bare "\n" is a blank context line; op stays space, body is the
			// newline. (A truly empty "" contributes nothing.)
			body = line
		}

		switch op {
		case ' ':
			b.WriteString(body)
			a.WriteString(body)
		case '-':
			b.WriteString(body)
		case '+':
			a.WriteString(body)
		}
	}
	return b.String(), a.String()
}

// hunkChanges reports whether a hunk has any '-' or '+' lines. A pure-context
// hunk (only ' ' lines) changes nothing and is dropped before apply — applying
// it would be a no-op search-and-replace of text onto itself. Upstream drops
// these implicitly because before == after.
func hunkChanges(h Hunk) bool {
	for _, line := range h.Lines {
		if len(line) >= 1 && (line[0] == '-' || line[0] == '+') {
			return true
		}
	}
	return false
}

// ============================================================================
// PART 3 — APPLYING: locate the hunk's context in the file, splice the change
// ============================================================================

// ApplyEdits applies each hunk to disk and partitions them into applied / failed.
// A failure (the hunk's context could not be located) is reported, not fatal: the
// file is left untouched and the hunk is returned in `failed` so the caller can
// ask the model to retry. Upstream collects failures the same way and raises a
// "UnifiedDiffNoMatch" message (udiff_coder.py apply_edits L69-118).
//
// Hunks are applied IN ORDER, each against the result of the previous one, so a
// file with several hunks accumulates all of them (later hunks see earlier
// edits). This is why a hunk's context must come from the *current* file state.
func (c *Coder) ApplyEdits(hunks []Hunk) (applied, failed []Hunk, err error) {
	for _, h := range hunks {
		// Drop pure-context hunks: they change nothing. Upstream normalize_hunk
		// produces an empty diff for these and `continue`s (L73-75).
		if !hunkChanges(h) {
			continue
		}
		ok, applyErr := c.applyOne(h)
		if applyErr != nil {
			return applied, failed, applyErr
		}
		if ok {
			applied = append(applied, h)
		} else {
			failed = append(failed, h)
		}
	}
	return applied, failed, nil
}

// applyOne applies a single hunk. Returns (true, nil) on success, (false, nil)
// when the hunk's context could not be matched (a soft failure), or (_, err) on
// a real I/O error. This is the Go analog of do_replace (L121-149).
func (c *Coder) applyOne(h Hunk) (bool, error) {
	full := c.absPath(h.Path)
	before, after := hunkToBeforeAfter(h)

	// Create-file / append: a hunk that only ADDS lines (no context, no removals)
	// has an empty before-text. There is nothing to locate, so we append to an
	// existing file or create a fresh one. Upstream do_replace: `if not
	// before_text.strip(): new_content = content + after_text` (L135-138).
	if strings.TrimSpace(before) == "" {
		existing, _ := os.ReadFile(full) // ignore not-exist; treat as empty
		if err := os.WriteFile(full, []byte(string(existing)+after), 0o644); err != nil {
			return false, fmt.Errorf("create/append %s: %w", h.Path, err)
		}
		return true, nil
	}

	content, err := os.ReadFile(full)
	if err != nil {
		// A hunk against a file that doesn't exist is a match failure, not a
		// crash — the model referenced a file we don't have. Report it softly.
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("read %s: %w", h.Path, err)
	}

	// THE KEY STEP: locate the hunk by its CONTEXT (we ignore the @@ line
	// numbers) and splice the change. replaceMostSimilarChunk is s03's tiered,
	// whitespace-flexible matcher reused verbatim.
	updated, ok := replaceMostSimilarChunk(string(content), before, after)
	if !ok {
		return false, nil // could not locate the hunk's context -> soft failure
	}
	if err := os.WriteFile(full, []byte(updated), 0o644); err != nil {
		return false, fmt.Errorf("write %s: %w", h.Path, err)
	}
	return true, nil
}

// absPath resolves a (possibly relative) edit path under the coder's root.
func (c *Coder) absPath(path string) string {
	if filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(c.root, path)
}

// ============================================================================
// PART 4 — the search/replace matcher (trimmed from s03 editblock.go)
// ============================================================================
//
// This is the same engine s03 built for SEARCH/REPLACE, reused unchanged to
// apply unified-diff hunks. A hunk reduces to (before, after) via
// hunkToBeforeAfter above, and from there it IS the s03 problem: locate `before`
// in the file (tolerating whitespace drift) and substitute `after`. We carry it
// here (rather than importing s03) so every chapter stays a self-contained
// module. It is trimmed to the tiers that earn their keep for diffs.

// replaceMostSimilarChunk is the TIERED matcher — the load-bearing fuzzy logic.
// It tries, in order of increasing tolerance:
//
//  1. perfectReplace          — exact line-for-line match (the happy path)
//  2. whitespaceFlexible      — same lines, uniformly different leading indent
//  3. drop a spurious leading blank line, then retry 1+2
//
// Returns (newWhole, true) on the first tier that matches, else ("", false).
// Upstream editblock: replace_most_similar_chunk (editblock_coder.py L157-188).
// For udiff specifically, the whitespace tier is doubly important: the model's
// context lines are quoted from memory and frequently re-indented.
func replaceMostSimilarChunk(whole, part, replace string) (string, bool) {
	wholeLines := splitKeepNL(whole)
	partLines := splitKeepNL(part)
	replaceLines := splitKeepNL(replace)

	if res, ok := perfectOrWhitespace(wholeLines, partLines, replaceLines); ok {
		return res, true
	}

	// The model sometimes prepends a spurious blank context line to the hunk. If
	// the first part line is blank and there's more than one line, retry without
	// it. (Upstream editblock issue #25; the same drift shows up in diff context.)
	if len(partLines) > 2 && strings.TrimSpace(partLines[0]) == "" {
		if res, ok := perfectOrWhitespace(wholeLines, partLines[1:], replaceLines); ok {
			return res, true
		}
	}

	return "", false
}

// perfectOrWhitespace runs the two cheapest tiers: an exact match, then a
// whitespace-flexible match. Upstream editblock: perfect_or_whitespace (L134-144).
func perfectOrWhitespace(wholeLines, partLines, replaceLines []string) (string, bool) {
	if res, ok := perfectReplace(wholeLines, partLines, replaceLines); ok {
		return res, true
	}
	return replacePartWithMissingLeadingWhitespace(wholeLines, partLines, replaceLines)
}

// perfectReplace finds a contiguous run of `whole` lines that exactly equals
// `part`, and splices `replace` in its place. This is the byte-exact happy path.
// Upstream editblock: perfect_replace (L146-154).
func perfectReplace(wholeLines, partLines, replaceLines []string) (string, bool) {
	n := len(partLines)
	if n == 0 {
		return "", false
	}
	for i := 0; i+n <= len(wholeLines); i++ {
		if slicesEqual(wholeLines[i:i+n], partLines) {
			out := concat(wholeLines[:i], replaceLines, wholeLines[i+n:])
			return strings.Join(out, ""), true
		}
	}
	return "", false
}

// replacePartWithMissingLeadingWhitespace handles the SINGLE most common LLM
// mistake: it reproduces the right lines but at the wrong indentation — usually
// uniformly (it dropped all leading whitespace, or kept only some). The fix:
//
//  1. Outdent both `part` and `replace` by the max indent they share, so a model
//     that over-indented uniformly is normalized.
//  2. Slide that outdented `part` over `whole`. At each position, ask "do these
//     match except for a single, consistent leading-whitespace prefix?"
//     (matchButForLeadingWhitespace). If yes, we recovered the file's real
//     indent; re-apply it to every non-blank `replace` line and splice.
//
// Upstream editblock: replace_part_with_missing_leading_whitespace (L243-273) and
// match_but_for_leading_whitespace (L276-293). This tier is why a model can be
// sloppy about the indentation of a hunk's context and still land the edit.
func replacePartWithMissingLeadingWhitespace(wholeLines, partLines, replaceLines []string) (string, bool) {
	// Compute the common leading-whitespace length across all non-blank lines of
	// BOTH part and replace, then outdent by that amount.
	leading := minLeadingWhitespace(append(append([]string{}, partLines...), replaceLines...))
	if leading > 0 {
		partLines = outdent(partLines, leading)
		replaceLines = outdent(replaceLines, leading)
	}

	n := len(partLines)
	if n == 0 {
		return "", false
	}
	for i := 0; i+n <= len(wholeLines); i++ {
		addLeading, ok := matchButForLeadingWhitespace(wholeLines[i:i+n], partLines)
		if !ok {
			continue
		}
		// Re-indent the `replace` lines by the prefix we found in the file,
		// leaving blank lines blank.
		reindented := make([]string, len(replaceLines))
		for k, rl := range replaceLines {
			if strings.TrimSpace(rl) == "" {
				reindented[k] = rl
			} else {
				reindented[k] = addLeading + rl
			}
		}
		out := concat(wholeLines[:i], reindented, wholeLines[i+n:])
		return strings.Join(out, ""), true
	}
	return "", false
}

// matchButForLeadingWhitespace reports whether `wholeLines` equals `partLines`
// once you ignore leading whitespace AND the difference in leading whitespace is
// the SAME single prefix on every non-blank line. If so it returns that prefix,
// which is the file's true indentation for this block. A non-uniform offset
// (len(set) != 1) is rejected so we don't apply a garbage indent. Upstream
// editblock: match_but_for_leading_whitespace (L276-293).
func matchButForLeadingWhitespace(wholeLines, partLines []string) (string, bool) {
	if len(wholeLines) != len(partLines) {
		return "", false
	}
	// 1) The non-whitespace content of every line must agree.
	for i := range wholeLines {
		if strings.TrimLeft(wholeLines[i], " \t") != strings.TrimLeft(partLines[i], " \t") {
			return "", false
		}
	}
	// 2) The added leading whitespace must be one consistent prefix.
	adds := map[string]struct{}{}
	for i := range wholeLines {
		if strings.TrimSpace(wholeLines[i]) == "" {
			continue // blank lines carry no reliable indent signal
		}
		prefix := wholeLines[i][:len(wholeLines[i])-len(partLines[i])]
		adds[prefix] = struct{}{}
	}
	if len(adds) != 1 {
		return "", false
	}
	for p := range adds {
		return p, true
	}
	return "", false
}

// ============================================================================
// small helpers
// ============================================================================

// splitKeepNL splits text into lines, keeping the trailing "\n" on each line
// (like Python's splitlines(keepends=True)). This lets the matcher join slices
// back without losing newline boundaries. A trailing newline is added if absent,
// mirroring upstream prep().
func splitKeepNL(s string) []string {
	if s == "" {
		return nil
	}
	if !strings.HasSuffix(s, "\n") {
		s += "\n"
	}
	parts := strings.SplitAfter(s, "\n")
	// SplitAfter leaves a trailing "" after the final "\n"; drop it.
	if len(parts) > 0 && parts[len(parts)-1] == "" {
		parts = parts[:len(parts)-1]
	}
	return parts
}

func minLeadingWhitespace(lines []string) int {
	min := -1
	for _, l := range lines {
		if strings.TrimSpace(l) == "" {
			continue // ignore blank lines
		}
		n := len(l) - len(strings.TrimLeft(l, " \t"))
		if min == -1 || n < min {
			min = n
		}
	}
	if min < 0 {
		return 0
	}
	return min
}

func outdent(lines []string, n int) []string {
	out := make([]string, len(lines))
	for i, l := range lines {
		if strings.TrimSpace(l) == "" {
			out[i] = l
			continue
		}
		if len(l) >= n {
			out[i] = l[n:]
		} else {
			out[i] = l
		}
	}
	return out
}

func slicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func concat(parts ...[]string) []string {
	var out []string
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}
