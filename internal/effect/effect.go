// Package effect describes what a tool call will actually DO, independent of
// which tool is doing it.
//
// The approval layer used to switch on tool names. That works only while the
// tool set is closed and authored in-repo; the moment third-party tools
// (MCP servers) join, an unknown name falls through to "no approval needed"
// and the whole gate is bypassed silently. Effects invert that: every call is
// described by its consequence, the policy is a pure function of that
// description, and anything we cannot describe becomes KindUnknown — which
// the policy treats as "always ask".
//
// Go has no discriminated unions, so Effect is one flat struct with a Kind
// discriminator and optional per-kind fields. That costs compile-time
// exhaustiveness (the policy's default branch covers it) and buys a shape
// that marshals straight to the SSE frame the approval card reads.
package effect

import (
	"context"
	"encoding/json"
)

// Kind is the consequence category. Policy decisions switch on this.
type Kind string

const (
	KindFileRead  Kind = "filesystem-read"
	KindFileWrite Kind = "filesystem-write"
	// KindFileStructure creates directories only: no content written, nothing
	// overwritten. Split out from KindFileWrite so mkdir / create_workspace
	// keep running unprompted — gating create_workspace in particular would
	// stall the bootstrap of every new conversation.
	KindFileStructure Kind = "filesystem-structure"
	KindFileTransfer  Kind = "filesystem-transfer"
	KindProcessExec   Kind = "process-exec"
	KindNetwork       Kind = "network-request"
	KindSkillLoad     Kind = "skill-load"
	// KindUserInteraction is the tool asking the human something. Approving it
	// would be redundant — the interaction IS the approval. The tool raises
	// its own question interrupt downstream.
	KindUserInteraction Kind = "user-interaction"
	KindReadOnlyQuery   Kind = "readonly-query"
	// KindDelegate is a sub-agent invoked as a tool. It has no side effect of
	// its own; whatever the sub-agent does internally passes through that
	// sub-agent's own approval middleware.
	KindDelegate Kind = "delegate-agent"
	// KindUnknown is the fail-closed bucket: no deriver registered, or the
	// deriver errored. Policy always asks.
	KindUnknown Kind = "unknown"
)

// Scope says whether a path lands inside the conversation's workspace.
type Scope string

const (
	ScopeWorkspace Scope = "workspace"
	ScopeExternal  Scope = "external"
)

// Classification is the risk tier of a shell command.
type Classification string

const (
	// Harmless: every sub-command is on the read-only whitelist.
	Harmless Classification = "harmless"
	// Normal: does something, but nothing irreversible.
	Normal Classification = "normal"
	// Destructive: irreversible or unparseable. Always routed to a human,
	// even in full_access.
	Destructive Classification = "destructive"
)

// Effect describes one tool call's consequence.
//
// Only the fields relevant to Kind are populated; the rest stay zero and are
// omitted from JSON. Keep this struct JSON-stable — it crosses the SSE wire
// and is persisted on pending_approvals rows.
type Effect struct {
	Kind Kind `json:"kind"`
	// Scope is the decision-relevant scope. For multi-path effects it is the
	// worst case across all paths involved.
	Scope Scope `json:"scope,omitempty"`

	// filesystem-*: Path is already resolved to an absolute path.
	Path string `json:"path,omitempty"`
	// PathScope / DestScope keep both sides of a transfer visible. A single
	// worst-case Scope cannot tell the approval card whether the source or
	// the destination is the one leaving the workspace, and the classifier
	// needs the same distinction to judge `cp secret.txt /tmp` differently
	// from `cp /tmp/data.csv ./`.
	PathScope Scope  `json:"path_scope,omitempty"`
	DestPath  string `json:"dest_path,omitempty"`
	DestScope Scope  `json:"dest_scope,omitempty"`

	// Destructive marks an irreversible deletion. Set by the deriver at the
	// same granularity as the shell destructive wall: a recursive remove or
	// one targeting a dangerous path, not every rm.
	Destructive bool `json:"destructive,omitempty"`

	// process-exec
	Command        string         `json:"command,omitempty"`
	Cwd            string         `json:"cwd,omitempty"`
	Classification Classification `json:"classification,omitempty"`

	// network-request
	URL string `json:"url,omitempty"`

	// delegate-agent
	Agent string `json:"agent,omitempty"`

	// Note carries the derivation failure reason for KindUnknown, or any
	// extra context worth showing on the approval card. Never load-bearing
	// for policy.
	Note string `json:"note,omitempty"`
}

// Unknown builds the fail-closed effect with a diagnostic note.
func Unknown(note string) Effect {
	return Effect{Kind: KindUnknown, Note: note}
}

// JSON renders the effect for the wire. Marshal on a plain struct of strings
// and bools cannot fail; an error would still produce "" rather than panic.
func (e Effect) JSON() string {
	b, err := json.Marshal(e)
	if err != nil {
		return ""
	}
	return string(b)
}

// WorstScope returns the more permissive-to-deny of two scopes: if either
// side is external, the pair is external.
func WorstScope(a, b Scope) Scope {
	if a == ScopeExternal || b == ScopeExternal {
		return ScopeExternal
	}
	return ScopeWorkspace
}

// Deriver turns one tool call's raw arguments into its effect. Derivers are
// pure functions of (ctx, args) plus whatever the closure captured at
// registration — they must not mutate anything, because they run before the
// approval decision and may run on a call that is ultimately denied.
type Deriver func(ctx context.Context, argsJSON string) (Effect, error)

// Static builds a deriver for a tool whose effect doesn't depend on its
// arguments — a sub-agent delegation, a clock read, a question to the user.
func Static(e Effect) Deriver {
	return func(context.Context, string) (Effect, error) { return e, nil }
}

// Registry maps tool name to deriver. Built once at startup and read
// concurrently by every agent's approval middleware, so Register must not be
// called after the agents are wired.
type Registry struct {
	byTool map[string]Deriver
}

func NewRegistry() *Registry {
	return &Registry{byTool: make(map[string]Deriver)}
}

// Register attaches a deriver to a tool name. This is also the MCP mount
// point: when an MCP server connects, register a deriver per remote tool
// built from the server's declared capabilities. Remote tools with no usable
// metadata should be left unregistered so they land in KindUnknown and get
// human review.
func (r *Registry) Register(tool string, d Deriver) {
	if r == nil || tool == "" || d == nil {
		return
	}
	r.byTool[tool] = d
}

// Has reports whether a deriver is registered. Used by tests and startup
// checks to catch a tool that shipped without one.
func (r *Registry) Has(tool string) bool {
	if r == nil {
		return false
	}
	_, ok := r.byTool[tool]
	return ok
}

// Derive never returns an error: an unregistered tool and a failing deriver
// both resolve to KindUnknown, which the policy always routes to a human.
// Returning an error instead would force every caller to decide what to do
// with it, and the only safe answer is the one baked in here.
func (r *Registry) Derive(ctx context.Context, tool, argsJSON string) Effect {
	if r == nil {
		return Unknown("no effect registry wired")
	}
	d, ok := r.byTool[tool]
	if !ok {
		return Unknown("no effect deriver registered for " + tool)
	}
	e, err := d(ctx, argsJSON)
	if err != nil {
		return Unknown("effect derivation failed for " + tool + ": " + err.Error())
	}
	if e.Kind == "" {
		return Unknown("effect deriver for " + tool + " returned an empty kind")
	}
	return e
}
