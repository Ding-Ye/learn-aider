package main

// editblock.go — the SEARCH/REPLACE edit format and its fuzzy matcher.
//
// This is the heart of aider. Instead of asking the model to rewrite a whole
// file (s02), we ask it to emit *surgical* edits: a block of lines to find
// (SEARCH) and the lines to put in their place (REPLACE). The model only has to
// reproduce the few lines it wants to change, which is far cheaper in tokens and
// far less likely to clobber unrelated code.
//
// The format the model emits, per file:
//
//	path/to/file.go
//	<<<<<<< SEARCH
//	old code
//	=======
//	new code
//	>>>>>>> REPLACE
//
// Two jobs live here:
//
//  1. PARSE the reply into (path, search, replace) triples.
//     Upstream: editblock_coder.py find_original_update_blocks (L439-560).
//
//  2. APPLY each triple to a file by LOCATING `search` and substituting
//     `replace`. The catch: LLMs rarely reproduce whitespace perfectly, so an
//     exact string match fails constantly on real code. We mirror aider's TIERED
//     matcher — try an exact match first, then a whitespace-flexible match that
//     tolerates uniformly-shifted indentation, then a "..." elision match.
//     Upstream: replace_most_similar_chunk (L157-188), perfect_or_whitespace
//     (L134-144), replace_part_with_missing_leading_whitespace (L243-273).
//
// WHY fuzzy matching matters: the model is generating the SEARCH text from
// memory of what it read, not copy-pasting bytes. It will re-indent, normalize
// tabs vs spaces, or drop a blank line. If "apply" demanded a byte-exact match,
// a huge fraction of otherwise-correct edits would fail. Whitespace tolerance is
// what makes SEARCH/REPLACE usable in practice. This is the single most
// load-bearing piece of cleverness in aider.

import (
	"fmt"
	"os"
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

// Edit is one parsed file change. For whole-file (s02), Search is "" and Replace
// holds the entire new file. For the diff format introduced HERE, Search is the
// text to find and Replace is the substitution — upstream's (path, original,
// updated) tuples. s03 is the first chapter where Edit.Search is non-empty.
//
// Special case: an empty Search with a real Path means "create this file" (or
// append to it), mirroring upstream do_replace's `not before_text.strip()` path.
type Edit struct {
	Path    string
	Search  string // "" means create-file / append (no chunk to locate)
	Replace string
}

// Coder ties a Provider to an edit format and runs the loop. We collapse
// upstream's per-format Coder subclasses into one struct with a Format field;
// s05 reintroduces the per-format split. EditBlockCoder upstream sets
// edit_format = "diff".
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

// NewCoder builds an editblock (diff-format) coder writing to stdout/stderr.
func NewCoder(p Provider, root string, verbose bool) *Coder {
	if root == "" {
		root = "."
	}
	return &Coder{
		Provider: p,
		Format:   FormatDiff,
		Verbose:  verbose,
		root:     root,
		out:      os.Stdout,
		errw:     os.Stderr,
	}
}

// ============================================================================
// PART 1 — PARSING: reply text -> []Edit
// ============================================================================

// The fence markers. Upstream uses regexes that tolerate 5-9 angle brackets and
// 5-9 equals signs (HEAD/DIVIDER/UPDATED, L386-388) to forgive models that emit
// slightly-off fences. We keep the canonical 7-char markers but match leniently
// by trimming and checking a prefix, which covers the same real cases without a
// regex dependency.
const (
	markSearch  = "<<<<<<< SEARCH"
	markDivider = "======="
	markReplace = ">>>>>>> REPLACE"
)

func isSearch(line string) bool  { return strings.HasPrefix(strings.TrimSpace(line), "<<<<<<<") }
func isDivider(line string) bool { return strings.HasPrefix(strings.TrimSpace(line), "=======") }
func isReplace(line string) bool { return strings.HasPrefix(strings.TrimSpace(line), ">>>>>>>") }

// GetEdits parses an LLM reply into SEARCH/REPLACE edits. It is the Go analog of
// find_original_update_blocks (editblock_coder.py L439-560): walk the reply line
// by line; when a `<<<<<<< SEARCH` line appears, the filename is on a line just
// above it, the SEARCH body runs until `=======`, and the REPLACE body runs
// until `>>>>>>> REPLACE`.
//
// A malformed block (a SEARCH with no DIVIDER, or no closing REPLACE) is a hard
// error — upstream raises ValueError too. Returning an error rather than a
// partial parse keeps a broken reply from silently applying half an edit.
func (c *Coder) GetEdits(reply string) ([]Edit, error) {
	lines := strings.Split(reply, "\n")
	var edits []Edit
	currentFilename := "" // sticky: a 2nd block for the same file can omit the name

	i := 0
	for i < len(lines) {
		if !isSearch(lines[i]) {
			i++
			continue
		}

		// Found a SEARCH marker. The filename is on one of the up-to-3 lines
		// above it (the model may put a blank line or a fence between the name
		// and the marker). Upstream: find_filename(lines[i-3:i]).
		filename := findFilename(lines[maxInt(0, i-3):i])
		if filename == "" {
			filename = currentFilename
		}
		if filename == "" {
			return nil, fmt.Errorf(
				"SEARCH block with no filename above it (line %d); the filename must be alone on the line before %q",
				i+1, markSearch)
		}
		currentFilename = filename

		// SEARCH body: everything until the divider.
		i++
		var search []string
		for i < len(lines) && !isDivider(lines[i]) {
			search = append(search, lines[i])
			i++
		}
		if i >= len(lines) {
			return nil, fmt.Errorf("expected %q to close the SEARCH section for %s", markDivider, filename)
		}

		// REPLACE body: everything until the replace marker.
		i++ // skip the divider
		var replace []string
		for i < len(lines) && !isReplace(lines[i]) {
			replace = append(replace, lines[i])
			i++
		}
		if i >= len(lines) {
			return nil, fmt.Errorf("expected %q to close the REPLACE section for %s", markReplace, filename)
		}
		i++ // skip the replace marker

		edits = append(edits, Edit{
			Path:    filename,
			Search:  linesToText(search),
			Replace: linesToText(replace),
		})
	}

	return edits, nil
}

// findFilename recovers the filename that precedes a SEARCH marker. It scans the
// preceding lines bottom-up and returns the first that looks like a path,
// stripping the markdown noise models add (**bold**, `backticks`, leading "#",
// trailing ":"). This mirrors strip_filename + find_filename (L408-436, 538-599)
// but without the fuzzy valid_fnames matching — s03 trusts the name as written.
func findFilename(prev []string) string {
	for j := len(prev) - 1; j >= 0; j-- {
		name := stripFilename(prev[j])
		if name == "" {
			continue
		}
		// A bare fence line ("```" or "```go") is not a filename; keep scanning.
		if strings.HasPrefix(name, "```") {
			continue
		}
		// Heuristic: a real filename has an extension or a path separator. This
		// avoids grabbing a stray prose word that happened to sit above the fence.
		if strings.Contains(name, ".") || strings.Contains(name, "/") {
			return name
		}
	}
	return ""
}

// stripFilename removes the wrapping models like to add around a filename.
// Upstream: strip_filename (L408-436).
func stripFilename(s string) string {
	s = strings.TrimSpace(s)
	if s == "" || s == "..." {
		return ""
	}
	s = strings.TrimSuffix(s, ":") // "file.go:"
	s = strings.TrimLeft(s, "#")   // "# file.go" (markdown heading)
	s = strings.Trim(s, "`")       // "`file.go`"
	s = strings.Trim(s, "*")       // "**file.go**"
	return strings.TrimSpace(s)
}

// ============================================================================
// PART 2 — APPLYING: locate `search` in the file, substitute `replace`
// ============================================================================

// ApplyEdits applies each edit to disk and partitions them into applied / failed.
// A failure (the SEARCH text could not be located) is reported, not fatal: the
// file is left untouched and the edit is returned in `failed` so the caller can
// ask the model to retry. Upstream collects failures the same way and raises a
// "SEARCH/REPLACE block failed to match" message (apply_edits L41-124).
func (c *Coder) ApplyEdits(edits []Edit) (applied, failed []Edit, err error) {
	for _, e := range edits {
		ok, applyErr := c.applyOne(e)
		if applyErr != nil {
			return applied, failed, applyErr
		}
		if ok {
			applied = append(applied, e)
		} else {
			failed = append(failed, e)
		}
	}
	return applied, failed, nil
}

// applyOne applies a single edit. Returns (true, nil) on success, (false, nil)
// when the SEARCH text could not be matched (a soft failure), or (_, err) on a
// real I/O error. This is the Go analog of do_replace (L364-383).
func (c *Coder) applyOne(e Edit) (bool, error) {
	full := c.absPath(e.Path)

	// Create-file / append: an empty SEARCH means there is nothing to locate.
	// Upstream do_replace: `if not before_text.strip(): new_content = content +
	// after_text` (and touches the file if it doesn't exist). We append to an
	// existing file or create a fresh one.
	if strings.TrimSpace(e.Search) == "" {
		existing, _ := os.ReadFile(full) // ignore not-exist; treat as empty
		if err := os.WriteFile(full, []byte(string(existing)+e.Replace), 0o644); err != nil {
			return false, fmt.Errorf("create/append %s: %w", e.Path, err)
		}
		return true, nil
	}

	content, err := os.ReadFile(full)
	if err != nil {
		// A SEARCH against a file that doesn't exist is a match failure, not a
		// crash — the model referenced a file we don't have. Report it softly.
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("read %s: %w", e.Path, err)
	}

	updated, ok := replaceMostSimilarChunk(string(content), e.Search, e.Replace)
	if !ok {
		return false, nil // could not locate the SEARCH text -> soft failure
	}
	if err := os.WriteFile(full, []byte(updated), 0o644); err != nil {
		return false, fmt.Errorf("write %s: %w", e.Path, err)
	}
	return true, nil
}

// replaceMostSimilarChunk is the TIERED matcher — the load-bearing fuzzy logic.
// It tries, in order of increasing tolerance:
//
//  1. perfectReplace          — exact line-for-line match (the happy path)
//  2. whitespaceFlexible      — same lines, uniformly different leading indent
//  3. drop a spurious leading blank line, then retry 1+2 (GPT issue #25)
//  4. tryDotDotDots           — the model elided unchanged middle with "..."
//
// Returns (newWhole, true) on the first tier that matches, else ("", false).
// Upstream: replace_most_similar_chunk (L157-188). We stop where upstream's
// `return` at L183 stops — the edit-distance tier below that line is dead code
// upstream too (see the doc and the upstream-reading file).
func replaceMostSimilarChunk(whole, part, replace string) (string, bool) {
	wholeLines := splitKeepNL(whole)
	partLines := splitKeepNL(part)
	replaceLines := splitKeepNL(replace)

	if res, ok := perfectOrWhitespace(wholeLines, partLines, replaceLines); ok {
		return res, true
	}

	// GPT sometimes prepends a spurious blank line to the SEARCH block. If the
	// first part line is blank and there's more than one line, retry without it.
	if len(partLines) > 2 && strings.TrimSpace(partLines[0]) == "" {
		if res, ok := perfectOrWhitespace(wholeLines, partLines[1:], replaceLines); ok {
			return res, true
		}
	}

	// The model may have written "..." to stand in for unchanged code it didn't
	// want to repeat. Try to apply the non-elided pieces individually.
	if res, ok := tryDotDotDots(whole, part, replace); ok {
		return res, true
	}

	return "", false
}

// perfectOrWhitespace runs the two cheapest tiers: an exact match, then a
// whitespace-flexible match. Upstream: perfect_or_whitespace (L134-144).
func perfectOrWhitespace(wholeLines, partLines, replaceLines []string) (string, bool) {
	if res, ok := perfectReplace(wholeLines, partLines, replaceLines); ok {
		return res, true
	}
	return replacePartWithMissingLeadingWhitespace(wholeLines, partLines, replaceLines)
}

// perfectReplace finds a contiguous run of `whole` lines that exactly equals
// `part`, and splices `replace` in its place. This is the byte-exact happy path.
// Upstream: perfect_replace (L146-154).
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
//     indent; re-apply it to every non-blank REPLACE line and splice.
//
// Upstream: replace_part_with_missing_leading_whitespace (L243-273) and
// match_but_for_leading_whitespace (L276-293). This tier is why a model can be
// sloppy about indentation and still land a correct edit.
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
		// Re-indent the REPLACE lines by the prefix we found in the file, leaving
		// blank lines blank.
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
// (len(set) != 1) is rejected so we don't apply a garbage indent. Upstream:
// match_but_for_leading_whitespace (L276-293).
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

// tryDotDotDots handles SEARCH/REPLACE blocks where the model wrote a line that
// is just "..." to elide unchanged code. We split both sides on the "..." lines;
// the odd (elided) pieces must match between SEARCH and REPLACE, and each
// non-elided SEARCH piece must appear EXACTLY ONCE in the file so the splice is
// unambiguous. Upstream: try_dotdotdots (L190-240). Returns (_, false) when
// there are no dots (so the caller falls through to "no match").
func tryDotDotDots(whole, part, replace string) (string, bool) {
	if !strings.Contains(part, "\n...\n") && !strings.HasPrefix(part, "...\n") {
		// Cheap pre-check: no "..." line at all -> this tier doesn't apply.
		if !containsDotsLine(part) {
			return "", false
		}
	}
	partPieces := splitOnDotsLine(part)
	replacePieces := splitOnDotsLine(replace)
	if len(partPieces) == 1 {
		return "", false // no dots after all
	}
	if len(partPieces) != len(replacePieces) {
		return "", false // unpaired "..." -> give up (upstream raises; we soft-fail)
	}

	for k := 0; k < len(partPieces); k++ {
		p, r := partPieces[k], replacePieces[k]
		if p == "" && r == "" {
			continue
		}
		if p == "" && r != "" {
			if !strings.HasSuffix(whole, "\n") {
				whole += "\n"
			}
			whole += r
			continue
		}
		if strings.Count(whole, p) != 1 {
			return "", false // zero or ambiguous -> can't splice safely
		}
		whole = strings.Replace(whole, p, r, 1)
	}
	return whole, true
}

// ============================================================================
// small helpers
// ============================================================================

// splitKeepNL splits text into lines, keeping the trailing "\n" on each line
// (like Python's splitlines(keepends=True)). This lets the matcher join slices
// back without losing newline boundaries. A trailing newline is added if absent,
// mirroring upstream prep() (L127-131).
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

// linesToText joins parser lines (split on "\n") back into one string with a
// trailing newline, so a SEARCH/REPLACE body round-trips cleanly into the matcher.
func linesToText(lines []string) string {
	if len(lines) == 0 {
		return ""
	}
	return strings.Join(lines, "\n") + "\n"
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

func containsDotsLine(s string) bool {
	for _, l := range strings.Split(s, "\n") {
		if strings.TrimSpace(l) == "..." {
			return true
		}
	}
	return false
}

// splitOnDotsLine splits text on lines that are exactly "..." (ignoring
// surrounding whitespace). The even-indexed pieces are real content; the
// odd-indexed pieces are the "..." separators (returned as "").
func splitOnDotsLine(s string) []string {
	lines := strings.Split(s, "\n")
	var pieces []string
	var cur []string
	for _, l := range lines {
		if strings.TrimSpace(l) == "..." {
			pieces = append(pieces, strings.Join(cur, "\n"))
			pieces = append(pieces, "") // the separator slot
			cur = nil
			continue
		}
		cur = append(cur, l)
	}
	pieces = append(pieces, strings.Join(cur, "\n"))
	return pieces
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

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
