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
import {
  listInstructions,
  type Instruction,
} from "@/lib/api";
import {
  InstructionIcon,
  InstructionPicker,
} from "./InstructionPicker";
import { ContextPicker } from "./ContextPicker";
import {
  findContextMention,
  replaceContextMention,
  workspaceEntryToAttachment,
} from "./context-mention";
import { useAttachmentsStore } from "./attachments-store";

const PICKER_NAVIGATION_KEYS = new Set(["ArrowUp", "ArrowDown", "Enter"]);

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
  context,
  placeholder,
  blockedHint,
}: {
  streaming: boolean;
  // Hard stop on submitting. Used for HITL interrupts and preconditions such
  // as choosing a workspace. Draft text remains editable and is preserved.
  blocked?: boolean;
  // Called on submit regardless of `streaming`. It's the caller's job to
  // decide between sending now and queueing.
  onSend: (text: string, instruction?: Instruction) => void;
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
  context?: {
    conversationId: string;
    projectId: string;
    workspace: string;
  };
  // Lets non-HITL callers explain why submission is blocked without reusing
  // the approval-specific copy.
  placeholder?: string;
  blockedHint?: string;
}) {
  const [text, setText] = useState("");
  const [instructions, setInstructions] = useState<Instruction[]>([]);
  const [instructionsLoading, setInstructionsLoading] = useState(false);
  const [selectedInstruction, setSelectedInstruction] = useState<Instruction>();
  const [manualPickerOpen, setManualPickerOpen] = useState(false);
  const [caret, setCaret] = useState(0);
  const [composing, setComposing] = useState(false);
  const addAttachments = useAttachmentsStore((state) => state.add);
  const textareaRef = useRef<HTMLTextAreaElement>(null);
  const cardRef = useRef<HTMLDivElement>(null);
  const pickerCommandRef = useRef<HTMLDivElement>(null);

  const contextMention =
    context && !composing ? findContextMention(text, caret) : null;
  const contextPickerOpen = contextMention !== null;
  const slashQuery = contextPickerOpen ? undefined : /^\/(\S*)$/.exec(text)?.[1];
  const slashPickerOpen = slashQuery !== undefined;
  const instructionPickerOpen =
    !contextPickerOpen && (slashPickerOpen || manualPickerOpen);
  const filteredInstructions = slashPickerOpen
    ? instructions.filter((instruction) => {
        const query = slashQuery.toLowerCase();
        return (
          instruction.name.toLowerCase().includes(query) ||
          instruction.label.toLowerCase().includes(query) ||
          instruction.description.toLowerCase().includes(query)
        );
      })
    : instructions;

  useEffect(() => {
    let active = true;
    setInstructionsLoading(true);
    void listInstructions()
      .then((items) => {
        if (active) setInstructions(items);
      })
      .catch((err) => {
        console.error("[instructions] list failed:", err);
      })
      .finally(() => {
        if (active) setInstructionsLoading(false);
      });
    return () => {
      active = false;
    };
  }, []);

  useEffect(() => {
    if (!manualPickerOpen) return;
    const onPointerDown = (event: MouseEvent) => {
      if (!cardRef.current?.contains(event.target as Node)) {
        setManualPickerOpen(false);
      }
    };
    document.addEventListener("mousedown", onPointerDown);
    return () => document.removeEventListener("mousedown", onPointerDown);
  }, [manualPickerOpen]);

  // Auto-resize: reset to `auto` first so shrinking works, then match
  // scrollHeight. The min-height / max-height are enforced by CSS below.
  useEffect(() => {
    const el = textareaRef.current;
    if (!el) return;
    el.style.height = "auto";
    el.style.height = `${el.scrollHeight}px`;
  }, [text]);

  const canSend =
    (text.trim().length > 0 || hasAttachments || !!selectedInstruction) &&
    !blocked;

  const submit = () => {
    const t = text.trim();
    if (!t && !hasAttachments && !selectedInstruction) return;
    if (blocked) return;
    onSend(t, selectedInstruction);
    setText("");
    setCaret(0);
    setSelectedInstruction(undefined);
    setManualPickerOpen(false);
    // Reset height explicitly — the effect will re-run on next render but
    // clearing here avoids a flash of the old height between paints.
    if (textareaRef.current) textareaRef.current.style.height = "auto";
  };

  const onKey = (e: ReactKeyboardEvent<HTMLTextAreaElement>) => {
    // While an IME is composing (e.g. picking a Chinese candidate) Enter
    // must select the candidate, not submit the message.
    if (e.nativeEvent.isComposing) return;

    if (
      e.key === "Backspace" &&
      text.length === 0 &&
      selectedInstruction
    ) {
      e.preventDefault();
      setSelectedInstruction(undefined);
      return;
    }
    if (contextMention && e.key === "Escape") {
      e.preventDefault();
      const next = replaceContextMention(text, contextMention);
      setText(next.text);
      setCaret(next.caret);
      requestAnimationFrame(() => {
        textareaRef.current?.setSelectionRange(next.caret, next.caret);
      });
      return;
    }
    if (instructionPickerOpen && e.key === "Escape") {
      e.preventDefault();
      if (slashPickerOpen) setText("");
      setManualPickerOpen(false);
      return;
    }
    if (
      (contextPickerOpen ||
        (slashPickerOpen && filteredInstructions.length > 0)) &&
      PICKER_NAVIGATION_KEYS.has(e.key)
    ) {
      e.preventDefault();
      pickerCommandRef.current?.dispatchEvent(
        new KeyboardEvent("keydown", {
          key: e.key,
          bubbles: true,
          cancelable: true,
        }),
      );
      return;
    }

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
    // cmdk renders selectable rows as role="option", not buttons. Treat the
    // picker as interactive too; preventing its mousedown would focus the
    // textarea and cancel the row's click before onSelect can run.
    if (
      target.closest(
        "button, textarea, input, a, [role='button'], [role='option'], [cmdk-item]",
      )
    ) {
      return;
    }
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
            "relative cursor-text rounded-xl border border-rule bg-paper transition-shadow",
            "shadow-[0_1px_2px_rgba(20,30,50,0.03)]",
            "hover:shadow-[0_4px_16px_rgba(20,30,50,0.06)]",
            "focus-within:border-accent",
            "focus-within:shadow-[0_0_0_3px_oklch(0.36_0.10_245/0.12)]",
          )}
        >
          {contextMention && context && (
            <ContextPicker
              commandRef={pickerCommandRef}
              conversationId={context.conversationId}
              projectId={context.projectId}
              query={contextMention.query}
              onSelect={(entry) => {
                addAttachments(context.conversationId, [
                  workspaceEntryToAttachment(context.workspace, entry),
                ]);
                const next = replaceContextMention(text, contextMention);
                setText(next.text);
                setCaret(next.caret);
                requestAnimationFrame(() => {
                  textareaRef.current?.focus();
                  textareaRef.current?.setSelectionRange(next.caret, next.caret);
                });
              }}
            />
          )}
          {instructionPickerOpen && (
            <InstructionPicker
              commandRef={pickerCommandRef}
              instructions={filteredInstructions}
              loading={instructionsLoading}
              onClose={() => setManualPickerOpen(false)}
              onSelect={(instruction) => {
                setSelectedInstruction(instruction);
                setManualPickerOpen(false);
                if (slashPickerOpen) {
                  setText("");
                  setCaret(0);
                }
                requestAnimationFrame(() => textareaRef.current?.focus());
              }}
              showSearchInput={!slashPickerOpen}
            />
          )}
          {topSlot}
          {selectedInstruction && (
            <div className="px-3 pt-3">
              <div className="flex w-fit max-w-full items-center gap-1.5 rounded-full border border-rule bg-subtle/60 px-2.5 py-1 text-xs text-ink">
                <InstructionIcon className="size-3.5 shrink-0 text-accent" />
                <span className="truncate">{selectedInstruction.label}</span>
                <button
                  type="button"
                  className="ml-0.5 inline-flex size-4 items-center justify-center rounded-full text-muted hover:bg-rule hover:text-ink"
                  title="取消快捷指令"
                  aria-label="取消快捷指令"
                  onClick={() => setSelectedInstruction(undefined)}
                >
                  <XIcon />
                </button>
              </div>
            </div>
          )}
          <textarea
            ref={textareaRef}
            rows={1}
            value={text}
            onChange={(e) => {
              setText(e.target.value);
              setCaret(e.target.selectionStart);
            }}
            onSelect={(e) => setCaret(e.currentTarget.selectionStart)}
            onClick={(e) => setCaret(e.currentTarget.selectionStart)}
            onKeyUp={(e) => setCaret(e.currentTarget.selectionStart)}
            onCompositionStart={() => setComposing(true)}
            onCompositionEnd={(e) => {
              setComposing(false);
              setCaret(e.currentTarget.selectionStart);
            }}
            onFocus={() => {
              if (manualPickerOpen) setManualPickerOpen(false);
            }}
            onKeyDown={onKey}
            onPaste={onPaste}
            aria-expanded={contextPickerOpen || slashPickerOpen}
            aria-controls={
              contextPickerOpen
                ? "context-picker"
                : slashPickerOpen
                  ? "instruction-picker"
                  : undefined
            }
            placeholder={
              placeholder ??
              (blocked
                ? "先回答上面的问题"
                : streaming
                  ? "响应中 · 继续输入会排队"
                  : "写点什么 · 输入 @ 添加上下文")
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
                    ? (blockedHint ?? "等待你的回答")
                    : streaming
                      ? "Enter 加入队列 · Shift+Enter 换行"
                      : "Enter 发送 · Shift+Enter 换行"}
                </div>
              )}
              <button
                type="button"
                onClick={() => {
                  if (contextMention) {
                    const next = replaceContextMention(text, contextMention);
                    setText(next.text);
                    setCaret(next.caret);
                  }
                  setManualPickerOpen((open) => !open);
                }}
                title="选择快捷指令"
                aria-label="选择快捷指令"
                aria-expanded={manualPickerOpen}
                aria-controls={manualPickerOpen ? "instruction-picker" : undefined}
                aria-haspopup="listbox"
                className={cn(
                  "inline-flex h-7 items-center gap-1.5 rounded-md border border-rule/60 bg-paper px-2 text-[12px] transition-colors",
                  selectedInstruction
                    ? "text-accent"
                    : "text-muted hover:bg-subtle hover:text-ink",
                )}
              >
                <InstructionIcon className="size-3.5" />
                <span>快捷指令</span>
              </button>
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

function XIcon() {
  return (
    <svg
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="2"
      strokeLinecap="round"
      className="size-3"
      aria-hidden
    >
      <path d="M18 6 6 18M6 6l12 12" />
    </svg>
  );
}
