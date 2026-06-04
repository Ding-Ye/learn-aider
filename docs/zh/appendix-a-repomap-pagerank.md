---
title: "附录 A · RepoMap 的 PageRank 直觉"
appendix: A
slug: appendix-a-repomap-pagerank
est_read_min: 12
---

# 附录 A · RepoMap 的 PageRank 直觉 / Appendix A · RepoMap PageRank intuition

> 配套章节：[s08 RepoMap](./s08-repomap-pagerank.md)。上游对应 `aider/repomap.py`，固定在 SHA `5dc9490bb35f9729ef2c95d00a19ccd30c26339c`。

---

## 这份附录讲什么 / What this appendix is about

s08 教你把一个仓库变成「带排名、带预算」的上下文：扫描出符号 → 建图 → 跑 PageRank → 在 token 预算下挑选。那一章为了能在 ~950 行 Go 里讲清楚，做了大量简化——它用正则启发式抽符号，而不是 tree-sitter；它把 PageRank 写成几十行的幂迭代，而不是调用 `networkx`。

但 RepoMap 是 aider 最有「算法味」的一块，也是最值得单独拎出来讲透的一块。它把两个看似无关的领域缝在了一起：

- **tree-sitter**——一套增量解析器框架，能为 40+ 种语言产出真正的语法树，从而精确地区分「定义（def）」和「引用（ref）」；
- **PageRank**——Google 当年给网页排序用的特征向量中心性算法，在这里被借来给**代码符号**排序。

把它们拼起来，再加上一层 token 预算约束，就得到了 aider 的核心魔法：让一个上下文窗口装不下的代码库，也能让模型「看见」它的骨架。这份附录就专门讲清楚**为什么这套组合是对的**、**它在上游的哪几行**、以及**我们的 mini 版到底简化了什么**。它是一篇「读」的材料，不是「写」的章节——读完你应该能回到 `aider/repomap.py` 自己把那 200 行啃下来。

## 心智模型 / The mental model

把整件事想成四步流水线。每一步的输出是下一步的输入：

```
源文件                tags(def/ref)           有向图               PageRank             预算填充
┌────────┐  抽取   ┌──────────────┐  建边   ┌──────────────┐  幂迭代  ┌──────────┐  二分  ┌──────────┐
│ cart.go│ ──────▶ │ def Store    │ ──────▶ │ cart ──▶ store│ ──────▶ │store 0.41│ ─────▶ │ map 文本 │
│store.go│         │ ref Store ×3 │         │ report ─▶store│         │cart  0.22│        │（≤ 预算）│
│report  │         │ def Checkout │         │ store ──▶...  │         │report0.18│        └──────────┘
└────────┘         └──────────────┘         └──────────────┘         └──────────┘
```

**第一步：tags（定义与引用）。** 每个文件被解析成一串 `Tag`。上游的结构是一个 namedtuple：`Tag = namedtuple("Tag", "rel_fname fname line name kind")`（`repomap.py:29`），`kind` 取 `"def"` 或 `"ref"`。`def Store` 表示「这个文件定义了符号 Store」，`ref Store` 表示「这个文件用到了 Store」。

**第二步：图。** 节点是**文件**，不是符号。对每一个「在 A 文件被引用、在 B 文件被定义」的标识符，连一条边 `A → B`（引用者指向定义者），权重正比于引用次数。直觉很简单：**一条 `referencer → definer` 的边，就等价于网页世界里的一条超链接。** 一个被大家 import 的文件，就像一个被大家链接的网页。

**第三步：personalized PageRank。** 在这张图上跑 PageRank。被很多高分文件指向的文件，得分高；没人引用的孤立文件，得分低。关键的一笔是**个性化（personalization）**：把 PageRank 的「随机跳转向量」偏向「当前在 chat 里的文件」，于是这些文件的依赖项会继承到更高的分——地图会朝你正在改的代码倾斜。上游还会把一个文件的得分**按它定义的符号摊开**，所以最终排的是「每个符号」而不只是「每个文件」。

**第四步：预算填充。** 排名是一个无限长的列表，但 prompt 是有限的。于是从高分往低走，一个个符号往里塞，同时用「约 4 字符 = 1 token」估算累计大小，直到再加一个就会超预算。上游用的是一个**二分搜索**来逼近预算上限（`get_ranked_tags_map_uncached`，`repomap.py:666-706`：`middle = max_map_tokens // 25`，不断折半），而不是朴素的线性贪心——目的是更精准地贴住 token 上限。

四步合起来：**一个装不下的代码库，被压缩成一块「最该让模型看到的符号」组成的、刚好塞进 prompt 的文本。**

## 在上游里的位置 / How it shows up in upstream

下面每一行都可以直接 `读 aider/repomap.py:<行号>`，行号钉死在 SHA `5dc9490`：

- 读 `aider/repomap.py:29` —— `Tag` namedtuple 的定义（`rel_fname fname line name kind`），整条流水线的原子数据结构。
- 读 `aider/repomap.py:103-167` —— `get_repo_map()`，对外入口。负责 token 预算的 padding（无 chat 文件时给更大的视野），再委托给 `get_ranked_tags_map`。
- 读 `aider/repomap.py:233-278` —— `get_tags()` 及其缓存层；真正的解析在 `get_tags_raw()`（`repomap.py:279`），那里调用 tree-sitter 的查询拿到 def/ref。
- 读 `aider/repomap.py:365-372` —— `get_ranked_tags()` 的开头：`defines`、`references`、`definitions` 三个字典，是建图的原料。
- 读 `aider/repomap.py:374-445` —— personalization 向量的构造：chat 文件、被提及的文件名、被提及的标识符各自如何加权（`personalize = 100 / len(fnames)`）。
- 读 `aider/repomap.py:470-514` —— 用 `nx.MultiDiGraph()` 建图、以及那一长串权重乘子（snake/camel 命名 ×10、被提及 ×10、chat 内引用者 ×50、`num_refs` 开方降权）。
- 读 `aider/repomap.py:519-531` —— `nx.pagerank(G, weight="weight", **pers_args)` 这一行就是核心调用；注意它用 `personalization` 同时作为 `dangling` 向量，并对 `ZeroDivisionError` 做了兜底。
- 读 `aider/repomap.py:533-574` —— PageRank 之后，如何把每个源节点的 rank 沿出边摊给「(目标文件, 标识符)」对，再排序得到 `ranked_tags`。
- 读 `aider/repomap.py:576-627` —— `get_ranked_tags_map()`：围绕 `get_ranked_tags` 的缓存与 refresh 策略（auto/files/always/manual）。
- 读 `aider/repomap.py:666-706` —— `get_ranked_tags_map_uncached()` 里的**二分预算填充**：用 `to_tree()` 渲染前 `middle` 个 tag、`token_count()` 估算、再根据是否超 `max_map_tokens` 折半。
- 读 `aider/repomap.py:710-746` —— `render_tree()`，借 `grep-ast` 的 `TreeContext` 把选中的「关注行（lines of interest）」渲染成带上下文的代码片段。

## 我们的 mini 为什么简化 / Why our mini simplifies it

s08 是整个课程里最「算法重」的一章，但它仍然是一个**教学版**，刻意砍掉了三类复杂度，理由如下：

**没有 tree-sitter。** 上游用 tree-sitter + `grep-ast` 为 40+ 种语言产出真正的语法树，再用每种语言的 `.scm` 查询精确抽取 def/ref。把这套搬进 Go 要么引入 cgo（`smacker/go-tree-sitter`），要么自己写解析器——两者都会喧宾夺主，淹没「图 + PageRank」这个真正的教学点。所以 mini 版用**正则启发式**抽符号，并把范围收到 Go 源码（可选 `go/parser`，零 cgo）。这意味着我们的 def/ref 没那么准（注释里出现的标识符也可能被误判），但流水线的形状是忠实的。

**启发式 tags，丢掉权重乘子。** 上游那一串乘子（`repomap.py:487-499`：长标识符 ×10、被提及 ×10、私有 `_` 前缀 ×0.1、定义超过 5 处 ×0.1）是多年调参的经验值。mini 版只保留「引用次数」和「chat 个性化」这两个最核心的信号，把其余乘子作为注释里的扩展练习——它们能提分，但不是理解 PageRank 的必需品。

**小号 PageRank，不依赖 networkx。** 上游一行 `nx.pagerank(...)` 背后是 networkx 经过严格测试的实现（稀疏矩阵、dangling 处理、收敛判据）。mini 版手写约 40 行的幂迭代（damping 0.85，固定 ~20 轮，无收敛检测），并用「4 字符 ≈ 1 token」近似 `tiktoken`，预算填充也用朴素的线性贪心而非二分。这些都是**有意为之的有损简化**：对一个几十文件的教学仓库，结果排序与上游一致；但在数千文件、需要缓存与增量解析的真实仓库上，请回到上游实现。

一句话：**mini 版教的是「为什么 def→ref 图 + PageRank 能选出中心代码」这个直觉，而把「在 40 种语言、几千文件上把它做快做对」留给了上游。**

## 延伸阅读 / Further reading

- **PageRank 原始论文**：S. Brin, L. Page, *The Anatomy of a Large-Scale Hypertextual Web Search Engine*（1998）；以及 L. Page 等人的 *The PageRank Citation Ranking*（1999）。算法综述见维基百科 <https://en.wikipedia.org/wiki/PageRank>。理解「特征向量中心性 + 阻尼随机游走」就够了。
- **NetworkX 的 pagerank 文档**：<https://networkx.org/documentation/stable/reference/algorithms/generated/networkx.algorithms.link_analysis.pagerank_alg.pagerank.html>——上游 `repomap.py:525` 直接调用它，文档里写清了 `personalization` 与 `dangling` 参数的语义。
- **tree-sitter 官网**：<https://tree-sitter.github.io/tree-sitter/>——增量解析、查询语法（`.scm`）、以及 aider 依赖的 `grep-ast`（<https://github.com/Aider-AI/grep-ast>）背后的引擎。
- **aider 官方 repo-map 文档**：<https://aider.chat/docs/repomap.html>——作者本人对 RepoMap 设计动机与 token 预算策略的讲解，与本附录互为印证。
- **课程内对照**：先读 [s08](./s08-repomap-pagerank.md) 的 Go 实现，再读上游 `aider/repomap.py:365-574`，最后回到[附录 B](./appendix-b-upstream-map.md) 的符号对照表把两边逐一对上。
