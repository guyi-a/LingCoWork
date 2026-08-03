export const API_BASE = "http://localhost:9001";

export type ConversationItem = {
  id: string;
  project_id?: string | null;
  title: string;
  agent_status?: string; // "idle" | "running" | "waiting_approval"
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
};

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
  };
  return {
    messages: data.messages ?? [],
    contextLimit: data.context_limit ?? 0,
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
  opts?: { projectId?: string },
): Promise<Response> {
  const qs = opts?.projectId
    ? `?project_id=${encodeURIComponent(opts.projectId)}`
    : "";
  return fetch(`${API_BASE}/chat/${encodeURIComponent(id)}${qs}`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ message }),
    signal,
  });
}

export async function resumeChat(
  id: string,
  signal: AbortSignal,
): Promise<Response | null> {
  const res = await fetch(`${API_BASE}/chat/${encodeURIComponent(id)}`, {
    method: "GET",
    signal,
  });
  if (res.status === 204) return null;
  if (!res.ok) throw new Error(`resumeChat: ${res.status}`);
  return res;
}

export async function cancelChat(id: string): Promise<void> {
  await fetch(`${API_BASE}/chat/${encodeURIComponent(id)}/cancel`, {
    method: "POST",
  }).catch(() => {});
}

// postApproval sends the user's approve/deny for one paused tool call.
// The backend fires runner.ResumeWithParams; the continuation streams over
// the existing SSE connection, so this call only needs to resolve/reject.
// 404 is treated as "already handled" — the caller can drop the pending
// card without surfacing an error.
export async function postApproval(
  conversationID: string,
  interruptID: string,
  decision: "approve" | "deny",
  reason?: string,
): Promise<{ handled: boolean }> {
  const res = await fetch(
    `${API_BASE}/conversations/${encodeURIComponent(conversationID)}/approvals/${encodeURIComponent(interruptID)}`,
    {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ decision, reason }),
    },
  );
  if (res.status === 404) return { handled: false };
  if (!res.ok) throw new Error(`approval ${res.status}`);
  return { handled: true };
}

// 一条 pending 中断的通用契约。kind 为 "approval" 时 tool/args_json 有意义；
// kind 为 "question" 时 questions_json 有意义（承载 hitl.Question 数组 JSON）。
// 老版后端未升级 kind 字段时默认视为 approval。
export type PendingInterruptItem = {
  kind?: "approval" | "question";
  interrupt_id: string;
  call_id?: string;
  tool?: string;
  args_json?: string;
  // 审批依据的 effect（后端 internal/effect 的序列化结果）。可能缺失：
  // 这个字段上线前落盘的 pending 行没有它，卡片回退到按 args_json 渲染。
  effect_json?: string;
  questions_json?: string;
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
): Promise<{ handled: boolean }> {
  const res = await fetch(
    `${API_BASE}/conversations/${encodeURIComponent(conversationID)}/questions/${encodeURIComponent(interruptID)}`,
    {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(payload),
    },
  );
  if (res.status === 404) return { handled: false };
  if (!res.ok) throw new Error(`question ${res.status}`);
  return { handled: true };
}

// The set of per-conversation approval modes. Kept in sync with backend
// approval.Mode — extending here without extending backend will 400 on POST.
export type ApprovalMode = "default" | "auto" | "full_access";

export async function getApprovalMode(
  conversationID: string,
): Promise<ApprovalMode> {
  const res = await fetch(
    `${API_BASE}/conversations/${encodeURIComponent(conversationID)}/approval-mode`,
  );
  if (!res.ok) throw new Error(`getApprovalMode: ${res.status}`);
  const data = (await res.json()) as { mode?: string };
  return (data.mode as ApprovalMode) ?? "default";
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

export function workspaceDownloadURL(
  conversationId: string,
  path: string,
  opts?: { projectId?: string },
): string {
  const params = new URLSearchParams({ path });
  if (opts?.projectId) params.set("project_id", opts.projectId);
  return `${API_BASE}/conversations/${encodeURIComponent(conversationId)}/workspace/download?${params.toString()}`;
}

export function workspaceInlineURL(
  conversationId: string,
  path: string,
  opts?: { projectId?: string },
): string {
  const params = new URLSearchParams({ path });
  if (opts?.projectId) params.set("project_id", opts.projectId);
  return `${API_BASE}/conversations/${encodeURIComponent(conversationId)}/workspace/inline?${params.toString()}`;
}
