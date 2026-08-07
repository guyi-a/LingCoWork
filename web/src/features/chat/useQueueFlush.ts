import { useEffect, useRef, useState } from "react";
import { useQueueStore, useConversationQueue, useQueuePaused } from "@/features/chat/queue-store";
import type { Instruction } from "@/lib/api";

// Drains the per-conversation message queue one item at a time, as soon as
// the conversation is genuinely idle.
//
// Only runs while the conversation route is mounted. Navigating away leaves
// the queue intact for when the user comes back — there is no background
// flusher, deliberately: the backend has no per-conversation serialisation,
// so a second flusher racing the foreground one has nothing to stop it.
export function useQueueFlush({
  conversationID,
  busy,
  hitlPending,
  error,
  dispatch,
}: {
  conversationID: string;
  // The conversation may have a run attached server-side. Wider than
  // `streaming` on purpose — see the route for what feeds it. Sending into a
  // busy conversation hits the backend's `IsStreaming` short-circuit, which
  // replays the running stream and discards the message without parsing it.
  busy: boolean;
  // A run paused on an approval or an ask_user. Reports as not busy — the SSE
  // buffer was finished at the interrupt — but POSTing now would start a
  // second run beside the paused checkpoint.
  hitlPending: boolean;
  // Last stream error from useChatStream. Non-null pauses the queue rather
  // than feeding the next message into a run that just failed.
  error: string | null;
  dispatch: (
    text: string,
    instruction?: Pick<Instruction, "name" | "label">,
  ) => Promise<boolean>;
}) {
  const queued = useConversationQueue(conversationID);
  const paused = useQueuePaused(conversationID);

  // Two guards, and both are load-bearing. `flushingRef` is synchronous, so
  // StrictMode's double-invoke of this effect can't dequeue twice within one
  // render. `flushing` is state so that clearing it re-renders and re-runs
  // the effect — without it nothing would wake up for the second item, since
  // streaming has already gone false by then.
  const flushingRef = useRef(false);
  const [flushing, setFlushing] = useState(false);

  useEffect(() => {
    if (!error) return;
    useQueueStore.getState().setPaused(conversationID, true);
  }, [error, conversationID]);

  // dispatch identity changes every render in practice; read it through a ref
  // so it can't retrigger the effect and double-send.
  const dispatchRef = useRef(dispatch);
  dispatchRef.current = dispatch;

  useEffect(() => {
    if (queued.length === 0) return;
    if (busy) return;
    if (hitlPending) return;
    if (paused) return;
    if (flushingRef.current || flushing) return;

    const next = useQueueStore.getState().dequeue(conversationID);
    if (!next) return;
    flushingRef.current = true;
    setFlushing(true);

    dispatchRef
      .current(next.text, next.instruction)
      .then((sent) => {
        if (!sent) throw new Error("queued message was not sent");
      })
      .catch((err) => {
        console.error("[queue] flush failed:", err);
        useQueueStore.getState().restoreFront(conversationID, next);
        useQueueStore.getState().setPaused(conversationID, true);
      })
      .finally(() => {
        flushingRef.current = false;
        setFlushing(false);
      });
  }, [queued.length, busy, hitlPending, paused, flushing, conversationID]);
}
