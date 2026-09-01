export const API_BASE =
  window.electronAPI?.runtimeConfig.apiBase ?? "http://127.0.0.1:9001";

export type ConversationItem = {
  id: string;
  project_id?: string | null;
  title: string;
  agent_status?: string; // idle | running | waiting_approval | waiting_user | waiting_plan
  chat_mode?: AgentMode;
  updated_at: string;
};

export type AgentMode = "agent" | "plan";

export type WorkItemStatus =
  | "pending"
  | "in_progress"
  | "completed"
  | "cancelled";

export type WorkItem = {
  id: string;
  content: string;
  status: WorkItemStatus;
  position: number;
};

export type WorkPlan = {
  id: string;
  conversation_id: string;
  user_message_seq: number;
  origin: "plan" | "agent";
  overview: string;
  body_md: string;
  status: "draft" | "awaiting" | "active" | "completed" | "cancelled";
  revision: number;
  items: WorkItem[];
  created_at: string;
  updated_at: string;
};

export type ProjectItem = {
  id: string;
  name: string;
  workspace: string;
  updated_at: string;
};

export type PersistedToolEvent = {
  id: string;
  name: string;
  args_json?: string;
  ok?: boolean;
  status?: "pending" | "running" | "ok" | "error" | "cancelled";
  content?: string;
  error?: string;
};

// One ReAct iteration inside an assistant turn: tools invoked in that
// iteration plus the assistant text for that iteration. Reasoning stays
// merged on the parent message.
export type PersistedSegment = {
  content?: string;
  tools?: PersistedToolEvent[];
};

// One event captured from a sub-agent (e.g. deep_research) during a single
// assistant turn. Persisted as an ordered array so the UI can replay the
// nested timeline after the page reloads. parent_tool_call_id links each
// event back to the root tool_call that triggered the sub-agent.
export type PersistedSubAgentEvent = {
  seq: number;
  agent: string;
  parent_tool_call_id?: string;
  type: "thinking" | "text" | "tool_call" | "tool_result" | "error";
  content?: string;
  tool_call_id?: string;
  name?: string;
  args_json?: string;
  ok?: boolean;
  error?: string;
};

// role "context_compacted" is a synthetic marker, not a stored row: history
// up to this point was folded into a summary to fit the context window. The
// summary text is deliberately not sent — it is context for the model, not
// something the user asked to read.
export type PersistedMessage = {
  seq: number;
  role: "user" | "assistant" | "tool" | "system" | "context_compacted";
  content: string;
  reasoning_content?: string;
  tools?: PersistedToolEvent[];
  segments?: PersistedSegment[];
  sub_events?: PersistedSubAgentEvent[];
  created_at: string;
  // Provider-reported context size this turn ran against. Present only on
  // assistant rows, and only once a run has reported usage.
  total_tokens?: number;
  compaction_id?: number;
  replaced_count?: number;
  instruction?: UserInstructionSnapshot;
  user_instruction?: UserInstructionSnapshot;
};

export type Instruction = {
  name: string;
  label: string;
  description: string;
  prompt: string;
};

export type InstructionInput = {
  name?: string;
  label: string;
  description: string;
  prompt: string;
};

export type InstructionRef = {
  name: string;
};

export type UserInstructionSnapshot = {
  name: string;
  label: string;
  raw_input: string;
};

async function instructionJSON<T>(res: Response, what: string): Promise<T> {
  const data = await res.json().catch(() => null);
  if (!res.ok) {
    const detail =
      data && typeof data === "object" && "error" in data
        ? String((data as { error?: unknown }).error)
        : `${what}: ${res.status}`;
    throw new Error(detail);
  }
  return data as T;
}

function instructionFromPayload(
  data: Instruction | { instruction?: Instruction },
): Instruction {
  const item: Instruction | undefined =
    "instruction" in data ? data.instruction : (data as Instruction);
  if (!item) throw new Error("instruction response is empty");
  return {
    name: item.name,
    label: item.label,
    description: item.description ?? "",
    prompt: item.prompt ?? "",
  };
}

export async function listInstructions(): Promise<Instruction[]> {
  const res = await fetch(`${API_BASE}/instructions`);
  const data = await instructionJSON<
    Instruction[] | { instructions?: Instruction[] }
  >(res, "listInstructions");
  const items = Array.isArray(data) ? data : (data.instructions ?? []);
  return items.map((item) => ({
    name: item.name,
    label: item.label,
    description: item.description ?? "",
    prompt: item.prompt ?? "",
  }));
}

export async function getInstruction(name: string): Promise<Instruction> {
  const res = await fetch(
    `${API_BASE}/instructions/${encodeURIComponent(name)}`,
  );
  const data = await instructionJSON<
    Instruction | { instruction?: Instruction }
  >(res, "getInstruction");
  return instructionFromPayload(data);
}

export async function createInstruction(
  input: InstructionInput,
): Promise<Instruction> {
  const res = await fetch(`${API_BASE}/instructions`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(input),
  });
  if (res.status === 204) {
    if (!input.name) throw new Error("createInstruction: missing name");
    return { ...input, name: input.name };
  }
  const data = await instructionJSON<
    Instruction | { instruction?: Instruction }
  >(res, "createInstruction");
  return instructionFromPayload(data);
}

export async function updateInstruction(
  name: string,
  input: InstructionInput,
): Promise<Instruction> {
  const res = await fetch(
    `${API_BASE}/instructions/${encodeURIComponent(name)}`,
    {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(input),
    },
  );
  if (res.status === 204) {
    return { ...input, name: input.name ?? name };
  }
  const data = await instructionJSON<
    Instruction | { instruction?: Instruction }
  >(res, "updateInstruction");
  return instructionFromPayload(data);
}

export async function deleteInstruction(name: string): Promise<void> {
  const res = await fetch(
    `${API_BASE}/instructions/${encodeURIComponent(name)}`,
    { method: "DELETE" },
  );
  if (res.ok || res.status === 204) return;
  const data = await res.json().catch(() => null);
  throw new Error(
    data && typeof data === "object" && "error" in data
      ? String((data as { error?: unknown }).error)
      : `deleteInstruction: ${res.status}`,
  );
}

export type MemoryScope = "user" | "project";

export type MemoryDoc = {
  scope: MemoryScope;
  path: string;
  content: string;
  /** 读取时那一刻的版本。保存时带回去，服务端用它判断这期间有没有被 agent 改过。 */
  hash: string;
  bytes: number;
  limit: number;
};

/** 保存冲突：读取之后 agent 也写过一次。调用方该重新加载再让用户决定。 */
export class MemoryConflictError extends Error {
  constructor(message: string) {
    super(message);
    this.name = "MemoryConflictError";
  }
}

/** 该会话还没绑定工作区，所以没有项目级记忆可读。 */
export class NoWorkspaceError extends Error {
  constructor(message: string) {
    super(message);
    this.name = "NoWorkspaceError";
  }
}

async function memoryJSON(res: Response, what: string): Promise<MemoryDoc> {
  const data = (await res.json().catch(() => null)) as
    | (MemoryDoc & { error?: string; code?: string })
    | null;
  if (!res.ok) {
    const detail = data?.error ?? `${what}: ${res.status}`;
    if (data?.code === "conflict") throw new MemoryConflictError(detail);
    if (data?.code === "no_workspace") throw new NoWorkspaceError(detail);
    throw new Error(detail);
  }
  if (!data) throw new Error(`${what}: empty response`);
  return data;
}

function memoryURL(conversationId?: string, projectId?: string): string {
  if (!conversationId) return `${API_BASE}/memory/user`;
  const base = `${API_BASE}/conversations/${encodeURIComponent(conversationId)}/memory`;
  return projectId
    ? `${base}?project_id=${encodeURIComponent(projectId)}`
    : base;
}

/** conversationId 省略时读用户级；传了就读那个会话所属工作区的项目级。 */
export async function getMemory(
  conversationId?: string,
  projectId?: string,
): Promise<MemoryDoc> {
  return memoryJSON(await fetch(memoryURL(conversationId, projectId)), "getMemory");
}

export async function saveMemory(
  content: string,
  hash: string,
  conversationId?: string,
  projectId?: string,
): Promise<MemoryDoc> {
  const res = await fetch(memoryURL(conversationId, projectId), {
    method: "PUT",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ content, hash }),
  });
  return memoryJSON(res, "saveMemory");
}

export async function listConversations(): Promise<ConversationItem[]> {
  const res = await fetch(`${API_BASE}/conversations`);
  if (!res.ok) throw new Error(`listConversations: ${res.status}`);
  const data = (await res.json()) as { conversations: ConversationItem[] };
  return data.conversations ?? [];
}

export async function listProjects(): Promise<ProjectItem[]> {
  const res = await fetch(`${API_BASE}/projects`);
  if (!res.ok) throw new Error(`listProjects: ${res.status}`);
  const data = (await res.json()) as { projects: ProjectItem[] };
  return data.projects ?? [];
}

export async function openProject(
  path: string,
  name?: string,
): Promise<{ project: ProjectItem; created: boolean }> {
  const res = await fetch(`${API_BASE}/projects`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ path, ...(name ? { name } : {}) }),
  });
  const data = (await res.json().catch(() => null)) as
    | { project?: ProjectItem; created?: boolean; error?: string }
    | null;
  if (!res.ok || !data?.project) {
    throw new Error(data?.error || `openProject: ${res.status}`);
  }
  return { project: data.project, created: data.created === true };
}

export async function renameProject(id: string, name: string): Promise<void> {
  const res = await fetch(`${API_BASE}/projects/${encodeURIComponent(id)}`, {
    method: "PATCH",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ name }),
  });
  if (!res.ok && res.status !== 204) {
    throw new Error(`renameProject: ${res.status}`);
  }
}

export async function deleteProject(id: string): Promise<{ warning?: string }> {
  const res = await fetch(`${API_BASE}/projects/${encodeURIComponent(id)}`, {
    method: "DELETE",
  });
  if (res.status === 204) return {};
  if (res.status === 200) {
    return (await res.json()) as { warning?: string };
  }
  throw new Error(`deleteProject: ${res.status}`);
}

export async function openProjectInFinder(id: string): Promise<void> {
  const res = await fetch(
    `${API_BASE}/projects/${encodeURIComponent(id)}/open`,
    { method: "POST" },
  );
  if (!res.ok && res.status !== 204) {
    throw new Error(`openProjectInFinder: ${res.status}`);
  }
}

export type MessageHistory = {
  messages: PersistedMessage[];
  lastSeq: number;
  // Token count at which history gets folded into a summary. 0 when
  // compaction is disabled — no threshold to show progress against.
  contextLimit: number;
};

export async function listMessages(id: string): Promise<MessageHistory> {
  const res = await fetch(
    `${API_BASE}/conversations/${encodeURIComponent(id)}/messages`,
  );
  if (!res.ok) throw new Error(`listMessages: ${res.status}`);
  const data = (await res.json()) as {
    messages: PersistedMessage[];
    context_limit?: number;
    last_seq?: number;
  };
  return {
    messages: data.messages ?? [],
    contextLimit: data.context_limit ?? 0,
    lastSeq: data.last_seq ?? 0,
  };
}

export async function deleteConversation(id: string): Promise<void> {
  const res = await fetch(
    `${API_BASE}/conversations/${encodeURIComponent(id)}`,
    { method: "DELETE" },
  );
  if (!res.ok && res.status !== 204) {
    throw new Error(`deleteConversation: ${res.status}`);
  }
}

export async function postChat(
  id: string,
  message: string,
  signal: AbortSignal,
  opts?: {
    projectId?: string;
    instruction?: InstructionRef;
    mode?: AgentMode;
  },
): Promise<Response> {
  const qs = opts?.projectId
    ? `?project_id=${encodeURIComponent(opts.projectId)}`
    : "";
  const res = await fetch(`${API_BASE}/chat/${encodeURIComponent(id)}${qs}`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({
      message,
      ...(opts?.instruction ? { instruction: opts.instruction } : {}),
      ...(opts?.mode ? { mode: opts.mode } : {}),
    }),
    signal,
  });
  if (!res.ok) {
    let detail = `chat: ${res.status}`;
    try {
      const body = (await res.json()) as { error?: string };
      if (body.error) detail = body.error;
    } catch {
      // Keep the status fallback when an intermediary returns a non-JSON body.
    }
    throw new Error(detail);
  }
  return res;
}

export async function getAgentMode(
  conversationID: string,
): Promise<AgentMode> {
  const res = await fetch(
    `${API_BASE}/conversations/${encodeURIComponent(conversationID)}/agent-mode`,
  );
  if (!res.ok) throw new Error(`getAgentMode: ${res.status}`);
  const data = (await res.json()) as { mode?: AgentMode };
  return data.mode ?? "agent";
}

export async function setAgentMode(
  conversationID: string,
  mode: AgentMode,
): Promise<AgentMode> {
  const res = await fetch(
    `${API_BASE}/conversations/${encodeURIComponent(conversationID)}/agent-mode`,
    {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ mode }),
    },
  );
  if (!res.ok) throw new Error(`setAgentMode: ${res.status}`);
  const data = (await res.json()) as { mode?: AgentMode };
  return data.mode ?? mode;
}

export type PlanDraftInput = {
  revision: number;
  overview: string;
  body_md: string;
  items: WorkItem[];
  interrupt_id?: string;
};

export async function getLatestPlan(
  conversationID: string,
): Promise<WorkPlan | null> {
  const res = await fetch(
    `${API_BASE}/conversations/${encodeURIComponent(conversationID)}/plans/latest`,
  );
  if (!res.ok) throw new Error(`getLatestPlan: ${res.status}`);
  const data = (await res.json()) as { plan?: WorkPlan | null };
  return data.plan ?? null;
}

export async function listPlans(
  conversationID: string,
): Promise<WorkPlan[]> {
  const res = await fetch(
    `${API_BASE}/conversations/${encodeURIComponent(conversationID)}/plans`,
  );
  if (!res.ok) throw new Error(`listPlans: ${res.status}`);
  const data = (await res.json()) as { plans?: WorkPlan[] };
  return data.plans ?? [];
}

export async function editPlan(
  conversationID: string,
  planID: string,
  input: PlanDraftInput,
): Promise<WorkPlan> {
  const res = await fetch(
    `${API_BASE}/conversations/${encodeURIComponent(conversationID)}/plans/${encodeURIComponent(planID)}`,
    {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(input),
    },
  );
  const data = (await res.json().catch(() => null)) as {
    plan?: WorkPlan;
    error?: string;
  } | null;
  if (!res.ok) {
    const err = new Error(data?.error ?? `editPlan: ${res.status}`) as Error & {
      latest?: WorkPlan;
    };
    if (data?.plan) err.latest = data.plan;
    throw err;
  }
  if (!data?.plan) throw new Error("editPlan: empty plan");
  return data.plan;
}

export async function startPlan(
  conversationID: string,
  planID: string,
  input: PlanDraftInput,
): Promise<{ plan: WorkPlan; resumed: boolean }> {
  const res = await fetch(
    `${API_BASE}/conversations/${encodeURIComponent(conversationID)}/plans/${encodeURIComponent(planID)}/start`,
    {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(input),
    },
  );
  const data = (await res.json().catch(() => null)) as {
    plan?: WorkPlan;
    resumed?: boolean;
    error?: string;
  } | null;
  if (!res.ok || !data?.plan) {
    throw new Error(data?.error ?? `startPlan: ${res.status}`);
  }
  return { plan: data.plan, resumed: data.resumed === true };
}

export async function cancelPlan(
  conversationID: string,
  planID: string,
  revision: number,
  interruptID: string,
): Promise<{ plan: WorkPlan; resumed: boolean }> {
  const res = await fetch(
    `${API_BASE}/conversations/${encodeURIComponent(conversationID)}/plans/${encodeURIComponent(planID)}/cancel`,
    {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ revision, interrupt_id: interruptID }),
    },
  );
  const data = (await res.json().catch(() => null)) as {
    plan?: WorkPlan;
    resumed?: boolean;
    error?: string;
  } | null;
  if (!res.ok || !data?.plan) {
    throw new Error(data?.error ?? `cancelPlan: ${res.status}`);
  }
  return { plan: data.plan, resumed: data.resumed === true };
}

export async function resumeChat(
  id: string,
  signal: AbortSignal,
  afterSeq: number,
): Promise<
  | { kind: "stream"; response: Response }
  | { kind: "idle" }
  | {
      kind: "retry";
      cursorStatus?: "client_stale" | "buffer_behind";
      durableSeq?: number;
    }
> {
  const params = new URLSearchParams({ after_seq: String(afterSeq) });
  const res = await fetch(`${API_BASE}/chat/${encodeURIComponent(id)}?${params}`, {
    method: "GET",
    signal,
  });
  if (res.status === 204) return { kind: "idle" };
  if (res.status === 409) {
    const data = (await res.json().catch(() => null)) as {
      cursor_status?: "client_stale" | "buffer_behind";
      durable_seq?: number;
    } | null;
    return {
      kind: "retry",
      cursorStatus: data?.cursor_status,
      durableSeq: data?.durable_seq,
    };
  }
  if (!res.ok) throw new Error(`resumeChat: ${res.status}`);
  return { kind: "stream", response: res };
}

export async function cancelChat(id: string): Promise<void> {
  await fetch(`${API_BASE}/chat/${encodeURIComponent(id)}/cancel`, {
    method: "POST",
  }).catch(() => {});
}

export type InterruptDecisionResult = {
  handled: boolean;
  // False when this answer was recorded but sibling interrupts from the same
  // checkpoint still need answers. Only true means a new SSE stream exists.
  resumed: boolean;
};

export type ApprovalScope = "once" | "session";

// postApproval sends one answer from a possibly parallel interrupt batch.
// 404 is treated as already handled; a successful response says whether this
// answer completed the batch and actually resumed the checkpoint.
export async function postApproval(
  conversationID: string,
  interruptID: string,
  decision: "approve" | "deny",
  scope: ApprovalScope,
  reason?: string,
): Promise<InterruptDecisionResult> {
  const res = await fetch(
    `${API_BASE}/conversations/${encodeURIComponent(conversationID)}/approvals/${encodeURIComponent(interruptID)}`,
    {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ decision, scope, reason }),
    },
  );
  if (res.status === 404) return { handled: false, resumed: false };
  if (!res.ok) throw new Error(`approval ${res.status}`);
  const data = (await res.json()) as { resumed?: boolean };
  return { handled: true, resumed: data.resumed === true };
}

// 一条 pending 中断的通用契约。kind 为 "approval" 时 tool/args_json 有意义；
// kind 为 "question" 时 questions_json 有意义（承载 hitl.Question 数组 JSON）。
// 老版后端未升级 kind 字段时默认视为 approval。
export type PendingInterruptItem = {
  kind?: "approval" | "question" | "plan";
  interrupt_id: string;
  call_id?: string;
  tool?: string;
  args_json?: string;
  // 审批依据的 effect（后端 internal/effect 的序列化结果）。可能缺失：
  // 这个字段上线前落盘的 pending 行没有它，卡片回退到按 args_json 渲染。
  effect_json?: string;
  // 后端明确判定该审批能否在会话内记忆。旧响应可能缺失。
  rememberable?: boolean;
  questions_json?: string;
  plan_json?: string;
};

export type PendingApprovalItem = PendingInterruptItem; // 名字保留兼容旧调用点

export async function listPendingApprovals(
  conversationID: string,
): Promise<PendingInterruptItem[]> {
  const res = await fetch(
    `${API_BASE}/conversations/${encodeURIComponent(conversationID)}/approvals/pending`,
  );
  if (!res.ok) throw new Error(`listPendingApprovals: ${res.status}`);
  const data = (await res.json()) as { approvals?: PendingInterruptItem[] };
  return data.approvals ?? [];
}

// ask_user 恢复：一条用户对某个 pending question 的回复。cancelled=true 时
// answers 允许空，服务端会把 Cancelled 标记传给工具体。
export type QuestionAnswerPayload = {
  cancelled?: boolean;
  answers?: Array<{
    question_id: string;
    selected?: string[];
    custom?: string;
  }>;
};

export async function postQuestionAnswer(
  conversationID: string,
  interruptID: string,
  payload: QuestionAnswerPayload,
): Promise<InterruptDecisionResult> {
  const res = await fetch(
    `${API_BASE}/conversations/${encodeURIComponent(conversationID)}/questions/${encodeURIComponent(interruptID)}`,
    {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(payload),
    },
  );
  if (res.status === 404) return { handled: false, resumed: false };
  if (!res.ok) throw new Error(`question ${res.status}`);
  const data = (await res.json()) as { resumed?: boolean };
  return { handled: true, resumed: data.resumed === true };
}

// The set of per-conversation approval modes. Kept in sync with backend
// approval.Mode — extending here without extending backend will 400 on POST.
export type ApprovalMode = "manual" | "accept-write" | "auto";

export async function getApprovalMode(
  conversationID: string,
): Promise<ApprovalMode> {
  const res = await fetch(
    `${API_BASE}/conversations/${encodeURIComponent(conversationID)}/approval-mode`,
  );
  if (!res.ok) throw new Error(`getApprovalMode: ${res.status}`);
  const data = (await res.json()) as { mode?: string };
  if (
    data.mode === "manual" ||
    data.mode === "accept-write" ||
    data.mode === "auto"
  ) {
    return data.mode;
  }
  return "manual";
}

export async function setApprovalMode(
  conversationID: string,
  mode: ApprovalMode,
): Promise<void> {
  const res = await fetch(
    `${API_BASE}/conversations/${encodeURIComponent(conversationID)}/approval-mode`,
    {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ mode }),
    },
  );
  if (!res.ok && res.status !== 204) {
    throw new Error(`setApprovalMode: ${res.status}`);
  }
}

export type MCPServerState =
  | "connecting"
  | "connected"
  | "disabled"
  | "needs_auth"
  | "error";

export type MCPServerStatus = {
  name: string;
  transport: "stdio" | "http";
  target: string;
  state: MCPServerState;
  tool_count: number;
  tools?: string[];
  error?: string;
  stderr?: string;
  trusted: boolean;
  auto_approve?: string[];
  oauth: boolean;
  authorized: boolean;
};

export type MCPIssue = { server: string; message: string };

export type MCPServersResponse = {
  servers: MCPServerStatus[];
  issues: MCPIssue[] | null;
  config_path: string;
};

export async function listMCPServers(): Promise<MCPServersResponse> {
  const res = await fetch(`${API_BASE}/mcp/servers`);
  if (!res.ok) throw new Error(`listMCPServers: ${res.status}`);
  return (await res.json()) as MCPServersResponse;
}

export type MCPTestResult = {
  ok: boolean;
  needs_auth?: boolean;
  tool_count: number;
  tools?: string[];
  error?: string;
};

export async function testMCPServer(name: string): Promise<MCPTestResult> {
  const res = await fetch(
    `${API_BASE}/mcp/servers/${encodeURIComponent(name)}/test`,
    { method: "POST" },
  );
  const data = await res.json();
  if (!res.ok) throw new Error(data?.error ?? `testMCPServer: ${res.status}`);
  return data as MCPTestResult;
}

export type MCPConfigDoc = { path: string; exists: boolean; content: string };

export async function getMCPConfig(): Promise<MCPConfigDoc> {
  const res = await fetch(`${API_BASE}/mcp/config`);
  if (!res.ok) throw new Error(`getMCPConfig: ${res.status}`);
  return (await res.json()) as MCPConfigDoc;
}

export async function saveMCPConfig(
  content: string,
): Promise<{ issues: MCPIssue[] | null }> {
  const res = await fetch(`${API_BASE}/mcp/config`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ content }),
  });
  const data = await res.json();
  if (!res.ok) throw new Error(data?.error ?? `saveMCPConfig: ${res.status}`);
  return data as { issues: MCPIssue[] | null };
}

// authorizeMCPServer returns the URL the user must visit. The caller opens it
// in a real browser window: the flow needs the provider's existing session
// cookies and often a platform passkey prompt, and an iframe gets neither.
export async function authorizeMCPServer(name: string): Promise<string> {
  const res = await fetch(
    `${API_BASE}/mcp/servers/${encodeURIComponent(name)}/authorize`,
    { method: "POST" },
  );
  const data = await res.json();
  if (!res.ok)
    throw new Error(data?.error ?? `authorizeMCPServer: ${res.status}`);
  return data.auth_url as string;
}

// deleteMCPServer removes the entry from the config file, drops the
// connection and forgets any OAuth token. The config file on disk changes, so
// callers holding its text must reload it.
export async function deleteMCPServer(name: string): Promise<void> {
  const res = await fetch(
    `${API_BASE}/mcp/servers/${encodeURIComponent(name)}`,
    { method: "DELETE" },
  );
  if (res.ok) return;
  const data = await res.json().catch(() => null);
  throw new Error(data?.error ?? `deleteMCPServer: ${res.status}`);
}

// ---- Skill Hub（内网 kskill 注册中心）----
// 注册中心不返回 CORS 头，浏览器不能直连，所有读操作都经后端转发。

export type SkillHubSkill = {
  id: string;
  scope?: string;
  slug: string;
  fullSlug: string;
  name: string;
  description?: string;
  owner?: string;
  tags?: string[];
  isTeam?: boolean;
  isEditorPick?: boolean;
  hotness?: { installs?: number; downloads?: number };
  latestVersion?: string;
  updatedAt?: string;
};

export type SkillHubAuthorProfile = {
  username: string;
  displayName?: string;
  avatarUrl?: string;
};

export type SkillHubPage = {
  items: SkillHubSkill[];
  authorProfiles: Record<string, SkillHubAuthorProfile>;
  total: number;
  page: number;
  pageSize: number;
};

export type SkillHubVersion = {
  version: string;
  changelog?: string;
  bundleSize?: number;
  isLatest?: boolean;
  createdAt?: string;
};

export type SkillHubInstalled = {
  name: string;
  fullSlug: string;
  version?: string;
  directory: string;
};

export type SkillHubCategoryCount = { id: string; count: number };

async function skillHubJSON<T>(res: Response, what: string): Promise<T> {
  const data = await res.json().catch(() => null);
  if (!res.ok) throw new Error(data?.error ?? `${what}: ${res.status}`);
  return data as T;
}

export async function listSkillHubSkills(params: {
  q?: string;
  category?: string;
  page?: number;
  pageSize?: number;
}): Promise<SkillHubPage> {
  const qs = new URLSearchParams();
  if (params.q) qs.set("q", params.q);
  if (params.category) qs.set("category", params.category);
  qs.set("page", String(params.page ?? 1));
  qs.set("pageSize", String(params.pageSize ?? 20));
  const res = await fetch(`${API_BASE}/skillhub/skills?${qs}`);
  return skillHubJSON<SkillHubPage>(res, "listSkillHubSkills");
}

// 分类由模型离线归类；分类器不可用时后端返回错误，调用方隐藏分类栏即可。
export async function getSkillHubCategories(): Promise<
  SkillHubCategoryCount[]
> {
  const res = await fetch(`${API_BASE}/skillhub/categories`);
  const data = await skillHubJSON<{ categories: SkillHubCategoryCount[] }>(
    res,
    "getSkillHubCategories",
  );
  return data.categories;
}

export async function getSkillHubReadme(fullSlug: string): Promise<string> {
  const res = await fetch(
    `${API_BASE}/skillhub/skill/readme?slug=${encodeURIComponent(fullSlug)}`,
  );
  const data = await skillHubJSON<{ content: string }>(res, "getSkillHubReadme");
  return data.content;
}

export async function getSkillHubVersions(
  fullSlug: string,
): Promise<SkillHubVersion[]> {
  const res = await fetch(
    `${API_BASE}/skillhub/skill/versions?slug=${encodeURIComponent(fullSlug)}`,
  );
  const data = await skillHubJSON<{ versions: SkillHubVersion[] }>(
    res,
    "getSkillHubVersions",
  );
  return data.versions;
}

export async function listSkillHubInstalled(): Promise<SkillHubInstalled[]> {
  const res = await fetch(`${API_BASE}/skillhub/installed`);
  const data = await skillHubJSON<{ installed: SkillHubInstalled[] }>(
    res,
    "listSkillHubInstalled",
  );
  return data.installed;
}

export async function installSkillHubSkill(
  fullSlug: string,
  version?: string,
): Promise<SkillHubInstalled> {
  const res = await fetch(`${API_BASE}/skillhub/install`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ fullSlug, version }),
  });
  return skillHubJSON<SkillHubInstalled>(res, "installSkillHubSkill");
}

export async function uninstallSkillHubSkill(fullSlug: string): Promise<void> {
  const res = await fetch(`${API_BASE}/skillhub/uninstall`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ fullSlug }),
  });
  await skillHubJSON<unknown>(res, "uninstallSkillHubSkill");
}

export type WorkspaceTreeEntry = {
  path: string;
  name: string;
  is_dir: boolean;
  size?: number;
  modified_at: string;
};

export type WorkspaceMeta = {
  project_id: string;
  root_name: string;
};

export type WorkspaceTree = {
  workspace: WorkspaceMeta;
  entries: WorkspaceTreeEntry[];
  truncated?: boolean;
};

export type WorkspaceFileKind =
  | "markdown"
  | "text"
  | "image"
  | "binary"
  | "unsupported";

export type WorkspaceFile = {
  path: string;
  name: string;
  size: number;
  mime?: string;
  kind: WorkspaceFileKind;
  is_binary: boolean;
  content?: string;
  truncated?: boolean;
};

export type WorkspaceChangeScope = "agent" | "all";

export type WorkspaceChangedFile = {
  path: string;
  old_path?: string;
  status: "modified" | "added" | "deleted" | "renamed";
  additions: number;
  deletions: number;
  binary?: boolean;
  sensitive?: boolean;
  too_large?: boolean;
  attribution?: string;
  tools?: string[];
};

export type WorkspaceChanges = {
  workspace: WorkspaceMeta;
  scope: WorkspaceChangeScope;
  git_repository: boolean;
  user_message_seq?: number;
  files: WorkspaceChangedFile[];
  truncated?: boolean;
};

export type WorkspaceRepositoryStatus = {
  workspace: WorkspaceMeta;
  root: string;
  git_repository: boolean;
  branch?: string;
  detached?: boolean;
  dirty: boolean;
  changed_files: number;
  staged: number;
  unstaged: number;
  untracked: number;
  ahead: number;
  behind: number;
  has_upstream: boolean;
};

export type ValidationKind =
  | "test"
  | "build"
  | "lint"
  | "typecheck"
  | "format";

export type ValidationDiagnostic = {
  id: string;
  severity: "error" | "warning" | "info";
  message: string;
  path?: string;
  line?: number;
  column?: number;
  code?: string;
  source?: string;
};

export type ValidationSummary = {
  kind: ValidationKind;
  passed: boolean;
  parser: string;
  parse_ok: boolean;
  diagnostics?: ValidationDiagnostic[];
  error_count: number;
  warning_count: number;
  truncated?: boolean;
};

export type ValidationRun = {
  tool_call_id: string;
  user_message_seq: number;
  command: string;
  cwd: string;
  exit_code: number;
  duration_ms: number;
  timed_out?: boolean;
  validation: ValidationSummary;
  created_at: string;
};

export type WorkspaceProblems = {
  scope: "current" | "conversation";
  user_message_seq?: number;
  runs: ValidationRun[];
  error_count: number;
  warning_count: number;
};

export type WorkspaceDiff = WorkspaceChangedFile & {
  scope: WorkspaceChangeScope;
  user_message_seq?: number;
  patch?: string;
  truncated?: boolean;
};

export async function fetchWorkspaceTree(
  conversationId: string,
  opts?: { projectId?: string },
  signal?: AbortSignal,
): Promise<WorkspaceTree> {
  const qs = opts?.projectId
    ? `?project_id=${encodeURIComponent(opts.projectId)}`
    : "";
  const res = await fetch(
    `${API_BASE}/conversations/${encodeURIComponent(conversationId)}/workspace/tree${qs}`,
    { signal },
  );
  if (!res.ok) throw new Error(`fetchWorkspaceTree: ${res.status}`);
  return res.json();
}

export async function fetchWorkspaceStatus(
  conversationId: string,
  opts?: { projectId?: string },
  signal?: AbortSignal,
): Promise<WorkspaceRepositoryStatus> {
  const params = new URLSearchParams();
  if (opts?.projectId) params.set("project_id", opts.projectId);
  const query = params.size > 0 ? `?${params.toString()}` : "";
  const res = await fetch(
    `${API_BASE}/conversations/${encodeURIComponent(conversationId)}/workspace/status${query}`,
    { signal },
  );
  if (!res.ok) throw new Error(`fetchWorkspaceStatus: ${res.status}`);
  return res.json();
}

export async function fetchWorkspaceProblems(
  conversationId: string,
  scope: "current" | "conversation",
  signal?: AbortSignal,
): Promise<WorkspaceProblems> {
  const params = new URLSearchParams({ scope });
  const res = await fetch(
    `${API_BASE}/conversations/${encodeURIComponent(conversationId)}/workspace/problems?${params.toString()}`,
    { signal },
  );
  if (!res.ok) throw new Error(`fetchWorkspaceProblems: ${res.status}`);
  return res.json();
}

export async function fetchWorkspaceFile(
  conversationId: string,
  path: string,
  opts?: { projectId?: string },
  signal?: AbortSignal,
): Promise<WorkspaceFile> {
  const params = new URLSearchParams({ path });
  if (opts?.projectId) params.set("project_id", opts.projectId);
  const res = await fetch(
    `${API_BASE}/conversations/${encodeURIComponent(conversationId)}/workspace/file?${params.toString()}`,
    { signal },
  );
  if (!res.ok) throw new Error(`fetchWorkspaceFile: ${res.status}`);
  return res.json();
}

export async function fetchWorkspaceChanges(
  conversationId: string,
  scope: WorkspaceChangeScope,
  opts?: { projectId?: string },
  signal?: AbortSignal,
): Promise<WorkspaceChanges> {
  const params = new URLSearchParams({ scope });
  if (opts?.projectId) params.set("project_id", opts.projectId);
  const res = await fetch(
    `${API_BASE}/conversations/${encodeURIComponent(conversationId)}/workspace/changes?${params.toString()}`,
    { signal },
  );
  if (!res.ok) throw new Error(`fetchWorkspaceChanges: ${res.status}`);
  return res.json();
}

export async function fetchWorkspaceDiff(
  conversationId: string,
  path: string,
  scope: WorkspaceChangeScope,
  opts?: { projectId?: string },
  signal?: AbortSignal,
): Promise<WorkspaceDiff> {
  const params = new URLSearchParams({ path, scope });
  if (opts?.projectId) params.set("project_id", opts.projectId);
  const res = await fetch(
    `${API_BASE}/conversations/${encodeURIComponent(conversationId)}/workspace/diff?${params.toString()}`,
    { signal },
  );
  if (!res.ok) throw new Error(`fetchWorkspaceDiff: ${res.status}`);
  return res.json();
}

export function workspaceDownloadURL(
  conversationId: string,
  path: string,
  opts?: { projectId?: string; version?: number },
): string {
  const params = new URLSearchParams({ path });
  if (opts?.projectId) params.set("project_id", opts.projectId);
  if (opts?.version !== undefined) params.set("v", String(opts.version));
  return `${API_BASE}/conversations/${encodeURIComponent(conversationId)}/workspace/download?${params.toString()}`;
}

export function workspaceInlineURL(
  conversationId: string,
  path: string,
  opts?: { projectId?: string; version?: number },
): string {
  const params = new URLSearchParams({ path });
  if (opts?.projectId) params.set("project_id", opts.projectId);
  if (opts?.version !== undefined) params.set("v", String(opts.version));
  return `${API_BASE}/conversations/${encodeURIComponent(conversationId)}/workspace/inline?${params.toString()}`;
}
