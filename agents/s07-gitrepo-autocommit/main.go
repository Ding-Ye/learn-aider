package main

// main.go — a CLI that demonstrates the chapter's mechanism end to end:
//
//	apply a change to a file  ->  AUTO-COMMIT it  ->  print the new sha
//
// In the full aider loop (and in s01..s06) the "apply a change" step is the
// coder writing the model's edit to disk. Here we keep that step deliberately
// tiny — write the file from a flag, OR (with -instruction + a key) ask a model
// for the new contents — so the spotlight stays on the NEW thing s07 adds: the
// autoCommit() call that runs right after the edit lands. That post-apply hook
// is upstream base_coder.py auto_commit() -> repo.py commit() (L131); everything
// you see committed below is reversible with `git reset --hard HEAD~1`.
//
// No network is required for the default demo. Tests never call a model.

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func main() {
	var (
		repoDir     = flag.String("repo", ".", "path inside the git repo to operate on")
		file        = flag.String("file", "", "file to edit (relative to repo root or absolute)")
		content     = flag.String("content", "", "new file contents to write (whole-file edit)")
		message     = flag.String("m", "", "commit summary (else a default is generated)")
		instruction = flag.String("instruction", "", "natural-language change; asks the LLM for new contents (needs ANTHROPIC_API_KEY)")
		coAuthored  = flag.Bool("co-authored-by", false, "credit aider via a Co-authored-by trailer instead of rewriting the author name")
		model       = flag.String("model", "claude-sonnet-4-6", "model name (for -instruction and the co-authored-by trailer)")
		verbose     = flag.Bool("v", false, "verbose: print each step to stderr")
	)
	flag.Parse()

	if *file == "" {
		fmt.Fprintln(os.Stderr, "usage: go run . -file NAME -content '...'   (inside a git repo)")
		fmt.Fprintln(os.Stderr, "   or: go run . -file NAME -instruction '...' (with ANTHROPIC_API_KEY)")
		os.Exit(2)
	}

	if err := run(*repoDir, *file, *content, *message, *instruction, *model, *coAuthored, *verbose); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(repoDir, file, content, message, instruction, model string, coAuthored, verbose bool) error {
	// 1. Locate the git repo that contains repoDir. aider does this at startup
	//    (main.py get_git_root -> GitRepo); without a repo there is no undo
	//    buffer, so auto-commit is simply skipped upstream. Here we require one.
	repo, err := OpenGitRepo(repoDir)
	if err != nil {
		return err
	}
	if verbose {
		fmt.Fprintf(os.Stderr, "[s07] repo root: %s\n", repo.Root())
	}

	absFile := file
	if !filepath.IsAbs(absFile) {
		absFile = filepath.Join(repo.Root(), file)
	}

	// 2. Produce the new file contents. Either taken verbatim from -content, or
	//    asked from a model when -instruction is given. This stands in for the
	//    s02..s05 coder that parses an edit out of an LLM reply.
	newContent := content
	if instruction != "" {
		got, gerr := askModel(model, absFile, instruction, verbose)
		if gerr != nil {
			return gerr
		}
		newContent = got
	}
	if newContent == "" && instruction == "" {
		return errors.New("nothing to write: pass -content or -instruction")
	}

	// 3. APPLY the edit (write it to disk). In aider this is apply_edits().
	if err := os.WriteFile(absFile, []byte(newContent), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", absFile, err)
	}
	if verbose {
		fmt.Fprintf(os.Stderr, "[s07] wrote %d bytes to %s\n", len(newContent), file)
	}

	// 4. AUTO-COMMIT — the new step this chapter introduces. Everything above is
	//    "apply an edit"; this is the line that makes the edit a reversible,
	//    attributed commit. See autoCommit below.
	sha, err := autoCommit(repo, []string{absFile}, message, model, coAuthored, verbose)
	if err != nil {
		return err
	}
	fmt.Printf("committed %s\n", sha)
	return nil
}

// autoCommit is the post-apply hook: stage the edited files and commit them with
// aider-style attribution. This is base_coder.py auto_commit() condensed to its
// essence (build a context line + summary, choose attribution, call repo.commit).
// A clean tree (the edit changed nothing) is reported, not committed — exactly
// like upstream's silent return on `not diffs`.
func autoCommit(repo *GitRepo, files []string, summary, model string, coAuthored, verbose bool) (string, error) {
	attr := AiderDefaults() // rewrite author + committer to "(aider)"
	prefix := ""
	if coAuthored {
		// The co-authored-by style keeps the human as author and instead tags
		// the model in a trailer. Mutually exclusive with name-rewriting here.
		attr = Attribution{CoAuthoredBy: true, Model: model}
	}

	if summary == "" {
		// aider asks a weak model to summarize the diff; we keep it deterministic
		// and offline by naming the files touched. The mechanism (a generated
		// message) is the same; only the generator differs.
		summary = "edit " + strings.Join(relNames(repo, files), ", ")
	}

	sha, err := repo.Commit(files, prefix, summary, attr)
	if errors.Is(err, ErrNothingToCommit) {
		if verbose {
			fmt.Fprintln(os.Stderr, "[s07] no changes to commit (clean tree) — skipping")
		}
		return "(no commit: nothing changed)", nil
	}
	if err != nil {
		return "", err
	}
	if verbose {
		fmt.Fprintf(os.Stderr, "[s07] committed %s: %s\n", sha, summary)
	}
	return sha, nil
}

// relNames renders file paths relative to the repo root for a readable message.
func relNames(repo *GitRepo, files []string) []string {
	names := make([]string, 0, len(files))
	for _, f := range files {
		if rel, err := filepath.Rel(repo.Root(), f); err == nil {
			names = append(names, rel)
		} else {
			names = append(names, filepath.Base(f))
		}
	}
	return names
}

// askModel asks the LLM for the entire new contents of a file (a whole-file
// edit). It exists so the demo can be driven by a real instruction; the tests
// never reach it. The reply is stripped of an optional ``` fence so what we
// write to disk is just code.
func askModel(model, absFile, instruction string, verbose bool) (string, error) {
	apiKey := os.Getenv("ANTHROPIC_API_KEY")
	if apiKey == "" {
		return "", errors.New("-instruction requires ANTHROPIC_API_KEY (or use -content for an offline demo)")
	}
	var current string
	if b, err := os.ReadFile(absFile); err == nil {
		current = string(b)
	}
	provider := NewAnthropicProvider(apiKey, model)
	system := "You are a coding assistant. Reply with ONLY the complete new contents " +
		"of the file, no prose, no fences."
	user := fmt.Sprintf("File %s currently contains:\n\n%s\n\nApply this change: %s",
		filepath.Base(absFile), current, instruction)

	if verbose {
		fmt.Fprintf(os.Stderr, "[s07] asking %s for new contents of %s\n", model, filepath.Base(absFile))
	}
	resp, err := provider.CreateMessage(context.Background(), CreateMessageRequest{
		Messages: []Message{{Role: "user", Content: []ContentBlock{{Type: "text", Text: user}}}},
		System:   system,
	})
	if err != nil {
		return "", err
	}
	var sb strings.Builder
	for _, b := range resp.Content {
		if b.Type == "text" {
			sb.WriteString(b.Text)
		}
	}
	return stripFence(sb.String()), nil
}

// stripFence removes a single leading/trailing ``` fence if the model wrapped
// the file in one despite being told not to.
func stripFence(s string) string {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "```") {
		return s + "\n"
	}
	lines := strings.Split(s, "\n")
	if len(lines) >= 2 {
		lines = lines[1:] // drop opening ```lang
		if strings.TrimSpace(lines[len(lines)-1]) == "```" {
			lines = lines[:len(lines)-1]
		}
	}
	return strings.Join(lines, "\n") + "\n"
}
