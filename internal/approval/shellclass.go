package approval

import (
	"strings"

	"github.com/guyi-a/Interview-Agent/internal/effect"
)

// ClassifyShellCommand puts a shell command into one of three risk tiers.
//
// The two existing rule sets each answer half the question — the destructive
// wall says "is this irreversible", the auto fast path says "is this purely
// read-only" — and neither alone is what an effect needs. This composes them
// in the order that matters: destructive wins over harmless, so a pipeline
// with one dangerous segment is destructive no matter how tame the rest is.
//
// Takes the raw command string rather than the tool's arguments blob: effect
// derivation already has the command in hand, and round-tripping it back
// through JSON only to re-parse it here would be silly.
func ClassifyShellCommand(command string) effect.Classification {
	cmd := strings.TrimSpace(command)
	if cmd == "" {
		// The tool rejects this on its own. Not harmless — there is nothing
		// here to call harmless — but nothing irreversible either.
		return effect.Normal
	}
	// Parse failure resolves to destructive inside classifyShellCommand: an
	// unusual quoting pattern we can't read is exactly what we must not wave
	// through.
	if bad, _ := classifyShellCommand(cmd); bad {
		return effect.Destructive
	}
	if ok, _ := isReadOnlyShellCommand(cmd); ok {
		return effect.Harmless
	}
	return effect.Normal
}
