import { create } from "zustand";
import {
  fetchWorkspaceProblems,
  type WorkspaceProblems,
} from "@/lib/api";

type Scope = "current" | "conversation";

interface ProblemsState {
  data: Record<string, WorkspaceProblems | undefined>;
  scope: Record<string, Scope | undefined>;
  loading: Record<string, boolean | undefined>;
  error: Record<string, string | undefined>;
  setScope: (conversationID: string, scope: Scope) => void;
  load: (conversationID: string, signal?: AbortSignal) => Promise<void>;
  clear: (conversationID: string) => void;
}

export const useProblemsStore = create<ProblemsState>((set, get) => ({
  data: {},
  scope: {},
  loading: {},
  error: {},
  setScope: (conversationID, scope) =>
    set({ scope: { ...get().scope, [conversationID]: scope } }),
  load: async (conversationID, signal) => {
    const scope = get().scope[conversationID] ?? "current";
    set({
      loading: { ...get().loading, [conversationID]: true },
      error: { ...get().error, [conversationID]: undefined },
    });
    try {
      const data = await fetchWorkspaceProblems(conversationID, scope, signal);
      if (signal?.aborted) return;
      set({ data: { ...get().data, [conversationID]: data } });
    } catch (err) {
      if ((err as { name?: string }).name === "AbortError") return;
      set({
        error: {
          ...get().error,
          [conversationID]: err instanceof Error ? err.message : String(err),
        },
      });
    } finally {
      if (!signal?.aborted) {
        set({ loading: { ...get().loading, [conversationID]: false } });
      }
    }
  },
  clear: (conversationID) => {
    const data = { ...get().data };
    const loading = { ...get().loading };
    const error = { ...get().error };
    delete data[conversationID];
    delete loading[conversationID];
    delete error[conversationID];
    set({ data, loading, error });
  },
}));

export type { Scope as ProblemsScope };
