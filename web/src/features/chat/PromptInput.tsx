import {
  useEffect,
  useRef,
  useState,
  type ClipboardEvent as ReactClipboardEvent,
  type DragEvent as ReactDragEvent,
  type KeyboardEvent as ReactKeyboardEvent,
  type ReactNode,
} from "react";
import { cn } from "@/lib/utils";

// A card-style composer: unadorned textarea sits inside a rounded border
// that tracks focus, with a small toolbar row at the bottom. Behaviour
// notes:
//   - Enter sends, Shift+Enter inserts a newline (browser default).
//   - ⌘/Ctrl+Enter also sends — matches long-standing chat habits.
//   - IME composition is respected: while the user is composing a Chinese/
//     Japanese/Korean word, Enter picks a candidate rather than submitting.
//   - Clicking anywhere in the card that isn't an interactive control
//     focuses the textarea, widening the hit target.
//   - Textarea auto-grows with content up to a max height, then scrolls.
//   - Streaming mode: textarea stays enabled and submitting still works —
//     the caller queues it. The stop button appears alongside a secondary
//     send button so both actions stay reachable by mouse.
export function PromptInput({
  streaming,
  blocked = false,
  onSend,
  onCancel,
  leftActions,
  rightActions,
  topSlot,
  hasAttachments = false,
  onImageFiles,
}: {
  streaming: boolean;
  // Hard stop on submitting: a HITL interrupt is waiting for the user. The
  // dock covers this composer visually, but the textarea is still in the
  // DOM and may hold focus, and a POST right now would start a second run
  // beside the paused checkpoint. Draft text is kept — the user gets it
  // back once they answer.
  blocked?: boolean;
  // Called on submit regardless of `streaming`. It's the caller's job to
  // decide between sending now and queueing.
  onSend: (text: string) => void;
  onCancel: () => void;
  // Bottom-left toolbar cluster. Typically the attach `+` button.
  leftActions?: ReactNode;
  // Bottom-right toolbar cluster (sits between the hint area and the send
  // button). Typically the approval-mode dropdown.
  rightActions?: ReactNode;
  // Rendered inside the composer card, above the textarea. Used for the
  // attachment chip strip so it visually belongs to this composer, not a
  // separate hovering panel.
  topSlot?: ReactNode;
  // Lets the composer submit with an empty text field when the caller has
  // other content lined up (attachments, quoted text, etc.). Without this,
  // an attach-only message would be blocked by the empty-text guard.
  hasAttachments?: boolean;
  // Called when the user pastes into the textarea or drops files onto the
  // composer card AND at least one of them is an image (image/*). The
  // route decides what to do — typically save the bytes to disk via the
  // Electron IPC and drop them into the attachments store. Non-image
  // clipboard/drop content (plain text, non-image files) is left alone so
  // native paste/drop behaviour still works.
  onImageFiles?: (files: File[]) => void;
}) {
  const [text, setText] = useState("");
  const textareaRef = useRef<HTMLTextAreaElement>(null);
  const cardRef = useRef<HTMLDivElement>(null);

  // Auto-resize: reset to `auto` first so shrinking works, then match
  // scrollHeight. The min-height / max-height are enforced by CSS below.
  useEffect(() => {
    const el = textareaRef.current;
    if (!el) return;
    el.style.height = "auto";
    el.style.height = `${el.scrollHeight}px`;
  }, [text]);

  const canSend = (text.trim().length > 0 || hasAttachments) && !blocked;

  const submit = () => {
    const t = text.trim();
    if (!t && !hasAttachments) return;
    if (blocked) return;
    onSend(t);
    setText("");
    // Reset height explicitly — the effect will re-run on next render but
    // clearing here avoids a flash of the old height between paints.
    if (textareaRef.current) textareaRef.current.style.height = "auto";
  };

  const onKey = (e: ReactKeyboardEvent<HTMLTextAreaElement>) => {
    // While an IME is composing (e.g. picking a Chinese candidate) Enter
    // must select the candidate, not submit the message.
    if (e.nativeEvent.isComposing) return;

    const isEnter = e.key === "Enter";
    if (!isEnter) return;

    // ⌘/Ctrl+Enter submits regardless of Shift (habit compatibility).
    if (e.metaKey || e.ctrlKey) {
      e.preventDefault();
      submit();
      return;
    }
    // Plain Enter submits; Shift+Enter falls through as a newline.
    if (!e.shiftKey) {
      e.preventDefault();
      submit();
    }
  };

  // Click empty card area → focus textarea. Buttons and the textarea
  // itself keep their native behaviour.
  const onCardMouseDown = (e: React.MouseEvent) => {
    const target = e.target as HTMLElement;
    if (target.closest("button, textarea, input, a, [role='button']")) return;
    e.preventDefault();
    textareaRef.current?.focus();
  };

  // Paste handler on the textarea: pull image/* items out of the clipboard
  // and hand them to the caller. Anything else in the clipboard is left
  // untouched so pasting plain text still works normally. We only
  // preventDefault when we actually consume image bytes — else Chromium
  // would drop the user's normal text paste.
  const onPaste = (e: ReactClipboardEvent<HTMLTextAreaElement>) => {
    if (!onImageFiles) return;
    const items = e.clipboardData?.items;
    if (!items || items.length === 0) return;
    const images: File[] = [];
    for (const item of items) {
      if (item.kind !== "file") continue;
      if (!item.type.startsWith("image/")) continue;
      const file = item.getAsFile();
      if (file) images.push(file);
    }
    if (images.length === 0) return;
    e.preventDefault();
    onImageFiles(images);
  };

  // Drag-over on the composer card: signal "I'll take that" so the OS
  // shows the drop cursor. Without preventDefault, the browser's default
  // is to reject the drop. Cheap to always allow — the drop handler
  // filters to image/* before doing anything.
  const onDragOver = (e: ReactDragEvent<HTMLDivElement>) => {
    if (!onImageFiles) return;
    if (!e.dataTransfer?.types.includes("Files")) return;
    e.preventDefault();
    e.dataTransfer.dropEffect = "copy";
  };

  const onDrop = (e: ReactDragEvent<HTMLDivElement>) => {
    if (!onImageFiles) return;
    const files = Array.from(e.dataTransfer?.files ?? []);
    const images = files.filter((f) => f.type.startsWith("image/"));
    if (images.length === 0) return;
    e.preventDefault();
    onImageFiles(images);
  };

  return (
    <div className="px-6 pb-5 pt-3 bg-paper">
      <div className="max-w-3xl mx-auto">
        <div
          ref={cardRef}
          onMouseDown={onCardMouseDown}
          onDragOver={onDragOver}
          onDrop={onDrop}
          className={cn(
            "cursor-text rounded-xl border border-rule bg-paper transition-shadow",
            "shadow-[0_1px_2px_rgba(20,30,50,0.03)]",
            "hover:shadow-[0_4px_16px_rgba(20,30,50,0.06)]",
            "focus-within:border-accent",
            "focus-within:shadow-[0_0_0_3px_oklch(0.36_0.10_245/0.12)]",
          )}
        >
          {topSlot}
          <textarea
            ref={textareaRef}
            rows={1}
            value={text}
            onChange={(e) => setText(e.target.value)}
            onKeyDown={onKey}
            onPaste={onPaste}
            placeholder={
              blocked
                ? "先回答上面的问题"
                : streaming
                  ? "响应中 · 继续输入会排队"
                  : "写点什么"
            }
            className={cn(
              "block w-full resize-none bg-transparent px-5 pt-4 pb-2",
              "text-[15px] leading-7 text-ink",
              "placeholder:italic placeholder:text-muted",
              "focus:outline-none",
            )}
            style={{ minHeight: "44px", maxHeight: "260px" }}
          />

          <div className="flex items-center justify-between gap-3 px-3 pb-2.5 pt-1">
            <div className="flex items-center gap-2 pl-2 min-w-0">
              {leftActions ?? (
                <div className="font-mono text-[10px] uppercase tracking-[0.18em] text-muted">
                  {blocked
                    ? "等待你的回答"
                    : streaming
                      ? "Enter 加入队列 · Shift+Enter 换行"
                      : "Enter 发送 · Shift+Enter 换行"}
                </div>
              )}
            </div>

            <div className="flex items-center gap-2 shrink-0">
              {rightActions}
              {/* While streaming both actions stay available: queueing is
                  secondary (outlined) so stop keeps the filled emphasis. */}
              {streaming && (
                <button
                  type="button"
                  onClick={submit}
                  disabled={!canSend}
                  title="加入队列 (Enter)"
                  aria-label="加入队列"
                  className={cn(
                    "flex h-9 w-9 items-center justify-center rounded-lg",
                    "border transition-colors",
                    canSend
                      ? "border-rule bg-paper text-ink hover:bg-subtle cursor-pointer"
                      : "border-rule/60 bg-subtle text-muted cursor-not-allowed",
                  )}
                >
                  <ArrowUpIcon />
                </button>
              )}
              {streaming ? (
                <button
                  type="button"
                  onClick={onCancel}
                  title="停止 (Esc)"
                  aria-label="停止响应"
                  className={cn(
                    "flex h-9 w-9 items-center justify-center rounded-lg",
                    "bg-ink text-paper transition-opacity",
                    "hover:opacity-85 cursor-pointer",
                  )}
                >
                  <StopIcon />
                </button>
              ) : (
                <button
                  type="button"
                  onClick={submit}
                  disabled={!canSend}
                  title="发送 (Enter)"
                  aria-label="发送"
                  className={cn(
                    "flex h-9 w-9 items-center justify-center rounded-lg transition-colors",
                    canSend
                      ? "bg-accent text-paper hover:bg-accent-hover cursor-pointer"
                      : "bg-subtle text-muted cursor-not-allowed",
                  )}
                >
                  <ArrowUpIcon />
                </button>
              )}
            </div>
          </div>
        </div>
      </div>
    </div>
  );
}

// Two inline glyphs — we only need these two, so a full icon library is
// overkill. Paths borrowed from lucide (MIT).
function ArrowUpIcon() {
  return (
    <svg
      width="16"
      height="16"
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="2"
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden="true"
    >
      <path d="M12 19V5" />
      <path d="m5 12 7-7 7 7" />
    </svg>
  );
}

function StopIcon() {
  return (
    <svg
      width="12"
      height="12"
      viewBox="0 0 24 24"
      fill="currentColor"
      aria-hidden="true"
    >
      <rect x="5" y="5" width="14" height="14" rx="1.5" />
    </svg>
  );
}
