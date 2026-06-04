---
title: "s07 · Git 集成与自动提交"
chapter: 7
slug: s07-gitrepo-autocommit
est_read_min: 13
---

# s07 · Git 集成与自动提交

> 教什么：*自动提交*机制——aider 的 "git-as-undo"（用 git 当撤销）哲学。每次应用完编辑，循环就把这次改动变成一个独立的 git 提交，于是用户随时能 `git log` / `git show` / `git reset`（即 `/undo`）来检查或回滚 AI 干了什么。我们在真实 `git` 二进制之上（`os/exec`）封装一个 `GitRepo`：找到仓库根、暂存被编辑的文件、带 aider 风格的归属信息提交、返回新的 SHA——并带一个"不产生空提交"的护栏。

---

## Problem / 问题

到 s06 为止我们已经有了一个真正的交互式会话：coder 解析某种编辑格式（s02–s05），`InputOutput` 层负责打印和确认，`/` 命令改变文件作用域。但它的正中央藏着一种无声的恐惧：一个 LLM 即将*改写你磁盘上的文件*。如果它把某次编辑做错了——覆盖了一个函数、删掉了不该删的一行——什么在保护你？此刻什么都没有。编辑落地，旧内容就没了。

痛点是信任。人们不会让 AI 碰真实代码库，除非犯错的代价很低、很好撤销。你当然可以自建一套撤销栈（每次编辑前给每个文件拍快照、维护历史、再加个 `/redo`……），但那是在重新发明一样每个开发者早已拥有并信任的东西：版本控制。本章用 aider 的方式解决信任问题——把**每次 AI 编辑都做成一个原子 git 提交**。提交历史就成了撤销缓冲区，用用户已经熟悉的工具来检查和回滚。

## Solution / 解决方案

依赖 git，而不是发明一套撤销系统。coder 应用完编辑后，循环调用 `autoCommit(editedFiles)`，它只暂存这些文件并提交它们。新机制 = 一个应用后步骤 + 一个对 `git` CLI 的小 `GitRepo` 封装。

三个决策撑起整个设计：

1. **提交就是撤销缓冲区。** 我们不自己给文件拍快照。每次编辑变成一个限定范围的提交（`git add -- <file>` 然后 `git commit`），于是用户白白获得 `git log`、`git show <sha>` 和 `git reset --hard HEAD~1`（即 aider 的 `/undo`）。原子性很关键：只暂存被编辑的文件，保证一次提交 = 一次编辑，哪怕工作树里还有别的脏文件。
2. **归属信息标记 AI 的产出，两种方式。** 要么通过 `GIT_AUTHOR_NAME` / `GIT_COMMITTER_NAME` 环境变量把 author/committer 的*名字*改写成 `"You (aider)"`，要么加一行 `Co-authored-by: aider (<model>) <aider@aider.chat>` trailer。两者都能让看 `git log` 的人分辨哪些提交来自 AI；trailer 方式保留人类作为 author，对 `git blame` 更友好。
3. **不产生空提交。** 什么都没改的编辑绝不能制造噪音提交。暂存后我们探测 `git diff --cached --quiet`；干净的索引返回 `ErrNothingToCommit`，循环静默跳过——正是上游在 `not diffs` 时的提前 `return`。

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

提交核心（节选自 [`agents/s07-gitrepo-autocommit/gitrepo.go`](https://github.com/Ding-Ye/learn-aider/blob/main/agents/s07-gitrepo-autocommit/gitrepo.go)）：

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

**四个非显然之处**：

1. **护栏是在暂存*之后*检查的，不是之前。** `git diff --cached --quiet` 检查的是*索引*，所以必须先暂存再探测。它的退出码就是那个布尔值：退出 0 = 干净 = 无可提交；非 0 = 存在已暂存改动。这里我们故意把非 0 退出当作*成功*信号——这就是为什么 `hasStagedChanges` 返回 `err != nil`。
2. **归属是按每次提交注入的，不是全局配置。** 我们从 `os.Environ()` 起步，只追加调用方要求的名字变量，于是子 `git` 在其他一切上都继承用户真实配置，只有这一次提交的*显示名字*被覆盖。上游用 context manager 设置-再-恢复同样的环境变量；向一个全新的子进程环境里追加，达到同样的隔离，而无需恢复（因为我们从没改自己进程）。
3. **SHA 是从 git 读回来的，不是猜的。** 提交后我们跑 `git rev-parse --short HEAD`。我们从不构造或预测哈希；"我们刚做了哪个提交"的事实来源是 git 本身（对应 `get_head_commit_sha`）。
4. **路径被规范化到仓库根。** coder 交给我们的是绝对编辑路径；`relPaths` 在 `git add` 之前把它们转成相对仓库根的路径，于是无论你传 `/abs/path/file.go` 还是裸 `file.go`，`Commit` 都能工作。

## What Changed (vs. s06) / 与 s06 的变化

s06 的循环停在"应用编辑"。s07 加了一个应用后步骤：编辑被持久化为一个原子、带归属的 git 提交。新增的接口面是一个 `GitRepo` 封装和一次 `autoCommit` 调用。

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

语义上：在 s06，编辑是一次单向写入——应用它然后祈祷。在 s07，每次编辑都被版本控制包裹起来。循环获得了记忆：每一轮都留下一个提交，而那段提交历史正是让"自信的 AI 结对编程"成为可能的审计轨迹与撤销缓冲区。编辑*怎么解析*没有任何变化；变的是编辑现在以可回滚的历史*存活*下来。

## Try It / 动手试一试

```bash
cd agents/s07-gitrepo-autocommit

# 建一个临时仓库来玩。
mkdir -p /tmp/demo && (cd /tmp/demo && git init -q && \
  git config user.name "Ada" && git config user.email a@e.com && \
  echo hi > app.txt && git add app.txt && git commit -q -m init)

# 离线：应用一次整文件编辑，然后自动提交。打印新 sha。
go run . -repo /tmp/demo -file app.txt -content $'hello\nworld\n' -m "say hello" -v

# 看看刚刚发生了什么——提交就是撤销缓冲区：
git -C /tmp/demo log -1 --pretty='%h %s  (author: %an)'
git -C /tmp/demo reset --hard HEAD~1     # 这正是 aider 的 /undo 所跑的

# 没有实际改动 -> 不产生空提交（干净树空操作）。
go run . -repo /tmp/demo -file app.txt -content "$(cat /tmp/demo/app.txt)" -v

# 用 Co-authored-by 归属，而不是改写 author 名字。
echo more >> /tmp/demo/app.txt
go run . -repo /tmp/demo -file app.txt -content "$(cat /tmp/demo/app.txt)" \
  -m "append a line" -co-authored-by -model claude-sonnet-4-6

# 运行：让 LLM 产出新内容，然后自动提交。
export ANTHROPIC_API_KEY=sk-ant-...
go run . -repo /tmp/demo -file app.txt -instruction "uppercase everything" -v

# 测试（无网络；临时仓库里跑真实 git）
go test -v ./...
```

期望输出形态：

```
# go run . -content ... -v   （SHA 每次都不同）：
[s07] repo root: /tmp/demo
[s07] wrote 12 bytes to app.txt
[s07] committed a1b2c3d: say hello
committed a1b2c3d

# 之后的 git log -1：
a1b2c3d say hello  (author: Ada (aider))
#                                  ^^^^^^^ author 名字被改写以标记 aider 的产出

# 无改动再跑一次：
[s07] no changes to commit (clean tree) — skipping
committed (no commit: nothing changed)

# -co-authored-by 那次，git log -1 --pretty=%B：
append a line

Co-authored-by: aider (claude-sonnet-4-6) <aider@aider.chat>
```

一次完整的 编辑 → 提交 → 撤销 记录见 [`testdata/expected.txt`](https://github.com/Ding-Ye/learn-aider/blob/main/agents/s07-gitrepo-autocommit/testdata/expected.txt)。

## Upstream Source Reading / 上游源码阅读

aider 的自动提交在 `aider/repo.py` 的 `GitRepo.commit()`。它由 `base_coder.py` 的 `auto_commit()` 在 `apply_edits` 之后触发。这个方法很长（L131–318），主要是因为一套错综复杂的归属标志矩阵；而*可执行*的核心很短，正是我们移植的部分。下面这段就是那个核心：不产生空提交的护栏、消息拼装、限定范围的暂存、归属环境变量、提交、以及读回 SHA。

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

**对照阅读要点**：

- **GitPython vs. `os/exec`。** 上游封装一个 GitPython `Repo`；`self.repo.git.add(...)` 和 `self.repo.git.commit(cmd)` 底层也是在 shell out。我们直接调 `git` 二进制，于是每条命令（`git add -- <file>`、`git commit -m`、`git rev-parse --short HEAD`）都可见，且无额外依赖。同样的命令，更少的层。
- **限定范围的暂存就是原子性保证。** `for fname in fnames: self.repo.git.add(fname)` 循环加上 `cmd += ["--"] + fnames` 只暂存被编辑的文件。这正是"一次编辑 = 一个提交"的由来；s07 的 `Stage(paths...)` 是直接移植。
- **归属是按每次提交的设置-再-恢复环境变量。** 上游在 `contextlib.ExitStack` 里用 `set_git_env`，于是 `GIT_AUTHOR_NAME` / `GIT_COMMITTER_NAME` 事后会被恢复。s07 改为向一个*全新的子进程环境*追加（`os.Environ()` + 覆盖）并传给那一次 `git commit`——同样的隔离，无需恢复，因为我们从没改过自己进程。
- **不产生空提交的护栏就在这段节选的上方**（repo.py L201–206：`if not fnames and not self.repo.is_dirty(): return`，再 `if not diffs: return`）。s07 用 `git diff --cached --quiet` → `ErrNothingToCommit` 表达它。
- **一个我们故意简化的部分。** 那约 50 行的归属标志优先级（L218–267）会把 `--attribute-author` / `--attribute-committer` / `--attribute-co-authored-by` 与"编辑是否由 AI 产生"放在一起决议。s07 保留了那*两种风格*和环境变量机制，但把它们暴露成朴素的 `Attribution` 布尔——本课程教的是机制，不是标志矩阵。

**想读更多**：从 `aider/coders/base_coder.py` → `auto_commit`（`apply_edits` 之后的触发点）入手，跟着它进 `aider/repo.py` → `commit`（上面的节选），再读 `repo.py` → `get_diffs` / `get_head_commit_sha`，看脏检查和 SHA 读回是怎么从 git 取来的。这条线——应用 → 提交 → 读回——就是 s07 的真实代码地图；而 LLM 生成的提交消息（`get_commit_message`）会交棒给你将在 s10 见到的弱模型层。

---

**下一节预告**：s08 构建 RepoMap——对整个仓库做一份排序过、受 token 预算约束的摘要（tree-sitter 标签 + PageRank），让模型不必加载每个文件就能"看见"跨文件的结构。自动提交持久化编辑；而 repo map 决定模型一开始能看到什么。
