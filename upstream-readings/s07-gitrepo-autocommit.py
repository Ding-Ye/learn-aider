# Source: aider/repo.py  (GitRepo, lines 201-318 of commit(), excerpted +
#          is_dirty/get_head_commit_sha L598-616, annotated)
# Upstream: https://github.com/Aider-AI/aider
#   @ 5dc9490bb35f9729ef2c95d00a19ccd30c26339c
#   /blob/5dc9490bb35f9729ef2c95d00a19ccd30c26339c/aider/repo.py
#
# License: Apache-2.0 (Copyright Aider-AI contributors). Verbatim excerpts
# reproduced under Apache-2.0 for study; only comments/whitespace were trimmed
# and the long attribution docstring (L132-200) elided. repo.py is 622 lines.
#
# WHY THIS EXCERPT FOR s07
# ------------------------
# This is the AUTO-COMMIT mechanism — the thing that makes aider safe to use. The
# loop calls repo.commit() after every applied edit (base_coder.py auto_commit),
# so each AI change becomes its own atomic git commit. The user can then `git
# log` / `git show` / `git reset` (= aider's /undo) to inspect or revert any
# single edit. The commit IS the undo buffer; that is the "git-as-undo"
# philosophy named in the research notes.
#
# Three load-bearing pieces appear below, and s07's gitrepo.go ports each:
#   (a) the NO-EMPTY-COMMIT guard (L201-206)        -> GitRepo.hasStagedChanges + ErrNothingToCommit
#   (b) the message + STAGING + commit (L277-314)   -> GitRepo.Commit (-m, git add, git commit)
#   (c) ATTRIBUTION via GIT_*_NAME env vars         -> Attribution + the env we pass to runGitEnv


# ===== aider/repo.py  GitRepo.commit() — the executable core (L201-318) =====
def commit(self, fnames=None, context=None, message=None, aider_edits=False, coder=None):
    # (a) NO-EMPTY-COMMIT GUARD. If we were asked to commit "everything" and the
    #     tree is clean, there is nothing to do — bail before making a noise
    #     commit. s07 checks `git diff --cached --quiet` after staging and
    #     returns ErrNothingToCommit.
    if not fnames and not self.repo.is_dirty():
        return

    diffs = self.get_diffs(fnames)
    if not diffs:          # the requested files have no changes -> also bail.
        return

    # The commit message: explicit, or generated from the diff by a weak model.
    # s07 takes the message as a plain string arg (autoCommit names the files);
    # the GENERATOR differs, the role is the same.
    if message:
        commit_message = message
    else:
        commit_message = self.get_commit_message(diffs, context, None)

    # ... ~50 lines elided: reading the --attribute-* flags off coder.args and
    # resolving the precedence rules (the long docstring at L149-200 spells these
    # out). The NET RESULT is three booleans we reproduce as Attribution fields:
    use_attribute_author = ...      # -> Attribution.AttributeAuthor
    use_attribute_committer = ...   # -> Attribution.AttributeCommitter
    attribute_co_authored_by = ...  # -> Attribution.CoAuthoredBy

    # (b-1) The Co-authored-by TRAILER. Added only for AI edits when the flag is
    #       on; it credits the MODEL without touching author/committer names.
    commit_message_trailer = ""
    if aider_edits and attribute_co_authored_by:
        model_name = coder.main_model.name if coder else "unknown-model"
        commit_message_trailer = f"\n\nCo-authored-by: aider ({model_name}) <aider@aider.chat>"

    if prefix_commit_message:                       # set when attribute_commit_message_*
        commit_message = "aider: " + commit_message # -> s07's `prefix` arg
    full_commit_message = commit_message + commit_message_trailer

    # (b-2) Build the git command and STAGE exactly the touched files. Scoping the
    #       add to `fnames` is what keeps each commit to one edit. s07: Stage().
    cmd = ["-m", full_commit_message]
    if fnames:
        fnames = [str(self.abs_root_path(fn)) for fn in fnames]
        for fname in fnames:
            self.repo.git.add(fname)                # -> `git add -- <file>`
        cmd += ["--"] + fnames
    else:
        cmd += ["-a"]

    # (c) ATTRIBUTION via environment variables. aider rewrites the AUTHOR /
    #     COMMITTER *name* to "<user.name> (aider)" by exporting GIT_AUTHOR_NAME /
    #     GIT_COMMITTER_NAME just for this one `git commit`. s07 does the same:
    #     it appends those vars to os.Environ() and passes them to runGitEnv.
    original_user_name = self.repo.git.config("--get", "user.name")
    committer_name = f"{original_user_name} (aider)"
    with contextlib.ExitStack() as stack:
        if use_attribute_committer:
            stack.enter_context(set_git_env("GIT_COMMITTER_NAME", committer_name, ...))
        if use_attribute_author:
            stack.enter_context(set_git_env("GIT_AUTHOR_NAME", committer_name, ...))

        self.repo.git.commit(cmd)                   # the actual `git commit`
        commit_hash = self.get_head_commit_sha(short=True)  # read back new sha
        self.io.tool_output(f"Commit {commit_hash} {commit_message}", bold=True)
        return commit_hash, commit_message          # -> s07 returns the short sha


# ===== aider/repo.py  the state helpers s07 also ports (L598-616) =====
def is_dirty(self, path=None):
    if path and not self.path_in_repo(path):
        return True
    return self.repo.is_dirty(path=path)            # -> GitRepo.IsDirty (git status --porcelain)


def get_head_commit_sha(self, short=False):
    commit = self.get_head_commit()
    if not commit:                                  # HEAD unborn (no commits yet)
        return                                      # -> s07 returns an error
    return commit.hexsha[:7] if short else commit.hexsha   # -> GitRepo.HeadSHA


# READING MAP
# -----------
# 1. THE TRIGGER (base_coder.py): after apply_edits, the loop calls
#    self.auto_commit(edited) (base_coder.py ~L2375), which calls
#    repo.commit(fnames=edited, aider_edits=True, coder=self). -> s07 main.go's
#    autoCommit(repo, files, ...). THIS is the "loop gains a post-apply step"
#    change the chapter is about.
#
# 2. THE COMMIT (repo.py commit, above): guard -> message -> stage -> attribute
#    -> commit -> sha. -> s07 GitRepo.Commit. The os/exec equivalents are exactly
#    the git subcommands shown: `git diff --cached --quiet`, `git add -- <file>`,
#    `git commit -m`, `git rev-parse --short HEAD`.
#
# 3. THE ATTRIBUTION LOGIC (repo.py L149-267, elided here): the precedence rules
#    between --attribute-author / --attribute-committer / --attribute-co-authored-by
#    are intricate. s07 keeps the TWO STYLES (name-rewrite vs trailer) and the
#    env-var mechanism, but exposes them as plain Attribution booleans instead of
#    re-deriving the precedence — the curriculum teaches the mechanism, not the flag matrix.
#
# WHAT s07 OMITS ON PURPOSE
# - GitPython: upstream wraps a GitPython Repo object; s07 shells out to the real
#   `git` binary so every command is visible (and there is no extra dependency).
# - LLM-generated commit messages (get_commit_message, L326): s07 takes the
#   message as a string; the generator is a weak model upstream (an s10 concern).
# - .aiderignore / subtree_only / token budgeting on the diff: real but orthogonal
#   to the auto-commit lesson.
