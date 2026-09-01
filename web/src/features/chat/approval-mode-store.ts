import { useEffect, useState } from "react";
import { create } from "zustand";
import {
  getApprovalMode as apiGet,
  setApprovalMode as apiSet,
  type ApprovalMode,
} from "@/lib/api";

// Approval-mode store, keyed by conversation id. The backend persists the
// mode on the conversation; this cache mirrors optimistic writes and reloads
// on mount so refreshes and out-of-band changes converge.
interface ApprovalModeStore {
  byConv: Record<string, ApprovalMode>;
  set: (convID: string, mode: ApprovalMode) => void;
  clear: (convID: string) => void;
}

export const useApprovalModeStore = create<ApprovalModeStore>((set, get) => ({
  byConv: {},
  set: (convID, mode) => {
    set({ byConv: { ...get().byConv, [convID]: mode } });
  },
  clear: (convID) => {
    const map = { ...get().byConv };
    delete map[convID];
    set({ byConv: map });
  },
}));

// useApprovalMode: convenient hook that (a) reads the cached mode with
// "manual" as fallback, (b) fetches from the server on mount so the
// dropdown reflects backend truth, and (c) exposes a change() that writes
// through — optimistic local, then POST, revert on failure.
export function useApprovalMode(conversationID: string) {
  const cached = useApprovalModeStore(
    (s) => s.byConv[conversationID] ?? "manual",
  );
  const write = useApprovalModeStore((s) => s.set);
  const [pending, setPending] = useState(false);

  useEffect(() => {
    let cancelled = false;
    apiGet(conversationID)
      .then((m) => {
        if (!cancelled) write(conversationID, m);
      })
      .catch((err) => {
        console.error("[approval-mode] load failed:", err);
      });
    return () => {
      cancelled = true;
    };
  }, [conversationID, write]);

  const change = async (mode: ApprovalMode): Promise<void> => {
    if (pending) return;
    setPending(true);
    const prev = cached;
    write(conversationID, mode); // optimistic
    try {
      await apiSet(conversationID, mode);
    } catch (err) {
      console.error("[approval-mode] set failed, reverting:", err);
      write(conversationID, prev);
    } finally {
      setPending(false);
    }
  };

  return { mode: cached, change, pending };
}
