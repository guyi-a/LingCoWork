package main

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/guyi-a/Interview-Agent/internal/codingeval"
)

func TestRunSuiteValidatesExperimentFlags(t *testing.T) {
	err := run([]string{"run-suite", "--experiment", "exp"})
	if err == nil || !strings.Contains(err.Error(), "provided together") {
		t.Fatalf("err=%v", err)
	}
	err = run([]string{"run-suite", "--repetitions", "0"})
	if err == nil || !strings.Contains(err.Error(), "repetitions") {
		t.Fatalf("err=%v", err)
	}
}

func TestCompareCommandReadsPairedLedger(t *testing.T) {
	ledger := filepath.Join(t.TempDir(), "ledger.jsonl")
	for _, variant := range []string{"baseline", "candidate"} {
		err := codingeval.AppendLedger(ledger, codingeval.RunResult{
			TaskID: "task",
			Status: "passed",
			Experiment: &codingeval.ExperimentRun{
				Experiment: "exp",
				Variant:    variant,
				Iteration:  1,
			},
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := run([]string{
		"compare",
		"--ledger", ledger,
		"--experiment", "exp",
		"--baseline", "baseline",
		"--candidate", "candidate",
	}); err != nil {
		t.Fatal(err)
	}
}
