import { create } from "zustand";
import {
  getAgentMode,
  setAgentMode as postAgentMode,
  type AgentMode,
} from "@/lib/api";

interface AgentModeStore {
  modes: Record<string, AgentMode>;
  setLocal: (conversationID: string, mode: AgentMode) => void;
  load: (conversationID: string) => Promise<AgentMode>;
  save: (conversationID: string, mode: AgentMode) => Promise<AgentMode>;
}

export const useAgentModeStore = create<AgentModeStore>((set, get) => ({
  modes: {},
  setLocal: (conversationID, mode) =>
    set({ modes: { ...get().modes, [conversationID]: mode } }),
  load: async (conversationID) => {
    const before = get().modes[conversationID];
    const mode = await getAgentMode(conversationID);
    // A user click may win while the mount-time GET is in flight. Never let
    // that stale response switch the composer back underneath them.
    if (get().modes[conversationID] !== before) {
      return get().modes[conversationID] ?? mode;
    }
    set({ modes: { ...get().modes, [conversationID]: mode } });
    return mode;
  },
  save: async (conversationID, mode) => {
    const previous = get().modes[conversationID] ?? "agent";
    set({ modes: { ...get().modes, [conversationID]: mode } });
    try {
      const saved = await postAgentMode(conversationID, mode);
      set({ modes: { ...get().modes, [conversationID]: saved } });
      return saved;
    } catch (err) {
      set({ modes: { ...get().modes, [conversationID]: previous } });
      throw err;
    }
  },
}));
