package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/guyi-a/Interview-Agent/internal/codingeval"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "coding-eval:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		usage()
		return flag.ErrHelp
	}
	switch args[0] {
	case "list":
		return list(args[1:])
	case "validate":
		return validate(args[1:])
	case "run":
		return runTask(args[1:])
	case "run-agent":
		return runAgentTask(args[1:])
	case "run-suite":
		return runSuite(args[1:])
	case "summary":
		return summary(args[1:])
	case "schema":
		data, err := codingeval.TaskSchema()
		if err == nil {
			_, err = os.Stdout.Write(data)
		}
		return err
	default:
		usage()
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: coding-eval <list|validate|run|run-agent|run-suite|summary|schema> [options]")
}

func runAgentTask(args []string) error {
	flags := flag.NewFlagSet("run-agent", flag.ContinueOnError)
	catalogPath := flags.String("catalog", "", "optional catalog JSON path")
	ledger := flags.String("ledger", filepath.Join(".coding-eval", "ledger.jsonl"), "JSONL ledger path; empty disables ledger")
	baseURL := flags.String("base-url", "http://127.0.0.1:9001", "running LingCoWork API base URL")
	timeout := flags.Duration("timeout", 5*time.Minute, "agent task wall-clock timeout")
	keep := flags.Bool("keep", false, "keep temporary worktree for inspection")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 1 {
		return fmt.Errorf("run-agent requires exactly one task id")
	}
	catalog, err := loadCatalog(*catalogPath)
	if err != nil {
		return err
	}
	task, ok := codingeval.FindTask(catalog, flags.Arg(0))
	if !ok {
		return fmt.Errorf("task %q not found", flags.Arg(0))
	}
	result, runErr := codingeval.RunAgent(context.Background(), task, codingeval.AgentRunOptions{
		BaseURL: *baseURL, LedgerPath: *ledger, Keep: *keep, Timeout: *timeout,
	})
	data, marshalErr := codingeval.MarshalResult(result)
	if marshalErr == nil {
		fmt.Println(string(data))
	}
	if runErr != nil {
		return errors.Join(runErr, marshalErr)
	}
	if result.Status != "passed" {
		return fmt.Errorf("task status: %s", result.Status)
	}
	return marshalErr
}

func runSuite(args []string) error {
	flags := flag.NewFlagSet("run-suite", flag.ContinueOnError)
	catalogPath := flags.String("catalog", "", "optional catalog JSON path")
	ledger := flags.String("ledger", filepath.Join(".coding-eval", "ledger.jsonl"), "JSONL ledger path; empty disables ledger")
	driver := flags.String("driver", "reference", "reference or agent")
	baseURL := flags.String("base-url", "http://127.0.0.1:9001", "running LingCoWork API base URL for agent driver")
	timeout := flags.Duration("timeout", 5*time.Minute, "per-agent-task wall-clock timeout")
	full := flags.Bool("full", false, "include enabled tasks outside the smoke baseline")
	if err := flags.Parse(args); err != nil {
		return err
	}
	catalog, err := loadCatalog(*catalogPath)
	if err != nil {
		return err
	}
	failed := 0
	for _, task := range catalog.Tasks {
		if !task.Enabled || (!*full && !task.Baseline.Included) {
			continue
		}
		var result codingeval.RunResult
		if *driver == "agent" {
			result, err = codingeval.RunAgent(context.Background(), task, codingeval.AgentRunOptions{
				BaseURL: *baseURL, LedgerPath: *ledger, Timeout: *timeout,
			})
		} else if *driver == "reference" {
			result, err = codingeval.Run(context.Background(), task, codingeval.RunOptions{
				LedgerPath: *ledger,
			})
		} else {
			return fmt.Errorf("driver must be reference or agent")
		}
		fmt.Printf("%-32s %s\n", task.ID, result.Status)
		if err != nil || result.Status != "passed" {
			failed++
		}
	}
	if failed > 0 {
		return fmt.Errorf("%d suite task(s) failed", failed)
	}
	return nil
}

func list(args []string) error {
	flags := flag.NewFlagSet("list", flag.ContinueOnError)
	catalogPath := flags.String("catalog", "", "optional catalog JSON path")
	if err := flags.Parse(args); err != nil {
		return err
	}
	catalog, err := loadCatalog(*catalogPath)
	if err != nil {
		return err
	}
	sort.Slice(catalog.Tasks, func(i, j int) bool { return catalog.Tasks[i].ID < catalog.Tasks[j].ID })
	for _, task := range catalog.Tasks {
		state := "disabled"
		if task.Enabled {
			state = "enabled"
		}
		baseline := "not-baseline"
		if task.Baseline.Included {
			baseline = "baseline"
		}
		fmt.Printf("%-32s %-8s %-12s %s\n", task.ID, state, baseline, task.Title)
		if !task.Enabled {
			fmt.Printf("  reason: %s\n", task.DisabledReason)
		}
	}
	return nil
}

func validate(args []string) error {
	flags := flag.NewFlagSet("validate", flag.ContinueOnError)
	catalogPath := flags.String("catalog", "", "optional catalog JSON path")
	if err := flags.Parse(args); err != nil {
		return err
	}
	catalog, err := loadCatalog(*catalogPath)
	if err != nil {
		return err
	}
	enabled, baseline := 0, 0
	for _, task := range catalog.Tasks {
		if task.Enabled {
			enabled++
		}
		if task.Baseline.Included {
			baseline++
		}
	}
	fmt.Printf("valid: %d tasks (%d enabled, %d disabled, %d baseline)\n", len(catalog.Tasks), enabled, len(catalog.Tasks)-enabled, baseline)
	return nil
}

func runTask(args []string) error {
	flags := flag.NewFlagSet("run", flag.ContinueOnError)
	catalogPath := flags.String("catalog", "", "optional catalog JSON path")
	ledger := flags.String("ledger", filepath.Join(".coding-eval", "ledger.jsonl"), "JSONL ledger path; empty disables ledger")
	keep := flags.Bool("keep", false, "keep temporary worktree for inspection")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 1 {
		return fmt.Errorf("run requires exactly one task id")
	}
	catalog, err := loadCatalog(*catalogPath)
	if err != nil {
		return err
	}
	task, ok := codingeval.FindTask(catalog, flags.Arg(0))
	if !ok {
		return fmt.Errorf("task %q not found", flags.Arg(0))
	}
	result, runErr := codingeval.Run(context.Background(), task, codingeval.RunOptions{
		LedgerPath: *ledger,
		Keep:       *keep,
	})
	data, marshalErr := codingeval.MarshalResult(result)
	if marshalErr == nil {
		fmt.Println(string(data))
	}
	if runErr != nil {
		return errors.Join(runErr, marshalErr)
	}
	if result.Status != "passed" {
		return fmt.Errorf("task status: %s", result.Status)
	}
	return marshalErr
}

func summary(args []string) error {
	flags := flag.NewFlagSet("summary", flag.ContinueOnError)
	ledger := flags.String("ledger", filepath.Join(".coding-eval", "ledger.jsonl"), "JSONL ledger path")
	if err := flags.Parse(args); err != nil {
		return err
	}
	value, err := codingeval.SummarizeLedger(*ledger)
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err == nil {
		fmt.Println(string(data))
	}
	return err
}

func loadCatalog(path string) (codingeval.Catalog, error) {
	if path == "" {
		return codingeval.DefaultCatalog()
	}
	return codingeval.LoadCatalog(path)
}
