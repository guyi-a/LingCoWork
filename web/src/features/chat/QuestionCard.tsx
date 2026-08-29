import { useMemo, useState } from "react";
import { cn } from "@/lib/utils";
import { useQuestionStore } from "@/features/chat/question-store";
import { parseQuestionsJson, type NormalizedQuestion } from "@/features/chat/hitl-input";

// 单选场景的伪 value —— 用户勾了这个选项后 UI 切成文本输入框，允许自由填答案。
// 多选场景不给 Other（想勾自定义就把选项直接勾进去）。
const CUSTOM_VALUE = "__custom__";

// AnswerState —— 单选用 string；多选用 string[]（用 Set 更好但 zustand 序列化会麻烦）。
type AnswerState = Record<string, string | string[]>;

function getSelected(state: AnswerState, id: string): string[] {
  const v = state[id];
  if (Array.isArray(v)) return v;
  if (typeof v === "string" && v && v !== CUSTOM_VALUE) return [v];
  return [];
}

function isAnswered(
  q: NormalizedQuestion,
  state: AnswerState,
  customs: Record<string, string>,
): boolean {
  if (q.multiSelect) {
    return getSelected(state, q.id).length >= 1;
  }
  const v = state[q.id];
  if (typeof v !== "string" || v === "") return false;
  if (v === CUSTOM_VALUE) return Boolean(customs[q.id]?.trim());
  return true;
}

export function QuestionCard({
  conversationID,
  interruptId,
  callId,
  questionsJson,
  onDecision,
  onResume,
}: {
  conversationID: string;
  interruptId: string;
  callId: string;
  questionsJson: string;
  // onDecision 让外层给对应 tool 卡打状态（answered → running，cancelled →
  // cancelled）以及做 sidebar refresh；不传也能工作，只是 tool 卡状态可能停
  // 在 pending 直到 resume 后端事件回填。
  onDecision?: (
    callId: string,
    cancelled: boolean,
    resumed: boolean,
  ) => Promise<void> | void;
  onResume?: () => Promise<void> | void;
}) {
  const questions = useMemo(() => parseQuestionsJson(questionsJson), [questionsJson]);
  const answer = useQuestionStore((s) => s.answer);

  const [state, setState] = useState<AnswerState>({});
  const [customs, setCustoms] = useState<Record<string, string>>({});
  const [busy, setBusy] = useState(false);

  const allAnswered = questions.length > 0 && questions.every((q) => isAnswered(q, state, customs));

  const toggleMulti = (qid: string, option: string) => {
    setState((prev) => {
      const cur = getSelected(prev, qid);
      const next = cur.includes(option) ? cur.filter((s) => s !== option) : [...cur, option];
      return { ...prev, [qid]: next };
    });
  };

  const setSingle = (qid: string, option: string) => {
    setState((prev) => ({ ...prev, [qid]: option }));
  };

  const submit = async () => {
    if (busy) return;
    setBusy(true);
    try {
      const items = questions.map((q) => {
        const v = state[q.id];
        if (q.multiSelect) {
          return { question_id: q.id, selected: getSelected(state, q.id) };
        }
        if (typeof v === "string" && v === CUSTOM_VALUE) {
          return { question_id: q.id, selected: [], custom: customs[q.id]?.trim() ?? "" };
        }
        return {
          question_id: q.id,
          selected: typeof v === "string" && v ? [v] : [],
        };
      });
      const result = await answer(conversationID, interruptId, {
        cancelled: false,
        answers: items,
      });
      if (result.handled) await onDecision?.(callId, false, result.resumed);
      if (result.resumed) await onResume?.();
    } catch (err) {
      console.error("[question] post failed:", err);
    } finally {
      setBusy(false);
    }
  };

  const cancel = async () => {
    if (busy) return;
    setBusy(true);
    try {
      const result = await answer(conversationID, interruptId, {
        cancelled: true,
        answers: [],
      });
      if (result.handled) await onDecision?.(callId, true, result.resumed);
      if (result.resumed) await onResume?.();
    } catch (err) {
      console.error("[question] post failed:", err);
    } finally {
      setBusy(false);
    }
  };

  if (questions.length === 0) return null;

  return (
    <div
      className={cn(
        "rounded-xl border border-rule bg-paper px-4 py-3",
        "space-y-3",
        "shadow-[0_-8px_24px_-8px_rgba(0,0,0,0.14),0_-2px_6px_-2px_rgba(0,0,0,0.08)]",
      )}
    >
      <div className="flex items-center gap-2.5">
        <QuestionIcon />
        <span className="text-[15px] font-semibold text-ink">等待你的回答</span>
        {questions.length > 1 && (
          <span className="rounded bg-subtle px-1.5 py-0.5 font-mono text-[10px] text-muted tabular-nums">
            {questions.length} 题
          </span>
        )}
      </div>

      <div className="max-h-[50vh] overflow-y-auto space-y-4">
        {questions.map((q, qIdx) => (
          <div key={q.id} className="space-y-1.5">
            <div className="text-[13px] leading-relaxed text-ink">
              <span className="mr-1.5 font-mono text-[11px] text-muted">{qIdx + 1}.</span>
              {q.question}
              {q.multiSelect && (
                <span className="ml-1.5 text-[11px] text-muted">（多选，至少一个）</span>
              )}
            </div>
            <div className="space-y-1">
              {q.options.map((opt) => {
                const selected = q.multiSelect
                  ? getSelected(state, q.id).includes(opt.label)
                  : state[q.id] === opt.label;
                return (
                  <label
                    key={opt.label}
                    className={cn(
                      "flex cursor-pointer items-center gap-2 rounded-lg border px-3 py-2 text-[13px] transition-colors",
                      selected ? "border-ink/40 bg-subtle" : "border-rule/70 hover:bg-subtle/50",
                      busy && "pointer-events-none opacity-60",
                    )}
                  >
                    <input
                      type={q.multiSelect ? "checkbox" : "radio"}
                      name={`hitl-${q.id}`}
                      checked={selected}
                      onChange={() =>
                        q.multiSelect ? toggleMulti(q.id, opt.label) : setSingle(q.id, opt.label)
                      }
                      className="size-3.5 accent-ink"
                    />
                    <span className="flex-1 text-ink">{opt.label}</span>
                    {opt.recommended && (
                      <span className="shrink-0 rounded bg-ink/10 px-1.5 py-0.5 font-mono text-[10px] uppercase tracking-wider text-ink">
                        推荐
                      </span>
                    )}
                  </label>
                );
              })}
              {!q.multiSelect && (
                <label
                  className={cn(
                    "flex cursor-pointer items-center gap-2 rounded-lg border px-3 py-2 text-[13px] transition-colors",
                    state[q.id] === CUSTOM_VALUE
                      ? "border-ink/40 bg-subtle"
                      : "border-rule/70 hover:bg-subtle/50",
                    busy && "pointer-events-none opacity-60",
                  )}
                >
                  <input
                    type="radio"
                    name={`hitl-${q.id}`}
                    checked={state[q.id] === CUSTOM_VALUE}
                    onChange={() => setSingle(q.id, CUSTOM_VALUE)}
                    className="size-3.5 accent-ink"
                  />
                  {state[q.id] === CUSTOM_VALUE ? (
                    <input
                      type="text"
                      value={customs[q.id] ?? ""}
                      onChange={(e) =>
                        setCustoms((prev) => ({ ...prev, [q.id]: e.target.value.slice(0, 500) }))
                      }
                      placeholder="自定义答案…"
                      autoFocus
                      className="flex-1 bg-transparent text-[13px] outline-none placeholder:text-muted"
                    />
                  ) : (
                    <span className="text-muted">自定义答案…</span>
                  )}
                </label>
              )}
            </div>
          </div>
        ))}
      </div>

      <div className="flex items-center justify-end gap-2.5">
        <button
          type="button"
          disabled={busy}
          onClick={cancel}
          className={cn(
            "inline-flex h-8 items-center rounded-lg border border-rule bg-paper px-4 text-sm font-medium text-ink",
            "shadow-[0_1px_2px_rgba(20,30,50,0.06)] transition-colors hover:bg-subtle",
            busy && "pointer-events-none opacity-50",
          )}
        >
          暂不继续
        </button>
        <button
          type="button"
          disabled={busy || !allAnswered}
          onClick={submit}
          className={cn(
            "inline-flex h-8 items-center gap-1.5 rounded-lg bg-ink px-4 text-sm font-medium text-paper",
            "shadow-[0_1px_2px_rgba(20,30,50,0.12)] transition-opacity hover:opacity-90",
            (busy || !allAnswered) && "pointer-events-none opacity-50",
          )}
        >
          提交
        </button>
      </div>
    </div>
  );
}

function QuestionIcon() {
  return (
    <svg
      xmlns="http://www.w3.org/2000/svg"
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="2"
      strokeLinecap="round"
      strokeLinejoin="round"
      className="size-4 shrink-0 text-muted"
    >
      <circle cx="12" cy="12" r="10" />
      <path d="M9.09 9a3 3 0 0 1 5.83 1c0 2-3 3-3 3" />
      <line x1="12" y1="17" x2="12.01" y2="17" />
    </svg>
  );
}
