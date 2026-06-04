package main

// gitrepo.go — the mechanism this chapter teaches: GitRepo + auto-commit.
//
// WHY THIS EXISTS (aider's "git-as-undo" philosophy)
// --------------------------------------------------
// An LLM editing your files is terrifying unless every edit is reversible. Aider
// makes it reversible by turning *each* applied edit into its own git commit:
// after the coder writes a file, the loop stages exactly that file and commits
// it with a generated message. The user never has to trust the model blindly —
// they can `git log` to see what changed, `git show <sha>` to inspect one edit,
// and `git reset --hard HEAD~1` (aider's /undo) to throw it away. The commit IS
// the undo buffer. That single design decision is what makes confident,
// iterative AI pair-programming possible.
//
// Two more things aider does that we mirror here:
//
//  1. Attribution. The commit is marked as aider's work so a human reading
//     `git log` can tell at a glance which commits came from the AI. Upstream
//     offers two styles (aider/repo.py commit L131): rewrite the author/committer
//     NAME to "You (aider)" via the GIT_AUTHOR_NAME / GIT_COMMITTER_NAME env
//     vars, OR add a "Co-authored-by: aider (<model>) <aider@aider.chat>"
//     trailer to the message. We implement BOTH and let the caller choose.
//
//  2. No empty commits. If nothing actually changed on disk, committing would
//     create a noise commit (or, with the default git, fail). aider guards on
//     "is the tree dirty for these files?" before committing (repo.py L201-206).
//     We do the same: Commit on a clean tree is a no-op that returns ErrNothingToCommit.
//
// Upstream wraps GitPython. We shell out to the real `git` binary via os/exec —
// fewer moving parts, and you can read every command we run. Everything below is
// the minimum viable subset of aider/repo.py's GitRepo.

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// ErrNothingToCommit is returned by Commit when the working tree has no staged
// or unstaged changes for the requested files. Mirrors upstream's early `return`
// in repo.py commit() when `not self.repo.is_dirty()` / `not diffs`.
var ErrNothingToCommit = errors.New("gitrepo: nothing to commit (clean tree)")

// Attribution controls how a commit is credited to aider. The zero value adds
// nothing (a plain commit). aider's real default rewrites the author/committer
// name; the co-authored-by trailer is the alternative, kinder-to-blame style.
type Attribution struct {
	// AttributeAuthor rewrites GIT_AUTHOR_NAME to "<user.name> (aider)" so the
	// AI shows up as the author. Upstream flag: --attribute-author.
	AttributeAuthor bool
	// AttributeCommitter rewrites GIT_COMMITTER_NAME to "<user.name> (aider)".
	// Upstream flag: --attribute-committer. Upstream defaults this to True.
	AttributeCommitter bool
	// CoAuthoredBy appends a "Co-authored-by:" trailer naming the model instead
	// of touching author/committer. Upstream flag: --attribute-co-authored-by.
	CoAuthoredBy bool
	// Model is the model name embedded in the co-authored-by trailer. Only used
	// when CoAuthoredBy is true.
	Model string
}

// AiderDefaults returns the attribution aider applies to an AI edit out of the
// box: rewrite both author and committer names to "(aider)". (Upstream toggles
// these via --attribute-* flags; co-authored-by is off by default.)
func AiderDefaults() Attribution {
	return Attribution{AttributeAuthor: true, AttributeCommitter: true}
}

// GitRepo is a thin wrapper over the `git` CLI rooted at a working tree. It is
// the s07 analog of aider/repo.py's GitRepo class — minus the GitPython object,
// the .aiderignore handling, and the LLM-generated commit messages (we take the
// message as a plain string; aider can also ask a weak model to write it).
type GitRepo struct {
	root   string // absolute path to the working tree root
	gitBin string // resolved path to the git binary
}

// OpenGitRepo finds the git working tree that contains `startDir` and returns a
// GitRepo rooted there. This mirrors upstream's constructor, which calls
// git.Repo(fname, search_parent_directories=True) to walk UP from a file/dir to
// the enclosing repo (repo.py L110). We ask git itself via
// `git -C <dir> rev-parse --show-toplevel` so the answer is exactly git's.
func OpenGitRepo(startDir string) (*GitRepo, error) {
	gitBin, err := exec.LookPath("git")
	if err != nil {
		return nil, fmt.Errorf("gitrepo: git not found on PATH: %w", err)
	}
	abs, err := filepath.Abs(startDir)
	if err != nil {
		return nil, err
	}
	// --show-toplevel prints the absolute root of the working tree; it fails
	// (non-zero exit) if startDir is not inside any repo.
	out, err := runGit(gitBin, abs, "rev-parse", "--show-toplevel")
	if err != nil {
		return nil, fmt.Errorf("gitrepo: %s is not inside a git repo: %w", abs, err)
	}
	root := strings.TrimSpace(out)
	if root == "" {
		return nil, fmt.Errorf("gitrepo: empty repo root for %s", abs)
	}
	// EvalSymlinks normalizes e.g. macOS /var -> /private/var so later path
	// comparisons against the root line up (upstream's safe_abs_path does the
	// same resolution).
	if resolved, rerr := filepath.EvalSymlinks(root); rerr == nil {
		root = resolved
	}
	return &GitRepo{root: root, gitBin: gitBin}, nil
}

// Root returns the absolute working-tree root.
func (r *GitRepo) Root() string { return r.root }

// Stage adds the given paths to the git index. Paths may be absolute or relative
// to the repo root. This is upstream's `self.repo.git.add(fname)` loop inside
// commit() (repo.py L282-286): we stage exactly the files the coder touched, so
// the resulting commit is scoped to this one edit and nothing else the user may
// have left dirty.
func (r *GitRepo) Stage(paths ...string) error {
	if len(paths) == 0 {
		return nil
	}
	args := append([]string{"add", "--"}, r.relPaths(paths)...)
	if _, err := runGit(r.gitBin, r.root, args...); err != nil {
		return fmt.Errorf("gitrepo: stage %v: %w", paths, err)
	}
	return nil
}

// Commit stages `paths`, builds the commit message, and commits — but ONLY if
// those paths actually have changes. It returns the new short SHA.
//
// This is the heart of the chapter: it collapses upstream repo.py commit()
// (L131-318) to its load-bearing skeleton:
//
//	dirty guard  ->  build message (prefix + body + trailer)  ->  stage  ->
//	commit with attribution env vars  ->  read back the new sha
//
// `prefix` is prepended to `summary` ("aider: " in upstream when
// attribute_commit_message_* is set; empty otherwise). `attr` decides how the
// commit is credited.
func (r *GitRepo) Commit(paths []string, prefix, summary string, attr Attribution) (string, error) {
	if err := r.Stage(paths...); err != nil {
		return "", err
	}

	// No-empty-commit guard. We check AFTER staging: `git diff --cached --quiet`
	// exits 0 (clean) when the index has no staged changes, non-zero when it
	// does. This is the os/exec equivalent of upstream's `if not diffs: return`.
	if !r.hasStagedChanges() {
		return "", ErrNothingToCommit
	}

	message := buildCommitMessage(prefix, summary, attr)

	// Attribution via environment variables, exactly like upstream's
	// set_git_env(GIT_AUTHOR_NAME, ...) / set_git_env(GIT_COMMITTER_NAME, ...).
	// We compute "<user.name> (aider)" once and inject it only for the names the
	// caller asked to rewrite. We start from the current process env and override
	// just those keys, so the child git inherits everything else.
	env := os.Environ()
	if attr.AttributeAuthor || attr.AttributeCommitter {
		userName := r.configuredUserName()
		aiderName := userName + " (aider)"
		if attr.AttributeAuthor {
			env = append(env, "GIT_AUTHOR_NAME="+aiderName)
		}
		if attr.AttributeCommitter {
			env = append(env, "GIT_COMMITTER_NAME="+aiderName)
		}
	}

	if _, err := runGitEnv(r.gitBin, r.root, env, "commit", "-m", message); err != nil {
		return "", fmt.Errorf("gitrepo: commit: %w", err)
	}
	return r.HeadSHA()
}

// IsDirty reports whether the working tree has uncommitted changes (staged or
// not). With no paths it checks the whole tree; with paths it checks just those.
// Upstream: repo.py is_dirty() (L598) -> self.repo.is_dirty(). We use
// `git status --porcelain`, which prints one line per changed path and nothing
// for a clean tree.
func (r *GitRepo) IsDirty(paths ...string) bool {
	args := []string{"status", "--porcelain"}
	if len(paths) > 0 {
		args = append(args, "--")
		args = append(args, r.relPaths(paths)...)
	}
	out, err := runGit(r.gitBin, r.root, args...)
	if err != nil {
		// A failed status (e.g. brand-new repo edge cases) is treated as "dirty"
		// so we don't silently skip a commit. Upstream similarly errs on the
		// side of attempting the commit.
		return true
	}
	return strings.TrimSpace(out) != ""
}

// HeadSHA returns the short (7-char) SHA of the current HEAD commit. Upstream:
// get_head_commit_sha(short=True) (repo.py L610). Returns an error before the
// first commit exists (HEAD is unborn), mirroring upstream's `return None`.
func (r *GitRepo) HeadSHA() (string, error) {
	out, err := runGit(r.gitBin, r.root, "rev-parse", "--short", "HEAD")
	if err != nil {
		return "", fmt.Errorf("gitrepo: no HEAD commit yet: %w", err)
	}
	return strings.TrimSpace(out), nil
}

// HeadMessage returns the subject line of the current HEAD commit. Upstream:
// get_head_commit_message() (repo.py L618). Used by the tests to assert the
// message we built is what actually got recorded.
func (r *GitRepo) HeadMessage() (string, error) {
	out, err := runGit(r.gitBin, r.root, "log", "-1", "--pretty=%B")
	if err != nil {
		return "", fmt.Errorf("gitrepo: read HEAD message: %w", err)
	}
	return strings.TrimRight(out, "\n"), nil
}

// HeadAuthor returns the author name of the current HEAD commit. Used to verify
// attribution (that GIT_AUTHOR_NAME took effect). git pretty format %an = author
// name.
func (r *GitRepo) HeadAuthor() (string, error) {
	out, err := runGit(r.gitBin, r.root, "log", "-1", "--pretty=%an")
	if err != nil {
		return "", fmt.Errorf("gitrepo: read HEAD author: %w", err)
	}
	return strings.TrimSpace(out), nil
}

// TrackedFiles lists the repo-relative paths git knows about (committed + staged).
// Upstream: get_tracked_files() (repo.py L433), which walks the HEAD tree and
// unions in the index. `git ls-files` gives us both in one call, already
// repo-relative and in posix form.
func (r *GitRepo) TrackedFiles() ([]string, error) {
	out, err := runGit(r.gitBin, r.root, "ls-files")
	if err != nil {
		return nil, fmt.Errorf("gitrepo: list tracked files: %w", err)
	}
	var files []string
	for _, line := range strings.Split(out, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			files = append(files, line)
		}
	}
	return files, nil
}

// ---- internal helpers ----

// hasStagedChanges returns true if the index has changes ready to commit.
// `git diff --cached --quiet` exits 0 when clean, 1 when there are staged diffs.
func (r *GitRepo) hasStagedChanges() bool {
	_, err := runGit(r.gitBin, r.root, "diff", "--cached", "--quiet")
	// err != nil means a non-zero exit, i.e. there ARE staged changes.
	return err != nil
}

// configuredUserName reads `git config user.name`, falling back to "aider" if it
// is unset (so the "(aider)" suffix still produces a sensible name). Upstream
// does `self.repo.git.config("--get", "user.name")` (repo.py L291).
func (r *GitRepo) configuredUserName() string {
	out, err := runGit(r.gitBin, r.root, "config", "--get", "user.name")
	name := strings.TrimSpace(out)
	if err != nil || name == "" {
		return "aider"
	}
	return name
}

// relPaths converts a mix of absolute / relative paths into paths relative to
// the repo root, so the same call works whether the caller passes an absolute
// edit path (what a coder has) or a bare filename.
func (r *GitRepo) relPaths(paths []string) []string {
	rel := make([]string, 0, len(paths))
	for _, p := range paths {
		if filepath.IsAbs(p) {
			if rp, err := filepath.Rel(r.root, p); err == nil {
				rel = append(rel, rp)
				continue
			}
		}
		rel = append(rel, p)
	}
	return rel
}

// buildCommitMessage assembles the final message: optional prefix + summary,
// then the optional co-authored-by trailer. Upstream builds this as
// `full_commit_message = commit_message + commit_message_trailer`, with the
// "aider: " prefix added when attribute_commit_message_* is set (repo.py
// L269-275).
func buildCommitMessage(prefix, summary string, attr Attribution) string {
	if summary == "" {
		summary = "(no commit message provided)"
	}
	msg := prefix + summary
	if attr.CoAuthoredBy {
		model := attr.Model
		if model == "" {
			model = "unknown-model"
		}
		msg += fmt.Sprintf("\n\nCo-authored-by: aider (%s) <aider@aider.chat>", model)
	}
	return msg
}

// runGit runs `git -C <dir> <args...>` and returns combined stdout. On a
// non-zero exit it returns an error carrying stderr, so callers can both detect
// failure AND (for the --quiet probes) use the exit code as a boolean signal.
func runGit(gitBin, dir string, args ...string) (string, error) {
	return runGitEnv(gitBin, dir, nil, args...)
}

// runGitEnv is runGit with an explicit environment (used to inject the
// GIT_AUTHOR_NAME / GIT_COMMITTER_NAME attribution vars). A nil env means
// inherit the parent's.
func runGitEnv(gitBin, dir string, env []string, args ...string) (string, error) {
	cmd := exec.Command(gitBin, append([]string{"-C", dir}, args...)...)
	if env != nil {
		cmd.Env = env
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return stdout.String(), fmt.Errorf("git %s: %w: %s",
			strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}
