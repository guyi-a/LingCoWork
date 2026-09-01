package compaction

import (
	"github.com/guyi-a/Interview-Agent/internal/repository/model"
)

// Plan describes one fold: which rows collapse into a summary, where the new
// anchor lands, and what earlier summary to carry forward.
type Plan struct {
	// Folded are the rows to hand to the summarizer.
	Folded []model.Message
	// ThroughSeq becomes the new anchor.
	ThroughSeq int
	// PriorSummary is the previous compaction's text, or "" on a first fold.
	PriorSummary string
}

// Split picks the fold boundary within the not-yet-folded rows.
//
// Tool-call integrity is the whole game here: OpenAI and DeepSeek reject a
// history where an assistant row carrying tool_calls is not immediately
// followed by its tool rows, so a cut must never land inside a turn.
//
// The default (KeepLastUserTurns == 0) folds every row present, which is
// inherently safe because every row present is folded. When keeping recent
// turns, the cut is instead
// pushed back to just before a user row, since a user row is the only point
// guaranteed not to sit between a tool_call and its result.
//
// Returns ok=false when there is nothing worth folding.
func Split(rows []model.Message, active *model.Compaction, keepLastUserTurns int) (Plan, bool) {
	scope := ActiveRows(rows, active)
	if len(scope) == 0 {
		return Plan{}, false
	}

	prior := ""
	if active != nil {
		prior = active.Summary
	}

	cut := len(scope)
	if keepLastUserTurns > 0 {
		userIdx := make([]int, 0, len(scope))
		for i, r := range scope {
			if r.Role == "user" {
				userIdx = append(userIdx, i)
			}
		}
		// Fewer user turns than we promised to keep: nothing may be folded.
		if len(userIdx) <= keepLastUserTurns {
			return Plan{}, false
		}
		cut = userIdx[len(userIdx)-keepLastUserTurns]
	}

	if cut <= 0 {
		return Plan{}, false
	}
	folded := scope[:cut]

	// Folding a lone user row would replace it with a summary of a question
	// nobody answered yet — strictly worse than leaving it alone.
	if !hasAssistantContent(folded) {
		return Plan{}, false
	}

	return Plan{
		Folded:       folded,
		ThroughSeq:   folded[len(folded)-1].Seq,
		PriorSummary: prior,
	}, true
}

func hasAssistantContent(rows []model.Message) bool {
	for _, r := range rows {
		if r.Role == "assistant" || r.Role == "tool" {
			return true
		}
	}
	return false
}
