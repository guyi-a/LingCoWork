import { create } from "zustand";
import type { Instruction } from "@/lib/api";

// 一条排队中的消息。text 是**已经拼好附件标记的最终文本**——附件在入队那一刻
// 就被 serializeAttachments 冻进正文了，之后用户再改附件也影响不到它。
export type QueuedMessage = {
  id: string;
  text: string;
  createdAt: number;
  instruction?: Pick<Instruction, "name" | "label">;
};

interface QueueStore {
  // 按 conversation id 分桶，跟 approval / question store 一致：切走再回来
  // 队列还在（store 是 module 级的），但不会串到别的会话去。
  queues: Record<string, QueuedMessage[]>;
  // 暂停是「有东西排着但先别发」。由用户点停止、发送失败、流报错三处置起，
  // 只有用户点「继续发送」才解除——停止之后立刻被下一条顶上来最惹人烦。
  paused: Record<string, boolean>;

  enqueue: (
    convId: string,
    text: string,
    instruction?: Pick<Instruction, "name" | "label">,
  ) => void;
  dequeue: (convId: string) => QueuedMessage | undefined;
  restoreFront: (convId: string, item: QueuedMessage) => void;
  remove: (convId: string, id: string) => void;
  clear: (convId: string) => void;
  setPaused: (convId: string, paused: boolean) => void;
}

export const useQueueStore = create<QueueStore>((set, get) => ({
  queues: {},
  paused: {},

  enqueue: (convId, text, instruction) => {
    const current = get().queues[convId] ?? [];
    const item: QueuedMessage = {
      id: crypto.randomUUID(),
      text,
      createdAt: Date.now(),
      instruction,
    };
    set({ queues: { ...get().queues, [convId]: [...current, item] } });
  },

  dequeue: (convId) => {
    const current = get().queues[convId] ?? [];
    if (current.length === 0) return undefined;
    const [head, ...rest] = current;
    const queues = { ...get().queues };
    if (rest.length === 0) delete queues[convId];
    else queues[convId] = rest;
    set({ queues });
    return head;
  },

  restoreFront: (convId, item) => {
    const current = get().queues[convId] ?? [];
    set({ queues: { ...get().queues, [convId]: [item, ...current] } });
  },

  remove: (convId, id) => {
    const current = get().queues[convId] ?? [];
    const next = current.filter((m) => m.id !== id);
    const queues = { ...get().queues };
    const paused = { ...get().paused };
    if (next.length === 0) {
      delete queues[convId];
      // 空队列的暂停标志没有意义，留着还会挡住用户下一次入队的自动发送。
      delete paused[convId];
      set({ queues, paused });
      return;
    }
    queues[convId] = next;
    set({ queues });
  },

  clear: (convId) => {
    const queues = { ...get().queues };
    const paused = { ...get().paused };
    delete queues[convId];
    delete paused[convId];
    set({ queues, paused });
  },

  setPaused: (convId, next) => {
    const paused = { ...get().paused };
    if (next) {
      const queue = get().queues[convId];
      if (!queue || queue.length === 0) {
        delete paused[convId];
        set({ paused });
        return;
      }
      paused[convId] = true;
    } else {
      delete paused[convId];
    }
    set({ paused });
  },
}));

// Zustand selector 必须在 store 未变时返回同一个引用，否则
// useSyncExternalStore 会空转——空数组走常量。
const EMPTY_QUEUE: QueuedMessage[] = [];

export function useConversationQueue(convId: string): QueuedMessage[] {
  return useQueueStore((s) => s.queues[convId] ?? EMPTY_QUEUE);
}

export function useQueuePaused(convId: string): boolean {
  return useQueueStore((s) => !!s.paused[convId]);
}
