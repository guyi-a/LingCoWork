import { create } from "zustand";
import {
  deleteConversation,
  listConversations,
  type ConversationItem,
} from "@/lib/api";

interface ConversationStore {
  items: ConversationItem[];
  loading: boolean;
  loaded: boolean;
  refresh: () => Promise<void>;
  remove: (id: string) => Promise<void>;
  touch: (id: string, title?: string, opts?: { projectId?: string }) => void;
}

export const useConversationStore = create<ConversationStore>((set, get) => ({
  items: [],
  loading: false,
  loaded: false,

  refresh: async () => {
    set({ loading: true });
    try {
      const items = await listConversations();
      set({ items, loading: false, loaded: true });
    } catch (err) {
      console.error("[conversations] refresh failed:", err);
      set({ loading: false, loaded: true });
    }
  },

  remove: async (id) => {
    const prev = get().items;
    set({ items: prev.filter((c) => c.id !== id) });
    try {
      await deleteConversation(id);
    } catch (err) {
      console.error("[conversations] delete failed:", err);
      set({ items: prev });
    }
  },

  touch: (id, title, opts) => {
    const now = new Date().toISOString();
    const existing = get().items.find((c) => c.id === id);
    if (existing) {
      const updated = {
        ...existing,
        updated_at: now,
        ...(title !== undefined ? { title } : {}),
        ...(opts?.projectId !== undefined ? { project_id: opts.projectId } : {}),
      };
      set({
        items: [updated, ...get().items.filter((c) => c.id !== id)],
      });
    } else {
      set({
        items: [
          {
            id,
            title: title ?? "",
            updated_at: now,
            project_id: opts?.projectId ?? null,
          },
          ...get().items,
        ],
      });
    }
  },
}));
