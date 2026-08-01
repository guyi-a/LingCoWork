package compaction

import (
	"context"
	"log"
	"strings"

	"github.com/guyi-a/Interview-Agent/internal/config"
	"github.com/guyi-a/Interview-Agent/internal/repository/model"
)

// Repo is the persistence surface the compactor needs.
type Repo interface {
	Latest(ctx context.Context, conversationID string) (*model.Compaction, error)
	Append(ctx context.Context, c *model.Compaction) error
}

type Compactor struct {
	repo       Repo
	summarizer *Summarizer
	cfg        config.CompactionConfig
}

// New returns nil when compaction is not configured. A nil *Compactor is a
// valid no-op value — every method tolerates it — so callers never have to
// branch on whether the feature is on.
func New(repo Repo, cfg config.CompactionConfig) *Compactor {
	s := NewSummarizer(cfg)
	if s == nil || repo == nil {
		return nil
	}
	return &Compactor{repo: repo, summarizer: s, cfg: cfg}
}

// Active returns the compaction currently governing this conversation's LLM
// context projection, or nil if there is none.
func (c *Compactor) Active(ctx context.Context, convID string) *model.Compaction {
	if c == nil {
		return nil
	}
	active, err := c.repo.Latest(ctx, convID)
	if err != nil {
		log.Printf("compaction: load active (convID=%s): %v", convID, err)
		return nil
	}
	return active
}

// MaybeCompact folds history when the estimate crosses the threshold and
// returns the new anchor, or nil when nothing was folded.
//
// Best-effort by contract: below threshold, nothing to fold, or a failed
// summarization all return (nil, nil). Compaction must never be the reason a
// turn does not run — the worst case of skipping is the pre-existing
// behaviour of sending the full history.
//
// `rows` must be the history as of the turn boundary, loaded BEFORE the new
// user message is written, so the incoming message can never be folded into
// a summary of itself.
func (c *Compactor) MaybeCompact(ctx context.Context, convID string, rows []model.Message) *model.Compaction {
	if c == nil || len(rows) == 0 {
		return nil
	}

	active := c.Active(ctx, convID)

	estimated := EstimateTokens(rows, active, c.cfg)
	threshold := ThresholdTokens(c.cfg)
	if estimated < threshold {
		return nil
	}

	plan, ok := Split(rows, active, c.cfg.KeepLastUserTurns)
	if !ok {
		return nil
	}

	log.Printf("compaction: convID=%s estimated=%d threshold=%d folding=%d rows through seq=%d",
		convID, estimated, threshold, len(plan.Folded), plan.ThroughSeq)

	summary, err := c.summarizer.Summarize(ctx, plan.Folded, plan.PriorSummary)
	if err != nil {
		log.Printf("compaction: summarize failed (convID=%s), continuing uncompacted: %v", convID, err)
		return nil
	}
	if strings.TrimSpace(summary) == "" {
		log.Printf("compaction: empty summary (convID=%s), continuing uncompacted", convID)
		return nil
	}

	rec := &model.Compaction{
		ConversationID:  convID,
		ThroughSeq:      plan.ThroughSeq,
		Summary:         summary,
		ReplacedCount:   len(plan.Folded),
		EstimatedTokens: estimated,
	}
	if err := c.repo.Append(ctx, rec); err != nil {
		// The summary exists but could not be stored. Returning it anyway
		// would apply a fold this turn that vanishes on reload, so the
		// projection and the persisted history would disagree.
		log.Printf("compaction: persist failed (convID=%s), continuing uncompacted: %v", convID, err)
		return nil
	}
	return rec
}

// SummaryMessage renders the stored summary as the synthetic user message
// that stands in for everything the compaction folded away.
func SummaryMessage(c *model.Compaction) string {
	if c == nil {
		return ""
	}
	return wrapSummary(c.ID, c.Summary)
}
