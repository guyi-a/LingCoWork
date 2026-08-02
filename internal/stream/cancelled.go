package stream

import (
	"encoding/json"
	"strings"
)

// A tool call can end up cancelled in two ways, and neither produces a Go
// error the pipeline could carry: the approval middleware answers a denial
// payload as if the tool had returned it, and a cancelled run gets its
// unfinished calls back-filled with a placeholder so the persisted history
// stays a valid tool_use ↔ tool_result pairing.
//
// Both of those payloads are ours, with a shape we chose. Recognising them
// here — once, on the way out — is what lets everything downstream read a
// boolean instead of guessing from text. The frontend used to do the guessing
// with a substring search for "cancel", which quietly mislabelled any tool
// whose output merely discussed cancellation.

// CanceledPlaceholderPrefix opens the result written for a call that never
// ran because the run was cancelled.
const CanceledPlaceholderPrefix = "[canceled]"

// IsCanceledResult reports whether a tool result is one of our own
// cancellation envelopes.
//
// The JSON branch requires `canceled` to be true at the top level of an
// object, which is the denial payload's shape. A third-party tool would have
// to return that exact structure to be mistaken for one — as opposed to the
// old rule, which only required the word to appear anywhere in the output.
func IsCanceledResult(content string) bool {
	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		return false
	}
	if strings.HasPrefix(trimmed, CanceledPlaceholderPrefix) {
		return true
	}
	if !strings.HasPrefix(trimmed, "{") {
		return false
	}
	var probe struct {
		Canceled *bool `json:"canceled"`
	}
	if err := json.Unmarshal([]byte(trimmed), &probe); err != nil {
		return false
	}
	return probe.Canceled != nil && *probe.Canceled
}
