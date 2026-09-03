import { useCallback, useEffect, useMemo, useState } from "react";
import { cn } from "@/lib/utils";
import { MessageBody } from "@/features/chat/MessageBody";
import {
  getSkillHubCategories,
  getSkillHubReadme,
  getSkillHubVersions,
  installSkillHubSkill,
  listSkillHubInstalled,
  listSkillHubSkills,
  uninstallSkillHubSkill,
  type SkillHubCategoryCount,
  type SkillHubInstalled,
  type SkillHubPage,
  type SkillHubSkill,
  type SkillHubVersion,
} from "@/lib/api";

// 技能市场：浏览内网 kskill 注册中心，装到本机技能目录，下一轮对话生效。
// 所有注册中心请求都经后端转发（注册中心无 CORS）。
//
// 分类栏是可选增强：分类由模型归类，分类器不可用（无 key / 网络断）时
// getSkillHubCategories 报错，这里直接隐藏分类栏，浏览和搜索照常。

const PAGE_SIZE = 20;

const CATEGORY_LABELS: Record<string, string> = {
  "docs-knowledge": "文档与知识库",
  "office-collab": "办公协同",
  "internal-systems": "内部系统对接",
  "dev-debug": "开发与调试",
  "content-creation": "内容创作",
  "web-automation": "搜索与自动化",
  "agent-meta": "Agent 元能力",
  other: "其他",
};

export function SkillHub() {
  const [query, setQuery] = useState("");
  const [submittedQuery, setSubmittedQuery] = useState("");
  const [category, setCategory] = useState("");
  const [page, setPage] = useState(1);
  const [data, setData] = useState<SkillHubPage | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [categories, setCategories] = useState<SkillHubCategoryCount[] | null>(
    null,
  );
  const [installed, setInstalled] = useState<SkillHubInstalled[]>([]);
  const [selected, setSelected] = useState<SkillHubSkill | null>(null);

  const installedBySlug = useMemo(() => {
    const m = new Map<string, SkillHubInstalled>();
    for (const item of installed) m.set(item.fullSlug, item);
    return m;
  }, [installed]);

  const refreshInstalled = useCallback(async () => {
    try {
      setInstalled(await listSkillHubInstalled());
    } catch {
      // 已装列表拿不到不影响浏览。
    }
  }, []);

  useEffect(() => {
    void refreshInstalled();
    // 分类失败静默隐藏（分类器不可用是正常形态）。首次拉取可能触发
    // 全量归类，耗时较长，不挡列表加载。
    getSkillHubCategories()
      .then(setCategories)
      .catch(() => setCategories(null));
  }, [refreshInstalled]);

  useEffect(() => {
    let stale = false;
    setLoading(true);
    setError("");
    listSkillHubSkills({
      q: submittedQuery || undefined,
      category: category || undefined,
      page,
      pageSize: PAGE_SIZE,
    })
      .then((res) => {
        if (!stale) setData(res);
      })
      .catch((e) => {
        if (!stale) setError(e instanceof Error ? e.message : String(e));
      })
      .finally(() => {
        if (!stale) setLoading(false);
      });
    return () => {
      stale = true;
    };
  }, [submittedQuery, category, page]);

  const totalPages = data ? Math.max(1, Math.ceil(data.total / PAGE_SIZE)) : 1;

  if (selected) {
    return (
      <SkillDetail
        skill={selected}
        author={data?.authorProfiles[selected.owner ?? ""]}
        installedItem={installedBySlug.get(selected.fullSlug)}
        onBack={() => setSelected(null)}
        onChanged={() => void refreshInstalled()}
      />
    );
  }

  return (
    <div className="flex h-full flex-col overflow-y-auto">
      <header className="shrink-0 px-8 pb-4 pt-10 drag-region">
        <h1 className="text-[20px] font-semibold text-ink">技能市场</h1>
        <p className="mt-1 text-[13px] text-muted">
          浏览内网技能注册中心，安装到本机。装完下一轮对话即生效，不需要重启。
        </p>
      </header>

      <div className="min-h-0 space-y-4 px-8 pb-12">
        <form
          className="flex items-center gap-2"
          onSubmit={(e) => {
            e.preventDefault();
            setSubmittedQuery(query.trim());
            setPage(1);
          }}
        >
          <input
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            placeholder="搜索技能名称或描述…"
            className={cn(
              "h-9 w-full max-w-md rounded-xl border border-rule bg-subtle/50 px-3.5",
              "text-[13px] text-ink placeholder:text-muted/60",
              "focus:outline-none focus:ring-1 focus:ring-ink/20",
            )}
          />
          <button
            type="submit"
            className="inline-flex h-9 items-center rounded-xl border border-rule bg-paper px-4 text-[13px] font-medium text-ink transition-colors hover:bg-subtle"
          >
            搜索
          </button>
        </form>

        {categories && (
          <div className="flex flex-wrap gap-1.5">
            <CategoryChip
              label="全部"
              active={category === ""}
              onClick={() => {
                setCategory("");
                setPage(1);
              }}
            />
            {categories
              .filter((c) => c.count > 0)
              .map((c) => (
                <CategoryChip
                  key={c.id}
                  label={`${CATEGORY_LABELS[c.id] ?? c.id} ${c.count}`}
                  active={category === c.id}
                  onClick={() => {
                    setCategory(c.id);
                    setPage(1);
                  }}
                />
              ))}
          </div>
        )}

        {error && (
          <p className="rounded-lg bg-red-50 px-3 py-2 font-mono text-[12px] text-red-700 dark:bg-red-950/40 dark:text-red-400">
            {error}
          </p>
        )}

        {loading ? (
          <p className="py-10 text-center text-[13px] text-muted">加载中…</p>
        ) : data && data.items.length > 0 ? (
          <>
            <ul className="grid grid-cols-1 gap-3 md:grid-cols-2 xl:grid-cols-3">
              {data.items.map((s) => (
                <SkillCard
                  key={s.fullSlug}
                  skill={s}
                  author={data.authorProfiles[s.owner ?? ""]}
                  installed={installedBySlug.has(s.fullSlug)}
                  onClick={() => setSelected(s)}
                />
              ))}
            </ul>
            <div className="flex items-center justify-center gap-3 pt-2">
              <button
                type="button"
                disabled={page <= 1}
                onClick={() => setPage((p) => p - 1)}
                className={cn(
                  "inline-flex h-8 items-center rounded-lg border border-rule bg-paper px-3 text-[12px] text-ink transition-colors hover:bg-subtle",
                  page <= 1 && "pointer-events-none opacity-40",
                )}
              >
                上一页
              </button>
              <span className="font-mono text-[12px] text-muted tabular-nums">
                {page} / {totalPages} · 共 {data.total} 个
              </span>
              <button
                type="button"
                disabled={page >= totalPages}
                onClick={() => setPage((p) => p + 1)}
                className={cn(
                  "inline-flex h-8 items-center rounded-lg border border-rule bg-paper px-3 text-[12px] text-ink transition-colors hover:bg-subtle",
                  page >= totalPages && "pointer-events-none opacity-40",
                )}
              >
                下一页
              </button>
            </div>
          </>
        ) : (
          !error && (
            <div className="rounded-xl border border-dashed border-rule px-4 py-10 text-center text-[13px] text-muted">
              没有匹配的技能。
            </div>
          )
        )}
      </div>
    </div>
  );
}

function CategoryChip({
  label,
  active,
  onClick,
}: {
  label: string;
  active: boolean;
  onClick: () => void;
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      className={cn(
        "inline-flex h-7 items-center rounded-full border px-3 text-[12px] transition-colors",
        active
          ? "border-ink bg-ink text-paper"
          : "border-rule bg-paper text-muted hover:bg-subtle hover:text-ink",
      )}
    >
      {label}
    </button>
  );
}

function SkillCard({
  skill,
  author,
  installed,
  onClick,
}: {
  skill: SkillHubSkill;
  author?: { displayName?: string; username: string };
  installed: boolean;
  onClick: () => void;
}) {
  const installs = skill.hotness?.installs ?? 0;
  return (
    <li>
      <button
        type="button"
        onClick={onClick}
        className="flex h-full w-full flex-col rounded-xl border border-rule bg-paper px-4 py-3 text-left transition-colors hover:border-ink/30 hover:bg-subtle/40"
      >
        <div className="flex w-full items-center gap-2">
          <span className="truncate text-[14px] font-medium text-ink">
            {skill.name}
          </span>
          {installed && <Badge tone="emerald">已安装</Badge>}
          {skill.isEditorPick && <Badge tone="amber">精选</Badge>}
          {skill.isTeam && <Badge>团队</Badge>}
        </div>
        <p className="mt-1 line-clamp-2 min-h-[2.5em] text-[12px] leading-5 text-muted">
          {skill.description || "（没有描述）"}
        </p>
        <div className="mt-2 flex w-full items-center gap-2 font-mono text-[11px] text-muted/80">
          <span className="truncate">
            {author?.displayName || skill.owner || "匿名"}
          </span>
          <span className="ml-auto shrink-0 tabular-nums">
            {installs > 0 && `${installs} 次安装 · `}
            {skill.latestVersion ?? ""}
          </span>
        </div>
      </button>
    </li>
  );
}

function Badge({
  children,
  tone,
}: {
  children: React.ReactNode;
  tone?: "emerald" | "amber";
}) {
  return (
    <span
      className={cn(
        "shrink-0 rounded px-1.5 py-0.5 font-mono text-[10px]",
        tone === "emerald"
          ? "bg-emerald-50 text-emerald-700 dark:bg-emerald-950/40 dark:text-emerald-400"
          : tone === "amber"
            ? "bg-amber-50 text-amber-700 dark:bg-amber-950/40 dark:text-amber-400"
            : "bg-subtle text-muted",
      )}
    >
      {children}
    </span>
  );
}

function SkillDetail({
  skill,
  author,
  installedItem,
  onBack,
  onChanged,
}: {
  skill: SkillHubSkill;
  author?: { displayName?: string; username: string };
  installedItem?: SkillHubInstalled;
  onBack: () => void;
  onChanged: () => void;
}) {
  const [readme, setReadme] = useState<string | null>(null);
  const [versions, setVersions] = useState<SkillHubVersion[] | null>(null);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [notice, setNotice] = useState("");

  useEffect(() => {
    getSkillHubReadme(skill.fullSlug)
      .then(setReadme)
      .catch((e) =>
        setReadme(`加载 README 失败：${e instanceof Error ? e.message : e}`),
      );
    getSkillHubVersions(skill.fullSlug)
      .then(setVersions)
      .catch(() => setVersions([]));
  }, [skill.fullSlug]);

  const onInstall = async () => {
    setBusy(true);
    setError("");
    setNotice("");
    try {
      const res = await installSkillHubSkill(skill.fullSlug);
      setNotice(
        `已安装为「${res.name}」${res.version ? ` v${res.version}` : ""}，下一轮对话即可使用。`,
      );
      onChanged();
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(false);
    }
  };

  const onUninstall = async () => {
    setBusy(true);
    setError("");
    setNotice("");
    try {
      await uninstallSkillHubSkill(skill.fullSlug);
      setNotice("已卸载。");
      onChanged();
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="flex h-full flex-col overflow-y-auto">
      <header className="shrink-0 px-8 pb-4 pt-10 drag-region">
        {/* header 是 Electron 拖拽区，可点元素必须 no-drag，否则点击被
            当成拖窗口吃掉 */}
        <button
          type="button"
          onClick={onBack}
          className="no-drag text-[13px] text-muted transition-colors hover:text-ink"
        >
          ← 返回技能市场
        </button>
        <div className="mt-3 flex flex-wrap items-center gap-2.5">
          <h1 className="text-[20px] font-semibold text-ink">{skill.name}</h1>
          {installedItem && <Badge tone="emerald">已安装</Badge>}
          {skill.isEditorPick && <Badge tone="amber">精选</Badge>}
          {skill.isTeam && <Badge>团队</Badge>}
          <div className="no-drag ml-auto flex items-center gap-2">
            {installedItem ? (
              <button
                type="button"
                disabled={busy}
                onClick={onUninstall}
                className={cn(
                  "inline-flex h-8 items-center rounded-lg border border-rule bg-paper px-4 text-sm font-medium text-ink",
                  "transition-colors hover:border-red-200 hover:bg-red-50 hover:text-red-700 dark:hover:border-red-900 dark:hover:bg-red-950/40 dark:hover:text-red-400",
                  busy && "pointer-events-none opacity-40",
                )}
              >
                {busy ? "处理中…" : "卸载"}
              </button>
            ) : (
              <button
                type="button"
                disabled={busy}
                onClick={onInstall}
                className={cn(
                  "inline-flex h-8 items-center rounded-lg bg-ink px-4 text-sm font-medium text-paper transition-opacity hover:opacity-90",
                  busy && "pointer-events-none opacity-40",
                )}
              >
                {busy ? "安装中…" : "安装"}
              </button>
            )}
          </div>
        </div>
        <p className="mt-1 text-[13px] text-muted">
          {skill.description || "（没有描述）"}
        </p>
        <p className="mt-1 font-mono text-[11px] text-muted/70">
          {skill.fullSlug}
          {skill.latestVersion && ` · 最新 v${skill.latestVersion}`}
          {" · "}
          {author?.displayName || skill.owner || "匿名"}
          {installedItem?.version && ` · 本机 v${installedItem.version}`}
        </p>
      </header>

      <div className="min-h-0 space-y-6 px-8 pb-12">
        {notice && (
          <p className="rounded-lg bg-emerald-50 px-3 py-2 text-[13px] text-emerald-700 dark:bg-emerald-950/40 dark:text-emerald-400">
            {notice}
          </p>
        )}
        {error && (
          <p className="rounded-lg bg-red-50 px-3 py-2 font-mono text-[12px] text-red-700 dark:bg-red-950/40 dark:text-red-400">
            {error}
          </p>
        )}

        <section className="rounded-xl border border-rule bg-paper px-5 py-4">
          {readme === null ? (
            <p className="py-6 text-center text-[13px] text-muted">
              加载 README…
            </p>
          ) : (
            <MessageBody content={readme} />
          )}
        </section>

        {versions && versions.length > 0 && (
          <section className="space-y-2">
            <h2 className="font-mono text-[10px] uppercase tracking-[0.18em] text-muted/70">
              版本历史
            </h2>
            <ul className="space-y-1.5">
              {versions.map((v) => (
                <li
                  key={v.version}
                  className="flex items-baseline gap-2.5 rounded-lg border border-rule/70 bg-paper px-3 py-2"
                >
                  <span className="font-mono text-[12px] text-ink">
                    v{v.version}
                  </span>
                  {v.isLatest && <Badge tone="emerald">最新</Badge>}
                  {v.createdAt && (
                    <span className="font-mono text-[11px] text-muted/70">
                      {v.createdAt.slice(0, 10)}
                    </span>
                  )}
                  {v.changelog && (
                    <span className="min-w-0 truncate text-[12px] text-muted">
                      {v.changelog}
                    </span>
                  )}
                </li>
              ))}
            </ul>
          </section>
        )}
      </div>
    </div>
  );
}
