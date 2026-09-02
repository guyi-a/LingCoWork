package codingeval

import (
	"bufio"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"sort"
)

type MeanDelta struct {
	Baseline  float64 `json:"baseline"`
	Candidate float64 `json:"candidate"`
	Delta     float64 `json:"delta"`
}

type MetricsComparison struct {
	DurationMS         MeanDelta `json:"duration_ms"`
	ToolCalls          MeanDelta `json:"tool_calls"`
	ValidationCalls    MeanDelta `json:"validation_calls"`
	CompletionGateRuns MeanDelta `json:"completion_gate_runs"`
	ApprovalInterrupts MeanDelta `json:"approval_interrupts"`
}

type ComparisonSummary struct {
	Experiment        string            `json:"experiment"`
	Baseline          string            `json:"baseline"`
	Candidate         string            `json:"candidate"`
	BaselineSamples   int               `json:"baseline_samples"`
	CandidateSamples  int               `json:"candidate_samples"`
	Pairs             int               `json:"pairs"`
	BaselinePassRate  float64           `json:"baseline_pass_rate"`
	CandidatePassRate float64           `json:"candidate_pass_rate"`
	PassRateLift      float64           `json:"pass_rate_lift"`
	Metrics           MetricsComparison `json:"metrics"`
	Diagnostics       []string          `json:"diagnostics,omitempty"`
}

func CompareLedger(path, experiment, baseline, candidate string) (ComparisonSummary, error) {
	summary := ComparisonSummary{
		Experiment: experiment,
		Baseline:   baseline,
		Candidate:  candidate,
	}
	for name, value := range map[string]string{
		"experiment": experiment,
		"baseline":   baseline,
		"candidate":  candidate,
	} {
		if err := validateArtifactSegment(name, value); err != nil {
			return summary, err
		}
	}
	if baseline == candidate {
		return summary, fmt.Errorf("baseline and candidate must differ")
	}

	results, err := readLedger(path)
	if err != nil {
		return summary, err
	}
	baselineRuns := make(map[string]RunResult)
	candidateRuns := make(map[string]RunResult)
	for _, result := range results {
		if result.Experiment == nil || result.Experiment.Experiment != experiment {
			continue
		}
		var target map[string]RunResult
		switch result.Experiment.Variant {
		case baseline:
			target = baselineRuns
		case candidate:
			target = candidateRuns
		default:
			continue
		}
		key := comparisonKey(result)
		if _, exists := target[key]; exists {
			summary.Diagnostics = append(summary.Diagnostics,
				fmt.Sprintf("duplicate observation: %s/%s", result.Experiment.Variant, key))
			continue
		}
		target[key] = result
		if result.Status != "passed" && result.Status != "failed" {
			summary.Diagnostics = append(summary.Diagnostics,
				fmt.Sprintf("%s/%s has status %s", result.Experiment.Variant, key, result.Status))
		}
	}
	summary.BaselineSamples = len(baselineRuns)
	summary.CandidateSamples = len(candidateRuns)

	keys := make([]string, 0, len(baselineRuns)+len(candidateRuns))
	seen := make(map[string]bool)
	for key := range baselineRuns {
		seen[key] = true
		keys = append(keys, key)
	}
	for key := range candidateRuns {
		if !seen[key] {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)

	var baselinePasses, candidatePasses int
	var baselineMetrics, candidateMetrics metricTotals
	for _, key := range keys {
		baselineResult, hasBaseline := baselineRuns[key]
		candidateResult, hasCandidate := candidateRuns[key]
		if !hasBaseline || !hasCandidate {
			missing := baseline
			if !hasCandidate {
				missing = candidate
			}
			summary.Diagnostics = append(summary.Diagnostics,
				fmt.Sprintf("missing %s observation: %s", missing, key))
			continue
		}
		summary.Pairs++
		if baselineResult.Status == "passed" {
			baselinePasses++
		}
		if candidateResult.Status == "passed" {
			candidatePasses++
		}
		baselineMetrics.add(baselineResult)
		candidateMetrics.add(candidateResult)
	}
	if summary.Pairs > 0 {
		summary.BaselinePassRate = float64(baselinePasses) / float64(summary.Pairs)
		summary.CandidatePassRate = float64(candidatePasses) / float64(summary.Pairs)
		summary.PassRateLift = summary.CandidatePassRate - summary.BaselinePassRate
		summary.Metrics = compareMetrics(baselineMetrics, candidateMetrics, summary.Pairs)
	}
	sort.Strings(summary.Diagnostics)
	return summary, nil
}

func comparisonKey(result RunResult) string {
	return fmt.Sprintf("%s#%03d", result.TaskID, result.Experiment.Iteration)
}

type metricTotals struct {
	duration, tools, validation, gates, approvals float64
}

func (t *metricTotals) add(result RunResult) {
	t.duration += float64(result.DurationMS)
	t.tools += float64(result.Metrics.ToolCalls)
	t.validation += float64(result.Metrics.ValidationCalls)
	t.gates += float64(result.Metrics.CompletionGateRuns)
	t.approvals += float64(result.Metrics.ApprovalInterrupts)
}

func compareMetrics(baseline, candidate metricTotals, pairs int) MetricsComparison {
	count := float64(pairs)
	return MetricsComparison{
		DurationMS:         meanDelta(baseline.duration/count, candidate.duration/count),
		ToolCalls:          meanDelta(baseline.tools/count, candidate.tools/count),
		ValidationCalls:    meanDelta(baseline.validation/count, candidate.validation/count),
		CompletionGateRuns: meanDelta(baseline.gates/count, candidate.gates/count),
		ApprovalInterrupts: meanDelta(baseline.approvals/count, candidate.approvals/count),
	}
}

func meanDelta(baseline, candidate float64) MeanDelta {
	baseline = roundMetric(baseline)
	candidate = roundMetric(candidate)
	return MeanDelta{
		Baseline:  baseline,
		Candidate: candidate,
		Delta:     roundMetric(candidate - baseline),
	}
}

func roundMetric(value float64) float64 {
	return math.Round(value*1000) / 1000
}

func readLedger(path string) ([]RunResult, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var results []RunResult
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	line := 0
	for scanner.Scan() {
		line++
		var result RunResult
		if err := json.Unmarshal(scanner.Bytes(), &result); err != nil {
			return nil, fmt.Errorf("ledger line %d: %w", line, err)
		}
		results = append(results, result)
	}
	return results, scanner.Err()
}
