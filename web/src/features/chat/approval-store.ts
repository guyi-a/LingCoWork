import { create } from "zustand";
import { postApproval, type InterruptDecisionResult } from "@/lib/api";

// One paused tool call awaiting the user. Displayed by ApprovalBar as a card
// with tool name + arg summary + [reject] [allow] buttons.
export type PendingApproval = {
  interruptId: string;
  callId: string;
  tool: string;
  argsJson: string;
  // Serialised backend effect. Optional: absent on approvals that were
  // checkpointed before the field existed, and the card falls back to
  // summarising argsJson for those.
  effectJson?: string;
};

interface ApprovalStore {
  // Keyed by conversation id so switching between conversations doesn't
  // spill approvals from one into another's bar.
  pending: Record<string, PendingApproval[]>;
  add: (convId: string, item: PendingApproval) => void;
  drop: (convId: string, interruptId: string) => void;
  clear: (convId: string) => void;
  // decide POSTs the user's answer and, on success, drops the item locally.
  // Errors surface via console; the caller can keep the item and let the user
  // retry.
  decide: (
    convId: string,
    interruptId: string,
    decision: "approve" | "deny",
    reason?: string,
  ) => Promise<InterruptDecisionResult>;
}

export const useApprovalStore = create<ApprovalStore>((set, get) => ({
  pending: {},

  add: (convId, item) => {
    const current = get().pending[convId] ?? [];
    // Dedupe by interruptId — the backend can re-emit an approval_required
    // frame on stream reconnect, and we don't want a duplicate card.
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

  decide: async (convId, interruptId, decision, reason) => {
    const result = await postApproval(convId, interruptId, decision, reason);
    get().drop(convId, interruptId);
    return result;
  },
}));
