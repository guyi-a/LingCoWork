package mcp

import (
	"github.com/guyi-a/Interview-Agent/internal/effect"
	mcpgo "github.com/mark3labs/mcp-go/mcp"
)

// What a remote tool does is unknowable from here — it runs on someone else's
// machine and the only description of it is the server's own. So the effect
// records the claim and who made it, and the policy decides how much a claim
// from that source is worth.
//
// Trust is asymmetric. A server saying "I am destructive" is believed
// immediately: there is no incentive to lie in that direction, and being
// wrong costs a prompt. A server saying "I am read-only" is believed only
// once the user has vouched for it, because that is the direction an attacker
// would lie in and the MCP spec is explicit that unverified annotations must
// not be trusted.

// toolEffect describes one remote tool. It does not depend on the call's
// arguments — the annotations belong to the tool, not the invocation — so the
// result is registered with effect.Static.
func toolEffect(srv ServerConfig, t mcpgo.Tool) effect.Effect {
	a := t.Annotations
	e := effect.Effect{
		Kind:             effect.KindMCPCall,
		Server:           srv.Name,
		RemoteTool:       t.Name,
		Transport:        string(srv.Transport()),
		ReadOnly:         hint(a.ReadOnlyHint),
		OpenWorld:        hint(a.OpenWorldHint),
		Destructive:      hint(a.DestructiveHint),
		TrustAnnotations: srv.TrustAnnotations,
		AutoApproved:     srv.AutoApproves(t.Name),
	}
	if srv.Transport() == TransportHTTP {
		e.URL = srv.URL
	}
	e.Note = noteFor(e, a)
	return e
}

// hint reads a tri-state annotation. A nil pointer means the server said
// nothing, which is not the same as saying "false" and must not become one:
// "unstated" is what keeps a tool out of the read-only fast path.
func hint(b *bool) bool {
	return b != nil && *b
}

// noteFor explains, in one line on the approval card, why this call is or is
// not being gated.
func noteFor(e effect.Effect, a mcpgo.ToolAnnotation) string {
	switch {
	case e.AutoApproved && e.Destructive:
		return "server declares this destructive, but you listed it in autoApprove"
	case e.AutoApproved:
		return "you listed this tool in autoApprove for this server"
	case e.Destructive:
		return "server declares this tool may make destructive changes"
	case e.ReadOnly && e.TrustAnnotations:
		return "server declares this read-only and you trust this server's annotations"
	case e.ReadOnly:
		return "server declares this read-only, but the server is not trusted — set trustAnnotations to act on it"
	case a.ReadOnlyHint == nil && a.DestructiveHint == nil && a.OpenWorldHint == nil:
		return "server publishes no annotations for this tool"
	default:
		return "server does not declare this tool read-only"
	}
}
