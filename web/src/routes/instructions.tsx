import { useCallback, useEffect, useRef, useState } from "react";
import {
  createInstruction,
  deleteInstruction,
  getInstruction,
  listInstructions,
  updateInstruction,
  type Instruction,
  type InstructionInput,
} from "@/lib/api";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogTitle,
} from "@/components/ui/dialog";
import { cn } from "@/lib/utils";
import { InstructionIcon } from "@/features/chat/InstructionPicker";

export function Instructions() {
  const [items, setItems] = useState<Instruction[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [editing, setEditing] = useState<Instruction | null | undefined>(
    undefined,
  );
  const [deleting, setDeleting] = useState<string>();
  const editRequestRef = useRef(0);

  const load = useCallback(async () => {
    setLoading(true);
    try {
      setItems(await listInstructions());
      setError("");
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  const edit = async (item: Instruction) => {
    const request = ++editRequestRef.current;
    setError("");
    try {
      const instruction = await getInstruction(item.name);
      if (request === editRequestRef.current) setEditing(instruction);
    } catch (err) {
      if (request === editRequestRef.current) {
        setError(err instanceof Error ? err.message : String(err));
      }
    }
  };

  const remove = async (name: string) => {
    try {
      await deleteInstruction(name);
      setDeleting(undefined);
      await load();
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    }
  };

  return (
    <div className="flex h-full flex-col overflow-y-auto">
      <header className="drag-region flex shrink-0 items-start justify-between gap-5 px-8 pb-4 pt-10">
        <div>
          <h1 className="text-[20px] font-semibold text-ink">快捷指令</h1>
          <p className="mt-1 text-[13px] text-muted">
            用 Markdown 保存常用提示，在发送消息时按需选用。
          </p>
        </div>
        <button
          type="button"
          onClick={() => {
            editRequestRef.current++;
            setEditing(null);
          }}
          className="no-drag inline-flex h-8 shrink-0 items-center rounded-lg bg-ink px-4 text-sm font-medium text-paper transition-opacity hover:opacity-90"
        >
          新建指令
        </button>
      </header>

      <div className="min-h-0 px-8 pb-12">
        {error && (
          <p className="mb-3 rounded-lg bg-red-50 px-3 py-2 font-mono text-[12px] text-red-700">
            {error}
          </p>
        )}
        {loading ? (
          <div className="py-16 text-center text-sm text-muted">加载中…</div>
        ) : items.length === 0 ? (
          <div className="rounded-xl border border-dashed border-rule px-6 py-16 text-center">
            <InstructionIcon className="mx-auto size-6 text-muted" />
            <p className="mt-3 text-sm font-medium text-ink">还没有快捷指令</p>
            <p className="mt-1 text-[13px] text-muted">
              新建一个模板，正文可用 {"{{input}}"} 表示本次输入。
            </p>
          </div>
        ) : (
          <ul className="grid gap-3 lg:grid-cols-2">
            {items.map((item) => (
              <li
                key={item.name}
                className="rounded-xl border border-rule bg-paper px-4 py-4"
              >
                <div className="flex items-start gap-3">
                  <span className="mt-0.5 flex size-8 shrink-0 items-center justify-center rounded-lg bg-subtle text-accent">
                    <InstructionIcon className="size-4" />
                  </span>
                  <div className="min-w-0 flex-1">
                    <p className="truncate text-[14px] font-medium text-ink">
                      {item.label}
                    </p>
                    <p className="mt-0.5 truncate font-mono text-[10px] text-muted">
                      /{item.name}
                    </p>
                    <p className="mt-2 line-clamp-2 text-[12px] leading-5 text-muted">
                      {item.description || "暂无描述"}
                    </p>
                  </div>
                </div>
                <div className="mt-4 flex items-center justify-end gap-2 border-t border-rule/60 pt-3">
                  {deleting === item.name ? (
                    <>
                      <span className="mr-auto text-[11px] text-red-700">
                        删除后无法恢复
                      </span>
                      <button
                        type="button"
                        onClick={() => void remove(item.name)}
                        className="text-[12px] font-medium text-red-700 hover:text-red-800"
                      >
                        确认删除
                      </button>
                      <button
                        type="button"
                        onClick={() => setDeleting(undefined)}
                        className="text-[12px] text-muted hover:text-ink"
                      >
                        取消
                      </button>
                    </>
                  ) : (
                    <>
                      <button
                        type="button"
                        onClick={() => setDeleting(item.name)}
                        className="text-[12px] text-muted hover:text-red-700"
                      >
                        删除
                      </button>
                      <button
                        type="button"
                        onClick={() => void edit(item)}
                        className="inline-flex h-7 items-center rounded-lg border border-rule bg-paper px-3 text-[12px] font-medium text-ink hover:bg-subtle"
                      >
                        编辑
                      </button>
                    </>
                  )}
                </div>
              </li>
            ))}
          </ul>
        )}
      </div>

      <InstructionEditor
        value={editing}
        onClose={() => setEditing(undefined)}
        onSaved={async () => {
          setEditing(undefined);
          await load();
        }}
      />
    </div>
  );
}

function InstructionEditor({
  value,
  onClose,
  onSaved,
}: {
  value: Instruction | null | undefined;
  onClose: () => void;
  onSaved: () => Promise<void>;
}) {
  const [form, setForm] = useState<InstructionInput>({
    name: "",
    label: "",
    description: "",
    prompt: "",
  });
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState("");

  useEffect(() => {
    if (value === undefined) return;
    setForm(
      value
        ? {
            name: value.name,
            label: value.label,
            description: value.description,
            prompt: value.prompt,
          }
        : { name: "", label: "", description: "", prompt: "" },
    );
    setError("");
  }, [value]);

  const valid =
    !!form.name?.trim() &&
    !!form.label.trim() &&
    !!form.description.trim() &&
    !!form.prompt.trim();

  const save = async () => {
    if (!valid || saving) return;
    setSaving(true);
    setError("");
    try {
      if (value) await updateInstruction(value.name, form);
      else await createInstruction(form);
      await onSaved();
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setSaving(false);
    }
  };

  const field =
    "block w-full rounded-lg border border-rule bg-paper px-3 text-sm text-ink outline-none focus:border-accent focus:ring-2 focus:ring-accent/10";

  return (
    <Dialog open={value !== undefined} onOpenChange={(open) => !open && onClose()}>
      <DialogContent className="max-w-2xl rounded-2xl p-0">
        <div className="border-b border-rule px-6 py-5">
          <DialogTitle className="mb-1">
            {value ? "编辑快捷指令" : "新建快捷指令"}
          </DialogTitle>
          <DialogDescription>
            模板支持 Markdown；使用 {"{{input}}"} 插入用户输入，否则输入会追加在模板末尾。
          </DialogDescription>
        </div>
        <div className="space-y-4 px-6 py-5">
          <div className="grid grid-cols-2 gap-4">
            <label className="space-y-1.5">
              <span className="text-[12px] font-medium text-ink">名称</span>
              <input
                value={form.name ?? ""}
                onChange={(e) => setForm((f) => ({ ...f, name: e.target.value }))}
                disabled={!!value}
                placeholder="code-review"
                spellCheck={false}
                className={cn(
                  field,
                  "h-9 font-mono text-[12px]",
                  value && "cursor-not-allowed opacity-60",
                )}
              />
            </label>
            <label className="space-y-1.5">
              <span className="text-[12px] font-medium text-ink">显示名称</span>
              <input
                value={form.label}
                onChange={(e) => setForm((f) => ({ ...f, label: e.target.value }))}
                placeholder="代码审查"
                className={cn(field, "h-9")}
              />
            </label>
          </div>
          <label className="block space-y-1.5">
            <span className="text-[12px] font-medium text-ink">描述</span>
            <input
              value={form.description}
              onChange={(e) =>
                setForm((f) => ({ ...f, description: e.target.value }))
              }
              placeholder="一句话说明适用场景"
              className={cn(field, "h-9")}
            />
          </label>
          <label className="block space-y-1.5">
            <span className="text-[12px] font-medium text-ink">
              Markdown 提示词
            </span>
            <textarea
              value={form.prompt}
              onChange={(e) => setForm((f) => ({ ...f, prompt: e.target.value }))}
              rows={13}
              spellCheck={false}
              placeholder={"请审查下面的代码：\n\n{{input}}"}
              className={cn(
                field,
                "resize-y px-4 py-3 font-mono text-[12px] leading-5",
              )}
            />
          </label>
          {error && (
            <p className="rounded-lg bg-red-50 px-3 py-2 font-mono text-[12px] text-red-700">
              {error}
            </p>
          )}
        </div>
        <div className="flex justify-end gap-2 border-t border-rule px-6 py-4">
          <button
            type="button"
            onClick={onClose}
            className="inline-flex h-8 items-center rounded-lg border border-rule bg-paper px-4 text-sm text-ink hover:bg-subtle"
          >
            取消
          </button>
          <button
            type="button"
            disabled={!valid || saving}
            onClick={() => void save()}
            className={cn(
              "inline-flex h-8 items-center rounded-lg bg-ink px-4 text-sm font-medium text-paper transition-opacity",
              (!valid || saving) && "pointer-events-none opacity-40",
            )}
          >
            {saving ? "保存中…" : "保存"}
          </button>
        </div>
      </DialogContent>
    </Dialog>
  );
}
