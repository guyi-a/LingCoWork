import { create } from "zustand";
import {
  postQuestionAnswer,
  type InterruptDecisionResult,
  type QuestionAnswerPayload,
} from "@/lib/api";

// PendingQuestion —— 一次 ask_user 中断，等着用户回答。跟 PendingApproval 平行，
// 由 SSE question_required frame / GET pending 里 kind=question 的项填充。
export type PendingQuestion = {
  interruptId: string;
  callId: string;
  // questions_json 保留原始 JSON 字符串。渲染时按需 parseQuestionsJson。
  // 存原文能避免多次序列化 / 反序列化的信息损失。
  questionsJson: string;
};

interface QuestionStore {
  // 按 conversation id 分区，切换会话时互不污染。
  pending: Record<string, PendingQuestion[]>;
  add: (convId: string, item: PendingQuestion) => void;
  drop: (convId: string, interruptId: string) => void;
  clear: (convId: string) => void;
  answer: (
    convId: string,
    interruptId: string,
    payload: QuestionAnswerPayload,
  ) => Promise<InterruptDecisionResult>;
}

export const useQuestionStore = create<QuestionStore>((set, get) => ({
  pending: {},

  add: (convId, item) => {
    const current = get().pending[convId] ?? [];
    // 按 interruptId 去重：SSE 断线重连 / restore 都可能重发同一条。
    if (current.some((p) => p.interruptId === item.interruptId)) return;
    set({ pending: { ...get().pending, [convId]: [...current, item] } });
  },

  drop: (convId, interruptId) => {
    const current = get().pending[convId] ?? [];
    const next = current.filter((p) => p.interruptId !== interruptId);
    const map = { ...get().pending };
    if (next.length === 0) delete map[convId];
    else map[convId] = next;
    set({ pending: map });
  },

  clear: (convId) => {
    const map = { ...get().pending };
    delete map[convId];
    set({ pending: map });
  },

  answer: async (convId, interruptId, payload) => {
    const result = await postQuestionAnswer(convId, interruptId, payload);
    get().drop(convId, interruptId);
    return result;
  },
}));
