package main

// wholefile.go — aider's "whole" edit format, the simplest one.
//
// THE MECHANISM. The LLM returns the *entire* rewritten file(s). Each file is a
// fenced code block, and the filename sits on the line ABOVE the opening fence:
//
//	path/to/foo.go
//	```go
//	package main
//	// ... the complete new contents of foo.go ...
//	```
//
// s01 parsed exactly one block and already knew the path (from argv). That is a
// toy: a real reply can rewrite several files at once, and the only place the
// filename appears is that line above the fence — often dressed up as
// **foo.go**, `foo.go`, or "# foo.go". So the real parser is a small state
// machine that (a) finds every fenced block, (b) recovers + cleans the filename
// above each one, and (c) handles the "bare fence, single chat file" fallback.
//
// We promote s01's inline extractCodeBlock into a reusable WholeFileCoder that
// satisfies the curriculum's Coder interface. ApplyEdits is still a trivial
// write-per-file — all the intelligence is in GetEdits, which is exactly why
// whole-file is the baseline format.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ---- aider-specific types (from the curriculum's shared catalog) ----

// EditFormat names the strategy the LLM is told to emit (upstream
// Coder.edit_format). It selects both the system prompt and the parser. s02
// implements FormatWhole; s03/s04 add the others.
type EditFormat string

const (
	FormatWhole EditFormat = "whole" // wholefile_coder.py — return the entire new file (this chapter)
	FormatDiff  EditFormat = "diff"  // editblock_coder.py — SEARCH/REPLACE (s03)
	FormatUDiff EditFormat = "udiff" // udiff_coder.py — unified diff (s04)
)

// Edit is one parsed file change. For whole-file, Search is "" and Replace holds
// the ENTIRE new file. For the diff/udiff formats added later, Search is the
// text to find and Replace the substitution (upstream's (path, original,
// updated) tuples). Whole-file always leaves Search empty.
type Edit struct {
	Path    string
	Search  string // "" means whole-file replace
	Replace string
}

// Coder is the per-format strategy: parse the LLM reply, then apply the edits.
// It mirrors upstream Coder.get_edits / Coder.apply_edits. s02 is the first
// chapter to actually satisfy this interface (s01 had the loop inline); s03/s04
// add sibling implementations, and s05 selects between them by EditFormat.
type Coder interface {
	Format() EditFormat
	SystemPrompt(fence [2]string) string
	GetEdits(response string) ([]Edit, error)
	ApplyEdits(edits []Edit) (applied, failed []Edit, err error)
}

// ---- WholeFileCoder ----

// WholeFileCoder implements the "whole" format. root anchors relative paths to a
// working directory (upstream Coder.abs_root_path); chatFiles is the set of
// files the user put "in chat" (editable), which drives the filename-recovery
// fallbacks. fence is the code-block delimiter pair, default ("```","```").
type WholeFileCoder struct {
	root      string
	chatFiles []string
	fence     [2]string
}

// NewWholeFileCoder builds a coder rooted at root with the given editable files.
// Passing chatFiles lets the parser fall back to the sole chat file when the
// model emits a bare fence with no filename above it (upstream's len==1 case).
func NewWholeFileCoder(root string, chatFiles []string) *WholeFileCoder {
	return &WholeFileCoder{
		root:      root,
		chatFiles: chatFiles,
		fence:     [2]string{"```", "```"},
	}
}

func (c *WholeFileCoder) Format() EditFormat { return FormatWhole }

// SystemPrompt is the format contract handed to the model. It mirrors aider's
// WholeFilePrompts.main_system: "to edit a file, write its path then the ENTIRE
// new file in one fenced block." The {fence} the model should use is injected so
// the prompt and the parser agree on the delimiter. (s05 generalizes prompts
// into a pluggable table; here it lives on the coder.)
func (c *WholeFileCoder) SystemPrompt(fence [2]string) string {
	return fmt.Sprintf(`You are a coding assistant that edits whole files.
To change a file, output its path on its own line, then the ENTIRE new contents
of that file inside a single fenced code block. You may edit MULTIPLE files —
just repeat "path then fenced block" for each one. For example:

path/to/file.go
%[1]s
<the complete new file here>
%[2]s

Rules:
- Output the WHOLE file, not a diff and not a snippet.
- Put the filename on the line directly above the opening fence.
- Preserve everything the instruction does not ask you to change.`, fence[0], fence[1])
}

// blockEdit is an intermediate parse result. source records HOW we learned the
// filename so we can de-dupe by reliability afterward, exactly like upstream's
// "block" > "saw" > "chat" priority.
type blockEdit struct {
	fname  string
	source string // "block" | "saw" | "chat"
	body   string
}

// GetEdits parses a whole-file reply into one Edit per rewritten file.
//
// It is a line-by-line state machine mirroring upstream wholefile_coder.py
// (get_edits, L36-122). The crucial state is fname: when nil/"" we are OUTSIDE a
// block; when set we are INSIDE one, accumulating the file body. A fence line
// toggles the two states. The subtlety — and the whole reason this is a chapter
// — is recovering the filename from the line ABOVE the opening fence and
// cleaning the markdown the model wraps it in.
func (c *WholeFileCoder) GetEdits(response string) ([]Edit, error) {
	lines := strings.Split(response, "\n")

	var (
		edits    []blockEdit
		sawFname string   // a filename mentioned in prose, e.g. "update `foo.go`"
		fname    string   // "" => outside a block; non-empty => inside, accumulating
		source   string   // how we learned fname for the current block
		body     []string // the accumulating file contents
		inBlock  bool     // distinguishes fname=="" inside a block from outside it
	)

	for i, raw := range lines {
		line := raw
		isFence := strings.HasPrefix(line, c.fence[0]) || strings.HasPrefix(line, c.fence[1])

		switch {
		case isFence && inBlock:
			// Closing fence: emit the block we were accumulating and reset. We
			// join with "\n" because we split on it; the body is the file as-is.
			edits = append(edits, blockEdit{fname: fname, source: source, body: strings.Join(body, "\n")})
			fname, source, body, inBlock = "", "", nil, false

		case isFence && !inBlock:
			// Opening fence: the filename is on the PREVIOUS line. This block is
			// what s01 had no notion of — it knew the path already.
			if i > 0 {
				fname = cleanFilename(lines[i-1])
				source = "block"

				// Issue #1232: a 250+ char "filename" is never real (the model
				// dumped prose where a path belongs). Drop it and fall through to
				// the fallbacks below.
				if len(fname) > 250 {
					fname = ""
				}

				// The model loves to echo the "path/to/" prefix from the one-shot
				// example in the prompt. If the full name isn't a chat file but
				// its basename is, collapse to the basename. (upstream L71-72)
				if fname != "" && !c.isChatFile(fname) && c.isChatFile(filepath.Base(fname)) {
					fname = filepath.Base(fname)
				}
			}

			if fname == "" {
				// A bare fence with no usable filename above it. Recover in
				// reliability order (upstream L73-84):
				switch {
				case sawFname != "":
					fname, source = sawFname, "saw" // mentioned earlier in prose
				case len(c.chatFiles) == 1:
					fname, source = c.chatFiles[0], "chat" // only one editable file
				default:
					// Genuinely ambiguous: multiple files in chat and no name.
					// Upstream raises ValueError here; we surface the same error.
					return nil, fmt.Errorf("no filename provided before %s in reply", c.fence[0])
				}
			}
			inBlock = true

		case inBlock:
			// Inside a block: this line is part of the new file body.
			body = append(body, line)

		default:
			// Prose outside any block. Scan it for a backtick-quoted chat file so
			// a later bare fence can fall back to it (upstream's saw_fname,
			// L88-95). This is what lets "rewrite `foo.go`:" + bare fence work.
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

	// Reply ended without a closing fence: still emit the block. The model ran
	// out of tokens mid-file, but the partial file is the best we have, and
	// upstream keeps it too (L105-106). This leniency is deliberate.
	if inBlock && fname != "" {
		edits = append(edits, blockEdit{fname: fname, source: source, body: strings.Join(body, "\n")})
	}

	return c.refine(edits), nil
}

// refine de-dupes blocks by filename, keeping the one whose filename came from
// the most reliable source. Upstream processes sources in the order
// "block" > "saw" > "chat" so a confidently-named block wins over a fallback.
// (wholefile_coder.py L108-122)
func (c *WholeFileCoder) refine(edits []blockEdit) []Edit {
	seen := map[string]bool{}
	var out []Edit
	for _, source := range []string{"block", "saw", "chat"} {
		for _, e := range edits {
			if e.source != source || seen[e.fname] {
				continue
			}
			seen[e.fname] = true
			out = append(out, Edit{Path: e.fname, Search: "", Replace: e.body})
		}
	}
	return out
}

// ApplyEdits writes each whole-file Edit to disk. It is intentionally trivial:
// resolve the path against root, create parent dirs, write the bytes. There is
// no matching or merging because Replace already IS the entire file — the whole
// point of the format. Returns which edits applied vs. failed so the caller (and
// later s09's reflection loop) can report partial success.
func (c *WholeFileCoder) ApplyEdits(edits []Edit) (applied, failed []Edit, err error) {
	for _, e := range edits {
		if e.Search != "" {
			// Defensive: whole-file never sets Search. A non-empty Search is a
			// diff-style edit (s03) handed to the wrong coder.
			failed = append(failed, e)
			continue
		}
		full := c.absPath(e.Path)
		if mkErr := os.MkdirAll(filepath.Dir(full), 0o755); mkErr != nil {
			failed = append(failed, e)
			err = mkErr
			continue
		}
		if wErr := os.WriteFile(full, []byte(e.Replace), 0o644); wErr != nil {
			failed = append(failed, e)
			err = wErr
			continue
		}
		applied = append(applied, e)
	}
	return applied, failed, err
}

// absPath resolves a (possibly relative) edit path against the coder's root,
// mirroring upstream Coder.abs_root_path. Absolute paths are returned untouched.
func (c *WholeFileCoder) absPath(p string) string {
	if filepath.IsAbs(p) || c.root == "" {
		return p
	}
	return filepath.Join(c.root, p)
}

// isChatFile reports whether name is one of the editable files. Used to validate
// recovered filenames and to collapse bogus "path/to/" prefixes to a basename.
func (c *WholeFileCoder) isChatFile(name string) bool {
	for _, cf := range c.chatFiles {
		if cf == name {
			return true
		}
	}
	return false
}

// cleanFilename strips the markdown the model wraps a filename in. The model
// emits things like **foo.go**, `foo.go`, "# foo.go", or "foo.go:" and we want
// just "foo.go". Order matters: trim surrounding whitespace, then **bold**, a
// trailing colon, surrounding backticks, a leading "#" heading marker, then
// whitespace again. This mirrors upstream L57-62 exactly.
func cleanFilename(line string) string {
	f := strings.TrimSpace(line)
	f = strings.Trim(f, "*")        // **filename.go**
	f = strings.TrimRight(f, ":")   // filename.go:
	f = strings.Trim(f, "`")        // `filename.go`
	f = strings.TrimLeft(f, "#")    // # filename.go (markdown heading)
	f = strings.TrimSpace(f)
	return f
}
