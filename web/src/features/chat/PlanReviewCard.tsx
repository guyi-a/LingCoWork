import {
  useEffect,
  useMemo,
  useRef,
  useState,
  type DragEvent,
} from "react";
import {
  cancelPlan,
  editPlan,
  startPlan,
  type WorkItem,
  type WorkPlan,
} from "@/lib/api";
import { cn } from "@/lib/utils";
import { MessageBody } from "./MessageBody";

type Draft = {
  overview: string;
  bodyMD: string;
  items: WorkItem[];
};

export function PlanReviewCard({
  conversationID,
  plan,
  interruptID,
  onPlan,
  onResolved,
}: {
  conversationID: string;
  plan: WorkPlan;
  interruptID: string;
  onPlan: (plan: WorkPlan) => void;
  onResolved: (resumed: boolean, cancelled: boolean) => Promise<void> | void;
}) {
  const [draft, setDraft] = useState<Draft>(() => fromPlan(plan));
  const [dirty, setDirty] = useState(false);
  const [saving, setSaving] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [dragging, setDragging] = useState<number>();
  const [editing, setEditing] = useState(false);
  const [expanded, setExpanded] = useState(false);
  const editVersion = useRef(0);

  useEffect(() => {
    if (!dirty) setDraft(fromPlan(plan));
  }, [plan, dirty]);

  useEffect(() => {
    if (!dirty || busy || !editing) return;
    const version = editVersion.current;
    const timer = window.setTimeout(() => {
      setSaving(true);
      setError("");
      void editPlan(conversationID, plan.id, payload(plan.revision, draft))
        .then((saved) => {
          onPlan(saved);
          if (editVersion.current === version) setDirty(false);
        })
        .catch((err: Error & { latest?: WorkPlan }) => {
          if (err.latest) {
            onPlan(err.latest);
            if (editVersion.current === version) {
              setDraft(fromPlan(err.latest));
              setDirty(false);
            }
          }
          setError(err.message);
        })
        .finally(() => setSaving(false));
    }, 650);
    return () => window.clearTimeout(timer);
  }, [
    busy,
    conversationID,
    dirty,
    draft,
    editing,
    onPlan,
    plan.id,
    plan.revision,
  ]);

  const valid = useMemo(
    () =>
      draft.items.length > 0 &&
      draft.items.every((item) => item.id.trim() && item.content.trim()),
    [draft.items],
  );

  const change = (next: Draft) => {
    editVersion.current++;
    setDraft(next);
    setDirty(true);
  };

  const start = async () => {
    if (!valid || saving || busy) return;
    setBusy(true);
    setError("");
    try {
      const result = await startPlan(
        conversationID,
        plan.id,
        payload(plan.revision, draft, interruptID),
      );
      setDirty(false);
      onPlan(result.plan);
      await onResolved(result.resumed, false);
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setBusy(false);
    }
  };

  const cancel = async () => {
    if (saving || busy) return;
    setBusy(true);
    setError("");
    try {
      const result = await cancelPlan(
        conversationID,
        plan.id,
        plan.revision,
        interruptID,
      );
      setDirty(false);
      onPlan(result.plan);
      await onResolved(result.resumed, true);
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setBusy(false);
    }
  };

  const dropAt = (event: DragEvent, target: number) => {
    event.preventDefault();
    if (dragging === undefined || dragging === target) return;
    const items = draft.items.slice();
    const [moved] = items.splice(dragging, 1);
    items.splice(target, 0, moved);
    change({ ...draft, items: withPositions(items) });
    setDragging(undefined);
  };

  return (
    <section className="my-5 overflow-hidden rounded-xl border border-rule/70 bg-paper">
      <div className="flex items-start gap-3 px-5 pb-3 pt-4">
        <div className="min-w-0">
          <p className="font-mono text-[10px] uppercase tracking-[0.16em] text-accent">
            Plan
          </p>
          {editing ? (
            <input
              value={draft.overview}
              onChange={(event) =>
                change({ ...draft, overview: event.target.value })
              }
              className="mt-1 w-full border-b border-rule bg-transparent pb-1 text-[15px] font-medium text-ink outline-none focus:border-accent/60"
              aria-label="计划概述"
            />
          ) : (
            <h3 className="mt-1 text-[15px] font-medium leading-6 text-ink">
              {draft.overview || "Implementation plan"}
            </h3>
          )}
        </div>

        <div className="ml-auto flex shrink-0 items-center gap-2">
          <span className="font-mono text-[10px] text-muted">
            {saving ? "保存中…" : dirty ? "待保存" : `v${plan.revision}`}
          </span>
          <button
            type="button"
            disabled={saving || busy || (editing && dirty)}
            onClick={() => setEditing((value) => !value)}
            className="rounded-md px-2 py-1 text-[11px] text-muted transition-colors hover:bg-subtle hover:text-ink disabled:opacity-40"
          >
            {editing ? "完成" : "编辑"}
          </button>
        </div>
      </div>

      <div className="px-5 pb-4">
        {editing ? (
          <textarea
            value={draft.bodyMD}
            onChange={(event) =>
              change({ ...draft, bodyMD: event.target.value })
            }
            rows={10}
            className="w-full resize-y border-y border-rule/70 bg-transparent py-3 font-mono text-xs leading-5 text-ink outline-none focus:border-accent/50"
            aria-label="详细计划"
          />
        ) : draft.bodyMD ? (
          <div>
            <div
              className={cn(
                "relative overflow-hidden text-sm leading-6",
                !expanded && "max-h-52",
              )}
            >
              <MessageBody content={draft.bodyMD} />
              {!expanded && (
                <div className="pointer-events-none absolute inset-x-0 bottom-0 h-12 bg-gradient-to-t from-paper to-transparent" />
              )}
            </div>
            <button
              type="button"
              onClick={() => setExpanded((value) => !value)}
              className="mt-1 text-[11px] text-muted hover:text-ink"
            >
              {expanded ? "收起详细计划" : "展开详细计划"}
            </button>
          </div>
        ) : null}

        <div className="mt-4">
          <div className="mb-1.5 flex items-center">
            <span className="font-mono text-[10px] uppercase tracking-[0.12em] text-muted">
              {draft.items.length} tasks
            </span>
            {editing && (
              <button
                type="button"
                onClick={() => {
                  const id = nextItemID(draft.items);
                  change({
                    ...draft,
                    items: withPositions([
                      ...draft.items,
                      { id, content: "", status: "pending", position: 0 },
                    ]),
                  });
                }}
                className="ml-auto text-[11px] text-accent hover:underline"
              >
                + 添加任务
              </button>
            )}
          </div>

          <div className="divide-y divide-rule/60 border-y border-rule/60">
            {draft.items.map((item, index) =>
              editing ? (
                <div
                  key={item.id}
                  draggable
                  onDragStart={() => setDragging(index)}
                  onDragOver={(event) => event.preventDefault()}
                  onDrop={(event) => dropAt(event, index)}
                  className={cn(
                    "group flex items-center gap-2 py-2.5",
                    dragging === index && "opacity-40",
                  )}
                >
                  <span
                    className="cursor-grab select-none text-muted/50 opacity-0 transition-opacity group-hover:opacity-100"
                    title="拖动排序"
                  >
                    ⋮⋮
                  </span>
                  <span className="w-5 shrink-0 font-mono text-[10px] text-muted">
                    {String(index + 1).padStart(2, "0")}
                  </span>
                  <input
                    value={item.content}
                    onChange={(event) => {
                      const items = draft.items.slice();
                      items[index] = { ...item, content: event.target.value };
                      change({ ...draft, items });
                    }}
                    className="min-w-0 flex-1 bg-transparent text-sm text-ink outline-none"
                    placeholder="任务内容"
                  />
                  <button
                    type="button"
                    onClick={() =>
                      change({
                        ...draft,
                        items: withPositions(
                          draft.items.filter(
                            (_, itemIndex) => itemIndex !== index,
                          ),
                        ),
                      })
                    }
                    className="px-1 text-muted opacity-0 transition-opacity hover:text-red-600 group-hover:opacity-100 dark:hover:text-red-400"
                    aria-label="删除任务"
                  >
                    ×
                  </button>
                </div>
              ) : (
                <div key={item.id} className="flex items-start gap-3 py-2.5">
                  <span className="mt-1 size-3.5 shrink-0 rounded-full border border-rule bg-paper" />
                  <span className="text-sm leading-5 text-ink">
                    {item.content}
                  </span>
                </div>
              ),
            )}
          </div>
        </div>

        {error && <p className="mt-3 text-xs text-red-600 dark:text-red-400">{error}</p>}
      </div>

      <div className="flex items-center justify-end gap-2 border-t border-rule/70 bg-subtle/20 px-5 py-3">
        <button
          type="button"
          disabled={saving || busy}
          onClick={cancel}
          className="rounded-lg px-3 py-2 text-xs text-muted transition-colors hover:bg-subtle hover:text-ink disabled:opacity-50"
        >
          取消
        </button>
        <button
          type="button"
          disabled={!valid || saving || busy}
          onClick={start}
          className="rounded-lg bg-ink px-4 py-2 text-xs font-medium text-paper disabled:opacity-40"
        >
          {busy ? "正在开始…" : "开始实施"}
        </button>
      </div>
    </section>
  );
}

export function PlanHistoryCard({ plan }: { plan: WorkPlan }) {
  const [open, setOpen] = useState(false);
  return (
    <section className="my-5 overflow-hidden rounded-xl border border-rule bg-paper">
      <button
        type="button"
        onClick={() => setOpen((value) => !value)}
        className="flex w-full items-center gap-2 px-4 py-3 text-left hover:bg-subtle/40"
        aria-expanded={open}
      >
        <span className={cn("text-muted transition-transform", open && "rotate-90")}>
          ›
        </span>
        <span className="text-sm font-medium text-ink">
          {plan.overview || "Implementation plan"}
        </span>
        <span className="ml-auto font-mono text-[10px] uppercase text-muted">
          {plan.status}
        </span>
      </button>
      {open && (
        <div className="space-y-3 border-t border-rule px-4 py-4">
          {plan.body_md && <MessageBody content={plan.body_md} />}
          <div className="space-y-1">
            {plan.items.map((item) => (
              <div key={item.id} className="flex gap-2 text-xs leading-5">
                <span className="font-mono text-muted">
                  {item.status === "completed"
                    ? "✓"
                    : item.status === "cancelled"
                      ? "×"
                      : "○"}
                </span>
                <span className="text-ink">{item.content}</span>
              </div>
            ))}
          </div>
        </div>
      )}
    </section>
  );
}

function fromPlan(plan: WorkPlan): Draft {
  return {
    overview: plan.overview,
    bodyMD: plan.body_md,
    items: plan.items.map((item) => ({ ...item })),
  };
}

function payload(
  revision: number,
  draft: Draft,
  interruptID?: string,
) {
  return {
    revision,
    overview: draft.overview,
    body_md: draft.bodyMD,
    items: withPositions(draft.items),
    ...(interruptID ? { interrupt_id: interruptID } : {}),
  };
}

function withPositions(items: WorkItem[]): WorkItem[] {
  return items.map((item, position) => ({ ...item, position }));
}

function nextItemID(items: WorkItem[]): string {
  const used = new Set(items.map((item) => item.id));
  let n = items.length + 1;
  while (used.has(`step-${n}`)) n++;
  return `step-${n}`;
}
