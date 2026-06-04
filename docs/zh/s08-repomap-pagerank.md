---
title: "s08 · RepoMap（标签 + PageRank + token 预算）"
chapter: 8
slug: s08-repomap-pagerank
est_read_min: 16
---

# s08 · RepoMap（标签 + PageRank + token 预算）

> 教什么：*RepoMap* —— aider 如何让模型"看见"一个大到无法整段粘贴的代码库。扫描每个文件的**定义**与**引用**，连成一张有向图（引用方 → 定义方），跑 **PageRank**，让被大家依赖的符号浮到顶端，再贪心地输出排名最高的符号，直到撞上 **token 预算**。这是整个课程里算法最丰富的一章：一个真正的图算法，手写实现，服务于上下文选择。

---

## Problem / 问题

到目前为止，每一章都默认相关代码已经*在对话里*——整段粘进去给模型读。一旦仓库大过上下文窗口，这个前提就崩了。一个 50 文件的项目没法整个塞进提示词，但模型仍需知道代码库的*形状*：哪个文件定义了 `Store`、`Checkout` 在哪、谁依赖谁。没有这些，它会臆造符号、改错文件，或者反复索要它看不到的文件。

朴素的修法——"把每个文件和它的函数都列出来"——同样不可行：清单本身就会撑爆预算，而一份没有排名的罗列会把真正重要的三个符号埋在一千个无关符号底下。我们真正需要的，是按重要性给符号*排名*，只输出能装进固定 token 预算的那一小段。难点在"重要性"：一个符号的重要性不是它出现多少次，而是它有多*核心*——其余代码有多依赖它。这是一个图问题，而 PageRank 正是经典答案。

## Solution / 解决方案

把仓库建模成一张图，让中心性自然涌现。三个想法支撑整个设计：

1. **仓库是一张有向图，一条边表示"依赖"。** 每个文件是一个节点。对每个在 A 文件里定义、在 B 文件里被使用的标识符，加一条 `引用方 → 定义方` 的边，权重是使用次数。被大家导入的文件，正像被大家链接的网页。
2. **PageRank 把"被很多人链接"变成一个分数。** 在这张图上跑 PageRank（幂迭代，约 20 轮，阻尼 0.85）。被很多高排名文件指向的文件得分高；无人引用的文件得分低。我们把一个文件的分数*摊到它定义的各个符号上*，于是排名是*按符号*的，而不仅仅按文件。**个性化（personalization）** 把传送向量偏向"在对话中"的文件，让它们的依赖分到更多排名——地图于是偏向你正在改的代码。
3. **靠贪心预算填充把排名变成定长地图。** 按排名从高到低遍历符号，在 `~字符数/4` 的 token 估算不超预算时逐个加入，下一个会溢出时就停。无限长的排名表于是变成一段装得进提示词的文本。

诚实的提醒：上游用 **tree-sitter**（40+ 语言、真正的文法）精确抽取标签。s08 改用**启发式正则**抽取器。被简化的是抽取器；*图 + PageRank + 预算*这条主线是忠实的。

## How It Works / 工作原理

```ascii-anim frames=2
┌────────────────────────────────────────────────────────────────┐
│  files ──▶ extractTags ──▶  Tag{Path,Name,Kind:def|ref,Line}    │
│                                                                │
│  buildGraph:   每个 FILE 一个节点，边 引用方 ─▶ 定义方          │
│                                                                │
│      cart.go ──3──▶ store.go ◀──3── report.go     unused.go     │
│         │              ▲                              (无边)    │
│         └── 使用 Store, Count, Add ──┘                          │
│                                                                │
│  pageRank（幂迭代，d=0.85，约 20 轮）：                         │
│      store.go 0.47   cart.go 0.18   report.go 0.18  unused 0.18 │
│                                                                │
│  rankTags ─▶ 把每个文件的 rank 摊到它的 defs ─▶ 排序            │
│  renderMap ─▶ 从高到低输出，直到 ~字符数/4 撞上预算            │
└────────────────────────────────────────────────────────────────┘
```

手写的 PageRank —— 算法核心（节选自 [`agents/s08-repomap-pagerank/repomap.go`](https://github.com/Ding-Ye/learn-aider/blob/main/agents/s08-repomap-pagerank/repomap.go)）：

```go
// rank(v) = (1-d)*p(v) + d * Σ_{u -> v} rank(u) * weight(u,v) / outWeight(u)
func pageRank(g *graph) map[string]float64 {
	n := len(g.nodes)
	rank := make(map[string]float64, n)
	if n == 0 {
		return rank
	}

	// 个性化（传送）向量 p，总和为 1：有 chat 文件就把质量全压上去，否则均匀分布。
	// 这就是让地图偏向你的代码的那一步。
	p := make(map[string]float64, n)
	chatCount := 0
	for _, v := range g.nodes {
		if g.personal[v] {
			chatCount++
		}
	}
	if chatCount > 0 {
		for _, v := range g.nodes {
			if g.personal[v] {
				p[v] = 1.0 / float64(chatCount)
			}
		}
	} else {
		for _, v := range g.nodes {
			p[v] = 1.0 / float64(n)
		}
	}

	outWeight := make(map[string]float64, n)
	for u := range g.edges {
		for _, w := range g.edges[u] {
			outWeight[u] += w
		}
	}
	for _, v := range g.nodes {
		rank[v] = p[v]
	}

	for iter := 0; iter < pageRankIters; iter++ {
		next := make(map[string]float64, n)
		dangling := 0.0 // 没有出边的节点会泄漏它的分数；沿 p 重新分配
		for _, v := range g.nodes {
			if outWeight[v] == 0 {
				dangling += rank[v]
			}
		}
		for _, v := range g.nodes {
			next[v] = (1.0-pageRankDamping)*p[v] + pageRankDamping*dangling*p[v]
		}
		for u, outs := range g.edges { // 把 rank 沿出边推出去
			ow := outWeight[u]
			if ow == 0 {
				continue
			}
			share := pageRankDamping * rank[u] / ow
			for v, w := range outs {
				next[v] += share * w
			}
		}
		rank = next
	}
	return rank
}
```

**四个非显然之处**：

1. **边*指向*依赖，而不是背离。** `引用方 → 定义方` 意思是"rank 流向被使用的那个东西"。PageRank 奖励*接收*边的节点，于是被用得最多的定义胜出——这正是你想放在仓库地图顶端的东西。
2. **个性化是传送向量，不是给边权打补丁。** 早先的一版去加权 chat 文件的*边*权；但当一个 chat 文件只有一条出边时，这毫无作用（比例没变）。忠实的机制——也是修复它的办法——是把 `(1-d)` 那份传送质量集中到 chat 文件上，让它们辐射出的 rank 真正抬升其依赖。这对应 `nx.pagerank(personalization=...)`。
3. **悬挂节点会泄漏 rank。** 一个只定义符号、不引用别人的文件没有出边，它的分数无处可去。每一轮我们收集这份"悬挂"质量，沿 `p` 重新分配，对应 networkx 的 `dangling=personalization` 默认行为——否则总 rank 会流失，分数也不再总和为 1。
4. **按符号的 rank ≠ 按文件的 rank。** `store.go` 的*文件* rank 最高，但它定义了四个符号，每个只继承四分之一——于是落到了某个只有一个重度使用符号的文件*之下*。我们排的是符号，所以一个孤立而核心的定义（测试 3 里的 `Core`）会胜过四个分一块更大蛋糕的兄弟。这是特性：地图偏爱聚焦的、被依赖的符号。

## What Changed (vs. s07) / 与 s07 的变化

s07 收尾了"让单次编辑安全"这条线：每次 AI 改动都成为一次原子 git 提交（`ApplyEdits` 之后 `autoCommit`）。loop 知道*对话里的文件*，却对*仓库的其余部分*一无所知。s08 补上缺失的这一半——一份对其余一切的、结构化的、带排名的视图——以及一个全新的图算法来产出它。

```diff
-// s07：loop 只见过明确加入对话的文件。
-edited, _, _ := coder.ApplyEdits(edits)
-repo.Commit(edited)                     // 持久化改动；这就是它的全部世界
+// s08：在 loop 跑之前，给仓库的其余部分建一张带排名的地图，
+// 只读地注入，让模型能"看见"从未被粘贴进来的代码。
+files, _ := readRepoFiles(root)
+repoMap := RepoMap(files, budgetTokens, chatFiles) // 标签 -> 图 -> PageRank -> 预算
+msg := Message{Role: "user", Content: []ContentBlock{{
+	Type: "text",
+	Text: "Here are summaries of files in my git repository.\n" +
+		"Do not propose changes to these files, treat them as *read-only*.\n\n" + repoMap,
+}}
```

语义上：s01–s07 一路长出的是*写*路径（解析一处编辑、应用它、提交它）。s08 是第一条能*扩展*的*读*路径——当代码库大到无法完整展示时，它决定*模型能看到什么*。上下文选择不再是"用户粘了什么就是什么"，而成了一个算法。

## Try It / 动手试一试

```bash
cd agents/s08-repomap-pagerank

# 在 token 预算下给一个目录建图（无网络、无 API key）。
go run . -dir .

# 查看驱动这张地图的依赖图与 PageRank 分数。
go run . -dir ../s05-prompt-system -v

# 更紧的预算只保留更少（排名更高）的符号。
go run . -dir . -tokens 80

# 把文件标记为"在对话中"——让排名偏向它们的依赖。
go run . -dir . -chat repomap.go

# 打印 loop（s01..s07）会注入的那条只读 Message。
go run . -dir . -as-message

# 测试（无网络——整条流水线是确定性的）
go test -v ./...
```

期望输出形态：

```
# go run . -dir <小仓库> -v   （确定性；无 LLM）：
[s08] scanned 4 files under ..., budget=256 tokens
[s08] dependency edges (referencer -> definer  weight):
    cart.go -> store.go  3.0
    report.go -> store.go  3.0
[s08] PageRank (higher = more central):
    0.4737  store.go
    0.1754  cart.go
    ...
===== REPO MAP (budget=256 tokens, ~45 used) =====
cart.go:
  Checkout  (line 3)
store.go:
  Add  (line 8)
  Count  (line 10)
  ...
```

调紧 `-tokens`，排名靠后的符号会从底部掉队，而核心的符号留下；加上 `-chat cart.go`，地图会朝 `cart.go` 的依赖重新排序。完整的示例记录见 [`testdata/expected.txt`](https://github.com/Ding-Ye/learn-aider/blob/main/agents/s08-repomap-pagerank/testdata/expected.txt)。

## Upstream Source Reading / 上游源码阅读

aider 的排名逻辑在 `repomap.py` 的 `get_ranked_tags`（L365-574）。用 tree-sitter 抽完标签后，它建一张 `nx.MultiDiGraph`，边是 `引用方 → 定义方`，再用一摞启发式给边加权（被提及的标识符 ×10、"有意思"的名字 ×10、私有 ×0.1、chat 文件 ×50、`sqrt(count)` 衰减），然后带着个性化向量调用 `nx.pagerank`。下面节选的是"建图到 PageRank"的核心段——正是 s08 用标准库重写的那一段。

```upstream:aider/repomap.py#L470-525
        G = nx.MultiDiGraph()

        # Add a small self-edge for every definition that has no references
        # Helps with tree-sitter 0.23.2 with ruby, where "def greet(name)"
        # isn't counted as a def AND a ref. tree-sitter 0.24.0 does.
        for ident in defines.keys():
            if ident in references:
                continue
            for definer in defines[ident]:
                G.add_edge(definer, definer, weight=0.1, ident=ident)

        for ident in idents:
            definers = defines[ident]

            mul = 1.0
            is_snake = ("_" in ident) and any(c.isalpha() for c in ident)
            is_kebab = ("-" in ident) and any(c.isalpha() for c in ident)
            is_camel = any(c.isupper() for c in ident) and any(c.islower() for c in ident)
            if ident in mentioned_idents:
                mul *= 10
            if (is_snake or is_kebab or is_camel) and len(ident) >= 8:
                mul *= 10
            if ident.startswith("_"):
                mul *= 0.1
            if len(defines[ident]) > 5:
                mul *= 0.1

            for referencer, num_refs in Counter(references[ident]).items():
                for definer in definers:
                    use_mul = mul
                    if referencer in chat_rel_fnames:
                        use_mul *= 50

                    # scale down so high freq (low value) mentions don't dominate
                    num_refs = math.sqrt(num_refs)

                    G.add_edge(referencer, definer, weight=use_mul * num_refs, ident=ident)

        if not references:
            pass

        if personalization:
            pers_args = dict(personalization=personalization, dangling=personalization)
        else:
            pers_args = dict()

        try:
            ranked = nx.pagerank(G, weight="weight", **pers_args)
        except ZeroDivisionError:
            ...
```

**对照阅读要点**：

- **`nx.pagerank` vs 我们的幂迭代。** 上游调 networkx；s08 把那个不动点手写出来（约 20 轮，阻尼 0.85——正是 networkx 默认的 `alpha`）。数学完全一致；我们用一个依赖换来对循环本身的可见性。
- **我们省掉的权重启发式。** 被提及/"有意思"的名字 `mul *= 10`、私有或满天飞的标识符 `*= 0.1`、`sqrt(num_refs)` 衰减——这些是上游真实的调参。s08 用原始的跨文件引用计数，好让图保持清晰；这些启发式是显然的扩展，而非另一种算法。
- **忠实的个性化。** 上游同时传 `personalization=...` 和 `dangling=...`。s08 构造同一个传送向量 `p`，并把它复用于悬挂分配——正是这种配对，才让一个 chat 文件抬升的是它的*依赖*，而不只是它自己。
- **自环这个边界 case。** 上游给"有定义但没有引用"的标识符加了一条 `weight=0.1` 的自环（针对 Ruby 的某个 tree-sitter 版本怪癖）。s08 没有 tree-sitter，于是跳过它；一个没有跨文件引用的定义就直接成为悬挂节点，由悬挂分配兜住。
- **一个我们故意保留的简化。** 上游的预算填充是对"包含多少个标签"做*二分查找*，每次试探都重新渲染并数 token（`get_ranked_tags_map` L666-706）。s08 用一趟贪心扫描，在 `~字符数/4` 的估算超预算前停下——正确且 `O(n)`，只是不是大小最优。

**想读更多**：从 `aider/repomap.py` 的 `get_ranked_tags`（上面的节选）入手，跟着 `ranked_tags` 进 `to_tree`（L748）和 `render_tree`（L710），看符号如何变成一段渲染好的字符串，最后上溯到 `aider/coders/base_coder.py` 的 `get_repo_map`，看那段字符串如何作为只读消息被注入。这条线——抽取 → 建图 → 排名 → 渲染 → 注入——就是 s08 → s09（它会用这张地图去检查模型做出的编辑）→ s_full 整合的真实代码地图。

---

**下一节预告**：s09 闭合整个循环——一处编辑被应用并提交后，跑一遍 **Linter**；若报错，把失败作为一条反思消息喂回去、loop 重新进入，于是模型能在没有人类往返的情况下修正自己的错误。
