---
title: "s07 · GitRepo integration + auto-commit"
chapter: 7
slug: s07-gitrepo-autocommit
est_read_min: 13
---

# s07 · GitRepo integration + auto-commit

> What this teaches: the *auto-commit* mechanism — aider's "git-as-undo" philosophy. After every applied edit the loop turns that change into its own git commit, so the user can always `git log` / `git show` / `git reset` (= `/undo`) to inspect or revert what the AI did. We build a `GitRepo` wrapper over the real `git` binary (`os/exec`): find the root, stage the edited files, commit with aider-style attribution, and report the new SHA — with a guard against empty commits.

---

## Problem / 问题

By s06 we have a real interactive session: a coder parses an edit format (s02–s05), an `InputOutput` layer prints and confirms, and `/`-commands mutate the file scope. But there is a quiet terror at the center of it. An LLM is about to *rewrite files on your disk*. If it gets an edit wrong — clobbers a function, deletes a line it shouldn't — what protects you? Right now, nothing. The edit lands and the previous contents are gone.

The pain is trust. People won't let an AI touch a real codebase unless mistakes are cheap to undo. You could build a bespoke undo stack (snapshot every file before every edit, keep a history, add a `/redo`…), but that reinvents something every developer already has and trusts: version control. This chapter solves the trust problem the way aider does — by making **every AI edit an atomic git commit**. The commit history becomes the undo buffer, inspected and reverted with tools the user already knows.

## Solution / 解决方案

Lean on git instead of inventing an undo system. After the coder applies an edit, the loop calls `autoCommit(editedFiles)`, which stages exactly those files and commits them. The new mechanism is one post-apply step plus a small `GitRepo` wrapper over the `git` CLI.

Three decisions carry the design:

1. **The commit is the undo buffer.** We don't snapshot files ourselves. Each edit becomes one scoped commit (`git add -- <file>` then `git commit`), so the user gets `git log`, `git show <sha>`, and `git reset --hard HEAD~1` (aider's `/undo`) for free. Atomicity matters: staging only the edited files keeps one commit = one edit, even if the tree has other dirty files.
2. **Attribution marks the AI's work, two ways.** Either rewrite the author/committer *name* to `"You (aider)"` via the `GIT_AUTHOR_NAME` / `GIT_COMMITTER_NAME` env vars, or add a `Co-authored-by: aider (<model>) <aider@aider.chat>` trailer. Both let a human reading `git log` tell which commits came from the AI; the trailer keeps the human as author, which is kinder to `git blame`.
3. **No empty commits.** An edit that changed nothing must not create a noise commit. After staging we probe `git diff --cached --quiet`; a clean index returns `ErrNothingToCommit` and the loop silently skips — exactly upstream's early `return` on `not diffs`.

## How It Works / 工作原理

```ascii-anim frames=2
┌───────────────────────────────────────────────────────────────┐
│  coder applies edit ──▶ writes file.go on disk                 │
│                              │                                 │
│                              ▼   autoCommit([file.go])         │
│              ┌───────────────────────────────────┐            │
│              │ GitRepo.Commit(paths, prefix, sum) │            │
│              └───────────────────────────────────┘            │
│                 │            │             │                   │
│        git add -- file   diff --cached   build message         │
│           (Stage)        --quiet ? ──────────┐                 │
│                 │         clean→ErrNothing    │ prefix+summary  │
│                 ▼         dirty↓              │ +co-author?     │
│        GIT_AUTHOR_NAME="You (aider)" ◀────────┘                 │
│        GIT_COMMITTER_NAME=...                                  │
│                 │                                             │
│                 ▼   git commit -m <message>                   │
│           rev-parse --short HEAD ──▶ "a1b2c3d" (the new sha)   │
└───────────────────────────────────────────────────────────────┘
```

The commit core (excerpt from [`agents/s07-gitrepo-autocommit/gitrepo.go`](https://github.com/Ding-Ye/learn-aider/blob/main/agents/s07-gitrepo-autocommit/gitrepo.go)):

```go
// Commit stages `paths`, builds the commit message, and commits — but ONLY if
// those paths actually have changes. It returns the new short SHA.
func (r *GitRepo) Commit(paths []string, prefix, summary string, attr Attribution) (string, error) {
	if err := r.Stage(paths...); err != nil {
		return "", err
	}

	// No-empty-commit guard. `git diff --cached --quiet` exits 0 (clean) when the
	// index has no staged changes; this is upstream's `if not diffs: return`.
	if !r.hasStagedChanges() {
		return "", ErrNothingToCommit
	}

	message := buildCommitMessage(prefix, summary, attr)

	// Attribution via environment variables, exactly like upstream's
	// set_git_env(GIT_AUTHOR_NAME, ...) / set_git_env(GIT_COMMITTER_NAME, ...).
	env := os.Environ()
	if attr.AttributeAuthor || attr.AttributeCommitter {
		aiderName := r.configuredUserName() + " (aider)"
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
```

**Four non-obvious points**:

1. **The guard is checked *after* staging, not before.** `git diff --cached --quiet` inspects the *index*, so we must stage first, then probe. Its exit code is the boolean: exit 0 = clean = nothing to commit; non-zero = staged changes exist. We deliberately treat a non-zero exit as the *success* signal here — that's why `hasStagedChanges` returns `err != nil`.
2. **Attribution is injected per-commit, not configured globally.** We start from `os.Environ()` and append only the name vars the caller asked for, so the child `git` inherits the user's real config for everything else and only the *display name* is overridden for this one commit. Upstream uses a context manager to set-then-restore the same env vars; appending to a fresh child env achieves the same isolation without mutating our own process.
3. **The SHA is read back from git, not guessed.** After committing we run `git rev-parse --short HEAD`. We never construct or predict a hash; the source of truth for "what commit did we just make" is git itself (mirrors `get_head_commit_sha`).
4. **Paths are normalized to the repo root.** A coder hands us absolute edit paths; `relPaths` turns them into repo-relative paths before `git add`, so `Commit` works whether you pass `/abs/path/file.go` or a bare `file.go`.

## What Changed (vs. s06) / 与 s06 的变化

s06's loop ended at "apply the edit." s07 adds a post-apply step: the edit is persisted as an atomic, attributed git commit. The new surface is a `GitRepo` wrapper and one `autoCommit` call.

```diff
  // s06: the loop applied the edit and stopped there.
  applied, failed, _ := coder.ApplyEdits(edits)
  io.ToolOutput(fmt.Sprintf("applied %d edits", len(applied)))
+
+ // s07: after applying, persist each edit as its OWN git commit so it's
+ // reversible (aider's git-as-undo). This is base_coder.py auto_commit().
+ repo, _ := OpenGitRepo(".")
+ edited := pathsOf(applied)
+ sha, err := repo.Commit(edited, "", summaryFor(edited), AiderDefaults())
+ if errors.Is(err, ErrNothingToCommit) {
+ 	// edit changed nothing on disk — no noise commit
+ } else if err == nil {
+ 	io.ToolOutput("committed " + sha) // user can `git reset --hard HEAD~1`
+ }
```

Semantically: in s06 an edit was a one-way write — apply it and hope. In s07 every edit is bracketed by version control. The loop gains a memory: each turn leaves a commit behind, and that commit history is exactly the audit trail and undo buffer that makes confident AI pair-programming possible. Nothing about how edits are *parsed* changed; what changed is that edits now *survive* as reversible history.

## Try It / 动手试一试

```bash
cd agents/s07-gitrepo-autocommit

# Set up a throwaway repo to play in.
mkdir -p /tmp/demo && (cd /tmp/demo && git init -q && \
  git config user.name "Ada" && git config user.email a@e.com && \
  echo hi > app.txt && git add app.txt && git commit -q -m init)

# OFFLINE: apply a whole-file edit, then auto-commit it. Prints the new sha.
go run . -repo /tmp/demo -file app.txt -content $'hello\nworld\n' -m "say hello" -v

# Inspect what just happened — the commit IS the undo buffer:
git -C /tmp/demo log -1 --pretty='%h %s  (author: %an)'
git -C /tmp/demo reset --hard HEAD~1     # this is what aider's /undo runs

# No actual change -> no empty commit (a clean-tree no-op).
go run . -repo /tmp/demo -file app.txt -content "$(cat /tmp/demo/app.txt)" -v

# Co-authored-by attribution instead of rewriting the author name.
echo more >> /tmp/demo/app.txt
go run . -repo /tmp/demo -file app.txt -content "$(cat /tmp/demo/app.txt)" \
  -m "append a line" -co-authored-by -model claude-sonnet-4-6

# RUN: let the LLM produce the new contents, then auto-commit.
export ANTHROPIC_API_KEY=sk-ant-...
go run . -repo /tmp/demo -file app.txt -instruction "uppercase everything" -v

# tests (no network; real git in a temp repo)
go test -v ./...
```

Expected output shape:

```
# go run . -content ... -v   (SHA differs every run):
[s07] repo root: /tmp/demo
[s07] wrote 12 bytes to app.txt
[s07] committed a1b2c3d: say hello
committed a1b2c3d

# git log -1 after it:
a1b2c3d say hello  (author: Ada (aider))
#                                  ^^^^^^^ author NAME rewritten to mark aider's work

# re-run with no change:
[s07] no changes to commit (clean tree) — skipping
committed (no commit: nothing changed)

# -co-authored-by run, git log -1 --pretty=%B:
append a line

Co-authored-by: aider (claude-sonnet-4-6) <aider@aider.chat>
```

A full edit → commit → undo transcript lives in [`testdata/expected.txt`](https://github.com/Ding-Ye/learn-aider/blob/main/agents/s07-gitrepo-autocommit/testdata/expected.txt).

## Upstream Source Reading / 上游源码阅读

aider's auto-commit lives in `aider/repo.py`'s `GitRepo.commit()`. It is triggered from `base_coder.py`'s `auto_commit()` right after `apply_edits`. The method is long (L131–318) mostly because of an intricate attribution flag matrix; the *executable* core is short and is what we ported. The excerpt below is that core: the no-empty-commit guard, the message build, the scoped staging, the attribution env vars, the commit, and reading back the SHA.

```upstream:aider/repo.py#L277-L318
        cmd = ["-m", full_commit_message]
        if not self.git_commit_verify:
            cmd.append("--no-verify")
        if fnames:
            fnames = [str(self.abs_root_path(fn)) for fn in fnames]
            for fname in fnames:
                try:
                    self.repo.git.add(fname)
                except ANY_GIT_ERROR as err:
                    self.io.tool_error(f"Unable to add {fname}: {err}")
            cmd += ["--"] + fnames
        else:
            cmd += ["-a"]

        original_user_name = self.repo.git.config("--get", "user.name")
        original_committer_name_env = os.environ.get("GIT_COMMITTER_NAME")
        original_author_name_env = os.environ.get("GIT_AUTHOR_NAME")
        committer_name = f"{original_user_name} (aider)"

        try:
            # Use context managers to handle environment variables
            with contextlib.ExitStack() as stack:
                if use_attribute_committer:
                    stack.enter_context(
                        set_git_env(
                            "GIT_COMMITTER_NAME", committer_name, original_committer_name_env
                        )
                    )
                if use_attribute_author:
                    stack.enter_context(
                        set_git_env("GIT_AUTHOR_NAME", committer_name, original_author_name_env)
                    )

                # Perform the commit
                self.repo.git.commit(cmd)
                commit_hash = self.get_head_commit_sha(short=True)
                self.io.tool_output(f"Commit {commit_hash} {commit_message}", bold=True)
                return commit_hash, commit_message

        except ANY_GIT_ERROR as err:
            self.io.tool_error(f"Unable to commit: {err}")
            # No return here, implicitly returns None
```

**Reading notes**:

- **GitPython vs. `os/exec`.** Upstream wraps a GitPython `Repo`; `self.repo.git.add(...)` and `self.repo.git.commit(cmd)` shell out under the hood. We call the `git` binary directly so every command (`git add -- <file>`, `git commit -m`, `git rev-parse --short HEAD`) is visible and there is no extra dependency. Same commands, fewer layers.
- **Scoped staging is the atomicity guarantee.** The `for fname in fnames: self.repo.git.add(fname)` loop plus `cmd += ["--"] + fnames` stages exactly the edited files. That is what makes one edit = one commit; s07's `Stage(paths...)` is the direct port.
- **Attribution is set-then-restore env, per commit.** Upstream uses `set_git_env` inside a `contextlib.ExitStack` so `GIT_AUTHOR_NAME` / `GIT_COMMITTER_NAME` are restored afterward. s07 instead appends to a *fresh child env* (`os.Environ()` + overrides) passed to that one `git commit` — same isolation, no need to restore because we never mutated our own process.
- **The no-empty-commit guard sits just above this excerpt** (repo.py L201–206: `if not fnames and not self.repo.is_dirty(): return`, then `if not diffs: return`). s07 expresses it as `git diff --cached --quiet` → `ErrNothingToCommit`.
- **A piece we deliberately simplify.** The ~50 lines of attribution-flag precedence (L218–267) resolve `--attribute-author` / `--attribute-committer` / `--attribute-co-authored-by` against whether the edit was AI-made. s07 keeps the *two styles* and the env-var mechanism but exposes them as plain `Attribution` booleans — the curriculum teaches the mechanism, not the flag matrix.

**Read further**: start at `aider/coders/base_coder.py` → `auto_commit` (the trigger after `apply_edits`), follow it into `aider/repo.py` → `commit` (the excerpt above), then read `repo.py` → `get_diffs` / `get_head_commit_sha` to see how the dirty-check and SHA-readback are sourced from git. That trace — apply → commit → read back — is the real-source map for s07; the LLM-generated commit message (`get_commit_message`) hands off to the weak-model layer you'll meet in s10.

---

**Next**: s08 builds the RepoMap — a ranked, token-budgeted summary of the whole repo (tree-sitter tags + PageRank) so the model can "see" structure across files without loading every one. Auto-commit persists edits; the repo map decides what the model gets to look at in the first place.
