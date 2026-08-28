package compaction

import (
	"regexp"

	"github.com/guyi-a/Interview-Agent/internal/config"
	"github.com/guyi-a/Interview-Agent/internal/repository/model"
)

const estimatedImageTokens = 384

var imageMarkerRE = regexp.MustCompile(`(?m)^\[image:\s*.+?\]\s*$`)

// EstimateTokens approximates how much context the next turn would carry.
//
// Hybrid strategy, as in klingwork: anchor on the provider's own token count
// from the most recent root assistant row, then character-estimate only the
// rows after it. That keeps the guesswork confined to a short tail instead of
// letting a chars/4 approximation compound over the whole conversation.
//
// Rows already folded by `active` are excluded, but the summary that replaced
// them is counted — it is real context the model will receive.
//
// Deliberately NOT counted: system prompt, workspace context, tool schemas.
// klingwork omits them too; Config.BufferTokens is what covers them.
func EstimateTokens(rows []model.Message, active *model.Compaction, cfg config.CompactionConfig) int {
	scope := ActiveRows(rows, active)

	// A usage figure recorded before the fold describes a context that no
	// longer exists, so only rows inside the current scope may anchor.
	anchorIdx := -1
	for i := len(scope) - 1; i >= 0; i-- {
		if scope[i].TotalTokens > 0 {
			anchorIdx = i
			break
		}
	}

	cpt := charsPerToken(cfg)
	if anchorIdx >= 0 {
		return scope[anchorIdx].TotalTokens + roughTokens(scope[anchorIdx+1:], cpt)
	}

	total := roughTokens(scope, cpt)
	if active != nil {
		total += len(active.Summary) / cpt
	}
	return total
}

// ActiveRows drops the rows a compaction has already folded away.
func ActiveRows(rows []model.Message, active *model.Compaction) []model.Message {
	if active == nil {
		return rows
	}
	for i, r := range rows {
		if r.Seq > active.ThroughSeq {
			return rows[i:]
		}
	}
	return nil
}

func roughTokens(rows []model.Message, charsPerToken int) int {
	chars := 0
	imageTokens := 0
	for _, r := range rows {
		chars += len(r.Content) + len(r.ReasoningContent) + len(r.ToolCalls)
		imageTokens += len(imageMarkerRE.FindAllString(r.Content, -1)) * estimatedImageTokens
	}
	return chars/charsPerToken + imageTokens
}
