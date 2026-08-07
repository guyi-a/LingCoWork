import type { Ref } from "react";
import type { Instruction } from "@/lib/api";
import {
  Command,
  CommandEmpty,
  CommandInput,
  CommandItem,
  CommandList,
} from "@/components/ui/command";

export function InstructionPicker({
  instructions,
  loading,
  onSelect,
  onClose,
  commandRef,
  showSearchInput,
}: {
  instructions: Instruction[];
  loading: boolean;
  onSelect: (instruction: Instruction) => void;
  onClose: () => void;
  commandRef?: Ref<HTMLDivElement>;
  showSearchInput: boolean;
}) {
  return (
    <div className="absolute bottom-full left-0 z-30 mb-2 w-full max-w-md overflow-hidden rounded-xl border border-rule bg-paper shadow-[0_12px_36px_oklch(0_0_0/0.14)]">
      <Command
        id="instruction-picker"
        loop
        ref={commandRef}
        shouldFilter={showSearchInput}
      >
        {showSearchInput && (
          <CommandInput
            autoFocus
            placeholder="搜索快捷指令…"
            onKeyDown={(event) => {
              if (event.key === "Escape") {
                event.preventDefault();
                onClose();
              }
            }}
          />
        )}
        <CommandList>
          <CommandEmpty>
            {loading ? "正在加载…" : "没有匹配的快捷指令"}
          </CommandEmpty>
          {instructions.map((instruction) => (
            <CommandItem
              key={instruction.name}
              value={`${instruction.name} ${instruction.label} ${instruction.description}`}
              onSelect={() => onSelect(instruction)}
            >
              <InstructionIcon className="size-4 shrink-0 text-accent" />
              <div className="min-w-0 flex-1">
                <div className="truncate text-[13px] font-medium text-ink">
                  {instruction.label}
                </div>
                <div className="truncate text-[11px] text-muted">
                  {instruction.description || instruction.name}
                </div>
              </div>
            </CommandItem>
          ))}
        </CommandList>
      </Command>
    </div>
  );
}

export function InstructionIcon({ className = "size-4" }: { className?: string }) {
  return (
    <svg
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.8"
      strokeLinecap="round"
      strokeLinejoin="round"
      className={className}
      aria-hidden
    >
      <path d="M6 3h9l3 3v15H6z" />
      <path d="M14 3v4h4M9 11h6M9 15h6" />
    </svg>
  );
}
