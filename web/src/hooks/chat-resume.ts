import type { ChatTurn } from "./useChatStream";

// An active stream always continues the latest assistant turn, even when all
// currently persisted tools are already settled. A text-only next ReAct
// segment must not create a second assistant bubble.
export function findResumeAssistant(turns: ChatTurn[]): ChatTurn | undefined {
  return [...turns].reverse().find((turn) => turn.role === "assistant");
}
