// Package compaction folds an over-long conversation history into a
// structured summary so the next turn still fits the model's context window.
//
// Design (ported from klingwork-app's context compaction): compaction is
// APPEND-ONLY. Message rows are never deleted or rewritten. A compaction
// record says "messages up to Seq N are now represented by this summary",
// and the LLM context is PROJECTED at read time — folded rows are swapped
// for one synthetic user message carrying the summary. The UI keeps
// rendering the raw history and just draws a marker at the fold point.
//
// This runs strictly between turns, never inside a live agent run.
package compaction

import (
	"github.com/guyi-a/Interview-Agent/internal/config"
)

// ThresholdTokens is the estimated context size at which compaction fires.
// With the defaults: floor(128000*0.85) - 8000 - 4000 = 96800.
//
// The floor keeps a nonsensical configuration (tiny window, huge reserve)
// from producing a threshold so low that every turn compacts.
func ThresholdTokens(c config.CompactionConfig) int {
	t := int(float64(c.WindowNominalTokens)*c.WindowUsableRatio) -
		c.ReservedOutputTokens - c.BufferTokens
	if t < 1024 {
		return 1024
	}
	return t
}

// charsPerToken guards a zero/negative config value from reaching a
// division. Estimation runs on every turn, so it must never panic.
func charsPerToken(c config.CompactionConfig) int {
	if c.CharsPerToken <= 0 {
		return 4
	}
	return c.CharsPerToken
}
