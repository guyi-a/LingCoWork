import { useCallback, useEffect, useState } from "react";
import { cn } from "@/lib/utils";
import {
  authorizeMCPServer,
  deleteMCPServer,
  getMCPConfig,
  listMCPServers,
  saveMCPConfig,
  testMCPServer,
  type MCPIssue,
  type MCPServerStatus,
  type MCPTestResult,
} from "@/lib/api";

// The connectors page manages MCP servers.
//
// It is an editor over one JSON file rather than a form over individual
// fields. The file's schema is Claude Desktop's, so the common case is
// pasting an entry someone published; a form would make that the awkward path
// and would have to grow a field every time the spec does.
//
// The file holds API keys. It is shown in full here — a redacted view cannot
// be edited, since saving it would write the redaction back over the key —
// and nowhere else: the status list below is built from a struct with no
// field for a credential to sit in.
//
// Nothing on this page asks for a restart. Servers connect, disconnect and
// reconnect while the app runs, which they have to: OAuth authorization
// happens in a browser window opened from here, so a server needing it could
// not possibly have been up when the process started.

// POLL_MS is how often the status list refreshes while something is still
// settling. Connecting is the normal state for the first seconds of a stdio
// server that installs itself, and authorizing finishes in another window
// that cannot tell us it is done.
const POLL_MS = 2000;

export function Connectors() {
  const [servers, setServers] = useState<MCPServerStatus[]>([]);
  const [issues, setIssues] = useState<MCPIssue[]>([]);
  const [configPath, setConfigPath] = useState("");
  const [content, setContent] = useState("");
  const [dirty, setDirty] = useState(false);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState("");
  const [saved, setSaved] = useState(false);
  const [tests, setTests] = useState<Record<string, MCPTestResult | "running">>(
    {},
  );

  const refreshStatus = useCallback(async () => {
    const status = await listMCPServers();
    setServers(status.servers);
    setIssues(status.issues ?? []);
    setConfigPath((p) => status.config_path || p);
  }, []);

  const load = useCallback(async () => {
    try {
      const [, doc] = await Promise.all([refreshStatus(), getMCPConfig()]);
      setConfigPath((p) => p || doc.path);
      setContent(doc.content);
      setDirty(false);
      setError("");
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    }
  }, [refreshStatus]);

  useEffect(() => {
    void load();
  }, [load]);

  // Poll only while something can still change on its own: a server still
  // dialling, or an authorization happening in another window that has no way
  // to tell us it finished. A settled list has no reason to keep asking.
  const [awaitingAuth, setAwaitingAuth] = useState(false);
  const settling = servers.some((s) => s.state === "connecting");
  useEffect(() => {
    if (!settling && !awaitingAuth) return;
    const id = setInterval(() => void refreshStatus().catch(() => {}), POLL_MS);
    return () => clearInterval(id);
  }, [settling, awaitingAuth, refreshStatus]);

  // Nothing left waiting on a browser window: the authorization either landed
  // or was for a server that has since gone away.
  const anyNeedsAuth = servers.some((s) => s.state === "needs_auth");
  useEffect(() => {
    if (awaitingAuth && !anyNeedsAuth) setAwaitingAuth(false);
  }, [awaitingAuth, anyNeedsAuth]);

  const onSave = async () => {
    setSaving(true);
    setError("");
    setSaved(false);
    try {
      const res = await saveMCPConfig(content);
      setIssues(res.issues ?? []);
      setDirty(false);
      setSaved(true);
      // The backend applies the new config in the background; give it a beat
      // before asking what it did.
      setTimeout(() => void refreshStatus().catch(() => {}), 300);
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setSaving(false);
    }
  };

  // Formatting is a parse and re-print, so it doubles as a syntax check: an
  // entry pasted from a README with a trailing comma fails here rather than
  // at save time. It also normalises the indentation of an entry typed by
  // hand, which is what makes a config with several servers readable at all.
  //
  // A `//` comment is a parse failure, correctly: the backend reads this with
  // a standard JSON parser too, so a document containing one could never have
  // been saved.
  const onFormat = () => {
    let formatted: string;
    try {
      formatted = JSON.stringify(JSON.parse(content), null, 2) + "\n";
    } catch (e) {
      setError(`格式化失败：${e instanceof Error ? e.message : String(e)}`);
      return;
    }
    setError("");
    if (formatted === content) return;
    setContent(formatted);
    setDirty(true);
    setSaved(false);
  };

  const onTest = async (name: string) => {
    setTests((t) => ({ ...t, [name]: "running" }));
    try {
      const res = await testMCPServer(name);
      setTests((t) => ({ ...t, [name]: res }));
    } catch (e) {
      setTests((t) => ({
        ...t,
        [name]: {
          ok: false,
          tool_count: 0,
          error: e instanceof Error ? e.message : String(e),
        },
      }));
    }
  };

  const onAuthorize = async (name: string) => {
    setError("");
    try {
      const url = await authorizeMCPServer(name);
      window.open(url, "_blank", "noopener,noreferrer");
      // The other window finishes the flow and the backend reconnects on its
      // own, so all this side has to do is keep looking. Stop after a couple
      // of minutes rather than polling forever behind an abandoned tab.
      setAwaitingAuth(true);
      void refreshStatus().catch(() => {});
      setTimeout(() => setAwaitingAuth(false), 120_000);
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    }
  };

  // Deleting rewrites the file, so the editor below is reloaded rather than
  // just the status list: leaving the deleted entry sitting in the textarea
  // would bring the server back on the next save, without its token.
  const onDelete = async (name: string) => {
    setError("");
    let failure = "";
    try {
      await deleteMCPServer(name);
    } catch (e) {
      failure = e instanceof Error ? e.message : String(e);
    }
    // After load(), which clears it: a partial delete still changed the file
    // and the reason has to survive the reload.
    await load();
    if (failure) setError(failure);
  };

  return (
    <div className="flex h-full flex-col overflow-y-auto">
      <header className="shrink-0 px-8 pb-4 pt-10 drag-region">
        <h1 className="text-[20px] font-semibold text-ink">连接器</h1>
        <p className="mt-1 text-[13px] text-muted">
          接入 MCP 服务器，把它们的工具交给 LingCoWork
          使用。改动即时生效，不需要重启。
        </p>
      </header>

      <div className="min-h-0 space-y-8 px-8 pb-12">
        <section className="space-y-3">
          <SectionTitle
            label="当前状态"
            hint={servers.length > 0 ? `${servers.length} 个服务器` : undefined}
          />

          {servers.length === 0 ? (
            <EmptyCard>
              还没有配置任何 MCP 服务器。在下面的配置里加一条，保存即可。
            </EmptyCard>
          ) : (
            <ul className="space-y-2">
              {servers.map((s) => (
                <ServerRow
                  key={s.name}
                  server={s}
                  test={tests[s.name]}
                  onTest={() => onTest(s.name)}
                  onAuthorize={() => onAuthorize(s.name)}
                  onDelete={() => onDelete(s.name)}
                />
              ))}
            </ul>
          )}

          {issues.length > 0 && (
            <div className="rounded-xl border border-amber-200 bg-amber-50/70 px-4 py-3">
              <p className="text-[13px] font-medium text-amber-800">
                有 {issues.length} 条配置被跳过
              </p>
              <ul className="mt-1.5 space-y-1">
                {issues.map((i) => (
                  <li
                    key={i.server}
                    className="font-mono text-[12px] text-amber-700"
                  >
                    {i.server}: {i.message}
                  </li>
                ))}
              </ul>
            </div>
          )}
        </section>

        <section className="space-y-3">
          <SectionTitle label="配置文件" hint={configPath} />

          <p className="text-[12px] leading-5 text-muted">
            这个文件可能包含 API Key 和 Authorization 头，只保存在本机，且已被 git
            忽略。OAuth 拿到的 token 存在本机数据库里，不在这个文件中。
          </p>

          <textarea
            value={content}
            spellCheck={false}
            onChange={(e) => {
              setContent(e.target.value);
              setDirty(true);
              setSaved(false);
            }}
            rows={18}
            className={cn(
              "block w-full resize-y rounded-xl border border-rule bg-subtle/50 px-4 py-3",
              "font-mono text-[12px] leading-5 text-ink",
              "focus:outline-none focus:ring-1 focus:ring-ink/20",
            )}
          />

          {error && (
            <p className="rounded-lg bg-red-50 px-3 py-2 font-mono text-[12px] text-red-700">
              {error}
            </p>
          )}

          <div className="flex items-center gap-3">
            <button
              type="button"
              disabled={saving || !dirty}
              onClick={onSave}
              className={cn(
                "inline-flex h-8 items-center rounded-lg bg-ink px-4 text-sm font-medium text-paper",
                "transition-opacity hover:opacity-90",
                (saving || !dirty) && "pointer-events-none opacity-40",
              )}
            >
              {saving ? "保存中…" : "保存"}
            </button>
            <button
              type="button"
              disabled={!content.trim()}
              onClick={onFormat}
              className={cn(
                "inline-flex h-8 items-center rounded-lg border border-rule bg-paper px-4 text-sm font-medium text-ink",
                "transition-colors hover:bg-subtle",
                !content.trim() && "pointer-events-none opacity-40",
              )}
            >
              格式化
            </button>
            <button
              type="button"
              onClick={() => void load()}
              className="inline-flex h-8 items-center rounded-lg border border-rule bg-paper px-4 text-sm font-medium text-ink transition-colors hover:bg-subtle"
            >
              放弃改动
            </button>
            {saved && <span className="text-[13px] text-muted">已保存</span>}
          </div>
        </section>
      </div>
    </div>
  );
}

function ServerRow({
  server,
  test,
  onTest,
  onAuthorize,
  onDelete,
}: {
  server: MCPServerStatus;
  test: MCPTestResult | "running" | undefined;
  onTest: () => void;
  onAuthorize: () => void;
  onDelete: () => void;
}) {
  // Deleting drops a stored token as well as the config entry, and neither
  // comes back, so it takes a second click. Inline rather than a modal: the
  // row already says which server this is.
  const [confirming, setConfirming] = useState(false);
  const needsAuth = server.state === "needs_auth";
  const connecting = server.state === "connecting";

  return (
    <li className="rounded-xl border border-rule bg-paper px-4 py-3">
      <div className="flex flex-wrap items-center gap-2.5">
        <StateDot state={server.state} />
        <span className="text-[14px] font-medium text-ink">{server.name}</span>
        <span className="rounded bg-subtle px-1.5 py-0.5 font-mono text-[10px] text-muted">
          {server.transport}
        </span>
        {server.oauth && (
          <span
            className="rounded bg-subtle px-1.5 py-0.5 font-mono text-[10px] text-muted"
            title={
              server.authorized
                ? "已通过 OAuth 授权，token 存在本机数据库；删除这个服务器会一并清除"
                : "这个服务器用 OAuth 授权"
            }
          >
            {server.authorized ? "已授权" : "oauth"}
          </span>
        )}
        {server.trusted && (
          <span
            className="rounded bg-subtle px-1.5 py-0.5 font-mono text-[10px] text-muted"
            title="已信任该服务器声明的 readOnlyHint，只读工具不再逐次询问"
          >
            信任注解
          </span>
        )}
        {connecting && (
          <span className="text-[11px] text-muted">连接中…</span>
        )}
        {server.state === "connected" && (
          <span className="font-mono text-[11px] text-muted tabular-nums">
            {server.tool_count} 个工具
          </span>
        )}

        <div className="ml-auto flex items-center gap-2">
          {needsAuth && (
            <button
              type="button"
              onClick={onAuthorize}
              className="inline-flex h-7 items-center rounded-lg bg-ink px-3 text-[12px] font-medium text-paper transition-opacity hover:opacity-90"
            >
              授权
            </button>
          )}
          {confirming ? (
            <>
              <button
                type="button"
                onClick={() => {
                  setConfirming(false);
                  onDelete();
                }}
                className="inline-flex h-7 items-center rounded-lg bg-red-600 px-3 text-[12px] font-medium text-paper transition-opacity hover:opacity-90"
              >
                确认删除
              </button>
              <button
                type="button"
                onClick={() => setConfirming(false)}
                className="text-[12px] text-muted transition-colors hover:text-ink"
              >
                取消
              </button>
            </>
          ) : (
            <button
              type="button"
              onClick={() => setConfirming(true)}
              title="从配置里删除这个服务器，并清除它的授权"
              className={cn(
                "inline-flex h-7 items-center rounded-lg border border-rule bg-paper px-3 text-[12px] font-medium text-ink",
                "transition-colors hover:border-red-200 hover:bg-red-50 hover:text-red-700",
              )}
            >
              删除
            </button>
          )}
          <button
            type="button"
            disabled={test === "running"}
            onClick={onTest}
            className={cn(
              "inline-flex h-7 items-center rounded-lg border border-rule bg-paper px-3 text-[12px] font-medium text-ink",
              "transition-colors hover:bg-subtle",
              test === "running" && "pointer-events-none opacity-50",
            )}
          >
            {test === "running" ? "测试中…" : "测试连接"}
          </button>
        </div>
      </div>

      <p className="mt-1 truncate font-mono text-[11px] text-muted">
        {server.target}
      </p>

      {needsAuth && (
        <p className="mt-2 rounded-lg bg-subtle/60 px-3 py-2 text-[12px] leading-5 text-muted">
          这个服务器要求授权。点「授权」会打开浏览器，完成后会自动连上。
        </p>
      )}

      {server.error && (
        <p className="mt-2 rounded-lg bg-red-50 px-3 py-2 font-mono text-[11px] leading-4 text-red-700">
          {server.error}
        </p>
      )}
      {server.stderr && (
        <pre className="mt-2 max-h-32 overflow-auto rounded-lg bg-subtle/60 px-3 py-2 font-mono text-[11px] leading-4 text-muted">
          {server.stderr}
        </pre>
      )}

      {test && test !== "running" && (
        <p
          className={cn(
            "mt-2 rounded-lg px-3 py-2 font-mono text-[11px] leading-4",
            test.ok ? "bg-subtle/60 text-muted" : "bg-red-50 text-red-700",
          )}
        >
          {test.ok ? `连接成功 · ${test.tool_count} 个工具` : test.error}
        </p>
      )}

      {server.tools && server.tools.length > 0 && (
        <ul className="mt-2 flex flex-wrap gap-1.5">
          {server.tools.map((t) => (
            <li
              key={t}
              className="rounded bg-subtle px-2 py-0.5 font-mono text-[11px] text-muted"
            >
              {t}
            </li>
          ))}
        </ul>
      )}
    </li>
  );
}

function StateDot({ state }: { state: MCPServerStatus["state"] }) {
  const color =
    state === "connected"
      ? "bg-emerald-500"
      : state === "error"
        ? "bg-red-500"
        : state === "needs_auth"
          ? "bg-amber-500"
          : state === "connecting"
            ? "animate-pulse bg-amber-400"
            : "bg-muted/40";
  return <span className={cn("size-2 shrink-0 rounded-full", color)} />;
}

function SectionTitle({ label, hint }: { label: string; hint?: string }) {
  return (
    <div className="flex items-baseline gap-2.5">
      <h2 className="font-mono text-[10px] uppercase tracking-[0.18em] text-muted/70">
        {label}
      </h2>
      {hint && (
        <span className="min-w-0 truncate font-mono text-[11px] text-muted/60">
          {hint}
        </span>
      )}
    </div>
  );
}

function EmptyCard({ children }: { children: React.ReactNode }) {
  return (
    <div className="rounded-xl border border-dashed border-rule px-4 py-6 text-center text-[13px] text-muted">
      {children}
    </div>
  );
}
