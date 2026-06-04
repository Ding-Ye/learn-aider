package main

// coder.go — the minimum coder loop.
//
// This is aider stripped to its spine. Upstream the loop is
// Coder.run -> run_one -> send_message -> apply_updates (aider/coders/
// base_coder.py, ~700 lines once you include reflection, token budgeting,
// streaming, git, and the repo map). Here it is one method, Coder.Run, doing
// five steps:
//
//	read -> build prompt -> Provider.CreateMessage -> extract one edit -> write
//
// Everything aider adds later (search/replace formats, prompts-per-format, git
// auto-commit, repo map, lint-and-reflect) is a layer on top of this skeleton.
// Get the skeleton clear first.

import (
	"context"
	"fmt"
	"os"
	"strings"
)

// ---- aider-specific types (from the curriculum's shared catalog) ----

// EditFormat names the strategy the LLM is told to emit. Upstream this is
// Coder.edit_format, and it selects both the system prompt and the parser. s01
// only implements the simplest one; s02..s04 add the rest.
type EditFormat string

const (
	FormatWhole EditFormat = "whole" // wholefile_coder.py — return the entire new file
	FormatDiff  EditFormat = "diff"  // editblock_coder.py — SEARCH/REPLACE (s03)
	FormatUDiff EditFormat = "udiff" // udiff_coder.py — unified diff (s04)
)

// Edit is one parsed file change. For whole-file, Search is "" and Replace holds
// the ENTIRE new file. For the diff/udiff formats added later, Search is the
// text to find and Replace is the substitution (upstream's (path, original,
// updated) tuples). s01 only ever produces whole-file edits.
type Edit struct {
	Path    string
	Search  string // "" means whole-file replace
	Replace string
}

// Coder ties a Provider to an edit format and runs the loop. Upstream the Coder
// class is huge and the edit format is chosen by subclass (WholeFileCoder,
// EditBlockCoder, ...). We collapse that to one struct with a Format field
// because s01 has exactly one format; s05 reintroduces the per-format split.
type Coder struct {
	Provider Provider
	Format   EditFormat
	Verbose  bool

	// out/errOut are injectable so tests can capture the transcript instead of
	// writing to the real terminal. Default to os.Stdout / os.Stderr in main.
	out  *os.File
	errw *os.File
}

// NewCoder builds a whole-file coder writing to stdout/stderr.
func NewCoder(p Provider, verbose bool) *Coder {
	return &Coder{
		Provider: p,
		Format:   FormatWhole,
		Verbose:  verbose,
		out:      os.Stdout,
		errw:     os.Stderr,
	}
}

// EditFileTool is declared for documentation: it shows the "real" tool-calling
// contract aider would use. But s01 deliberately does NOT make the model call
// it. Instead we steer the model (via the prompt) to reply with one fenced code
// block, and we parse that. Why? Because a fenced block is the lowest-common-
// denominator output that EVERY model — even ones without tool-calling — can
// produce, and parsing it is ~10 lines. This is exactly aider's "whole" edit
// format. Tool-calling enters the curriculum later.
func EditFileTool() ToolSchema {
	return ToolSchema{
		Name:        "edit_file",
		Description: "Overwrite a file on disk with new contents.",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"path":     map[string]interface{}{"type": "string"},
				"contents": map[string]interface{}{"type": "string"},
			},
			"required": []string{"path", "contents"},
		},
	}
}

// Run executes one read -> prompt -> call -> extract -> write cycle.
//
// This is the whole lesson. Compare upstream send_message (base_coder.py L1419)
// + apply_updates (L2296): same five beats, minus reflection, streaming, token
// budgeting, dry-run, and git.
func (c *Coder) Run(ctx context.Context, path, instruction string) error {
	// 1. READ the target file. The model needs to see the current contents to
	//    rewrite them. (Upstream this is get_files_content feeding format_messages.)
	original, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}

	// 2. BUILD the prompt. The system prompt defines the edit format contract;
	//    the user message carries the file + the instruction. Steering the model
	//    to "return the full file in ONE fenced block" is what makes the whole-
	//    file format work.
	req := CreateMessageRequest{
		System: wholeFileSystemPrompt(path),
		Messages: []Message{
			{
				Role: "user",
				Content: []ContentBlock{{
					Type: "text",
					Text: buildUserPrompt(path, string(original), instruction),
				}},
			},
		},
	}

	if c.Verbose {
		fmt.Fprintf(c.errw, "[s01] sending %d bytes of %s + instruction %q\n",
			len(original), path, instruction)
	}

	// 3. CALL the provider. This is the single network round-trip.
	resp, err := c.Provider.CreateMessage(ctx, req)
	if err != nil {
		return fmt.Errorf("provider call: %w", err)
	}
	reply := firstText(resp.Content)
	if c.Verbose {
		fmt.Fprintf(c.errw, "[s01] stop_reason=%s in=%d out=%d tokens\n",
			resp.StopReason, resp.Usage.InputTokens, resp.Usage.OutputTokens)
	}

	// 4. EXTRACT the edit. Whole-file = the first fenced code block in the reply.
	edit, err := c.extractEdit(path, reply)
	if err != nil {
		// Upstream a malformed reply becomes a reflected_message and the loop
		// retries. s01 has no reflection, so we surface the error and stop —
		// but we still print the raw reply so the user can see what went wrong.
		fmt.Fprintf(c.errw, "[s01] could not parse an edit from the reply:\n%s\n", reply)
		return err
	}

	// 5. WRITE it back. One edit, one file. (Upstream apply_edits, after a
	//    dry-run pass and a git stage.)
	if err := c.applyWholeFile(edit); err != nil {
		return fmt.Errorf("apply edit: %w", err)
	}
	fmt.Fprintf(c.out, "Applied edit to %s (%d bytes)\n", edit.Path, len(edit.Replace))
	return nil
}

// extractEdit turns an LLM reply into exactly one whole-file Edit. The whole-
// file format is: the model replies with one fenced code block whose contents
// ARE the new file. We pull that block out and pair it with the known path.
func (c *Coder) extractEdit(path, reply string) (Edit, error) {
	body, ok := extractCodeBlock(reply)
	if !ok {
		return Edit{}, fmt.Errorf("no fenced code block found in reply")
	}
	return Edit{Path: path, Search: "", Replace: body}, nil
}

// applyWholeFile writes Edit.Replace as the complete new contents of Edit.Path.
// "Whole-file" means Search is empty and Replace is the entire file, so applying
// it is a single os.WriteFile — no matching, no merging. That simplicity is the
// whole reason whole-file is aider's baseline format (and ours).
func (c *Coder) applyWholeFile(e Edit) error {
	if e.Search != "" {
		// Defensive: s01 only knows whole-file. A non-empty Search means some
		// caller handed us a diff-style edit we can't apply yet (that's s03).
		return fmt.Errorf("applyWholeFile got a non-whole-file edit (Search != \"\")")
	}
	return os.WriteFile(e.Path, []byte(e.Replace), 0o644)
}

// extractCodeBlock returns the body of the FIRST triple-backtick fenced block in
// s, with the optional language tag on the opening fence stripped. It returns
// ("", false) when there is no complete fenced block.
//
// We strip the fences because the model writes ```python\n<code>\n``` but the
// file on disk must contain only <code> — the backticks are chat syntax, not
// part of the program. We take only the FIRST block because the whole-file
// format is "one file per reply"; multi-file handling is s02's job (it scans the
// line ABOVE each fence for a filename). Keeping s01 to one block keeps the
// parser honest about how small the baseline really is.
func extractCodeBlock(s string) (string, bool) {
	lines := strings.Split(s, "\n")
	start := -1
	for i, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			start = i
			break
		}
	}
	if start == -1 {
		return "", false // no opening fence
	}
	// Everything after the opening fence, until the next fence line, is the body.
	var body []string
	for i := start + 1; i < len(lines); i++ {
		if strings.HasPrefix(strings.TrimSpace(lines[i]), "```") {
			return strings.Join(body, "\n"), true // closing fence found
		}
		body = append(body, lines[i])
	}
	return "", false // opening fence but no closing fence -> treat as malformed
}

// ---- prompt construction ----

// wholeFileSystemPrompt is the format contract. It mirrors aider's
// WholeFilePrompts.main_system: "return the entire file in a single fenced
// block." We pass the path so the model knows which file it is rewriting.
func wholeFileSystemPrompt(path string) string {
	return fmt.Sprintf(`You are a coding assistant that edits a single file.
The user gives you the current contents of %q and an instruction.
Reply with the ENTIRE new contents of the file inside ONE fenced code block:

`+"```"+`
<the complete new file here>
`+"```"+`

Rules:
- Output the WHOLE file, not a diff and not a snippet.
- Put nothing important outside the single code block.
- Preserve everything the instruction does not ask you to change.`, path)
}

// buildUserPrompt packs the file contents and the instruction into one user turn.
func buildUserPrompt(path, contents, instruction string) string {
	return fmt.Sprintf("File: %s\n\n```\n%s\n```\n\nInstruction: %s", path, contents, instruction)
}

// firstText returns the concatenation of all text blocks in a response. In s01
// the model replies with a single text block, but concatenating is robust if a
// provider splits the reply.
func firstText(content []ContentBlock) string {
	var sb strings.Builder
	for _, b := range content {
		if b.Type == "text" {
			sb.WriteString(b.Text)
		}
	}
	return sb.String()
}
