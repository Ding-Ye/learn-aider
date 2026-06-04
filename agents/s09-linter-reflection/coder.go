package main

// coder.go — a deliberately tiny whole-file Coder, just enough to give the
// reflection loop something real to drive. The CODER is not the lesson of s09
// (that was s02..s05); it is here so reflect.go has a concrete
// GetEdits/ApplyEdits to call. We use the whole-file format because it is the
// simplest: the model returns a filename line followed by a fenced block holding
// the entire new file, and ApplyEdits just writes it.
//
// This is a compressed version of s02's WholeFileCoder: enough to parse one
// fenced block and write it, without the full filename-cleanup state machine.

import (
	"fmt"
	"os"
	"strings"
)

// WholeFileCoder implements Coder for the whole-file edit format over a single
// target file. It is scoped to one file (the demo's -file) so the loop has an
// unambiguous path to lint.
type WholeFileCoder struct {
	path string
}

func NewWholeFileCoder(path string) *WholeFileCoder {
	return &WholeFileCoder{path: path}
}

func (c *WholeFileCoder) Format() EditFormat { return FormatWhole }

// SystemPrompt tells the model to reply with the whole updated file inside a
// fence. It is intentionally terse; s05 is where prompts get serious.
func (c *WholeFileCoder) SystemPrompt(fence [2]string) string {
	return fmt.Sprintf(`Act as an expert Go developer.
To change the file, return its ENTIRE new content as a single fenced code block.
Put the filename on the line just before the opening fence.
The fences are %s and %s. Do not add commentary inside the fence.
Make sure the code compiles and is gofmt-clean.`, fence[0], fence[1])
}

// GetEdits extracts the last fenced block from the reply and treats it as the
// whole new content of c.path. Whole-file means Search=="" (a full replace).
// Taking the LAST block is a small robustness trick: if the model echoes the old
// code then shows the fix, the fix wins.
func (c *WholeFileCoder) GetEdits(response string) ([]Edit, error) {
	content, ok := lastFencedBlock(response)
	if !ok {
		return nil, nil // no fenced block -> nothing to apply (not an error)
	}
	return []Edit{{Path: c.path, Search: "", Replace: content}}, nil
}

// ApplyEdits writes each whole-file edit to disk. A write failure is surfaced in
// `failed`; everything written lands in `applied`. Upstream's apply_edits has
// the same applied/failed split so the caller knows what actually changed.
func (c *WholeFileCoder) ApplyEdits(edits []Edit) (applied, failed []Edit, err error) {
	for _, e := range edits {
		if writeErr := os.WriteFile(e.Path, []byte(e.Replace), 0o644); writeErr != nil {
			failed = append(failed, e)
			continue
		}
		applied = append(applied, e)
	}
	return applied, failed, nil
}

// lastFencedBlock returns the contents of the final ``` ... ``` block in text.
// It ignores the info string on the opening fence (e.g. "```go"). This is a
// minimal fence scanner — s02 has the full version.
func lastFencedBlock(text string) (string, bool) {
	lines := strings.Split(text, "\n")
	var (
		inBlock bool
		buf     []string
		last    string
		found   bool
	)
	for _, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			if inBlock {
				// closing fence: remember this block, reset for a possible next one
				last = strings.Join(buf, "\n")
				found = true
				buf = nil
				inBlock = false
			} else {
				inBlock = true
				buf = nil
			}
			continue
		}
		if inBlock {
			buf = append(buf, line)
		}
	}
	if !found {
		return "", false
	}
	// Whole files should end with a trailing newline; add one if the model
	// dropped it so gofmt doesn't flag a missing final newline.
	if last != "" && !strings.HasSuffix(last, "\n") {
		last += "\n"
	}
	return last, true
}
