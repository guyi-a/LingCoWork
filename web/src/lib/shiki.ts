import {
  createHighlighter,
  type BundledLanguage,
  type BundledTheme,
  type Highlighter,
  type ShikiTransformer,
} from "shiki";

const THEME_LIGHT: BundledTheme = "github-light";
const THEME_DARK: BundledTheme = "github-dark";
const FALLBACK_LANG: BundledLanguage = "bash";

const LANG_BY_EXT: Record<string, BundledLanguage> = {
  ts: "typescript",
  tsx: "tsx",
  js: "javascript",
  jsx: "jsx",
  mjs: "javascript",
  cjs: "javascript",
  mts: "typescript",
  cts: "typescript",
  go: "go",
  py: "python",
  rb: "ruby",
  rs: "rust",
  java: "java",
  kt: "kotlin",
  swift: "swift",
  c: "c",
  h: "c",
  cpp: "cpp",
  cc: "cpp",
  cxx: "cpp",
  hpp: "cpp",
  json: "json",
  jsonc: "jsonc",
  yaml: "yaml",
  yml: "yaml",
  toml: "toml",
  xml: "xml",
  html: "html",
  htm: "html",
  css: "css",
  scss: "scss",
  sh: "bash",
  bash: "bash",
  zsh: "bash",
  env: "bash",
  sql: "sql",
  php: "php",
  md: "markdown",
  markdown: "markdown",
  ini: "ini",
  dockerfile: "dockerfile",
};

export function resolveLanguage(filePathOrName: string): BundledLanguage {
  if (!filePathOrName) return FALLBACK_LANG;
  const fileName = filePathOrName.split(/[/\\]/).pop() ?? "";
  const lower = fileName.toLowerCase();
  if (lower === "dockerfile" || lower.startsWith("dockerfile.")) {
    return "dockerfile";
  }
  if (lower === "makefile" || lower === "gnumakefile") {
    return "makefile";
  }
  const dot = fileName.lastIndexOf(".");
  if (dot < 0) return FALLBACK_LANG;
  const ext = fileName.slice(dot + 1).toLowerCase();
  return LANG_BY_EXT[ext] ?? FALLBACK_LANG;
}

let highlighterPromise: Promise<Highlighter> | null = null;

async function getHighlighter(): Promise<Highlighter> {
  if (!highlighterPromise) {
    highlighterPromise = createHighlighter({
      themes: [THEME_LIGHT, THEME_DARK],
      langs: [],
    });
  }
  return highlighterPromise;
}

export async function highlightCode(
  code: string,
  lang: BundledLanguage,
  options?: { showLineNumbers?: boolean; dark?: boolean },
): Promise<string> {
  const hl = await getHighlighter();
  if (!hl.getLoadedLanguages().includes(lang)) {
    try {
      await hl.loadLanguage(lang);
    } catch {
      /* fall through to fallback below */
    }
  }
  const actualLang = hl.getLoadedLanguages().includes(lang)
    ? lang
    : FALLBACK_LANG;
  const transformers: ShikiTransformer[] = options?.showLineNumbers
    ? [lineNumberTransformer()]
    : [];
  const html = hl.codeToHtml(code, {
    lang: actualLang,
    theme: options?.dark ? THEME_DARK : THEME_LIGHT,
    transformers,
  });
  // Shiki emits literal `\n` between .line spans; combined with our
  // `.line { display: block }` this doubles line breaks. Strip newlines
  // wherever they sit adjacent to a .line span — block layout gives us
  // the visual break.
  return html
    .replace(/<\/span>\n<span class="line">/g, '</span><span class="line">')
    .replace(/(<code[^>]*>)\n<span class="line">/g, '$1<span class="line">')
    .replace(/<\/span>\n<\/code>/g, "</span></code>");
}

function lineNumberTransformer(): ShikiTransformer {
  return {
    name: "line-numbers",
    line(node, line) {
      node.properties["data-line"] = String(line);
      node.children.unshift({
        type: "element",
        tagName: "span",
        properties: { className: ["shiki-line-no"] },
        children: [{ type: "text", value: String(line) }],
      });
    },
  };
}
