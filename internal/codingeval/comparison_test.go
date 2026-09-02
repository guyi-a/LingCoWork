package codingeval

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestCompareLedgerPairsVariantsAndComputesLift(t *testing.T) {
	ledger := filepath.Join(t.TempDir(), "ledger.jsonl")
	results := []RunResult{
		{TaskID: "legacy-task", Status: "passed"},
		comparisonResult("task-a", "baseline", 1, "passed", 100, 2),
		comparisonResult("task-a", "candidate", 1, "passed", 80, 1),
		comparisonResult("task-b", "baseline", 1, "failed", 200, 4),
		comparisonResult("task-b", "candidate", 1, "passed", 120, 3),
	}
	for _, result := range results {
		if err := AppendLedger(ledger, result); err != nil {
			t.Fatal(err)
		}
	}
	summary, err := CompareLedger(ledger, "experiment", "baseline", "candidate")
	if err != nil {
		t.Fatal(err)
	}
	if summary.Pairs != 2 || summary.BaselinePassRate != 0.5 ||
		summary.CandidatePassRate != 1 || summary.PassRateLift != 0.5 {
		t.Fatalf("summary=%#v", summary)
	}
	if summary.Metrics.DurationMS.Baseline != 150 ||
		summary.Metrics.DurationMS.Candidate != 100 ||
		summary.Metrics.ToolCalls.Delta != -1 {
		t.Fatalf("metrics=%#v", summary.Metrics)
	}
}

func TestCompareLedgerReportsMissingAndDuplicateObservations(t *testing.T) {
	ledger := filepath.Join(t.TempDir(), "ledger.jsonl")
	results := []RunResult{
		comparisonResult("task-a", "baseline", 1, "passed", 1, 0),
		comparisonResult("task-a", "baseline", 1, "passed", 1, 0),
		comparisonResult("task-b", "candidate", 1, "error", 1, 0),
	}
	for _, result := range results {
		if err := AppendLedger(ledger, result); err != nil {
			t.Fatal(err)
		}
	}
	summary, err := CompareLedger(ledger, "experiment", "baseline", "candidate")
	if err != nil {
		t.Fatal(err)
	}
	diagnostics := strings.Join(summary.Diagnostics, "\n")
	for _, expected := range []string{
		"duplicate observation",
		"missing baseline observation",
		"missing candidate observation",
		"has status error",
	} {
		if !strings.Contains(diagnostics, expected) {
			t.Fatalf("missing %q in diagnostics:\n%s", expected, diagnostics)
		}
	}
}

func comparisonResult(
	taskID, variant string,
	iteration int,
	status string,
	duration int64,
	toolCalls int,
) RunResult {
	return RunResult{
		TaskID:     taskID,
		Status:     status,
		DurationMS: duration,
		Experiment: &ExperimentRun{
			Experiment: "experiment",
			Variant:    variant,
			Iteration:  iteration,
		},
		Metrics: AgentMetrics{ToolCalls: toolCalls},
	}
}
