import Link from "next/link";
import { notFound } from "next/navigation";
import { CURRICULUM, chapterTitle, type Locale } from "@/lib/curriculum";

export default async function Landing({
  params,
}: {
  params: Promise<{ locale: string }>;
}) {
  const { locale } = await params;
  if (locale !== "zh" && locale !== "en") notFound();
  const l = locale as Locale;

  const intro = l === "zh" ? INTRO_ZH : INTRO_EN;
  const ctaLabel = l === "zh" ? "从 s01 开始 →" : "Start at s01 →";

  return (
    <article className="prose-doc">
      <h1>learn-aider</h1>
      <p className="text-[var(--fg-muted)]">
        {l === "zh"
          ? "用 Go 从零渐进重写 aider，每节末尾对照上游 Python 源码。"
          : "A Go re-implementation of aider built from scratch, chapter by chapter — each one ends with the upstream Python source."}
      </p>

      {intro.map((p, i) => (
        <p key={i}>{p}</p>
      ))}

      <p>
        <Link
          href={`/${l}/s/s01-minimum-coder-loop`}
          className="inline-block mt-2 px-4 py-2 rounded border border-[var(--accent-soft)] hover:border-[var(--accent)]"
        >
          {ctaLabel}
        </Link>
      </p>

      <h2>{l === "zh" ? "课程" : "Curriculum"}</h2>
      <ul>
        {CURRICULUM.map((c) => (
          <li key={c.slug}>
            <span className="font-mono text-[var(--fg-muted)] mr-2">
              {c.num}
            </span>
            {c.available ? (
              <Link href={`/${l}/s/${c.slug}`}>{chapterTitle(c, l)}</Link>
            ) : (
              <span className="text-[var(--fg-muted)]">
                {chapterTitle(c, l)}{" "}
                <span className="text-xs">
                  ({l === "zh" ? "未发布" : "not yet"})
                </span>
              </span>
            )}
          </li>
        ))}
      </ul>
    </article>
  );
}

const INTRO_ZH = [
  "这个仓库的目标不是教你「用」 aider，是教你「它怎么从零长出来」。aider 是一个把 LLM 接进终端、和你结对改代码的 AI 工具——它读懂仓库、构造提示、把模型回复解析成编辑、落盘并自动 git 提交。",
  "每一节只加一个承重机制，用 Go 写一份精简实现：最小 Coder 循环、整文件 / 搜索替换 / 统一 diff 三种编辑格式、按格式定制的提示系统、终端 I/O 与对话内命令、Git 自动提交、基于 tree-sitter 标签 + PageRank 的 RepoMap、检查器与反思循环，最后是模型/提供方抽象与重试流式。看完十节，aider 不再是一团黑魔法。",
  "Go 实现是教学骨架，aider 上游是 Python 实现。每节都是一个自包含的 Go module（无跨节 import），末尾的「上游源码阅读」把两边对照起来——你能从 mini 版顺着指针读到生产代码。",
];

const INTRO_EN = [
  "The goal of this repo is not to teach you to *use* aider — it is to teach you how it grows from scratch. aider is an AI pair programmer that wires an LLM into your terminal: it reads the repo, builds the prompt, parses the model's reply into edits, writes them to disk, and auto-commits to git.",
  "Each chapter adds exactly one load-bearing mechanism as a small Go implementation: the minimum coder loop; three edit formats (whole-file, search/replace, unified diff); a per-format prompt system; the terminal I/O layer and in-chat commands; git auto-commit; a RepoMap built from tree-sitter tags ranked by PageRank; a linter + reflection loop; and finally the model/provider abstraction with retries and streaming. After ten chapters, aider stops being black magic.",
  "Go is the teaching skeleton; the upstream Python is the production implementation. Each chapter is a self-contained Go module (no cross-chapter imports), and the 'Upstream Source Reading' section at the end bridges the two — follow the pointers from the mini version straight into the real code.",
];
