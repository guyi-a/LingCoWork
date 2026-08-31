import { create } from "zustand";
import { listPlans, type WorkPlan } from "@/lib/api";

export type PendingPlan = {
  interruptId: string;
  planId: string;
  callId: string;
};

interface PlanStore {
  plans: Record<string, WorkPlan | null>;
  history: Record<string, WorkPlan[]>;
  pending: Record<string, PendingPlan | undefined>;
  setPlan: (conversationID: string, plan: WorkPlan | null) => void;
  setPending: (conversationID: string, pending: PendingPlan) => void;
  clearPending: (conversationID: string) => void;
  clear: (conversationID: string) => void;
  load: (conversationID: string) => Promise<WorkPlan | null>;
}

export const usePlanStore = create<PlanStore>((set, get) => ({
  plans: {},
  history: {},
  pending: {},
  setPlan: (conversationID, plan) => {
    const history = get().history[conversationID] ?? [];
    const nextHistory = plan
      ? [...history.filter((item) => item.id !== plan.id), plan].sort(
          (a, b) => a.user_message_seq - b.user_message_seq,
        )
      : history;
    set({
      plans: { ...get().plans, [conversationID]: plan },
      history: { ...get().history, [conversationID]: nextHistory },
    });
  },
  setPending: (conversationID, pending) =>
    set({ pending: { ...get().pending, [conversationID]: pending } }),
  clearPending: (conversationID) => {
    const pending = { ...get().pending };
    delete pending[conversationID];
    set({ pending });
  },
  clear: (conversationID) => {
    const plans = { ...get().plans };
    const history = { ...get().history };
    const pending = { ...get().pending };
    delete plans[conversationID];
    delete history[conversationID];
    delete pending[conversationID];
    set({ plans, history, pending });
  },
  load: async (conversationID) => {
    const history = await listPlans(conversationID);
    const plan = history.at(-1) ?? null;
    set({
      plans: { ...get().plans, [conversationID]: plan },
      history: { ...get().history, [conversationID]: history },
    });
    return plan;
  },
}));
