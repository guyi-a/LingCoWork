package approval

import (
	"encoding/json"
	"strings"

	"github.com/guyi-a/Interview-Agent/internal/effect"
)

// The policy is a pure function of the effect. It never sees a tool name,
// which is the whole point: a name-based rule has to enumerate every tool
// that exists, and the first tool it doesn't know about — an MCP server's,
// say — falls through to "allowed". Enumerating consequences instead means
// the fallback is "unrecognised", and unrecognised asks.

// MustAsk reports the cases that go to a human no matter which mode the
// conversation is in, including full_access. Callers must consult this
// BEFORE checking the mode, or elevation becomes a way to skip it.
//
// Two things qualify. An irreversible operation, because full_access is a
// statement about trusting the agent's judgment, not about giving up the
// last chance to stop a `rm -rf`. And an effect we could not derive, because
// "we don't know what this does" is not a thing any mode should auto-approve.
func MustAsk(e effect.Effect) (bool, string) {
	if e.Kind == effect.KindUnknown {
		reason := "effect could not be derived"
		if e.Note != "" {
			reason = e.Note
		}
		return true, reason
	}
	if e.Destructive {
		// One exception, and only this one: the user named this exact tool
		// in a server's autoApprove list. That is the same decision the wall
		// exists to collect, taken deliberately and in advance.
		//
		// Without it the wall is unusable against MCP. The spec's default for
		// destructiveHint is true, and mcp-go stamps that default onto every
		// tool built with its helper, so a Go-based server typically declares
		// its entire tool set destructive without anyone having decided that.
		// A permanent prompt on every call, unskippable even in full_access
		// and with no way to say otherwise, is how a safety feature gets
		// clicked through on reflex.
		//
		// Nothing a server sends can set AutoApproved; it comes only from the
		// local config file.
		if e.AutoApproved {
			return false, ""
		}
		return true, "irreversible operation"
	}
	if e.Classification == effect.Destructive {
		return true, "destructive command"
	}
	return false, ""
}

// NeedsApproval reports whether an effect requires human approval in the
// modes that still consult policy — default, and auto before its fast path
// and classifier get a look. full_access has already returned by the time
// this is called, and MustAsk has already taken the cases no mode may skip,
// so this function deliberately takes no mode.
func NeedsApproval(e effect.Effect) bool {
	switch e.Kind {
	case effect.KindReadOnlyQuery,
		effect.KindUserInteraction,
		effect.KindAgentState,
		effect.KindSkillLoad,
		effect.KindNetwork:
		return false

	// A sub-agent has no side effect of its own. Everything it goes on to do
	// passes through its own copy of this middleware, so approving the
	// delegation would be approving nothing and would put a prompt in front
	// of every handoff.
	case effect.KindDelegate:
		return false

	// Creating a directory writes no content and destroys nothing.
	case effect.KindFileStructure:
		return false

	// Reading inside the workspace is the agent's own scratch space. Reading
	// outside it is the agent reaching into the user's machine, and that is
	// worth a look even though nothing is modified.
	case effect.KindFileRead:
		return e.Scope == effect.ScopeExternal

	case effect.KindFileWrite, effect.KindFileTransfer, effect.KindProcessExec:
		return true

	// An MCP tool is a black box. "Read-only" is the server's own word for
	// itself and anyone can say it, so it buys nothing until the user has
	// vouched for that server; naming the tool in autoApprove is the user
	// saying it directly and needs no corroboration.
	//
	// A server can declare a tool both read-only and destructive, and that is
	// not a hypothetical: mcp-go stamps destructiveHint onto every tool built
	// with its helper, so an author who sets readOnlyHint by hand produces
	// exactly this pair. Destructive wins, and only autoApprove overrides it
	// — the same rule MustAsk applies. Repeating it here rather than leaning
	// on MustAsk running first is the point: this function is documented as a
	// pure function of the effect, and one that answers "no approval needed"
	// for a self-declared destructive call is a landmine for whoever next
	// reorders the middleware.
	//
	// OpenWorld is recorded on the effect but deliberately not consulted.
	// KindNetwork above returns false, meaning web_search and web_fetch —
	// read-only calls to the open internet — already run unprompted. Holding
	// a server the user explicitly trusted to a stricter standard than our
	// own search tool would not be defensible.
	case effect.KindMCPCall:
		if e.AutoApproved {
			return false
		}
		return !(e.ReadOnly && e.TrustAnnotations && !e.Destructive)

	// Includes KindUnknown, which MustAsk has normally already caught. Any
	// kind added later lands here too: a new consequence is gated until
	// someone decides otherwise.
	default:
		return true
	}
}

// IsSafeAuto is the auto-mode fast path: a rule-based recogniser for the
// obviously-boring subset of gated calls, so the common case doesn't pay for
// an LLM round trip. Returning true skips both the classifier and the human.
//
// A false is not a verdict of "unsafe" — it means we have no cheap
// deterministic answer and the classifier should decide. The reason string
// is for logs only, never for the model.
//
// argsJSON comes along because the effect deliberately doesn't carry file
// CONTENT: effects cross the SSE wire and get persisted, and putting a
// megabyte of file body in one would be wasteful at best.
func IsSafeAuto(e effect.Effect, argsJSON string) (bool, string) {
	switch e.Kind {
	case effect.KindFileWrite, effect.KindFileTransfer:
		if e.Scope != effect.ScopeWorkspace {
			return false, "target outside the workspace"
		}
		for _, p := range []string{e.Path, e.DestPath} {
			if p == "" {
				continue
			}
			if reason, hit := pathIsSensitive(p); hit {
				return false, reason
			}
		}
		if reason, hit := contentLooksSensitive(writtenContent(argsJSON)); hit {
			return false, reason
		}
		return true, "workspace write, no sensitive path or content"

	case effect.KindProcessExec:
		if e.Classification == effect.Harmless {
			return true, "read-only command"
		}
		return false, "command is not read-only"

	// An external read is exactly the judgment call the classifier exists
	// for — whether this particular path outside the workspace is one the
	// user would expect the agent to open.
	case effect.KindFileRead:
		return false, "read outside the workspace"

	default:
		return false, "no fast-path rule for this effect"
	}
}

// writtenContent gathers every argument field that can carry a payload into
// a file, including the added lines carried by apply_patch.
func writtenContent(argsJSON string) string {
	if strings.TrimSpace(argsJSON) == "" {
		return ""
	}
	var probe struct {
		Content string `json:"content"`
		Patch   string `json:"patch"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &probe); err != nil {
		return ""
	}
	return probe.Content + "\n" + patchAddedContent(probe.Patch)
}

func patchAddedContent(patch string) string {
	var added strings.Builder
	for _, line := range strings.Split(strings.ReplaceAll(patch, "\r\n", "\n"), "\n") {
		if strings.HasPrefix(line, "+") {
			added.WriteString(line[1:])
			added.WriteByte('\n')
		}
	}
	return added.String()
}
