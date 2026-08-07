import { cn } from "@/lib/utils";
import { parseAttachmentMarkers } from "@/features/chat/attachments-store";
import {
  useQueueStore,
  useConversationQueue,
  useQueuePaused,
} from "@/features/chat/queue-store";

// Sits directly above the composer and shows what will be sent once the
// current run finishes. Doubles as the only affordance for un-pausing the
// queue after a stop — without it a queued message would sit there with no
// way to release it.
export function QueuedMessagesBar({
  conversationID,
}: {
  conversationID: string;
}) {
  const queued = useConversationQueue(conversationID);
  const paused = useQueuePaused(conversationID);
  const remove = useQueueStore((s) => s.remove);
  const setPaused = useQueueStore((s) => s.setPaused);

  // Nothing queued means no wrapper either — an empty shell would still eat
  // the composer's top padding.
  if (queued.length === 0) return null;

  return (
    <div className="bg-paper px-6 pt-3">
      <div
        className={cn(
          "mx-auto max-w-3xl rounded-xl border border-rule bg-subtle/40 px-4 py-2.5",
          "space-y-1.5",
        )}
      >
        <div className="flex items-center justify-between gap-2">
          <div className="flex items-center gap-1.5 font-mono text-[10px] uppercase tracking-[0.18em] text-muted">
            <ClockIcon />
            <span className="tabular-nums">排队中 · {queued.length}</span>
            {!paused && <span className="opacity-70">本轮结束后自动发送</span>}
          </div>
          {paused && (
            <button
              type="button"
              onClick={() => setPaused(conversationID, false)}
              className={cn(
                "inline-flex h-7 shrink-0 items-center gap-1.5 rounded-lg",
                "border border-rule bg-paper px-3 text-[13px] font-medium text-ink",
                "transition-colors hover:bg-subtle",
              )}
            >
              <PlayIcon />
              继续发送 ({queued.length})
            </button>
          )}
        </div>

        <ul className="space-y-1">
          {queued.map((item) => {
            // Queue items hold the on-wire text, attachment markers and all.
            // Show the prose and reduce the markers to a count, same as the
            // transcript does for sent messages.
            const { attachments, text } = parseAttachmentMarkers(item.text);
            return (
              <li key={item.id} className="flex items-center gap-2">
                {item.instruction && (
                  <span className="shrink-0 rounded bg-accent/10 px-1.5 py-0.5 text-[10px] text-accent">
                    {item.instruction.label}
                  </span>
                )}
                {attachments.length > 0 && (
                  <span className="shrink-0 rounded bg-subtle px-1.5 py-0.5 font-mono text-[10px] text-muted tabular-nums">
                    {attachments.length} 个附件
                  </span>
                )}
                <span className="min-w-0 flex-1 truncate text-[13px] leading-6 text-ink">
                  {text || attachments[0]?.name || "（空消息）"}
                </span>
                <button
                  type="button"
                  onClick={() => remove(conversationID, item.id)}
                  title="从队列移除"
                  aria-label="从队列移除"
                  className={cn(
                    "inline-flex size-5 shrink-0 items-center justify-center rounded",
                    "text-muted opacity-60 transition-colors",
                    "hover:bg-subtle hover:text-ink hover:opacity-100",
                  )}
                >
                  <XIcon />
                </button>
              </li>
            );
          })}
        </ul>
      </div>
    </div>
  );
}

function ClockIcon() {
  return (
    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2"
      strokeLinecap="round" strokeLinejoin="round"
      className="size-3" aria-hidden>
      <circle cx="12" cy="12" r="9" />
      <path d="M12 7v5l3 2" />
    </svg>
  );
}

function PlayIcon() {
  return (
    <svg viewBox="0 0 24 24" fill="currentColor" className="size-3" aria-hidden>
      <path d="M8 5.5v13l11-6.5z" />
    </svg>
  );
}

function XIcon() {
  return (
    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2"
      strokeLinecap="round" strokeLinejoin="round"
      className="size-3" aria-hidden>
      <path d="M18 6 6 18M6 6l12 12" />
    </svg>
  );
}
