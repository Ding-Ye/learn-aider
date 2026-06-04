# s07 · GitRepo integration + auto-commit / Git 集成与自动提交

Every AI edit becomes a git commit, so you can always revert. / 每次 AI 编辑都变成一个 git 提交，于是你随时能回滚。

s06 gave the loop a real terminal shell. But an LLM rewriting your files is only safe if every change is reversible — and aider's answer is beautifully simple: **after each applied edit, make a git commit**. The commit is the undo buffer. You can `git log` to see what the AI did, `git show <sha>` to inspect one edit, and `git reset --hard HEAD~1` (aider's `/undo`) to throw it away. s07 builds a `GitRepo` wrapper over the real `git` binary (`os/exec`): find the repo root, **stage** the edited files, **commit** them with an aider-style attribution, and report the new short SHA. A no-empty-commit guard means an edit that changed nothing makes no noise commit.
s06 给循环加了真正的终端外壳。但让 LLM 改你的文件，唯一安全的前提是每次改动都可回滚——aider 的答案极其朴素：**每次应用完编辑，就做一个 git 提交**。这个提交就是撤销缓冲区。你可以 `git log` 看 AI 干了什么、`git show <sha>` 检查某次编辑、`git reset --hard HEAD~1`（即 aider 的 `/undo`）把它丢掉。s07 在真实 `git` 二进制之上（`os/exec`）封装了一个 `GitRepo`：找到仓库根、**暂存**被编辑的文件、带着 aider 风格的归属信息**提交**它们、返回新的短 SHA。一个"不产生空提交"的护栏保证：什么都没改的编辑不会留下噪音提交。

## Run / 运行

```bash
cd agents/s07-gitrepo-autocommit

# OFFLINE demo (no network, no key): edit a file then auto-commit it.
# 离线 demo（无网络、无 key）：编辑一个文件然后自动提交。
mkdir -p /tmp/demo && (cd /tmp/demo && git init -q && \
  git config user.name "Ada" && git config user.email a@e.com && \
  echo hi > app.txt && git add app.txt && git commit -q -m init)
go run . -repo /tmp/demo -file app.txt -content $'hello\nworld\n' -m "say hello" -v

# Re-run with no change -> no empty commit (a no-op).
# 无改动再跑一次 -> 不产生空提交（空操作）。
go run . -repo /tmp/demo -file app.txt -content "$(cat /tmp/demo/app.txt)" -v

# Credit the model via a Co-authored-by trailer instead of rewriting the author.
# 用 Co-authored-by trailer 给模型署名，而不是改写 author 名字。
echo more >> /tmp/demo/app.txt
go run . -repo /tmp/demo -file app.txt -content "$(cat /tmp/demo/app.txt)" \
  -m "append a line" -co-authored-by -model claude-sonnet-4-6

# RUN: let the LLM produce the new contents, then auto-commit.
# 运行：让 LLM 产出新内容，然后自动提交。
export ANTHROPIC_API_KEY=sk-ant-...
go run . -repo /tmp/demo -file app.txt -instruction "uppercase everything" -v

# tests (no network; drive REAL git in a temp repo, skip if git is absent)
# 测试（无网络；在临时仓库里跑真实 git，无 git 时跳过）
go test ./...
```

## Files / 文件

| File | What it is / 是什么 |
|------|--------------------|
| `gitrepo.go` | **The heart of the chapter.** `GitRepo` over `os/exec git`: `OpenGitRepo` (find root), `Stage`, `Commit(paths, prefix, summary, Attribution) -> sha`, `IsDirty`, `HeadSHA`, `TrackedFiles`, plus the `Attribution` type. Comments explain the git-as-undo and attribution rationale. **Start here.** / **本章核心。** 基于 `os/exec git` 的 `GitRepo`：找根、暂存、提交、脏检查、读 SHA、列出跟踪文件，外加 `Attribution` 类型。注释解释 git-as-undo 与归属逻辑。**从这里读起。** |
| `main.go` | CLI: apply a whole-file edit (from `-content` or the LLM via `-instruction`) then `autoCommit` it — the new post-apply step. / 命令行：应用一次整文件编辑（来自 `-content` 或经 `-instruction` 调 LLM），然后 `autoCommit` 它——本章新增的应用后步骤。 |
| `provider.go` | The generic LLM core (Anthropic wire shape) + two providers. Unchanged from s01..s06; carried for the optional `-instruction` path. / 通用 LLM 核心 + 两个 provider，与 s01..s06 一致；为可选的 `-instruction` 路径保留。 |
| `gitrepo_test.go` | Tests against REAL git in `t.TempDir()`: one-commit, message recorded, author attribution, no-empty-commit + `IsDirty`, tracked files + root-from-subdir. `t.Skip` if `git` is missing. / 在 `t.TempDir()` 里对真实 git 的测试：单提交、消息落库、author 归属、不产生空提交 + `IsDirty`、跟踪文件 + 子目录找根。无 `git` 时 `t.Skip`。 |
| `testdata/expected.txt` | An illustrative edit -> commit -> undo transcript. / 一次 编辑 -> 提交 -> 撤销 的示例记录。 |

## Key teaching points / 关键教学点

1. **The commit IS the undo buffer.** aider doesn't build a custom undo stack; it leans on git. Auto-committing every edit means the user can inspect, diff, revert, or cherry-pick any AI change with tools they already know. See [`gitrepo.go` `Commit`](./gitrepo.go).
   **提交就是撤销缓冲区。** aider 没有自建撤销栈，而是依赖 git。把每次编辑都自动提交，意味着用户能用已熟悉的工具检查、对比、回滚或挑拣任何一次 AI 改动。

2. **Stage exactly the edited files.** `Commit` runs `git add -- <file>` for just the paths the coder touched, then `git commit`. Scoping the stage keeps each commit atomic — one edit, one commit — even if the user has other dirty files lying around. Upstream does the same in `repo.py` commit().
   **只暂存被编辑的文件。** `Commit` 只对 coder 改过的路径执行 `git add -- <file>`，再 `git commit`。限定暂存范围让每个提交保持原子——一次编辑、一个提交——即使用户还有别的脏文件。上游 `repo.py` commit() 也是这么做的。

3. **Attribution via env vars, two styles.** Rewrite `GIT_AUTHOR_NAME`/`GIT_COMMITTER_NAME` to `"You (aider)"`, OR add a `Co-authored-by: aider (<model>) <aider@aider.chat>` trailer. Both make `git log` show which commits came from the AI; the trailer keeps the human as author (kinder to `git blame`). This mirrors upstream's `set_git_env` + the `--attribute-*` flags.
   **用环境变量做归属，两种风格。** 把 `GIT_AUTHOR_NAME`/`GIT_COMMITTER_NAME` 改写成 `"You (aider)"`，或加一行 `Co-authored-by: aider (<model>) <aider@aider.chat>` trailer。两者都能让 `git log` 看出哪些提交来自 AI；trailer 方式保留人类作为 author（对 `git blame` 更友好）。对应上游的 `set_git_env` 与 `--attribute-*` 开关。

4. **No empty commits.** If the edit changed nothing on disk, `Commit` returns `ErrNothingToCommit` instead of creating a noise commit (default git would error on an empty commit anyway). The guard is `git diff --cached --quiet` after staging — upstream's `if not diffs: return`.
   **不产生空提交。** 如果编辑在磁盘上什么都没改，`Commit` 返回 `ErrNothingToCommit`，而不是制造噪音提交（默认 git 对空提交本就会报错）。护栏是暂存后跑 `git diff --cached --quiet`——对应上游的 `if not diffs: return`。

5. **We shell out to real `git`; upstream wraps GitPython.** Using `os/exec` means every command is visible and there's no extra dependency. The trade-off: we parse text output (`git status --porcelain`, `git ls-files`) instead of object APIs — fine for the teaching subset.
   **我们直接调真实 `git`，上游封装 GitPython。** 用 `os/exec` 让每条命令都可见，且无额外依赖。代价：我们解析文本输出（`git status --porcelain`、`git ls-files`）而非对象 API——对教学子集足够。

See the full chapter write-up: [`docs/en/s07-gitrepo-autocommit.md`](../../docs/en/s07-gitrepo-autocommit.md) · [`docs/zh/s07-gitrepo-autocommit.md`](../../docs/zh/s07-gitrepo-autocommit.md).
完整章节讲解见上面两个文档。
