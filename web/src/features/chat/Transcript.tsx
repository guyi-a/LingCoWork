import { useEffect, useMemo, useRef } from "react";
import { TranscriptEntry } from "./TranscriptEntry";
import type { ChatTurn, SubAgentEvent } from "@/hooks/useChatStream";

export function Transcript({
  turns,
  streaming,
  contextLimit,
}: {
  turns: ChatTurn[];
  streaming: boolean;
  contextLimit: number;
}) {
  const endRef = useRef<HTMLDivElement | null>(null);

  useEffect(() => {
    endRef.current?.scrollIntoView({ behavior: "smooth", block: "end" });
  }, [turns, streaming]);

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
          <TranscriptEntry
            key={t.id}
            turn={t}
            allSubEvents={allSubEvents}
            ownedToolIds={ownedToolIds}
            showRule={i > 0}
            streaming={streaming && i === turns.length - 1 && t.role === "assistant"}
            contextLimit={contextLimit}
          />
        ))}
        <div ref={endRef} />
      </div>
    </div>
  );
}
