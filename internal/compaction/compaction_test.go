package compaction

import (
	"strings"
	"testing"

	"github.com/guyi-a/Interview-Agent/internal/config"
	"github.com/guyi-a/Interview-Agent/internal/repository/model"
)

func testCfg() config.CompactionConfig {
	return config.CompactionConfig{
		APIKey:                           "k",
		BaseURL:                          "https://example.invalid",
		Model:                            "m",
		WindowNominalTokens:              1000000,
		WindowUsableRatio:                0.90,
		ReservedOutputTokens:             32000,
		BufferTokens:                     20000,
		CharsPerToken:                    4,
		ToolResultTruncateThresholdChars: 100,
		ToolResultTruncateKeepChars:      20,
	}
}

func TestThresholdTokens_Defaults(t *testing.T) {
	if got := ThresholdTokens(testCfg()); got != 848000 {
		t.Fatalf("threshold=%d want 848000", got)
	}
}

func TestThresholdTokens_FloorsAtAbsurdConfig(t *testing.T) {
	cfg := testCfg()
	cfg.WindowNominalTokens = 4000
	cfg.ReservedOutputTokens = 8000
	if got := ThresholdTokens(cfg); got != 1024 {
		t.Fatalf("threshold=%d want floor 1024", got)
	}
}

func TestEstimateTokens_UsesUsageAnchorPlusTail(t *testing.T) {
	cfg := testCfg()
	rows := []model.Message{
		{Seq: 1, Role: "user", Content: strings.Repeat("x", 4000)},
		{Seq: 2, Role: "assistant", Content: "done", TotalTokens: 5000},
		{Seq: 3, Role: "user", Content: strings.Repeat("y", 400)},
	}
	// Anchor 5000 covers seq 1-2; only seq 3 (400 chars / 4) is estimated.
	if got := EstimateTokens(rows, nil, cfg); got != 5100 {
		t.Fatalf("estimate=%d want 5100", got)
	}
}

func TestEstimateTokens_FallsBackToCharsWithoutAnchor(t *testing.T) {
	cfg := testCfg()
	rows := []model.Message{
		{Seq: 1, Role: "user", Content: strings.Repeat("x", 800)},
		{Seq: 2, Role: "assistant", Content: strings.Repeat("y", 400)},
	}
	if got := EstimateTokens(rows, nil, cfg); got != 300 {
		t.Fatalf("estimate=%d want 300", got)
	}
}

func TestEstimateTokensCountsNativeImageMarkers(t *testing.T) {
	cfg := testCfg()
	rows := []model.Message{{
		Seq:     1,
		Role:    "user",
		Content: "[image: /tmp/a.png]\n[image: /tmp/b.jpg]\nquestion",
	}}
	want := len(rows[0].Content)/cfg.CharsPerToken + 2*estimatedImageTokens
	if got := EstimateTokens(rows, nil, cfg); got != want {
		t.Fatalf("estimate=%d want %d", got, want)
	}
}

// A usage figure recorded before the fold describes a context that no longer
// exists. Anchoring on it would report the pre-compaction size forever and
// re-trigger compaction on every subsequent turn.
func TestEstimateTokens_IgnoresAnchorBeforeFold(t *testing.T) {
	cfg := testCfg()
	rows := []model.Message{
		{Seq: 1, Role: "user", Content: "hi"},
		{Seq: 2, Role: "assistant", Content: "old", TotalTokens: 90000},
		{Seq: 3, Role: "user", Content: strings.Repeat("z", 400)},
	}
	active := &model.Compaction{ID: 1, ThroughSeq: 2, Summary: strings.Repeat("s", 400)}
	// 400 chars of live rows + 400 chars of summary, both /4.
	if got := EstimateTokens(rows, active, cfg); got != 200 {
		t.Fatalf("estimate=%d want 200 (stale 90000 anchor must be ignored)", got)
	}
}

func TestActiveRows_DropsFoldedPrefix(t *testing.T) {
	rows := []model.Message{{Seq: 1}, {Seq: 2}, {Seq: 3}, {Seq: 4}}
	got := ActiveRows(rows, &model.Compaction{ThroughSeq: 2})
	if len(got) != 2 || got[0].Seq != 3 || got[1].Seq != 4 {
		t.Fatalf("active=%+v want seq 3,4", got)
	}
	if all := ActiveRows(rows, nil); len(all) != 4 {
		t.Fatalf("nil compaction should keep everything, got %d", len(all))
	}
}

func TestSplit_FoldsEverythingByDefault(t *testing.T) {
	rows := []model.Message{
		{Seq: 1, Role: "user", Content: "a"},
		{Seq: 2, Role: "assistant", Content: "b"},
		{Seq: 3, Role: "user", Content: "c"},
		{Seq: 4, Role: "assistant", Content: "d"},
	}
	plan, ok := Split(rows, nil, 0)
	if !ok {
		t.Fatal("expected a plan")
	}
	if len(plan.Folded) != 4 || plan.ThroughSeq != 4 {
		t.Fatalf("folded=%d through=%d want 4/4", len(plan.Folded), plan.ThroughSeq)
	}
	if plan.PriorSummary != "" {
		t.Fatalf("prior=%q want empty on first fold", plan.PriorSummary)
	}
}

// The cut must land on a user row so it can never fall between an assistant's
// tool_calls and the tool rows answering them.
func TestSplit_KeepLastUserTurnsCutsAtUserBoundary(t *testing.T) {
	rows := []model.Message{
		{Seq: 1, Role: "user", Content: "a"},
		{Seq: 2, Role: "assistant", ToolCalls: `[{"id":"c1"}]`},
		{Seq: 3, Role: "tool", ToolCallID: "c1", Content: "r"},
		{Seq: 4, Role: "assistant", Content: "b"},
		{Seq: 5, Role: "user", Content: "c"},
		{Seq: 6, Role: "assistant", Content: "d"},
	}
	plan, ok := Split(rows, nil, 1)
	if !ok {
		t.Fatal("expected a plan")
	}
	if plan.ThroughSeq != 4 {
		t.Fatalf("through=%d want 4 (cut before the kept user row)", plan.ThroughSeq)
	}
	for _, r := range plan.Folded {
		if r.Seq >= 5 {
			t.Fatalf("kept turn leaked into fold: %+v", r)
		}
	}
}

func TestSplit_RefusesWhenNotEnoughUserTurnsToKeep(t *testing.T) {
	rows := []model.Message{
		{Seq: 1, Role: "user", Content: "a"},
		{Seq: 2, Role: "assistant", Content: "b"},
	}
	if _, ok := Split(rows, nil, 1); ok {
		t.Fatal("must not fold the only turn we promised to keep")
	}
}

// Folding a lone unanswered user row would summarize a question nobody
// replied to — strictly worse than leaving it alone.
func TestSplit_RefusesUserOnlyScope(t *testing.T) {
	rows := []model.Message{{Seq: 1, Role: "user", Content: "a"}}
	if _, ok := Split(rows, nil, 0); ok {
		t.Fatal("expected refusal for a scope with no assistant/tool rows")
	}
}

func TestSplit_SecondFoldNarrowsScopeAndCarriesPriorSummary(t *testing.T) {
	rows := []model.Message{
		{Seq: 1, Role: "user", Content: "a"},
		{Seq: 2, Role: "assistant", Content: "b"},
		{Seq: 3, Role: "user", Content: "c"},
		{Seq: 4, Role: "assistant", Content: "d"},
	}
	active := &model.Compaction{ID: 7, ThroughSeq: 2, Summary: "earlier"}
	plan, ok := Split(rows, active, 0)
	if !ok {
		t.Fatal("expected a plan")
	}
	if len(plan.Folded) != 2 || plan.Folded[0].Seq != 3 {
		t.Fatalf("folded=%+v want only seq 3,4", plan.Folded)
	}
	if plan.ThroughSeq != 4 {
		t.Fatalf("through=%d want 4", plan.ThroughSeq)
	}
	if plan.PriorSummary != "earlier" {
		t.Fatalf("prior=%q want %q", plan.PriorSummary, "earlier")
	}
}

func TestSplit_NoScopeAfterActiveFold(t *testing.T) {
	rows := []model.Message{
		{Seq: 1, Role: "user", Content: "a"},
		{Seq: 2, Role: "assistant", Content: "b"},
	}
	if _, ok := Split(rows, &model.Compaction{ThroughSeq: 2}, 0); ok {
		t.Fatal("everything is already folded; expected no plan")
	}
}

func TestSummaryMessage_WrapsWithReplayRules(t *testing.T) {
	got := SummaryMessage(&model.Compaction{ID: 3, Summary: "## 1. User Intent\nstuff"})
	for _, want := range []string{
		`<compacted-summary id="3">`,
		"Do NOT restart the task.",
		"## 1. User Intent",
		"</compacted-summary>",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("summary message missing %q:\n%s", want, got)
		}
	}
	if SummaryMessage(nil) != "" {
		t.Fatal("nil compaction should render nothing")
	}
}

func TestTruncate_KeepsHeadAndTail(t *testing.T) {
	cfg := testCfg() // threshold 100, keep 20
	head := strings.Repeat("h", 20)
	tail := strings.Repeat("t", 20)
	in := head + strings.Repeat("m", 200) + tail

	got := truncate(in, cfg)
	if !strings.HasPrefix(got, head) {
		t.Fatalf("head not preserved: %q", got[:40])
	}
	if !strings.HasSuffix(got, tail) {
		t.Fatalf("tail not preserved: %q", got[len(got)-40:])
	}
	if !strings.Contains(got, "chars truncated for compaction") {
		t.Fatalf("missing truncation marker: %s", got)
	}
	if len(got) >= len(in) {
		t.Fatalf("truncation did not shrink: %d >= %d", len(got), len(in))
	}
}

func TestTruncate_LeavesSmallValuesAlone(t *testing.T) {
	cfg := testCfg()
	in := strings.Repeat("x", 50) // below the 100 threshold
	if got := truncate(in, cfg); got != in {
		t.Fatalf("short value was modified: %q", got)
	}
}

func TestBuildMessages_ShapeAndTrigger(t *testing.T) {
	s := NewSummarizer(testCfg())
	if s == nil {
		t.Fatal("summarizer should be enabled with a key + model")
	}
	folded := []model.Message{
		{Seq: 1, Role: "user", Content: "find the bug"},
		{Seq: 2, Role: "assistant", Content: "looking", ToolCalls: `[{"id":"c1"}]`},
		{Seq: 3, Role: "tool", ToolName: "read_file", Content: "file body"},
		{Seq: 4, Role: "assistant", Content: "found it"},
	}

	msgs := s.buildMessages(folded, "earlier summary")
	if len(msgs) != 6 {
		t.Fatalf("msgs=%d want 6 (prior + 4 rows + trigger)", len(msgs))
	}
	if !strings.Contains(msgs[0].Content, "<prior-summary>") {
		t.Fatalf("first message should carry the prior summary: %q", msgs[0].Content)
	}
	if !strings.Contains(msgs[len(msgs)-1].Content, "<compact-control>") {
		t.Fatalf("last message should be the trigger: %q", msgs[len(msgs)-1].Content)
	}
	// Tool rows replay as readable user-side text, not as real tool messages:
	// a partial history would fail the API's tool_call pairing rules.
	if msgs[3].Role != "user" || !strings.Contains(msgs[3].Content, "[tool result: read_file]") {
		t.Fatalf("tool row rendered as %+v", msgs[3])
	}
}

func TestBuildMessages_OmitsPriorSummaryOnFirstFold(t *testing.T) {
	s := NewSummarizer(testCfg())
	msgs := s.buildMessages([]model.Message{{Seq: 1, Role: "user", Content: "hi"}}, "")
	if len(msgs) != 2 {
		t.Fatalf("msgs=%d want 2 (row + trigger)", len(msgs))
	}
	if strings.Contains(msgs[0].Content, "prior-summary") {
		t.Fatalf("unexpected prior summary block: %q", msgs[0].Content)
	}
}

func TestNewSummarizer_DisabledWithoutKey(t *testing.T) {
	cfg := testCfg()
	cfg.APIKey = ""
	if NewSummarizer(cfg) != nil {
		t.Fatal("missing key must disable the summarizer")
	}
	if New(nil, cfg) != nil {
		t.Fatal("missing key must disable the compactor")
	}
}

// A nil *Compactor is the "feature off" value and must stay safe to call.
func TestNilCompactorIsNoOp(t *testing.T) {
	var c *Compactor
	if got := c.MaybeCompact(t.Context(), "conv", []model.Message{{Seq: 1}}); got != nil {
		t.Fatalf("nil compactor returned %+v", got)
	}
	if got := c.Active(t.Context(), "conv"); got != nil {
		t.Fatalf("nil compactor returned active %+v", got)
	}
}
