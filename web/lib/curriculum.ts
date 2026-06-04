// The locked curriculum from the plan. SessionNav and the landing page both
// read from this single source of truth. Slugs match docs/{zh,en}/<slug>.md.
//
// "available: false" means the chapter exists in the curriculum but its
// docs aren't written yet — the link will render but go to a placeholder.

export type ChapterMeta = {
  slug: string;
  num: string; // "s01", "s02", "s_full"
  title: { zh: string; en: string };
  available: boolean;
};

export const CURRICULUM: ChapterMeta[] = [
  {
    slug: "s01-minimum-coder-loop",
    num: "s01",
    title: { zh: "最小 Coder 循环", en: "The minimum coder loop" },
    available: true,
  },
  {
    slug: "s02-wholefile-format",
    num: "s02",
    title: { zh: "整文件编辑格式", en: "Whole-file edit format" },
    available: true,
  },
  {
    slug: "s03-editblock-searchreplace",
    num: "s03",
    title: { zh: "搜索/替换编辑块", en: "Search/replace edit blocks" },
    available: true,
  },
  {
    slug: "s04-unified-diff-format",
    num: "s04",
    title: { zh: "统一 diff 格式", en: "Unified diff edit format" },
    available: true,
  },
  {
    slug: "s05-prompt-system",
    num: "s05",
    title: {
      zh: "按格式定制的提示系统",
      en: "Per-format prompt system",
    },
    available: true,
  },
  {
    slug: "s06-inputoutput-commands",
    num: "s06",
    title: {
      zh: "终端 I/O 与对话内命令",
      en: "InputOutput layer + in-chat commands",
    },
    available: true,
  },
  {
    slug: "s07-gitrepo-autocommit",
    num: "s07",
    title: { zh: "Git 集成与自动提交", en: "GitRepo integration + auto-commit" },
    available: true,
  },
  {
    slug: "s08-repomap-pagerank",
    num: "s08",
    title: {
      zh: "RepoMap：tree-sitter 标签 + PageRank",
      en: "RepoMap (tags + PageRank under token budget)",
    },
    available: true,
  },
  {
    slug: "s09-linter-reflection",
    num: "s09",
    title: { zh: "检查器与反思循环", en: "Linter + reflection loop" },
    available: true,
  },
  {
    slug: "s10-provider-config-retries",
    num: "s10",
    title: {
      zh: "模型/提供方抽象与重试流式",
      en: "Model/provider config + retries/streaming",
    },
    available: true,
  },
  {
    slug: "s_full-integration",
    num: "s_full",
    title: {
      zh: "整合：端到端 mini-aider",
      en: "Integration: end-to-end mini-aider",
    },
    available: false,
  },
  {
    slug: "appendix-a-repomap-pagerank",
    num: "A",
    title: {
      zh: "附录 A · RepoMap 的 PageRank 直觉",
      en: "Appendix A · RepoMap PageRank intuition",
    },
    available: false,
  },
  {
    slug: "appendix-b-upstream-map",
    num: "B",
    title: {
      zh: "附录 B · 上游文件地图",
      en: "Appendix B · Upstream file map",
    },
    available: false,
  },
];

export type Locale = "zh" | "en";

export function chapterTitle(c: ChapterMeta, locale: Locale): string {
  return c.title[locale];
}
