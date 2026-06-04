# learn-aider

> 用 Go 从零渐进重写 [aider](https://github.com/Aider-AI/aider)，一节加一个承重机制，每节末尾对照上游 Python 源码。

[English](./README.en.md) · 中文

## 这是什么

[aider](https://github.com/Aider-AI/aider) 是一个把 LLM 接进终端、和你结对改代码的 AI
工具：它读懂仓库、构造提示、把模型回复解析成编辑、落盘并自动 git 提交。它本身用 Python
写成，约 4 万行代码。

**learn-aider 不是教你「用」 aider，而是教你「它怎么从零长出来」。** 这个仓库把 aider 的
承重机制逐章拆开，用 Go 重写一份精简实现。每一节都是一个**自包含的 Go module**（无跨节
import），只新增一个机制，并在末尾配一段**上游 Python 源码导读**，让你能从 mini 版顺着指针
读到生产代码。

教学路线（由浅入深）：最小 Coder 循环 → 三种编辑格式（整文件 / 搜索替换 / 统一 diff）→
按格式定制的提示系统 → 终端 I/O 与对话内命令 → Git 自动提交 → 基于 tree-sitter 标签 +
PageRank 的 RepoMap → 检查器与反思循环 → 模型/提供方抽象与重试流式 → 端到端整合。

## 课程

| 章节 | 标题 | 状态 |
|------|------|------|
| s01 | [最小 Coder 循环](docs/zh/s01-minimum-coder-loop.md) | ✅ |
| s02 | 整文件编辑格式 | ⏳ |
| s03 | 搜索/替换编辑块 | ⏳ |
| s04 | 统一 diff 格式 | ⏳ |
| s05 | 按格式定制的提示系统 | ⏳ |
| s06 | 终端 I/O 与对话内命令 | ⏳ |
| s07 | Git 集成与自动提交 | ⏳ |
| s08 | RepoMap：tree-sitter 标签 + PageRank | ⏳ |
| s09 | 检查器与反思循环 | ⏳ |
| s10 | 模型/提供方抽象与重试流式 | ⏳ |
| s_full | 整合：端到端 mini-aider | ⏳ |
| A | 附录 A · RepoMap 的 PageRank 直觉 | ⏳ |
| B | 附录 B · 上游文件地图 | ⏳ |

✅ 已发布　⏳ 规划中

## 快速开始

每一节都是一个独立可运行的 Go 程序。以 s01 为例：

```bash
cd agents/s01-minimum-coder-loop
export ANTHROPIC_API_KEY=sk-ant-...
go run . path/to/file.py "make the change"
```

程序会读取你给的文件，把指令发给 LLM，把回复解析成一次整文件编辑并写回磁盘。默认走
Anthropic；用 `-provider` 旗标可切换其他兼容的提供方。

## Web 文档阅读器

仓库自带一个 Next.js 文档站，可双语阅读每章正文并对照上游源码：

```bash
cd web
npm install
npm run dev
```

然后访问 http://localhost:3000 。

## 致谢

- 上游项目 [Aider-AI/aider](https://github.com/Aider-AI/aider)，采用 Apache-2.0 许可证。
  本仓库的所有机制、行号引用、源码导读均指向该上游（pinned SHA
  `5dc9490bb35f9729ef2c95d00a19ccd30c26339c`）。
- 教学结构受 [shareAI-lab/learn-claude-code](https://github.com/shareAI-lab/learn-claude-code)
  的「逐章自包含实现 + 上游对照」范式启发。

## 许可证

[MIT](./LICENSE)
