import { NavLink, useNavigate, useParams } from "react-router";
import { useConversationStore } from "@/stores/conversations";
import { cn, formatRelative } from "@/lib/utils";
import type { ConversationItem as ConvItem } from "@/lib/api";

export function ConversationItem({
  item,
  indent = false,
}: {
  item: ConvItem;
  indent?: boolean;
}) {
  const remove = useConversationStore((s) => s.remove);
  const navigate = useNavigate();
  const { id: activeId } = useParams();

  const onDelete = async (e: React.MouseEvent) => {
    e.preventDefault();
    e.stopPropagation();
    await remove(item.id);
    if (activeId === item.id) {
      navigate("/");
    }
  };

  return (
    <li>
      <NavLink
        to={`/c/${item.id}`}
        className={({ isActive }) =>
          cn(
            "group block rounded-lg transition-colors",
            indent ? "h-6 px-2.5" : "px-2.5 py-1.5",
            isActive
              ? "bg-rule/60 text-ink font-normal"
              : "text-muted hover:bg-rule/70 hover:text-ink",
          )
        }
      >
        <div className="flex h-full min-h-5 items-center gap-2">
          {indent ? <IndentSpacer /> : <MessageIcon />}
          <div className="flex-1 min-w-0">
            <div
              className={cn(
                "truncate leading-5 flex items-center gap-1.5",
                indent ? "text-xs" : "text-[13px]",
              )}
            >
              <span className="truncate">{item.title || "（未命名）"}</span>
              {item.agent_status === "waiting_approval" && (
                <span
                  className="shrink-0 rounded border border-rule bg-paper px-1.5 py-0.5 text-[10px] font-medium text-ink shadow-[0_1px_1px_rgba(20,30,50,0.04)]"
                  title="等待审批"
                >
                  等待审批
                </span>
              )}
              {item.agent_status === "waiting_plan" && (
                <span
                  className="shrink-0 rounded border border-rule bg-paper px-1.5 py-0.5 text-[10px] font-medium text-ink shadow-[0_1px_1px_rgba(20,30,50,0.04)]"
                  title="等待确认计划"
                >
                  等待计划
                </span>
              )}
            </div>
            {!indent && (
              <div className="font-mono text-[10px] tracking-wider uppercase text-muted/75 mt-0.5">
                {formatRelative(item.updated_at)}
              </div>
            )}
          </div>
          <button
            type="button"
            onClick={onDelete}
            aria-label="删除会话"
            className="opacity-0 group-hover:opacity-100 text-muted hover:text-ink text-xs px-1 cursor-pointer transition-opacity shrink-0"
          >
            ✕
          </button>
        </div>
      </NavLink>
    </li>
  );
}

function IndentSpacer() {
  return <span className="size-[23px] shrink-0" aria-hidden />;
}

function MessageIcon() {
  return (
    <svg
      width="13"
      height="13"
      viewBox="0 0 16 16"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.4"
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden
      className="shrink-0 text-muted/85"
    >
      <path d="M2.5 4A1.5 1.5 0 0 1 4 2.5h8A1.5 1.5 0 0 1 13.5 4v5A1.5 1.5 0 0 1 12 10.5H6.5L3.5 13v-2.5H4A1.5 1.5 0 0 1 2.5 9V4Z" />
    </svg>
  );
}
