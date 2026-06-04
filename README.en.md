# learn-aider

> A Go re-implementation of [aider](https://github.com/Aider-AI/aider) built from scratch — one load-bearing mechanism per chapter, each ending with a reading of the upstream Python source.

English · [中文](./README.md)

## What this is

[aider](https://github.com/Aider-AI/aider) is an AI pair programmer that wires an LLM
into your terminal: it reads the repo, builds the prompt, parses the model's reply into
edits, writes them to disk, and auto-commits to git. It is written in Python, ~40k lines.

**learn-aider is not about teaching you to *use* aider — it is about teaching you how it
grows from scratch.** This repo takes aider's load-bearing mechanisms apart chapter by
chapter and rebuilds a minimal version in Go. Each chapter is a **self-contained Go
module** (no cross-chapter imports) that adds exactly one mechanism, paired with an
**upstream Python source reading** so you can follow the pointers from the mini version
straight into the production code.

The path, shallow to deep: the minimum coder loop → three edit formats (whole-file,
search/replace, unified diff) → a per-format prompt system → the terminal I/O layer and
in-chat commands → git auto-commit → a RepoMap built from tree-sitter tags ranked by
PageRank → a linter + reflection loop → the model/provider abstraction with retries and
streaming → end-to-end integration.

## Curriculum

| Chapter | Title | Status |
|---------|-------|--------|
| s01 | [The minimum coder loop](docs/zh/s01-minimum-coder-loop.md) | ✅ |
| s02 | [Whole-file edit format](docs/en/s02-wholefile-format.md) | ✅ |
| s03 | [Search/replace edit blocks](docs/en/s03-editblock-searchreplace.md) | ✅ |
| s04 | [Unified diff edit format](docs/en/s04-unified-diff-format.md) | ✅ |
| s05 | [Per-format prompt system](docs/en/s05-prompt-system.md) | ✅ |
| s06 | [InputOutput layer + in-chat commands](docs/en/s06-inputoutput-commands.md) | ✅ |
| s07 | [GitRepo integration + auto-commit](docs/en/s07-gitrepo-autocommit.md) | ✅ |
| s08 | [RepoMap (tags + PageRank under token budget)](docs/en/s08-repomap-pagerank.md) | ✅ |
| s09 | [Linter + reflection loop](docs/en/s09-linter-reflection.md) | ✅ |
| s10 | [Model/provider config + retries/streaming](docs/en/s10-provider-config-retries.md) | ✅ |
| s_full | Integration: end-to-end mini-aider | ⏳ |
| A | Appendix A · RepoMap PageRank intuition | ⏳ |
| B | Appendix B · Upstream file map | ⏳ |

✅ published　⏳ planned

## Quickstart

Every chapter is a standalone, runnable Go program. For s01:

```bash
cd agents/s01-minimum-coder-loop
export ANTHROPIC_API_KEY=sk-ant-...
go run . path/to/file.py "make the change"
```

It reads the file you pass, sends the instruction to the LLM, parses the reply as a single
whole-file edit, and writes it back to disk. It defaults to Anthropic; use the `-provider`
flag to switch to another compatible provider.

## Web doc viewer

The repo ships a Next.js doc site for reading each chapter bilingually alongside the
upstream source:

```bash
cd web
npm install
npm run dev
```

Then open http://localhost:3000 .

## Acknowledgements

- The upstream project [Aider-AI/aider](https://github.com/Aider-AI/aider), licensed under
  Apache-2.0. Every mechanism, line reference, and source reading here points back to that
  upstream (pinned SHA `5dc9490bb35f9729ef2c95d00a19ccd30c26339c`).
- The pedagogy is inspired by
  [shareAI-lab/learn-claude-code](https://github.com/shareAI-lab/learn-claude-code) and its
  "self-contained per-chapter implementation + upstream reading" format.

## License

[MIT](./LICENSE)
