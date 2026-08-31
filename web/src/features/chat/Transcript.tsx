import {
  Fragment,
  useEffect,
  useMemo,
  useRef,
  type ReactNode,
} from "react";
import { TranscriptEntry } from "./TranscriptEntry";
import { PlanHistoryCard } from "./PlanReviewCard";
import type { ChatTurn, SubAgentEvent } from "@/hooks/useChatStream";
import type { WorkPlan } from "@/lib/api";

export function Transcript({
  turns,
  streaming,
  contextLimit,
  trailing,
  plans = [],
  pendingPlanID,
}: {
  turns: ChatTurn[];
  streaming: boolean;
  contextLimit: number;
  trailing?: ReactNode;
  plans?: WorkPlan[];
  pendingPlanID?: string;
}) {
  const endRef = useRef<HTMLDivElement | null>(null);

  useEffect(() => {
    endRef.current?.scrollIntoView({ behavior: "smooth", block: "end" });
  }, [turns, streaming, trailing]);

  // Flatten every turn's subEvents into one array so an approve/resume flow
  // that splits a sub-agent's tool_call and tool_result across two persisted
  // assistant rows still renders the child's status correctly. Each entry
  // still carries its own parentToolCallId; TranscriptEntry filters against
  // the tool it's rendering. Memoised on the turns array reference so a
  // pure streaming update doesn't rebuild it every render.
  const allSubEvents = useMemo<SubAgentEvent[]>(
    () => turns.flatMap((t) => t.subEvents),
    [turns],
  );
  // Set of every tool_call id that lives on some assistant turn. Used
  // by TranscriptEntry to decide whether a subEvent's parent still exists
  // somewhere in the transcript — if yes, it's owned by that tool and
  // shouldn't also render as an orphan under a different turn.
  const ownedToolIds = useMemo<Set<string>>(
    () => new Set(turns.flatMap((t) => t.tools.map((tc) => tc.id))),
    [turns],
  );
  const plansAfterTurn = useMemo(() => {
    const byIndex = new Map<number, WorkPlan[]>();
    for (const plan of plans) {
      if (plan.id === pendingPlanID) continue;
      const userIndex = turns.findIndex(
        (turn) => turn.role === "user" && turn.seq === plan.user_message_seq,
      );
      if (userIndex < 0) continue;
      let endIndex = turns.length - 1;
      for (let i = userIndex + 1; i < turns.length; i++) {
        if (turns[i].role === "user") {
          endIndex = i - 1;
          break;
        }
      }
      const current = byIndex.get(endIndex) ?? [];
      current.push(plan);
      byIndex.set(endIndex, current);
    }
    return byIndex;
  }, [pendingPlanID, plans, turns]);

  if (turns.length === 0) {
    return (
      <div className="flex-1 flex items-center justify-center">
        <p className="text-sm text-muted">
          开始一次对话——下方输入你的第一个问题。
        </p>
      </div>
    );
  }

  return (
    <div className="flex-1 overflow-y-auto scrollbar-subtle">
      <div className="max-w-3xl mx-auto px-8 py-10">
        {turns.map((t, i) => (
          <Fragment key={t.id}>
            <TranscriptEntry
              turn={t}
              allSubEvents={allSubEvents}
              ownedToolIds={ownedToolIds}
              showRule={i > 0}
              streaming={
                streaming && i === turns.length - 1 && t.role === "assistant"
              }
              contextLimit={contextLimit}
            />
            {plansAfterTurn.get(i)?.map((plan) => (
              <PlanHistoryCard key={plan.id} plan={plan} />
            ))}
          </Fragment>
        ))}
        {trailing}
        <div ref={endRef} />
      </div>
    </div>
  );
}
