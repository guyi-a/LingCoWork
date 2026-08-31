import {
  Fragment,
  useEffect,
  useMemo,
  useRef,
  useState,
  type ReactNode,
} from "react";
import { TranscriptEntry } from "./TranscriptEntry";
import { PlanHistoryCard } from "./PlanReviewCard";
import type { ChatTurn, SubAgentEvent } from "@/hooks/useChatStream";
import type { WorkPlan } from "@/lib/api";

function ArrowDownIcon({ className }: { className?: string }) {
  return (
    <svg
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="2"
      strokeLinecap="round"
      strokeLinejoin="round"
      className={className}
      aria-hidden="true"
    >
      <path d="M12 5v14" />
      <path d="m19 12-7 7-7-7" />
    </svg>
  );
}

export function Transcript({
  turns,
  streaming,
  contextLimit,
  trailing,
  plans = [],
  pendingPlanID,
  onRevealFinished,
}: {
  turns: ChatTurn[];
  streaming: boolean;
  contextLimit: number;
  trailing?: ReactNode;
  plans?: WorkPlan[];
  pendingPlanID?: string;
  onRevealFinished?: () => void;
}) {
  const endRef = useRef<HTMLDivElement | null>(null);
  const containerRef = useRef<HTMLDivElement | null>(null);
  const contentRef = useRef<HTMLDivElement | null>(null);
  // Auto-scroll follows new content only while the view is already near the
  // bottom. Scrolling up to read flips this off and surfaces the "回到底部"
  // button (see klingwork-app for the same pattern).
  const stickToBottom = useRef(true);
  const [showJump, setShowJump] = useState(false);
  // The effects below attach to the scroll container, which only exists once
  // there is content to scroll (the empty state renders a different tree).
  // Depending on this flag (instead of []) makes the listener attach when the
  // conversation first gains a message — with [] they'd run at mount against a
  // null container and never again.
  const hasContent = turns.length > 0;

  // Keep the view glued to the bottom while the conversation grows, but never
  // fight a user who has scrolled up to read. A ResizeObserver on the content
  // catches every height change — the streaming reveal, tool cards appearing,
  // and the final tools-collapse transition — so the viewport re-pins instead
  // of jumping. The old smooth scrollIntoView is gone because it fought the
  // typewriter's instant scroll on every SSE frame, which is what made the end
  // of each message visibly jerk.
  useEffect(() => {
    const container = containerRef.current;
    const content = contentRef.current;
    if (!container || !content) return;
    const follow = () => {
      if (stickToBottom.current) {
        container.scrollTop = container.scrollHeight;
      }
    };
    const ro = new ResizeObserver(follow);
    ro.observe(content);
    // Initial jump to the bottom when the conversation loads.
    container.scrollTop = container.scrollHeight;
    return () => ro.disconnect();
  }, [hasContent]);

  // Track whether the user has scrolled up so we can show "回到底部" and stop
  // chasing new content.
  useEffect(() => {
    const container = containerRef.current;
    if (!container) return;
    const onScroll = () => {
      const nearBottom =
        container.scrollHeight - container.scrollTop - container.clientHeight <
        80;
      stickToBottom.current = nearBottom;
      setShowJump(!nearBottom);
    };
    container.addEventListener("scroll", onScroll, { passive: true });
    return () => container.removeEventListener("scroll", onScroll);
  }, [hasContent]);

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
    <div className="relative flex-1 min-h-0">
      <div
        className="h-full overflow-y-auto scrollbar-subtle"
        ref={containerRef}
      >
        <div className="max-w-3xl mx-auto px-8 py-10" ref={contentRef}>
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
                onRevealFinished={onRevealFinished}
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
      {showJump ? (
        <button
          type="button"
          onClick={() => {
            const el = containerRef.current;
            if (!el) return;
            stickToBottom.current = true;
            setShowJump(false);
            el.scrollTo({ top: el.scrollHeight, behavior: "smooth" });
          }}
          className="absolute bottom-4 left-1/2 z-10 flex -translate-x-1/2 items-center gap-1.5 rounded-full border border-rule bg-paper px-3 py-1.5 text-xs text-muted shadow-lg hover:text-ink"
        >
          <ArrowDownIcon className="size-3.5" />
          回到底部
        </button>
      ) : null}
    </div>
  );
}
