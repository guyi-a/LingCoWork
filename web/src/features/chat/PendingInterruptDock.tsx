import { useApprovalStore, type PendingApproval } from "@/features/chat/approval-store";
import { useQuestionStore, type PendingQuestion } from "@/features/chat/question-store";
import { ApprovalBar } from "@/features/chat/ApprovalBar";
import { QuestionCard } from "@/features/chat/QuestionCard";

const EMPTY_APPROVALS: PendingApproval[] = [];
const EMPTY_QUESTIONS: PendingQuestion[] = [];

// PendingInterruptDock 是"底部弹卡"的唯一挂载点。任何 HITL 中断（审批 / 提问 /
// 未来可能加的新种类）都汇集到这里，dock 统一提供外壳，内部按 store 里
// 队首的类型选一个卡片渲染。
//
// 展示规则：有 approval 优先展示，其次 question。跟"破坏性操作应先审批"的
// 语义匹配；两个 store 目前没有跨 store 时间线，未来需要严格 FIFO 再补时间戳。
export function PendingInterruptDock({
  conversationID,
  onApprovalDecision,
  onQuestionDecision,
  onResume,
}: {
  conversationID: string;
  onApprovalDecision?: (
    item: PendingApproval,
    decision: "approve" | "deny",
    resumed: boolean,
  ) => Promise<void> | void;
  onQuestionDecision?: (
    callId: string,
    cancelled: boolean,
    resumed: boolean,
  ) => Promise<void> | void;
  onResume?: () => Promise<void> | void;
}) {
  const approvals = useApprovalStore(
    (s) => s.pending[conversationID] ?? EMPTY_APPROVALS,
  );
  const questions = useQuestionStore(
    (s) => s.pending[conversationID] ?? EMPTY_QUESTIONS,
  );

  if (approvals.length === 0 && questions.length === 0) return null;

  return (
    <div className="absolute inset-0 z-10 flex items-end bg-paper px-6 pb-3 pt-2">
      <div className="mx-auto w-full max-w-3xl">
        {approvals.length > 0 ? (
          <ApprovalBar
            conversationID={conversationID}
            onDecision={onApprovalDecision}
            onResume={onResume}
          />
        ) : (
          <QuestionCard
            conversationID={conversationID}
            interruptId={questions[0].interruptId}
            callId={questions[0].callId}
            questionsJson={questions[0].questionsJson}
            onDecision={onQuestionDecision}
            onResume={onResume}
          />
        )}
      </div>
    </div>
  );
}
