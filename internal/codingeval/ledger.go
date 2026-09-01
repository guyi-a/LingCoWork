package codingeval

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

type Summary struct {
	Total   int `json:"total"`
	Passed  int `json:"passed"`
	Failed  int `json:"failed"`
	Skipped int `json:"skipped"`
	Errors  int `json:"errors"`
}

func AppendLedger(path string, result RunResult) error {
	if path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	line, err := json.Marshal(result)
	if err != nil {
		return err
	}
	if _, err := f.Write(append(line, '\n')); err != nil {
		return err
	}
	return f.Sync()
}

func SummarizeLedger(path string) (Summary, error) {
	f, err := os.Open(path)
	if err != nil {
		return Summary{}, err
	}
	defer f.Close()
	var summary Summary
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		var result RunResult
		if err := json.Unmarshal(scanner.Bytes(), &result); err != nil {
			return Summary{}, fmt.Errorf("ledger line %d: %w", summary.Total+1, err)
		}
		summary.Total++
		switch result.Status {
		case "passed":
			summary.Passed++
		case "failed":
			summary.Failed++
		case "skipped":
			summary.Skipped++
		default:
			summary.Errors++
		}
	}
	return summary, scanner.Err()
}
