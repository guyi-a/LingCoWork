// Package approval implements human-in-the-loop tool call review on top of
// eino's tool.Interrupt / Runner.ResumeWithParams primitives.
//
// Layout:
//   - policy.go      — which tool calls need approval
//   - middleware.go  — compose.ToolMiddleware that pauses via tool.Interrupt
//   - pending.go     — in-memory map of pending approvals keyed by conv id
//   - types.go       — Decision + gob registration for resume payloads
package approval

import "encoding/gob"

// Decision is what the HTTP layer POSTs back and what the middleware pulls
// from tool.GetResumeContext on resume. Registered for gob so eino can
// persist it through the checkpoint store.
type Decision struct {
	Approved bool
	// EffectDigest binds an approval to the exact derived target shown in the
	// pending card. Empty only for checkpoints created by older builds.
	EffectDigest string
	// Reason is user-supplied text shown to the model when a call is denied,
	// so the agent's next ReAct turn can adjust rather than silently give up.
	// Ignored when Approved is true.
	Reason string
}

func init() {
	gob.Register(Decision{})
}
