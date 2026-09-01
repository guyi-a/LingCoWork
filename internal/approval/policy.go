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
// conversation is in, including auto. Callers must consult this
// BEFORE checking the mode, or elevation becomes a way to skip it.
//
// These include irreversible operations, because auto is a
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
	if e.Kind == effect.KindMemoryWrite {
		return true, "changes long-term memory"
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
		// A permanent prompt on every call, unskippable even in auto
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

// HasNoSideEffect identifies operations that should never spend user
// attention, even in manual mode.
func HasNoSideEffect(e effect.Effect) bool {
	switch e.Kind {
	case effect.KindReadOnlyQuery,
		effect.KindUserInteraction,
		effect.KindAgentState,
		effect.KindSkillLoad:
		return true

	case effect.KindDelegate:
		return true

	case effect.KindFileStructure:
		return true

	case effect.KindFileRead:
		return e.Scope == effect.ScopeWorkspace

	case effect.KindProcessExec:
		return e.Classification == effect.Harmless

	case effect.KindMCPCall:
		return e.AutoApproved

	default:
		return false
	}
}

// IsSensitiveCall returns whether a write touches a protected path or carries
// credential-looking content. The effect intentionally does not carry file
// bodies across SSE/SQLite, so raw arguments are inspected only in-process.
func IsSensitiveCall(e effect.Effect, argsJSON string) (bool, string) {
	if e.Kind != effect.KindFileWrite && e.Kind != effect.KindFileTransfer {
		return false, ""
	}
	for _, p := range []string{e.Path, e.DestPath} {
		if p == "" {
			continue
		}
		if reason, hit := pathIsSensitive(p); hit {
			return true, reason
		}
	}
	if reason, hit := contentLooksSensitive(writtenContent(argsJSON)); hit {
		return true, reason
	}
	return false, ""
}

// ShouldAsk is the deterministic three-mode policy. Callers must run
// MustAsk/IsSensitiveCall first because those are the safety wall shared by
// every mode.
func ShouldAsk(mode Mode, e effect.Effect) bool {
	if HasNoSideEffect(e) {
		return false
	}
	switch mode {
	case ModeAuto:
		return false
	case ModeAcceptWrite:
		switch e.Kind {
		case effect.KindFileRead, effect.KindFileWrite, effect.KindFileTransfer:
			return e.Scope == effect.ScopeExternal
		case effect.KindProcessExec:
			return e.Classification != effect.Harmless
		case effect.KindNetwork, effect.KindMCPCall:
			return e.PrivateHost
		default:
			return true
		}
	case ModeManual:
		return true
	default:
		return true
	}
}

// NeedsApproval remains as the manual-mode compatibility helper used by
// effect tests and callers that only need the conservative baseline.
func NeedsApproval(e effect.Effect) bool {
	if must, _ := MustAsk(e); must {
		return true
	}
	return ShouldAsk(ModeManual, e)
}

// IsSafeAuto is retained as a compatibility helper for tests outside this
// package. Auto is now deterministic: anything outside the safety wall is
// allowed, with no model classifier involved.
func IsSafeAuto(e effect.Effect, argsJSON string) (bool, string) {
	if must, why := MustAsk(e); must {
		return false, why
	}
	if sensitive, why := IsSensitiveCall(e, argsJSON); sensitive {
		return false, why
	}
	switch e.Kind {
	case effect.KindUnknown, effect.KindMemoryWrite:
		return false, "always-confirm effect"
	default:
		return true, "deterministic auto policy"
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
