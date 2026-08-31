package agentmode

import (
	"fmt"
	"strings"
)

type Mode string

const (
	Agent Mode = "agent"
	Plan  Mode = "plan"
)

var planTools = map[string]struct{}{
	"glob":                  {},
	"grep":                  {},
	"read_file":             {},
	"list_files":            {},
	"file_info":             {},
	"extract_document_text": {},
	"get_current_time":      {},
	"ask_user":              {},
	"explore":               {},
	"create_plan":           {},
	"rag_search":            {},
	"web_search":            {},
	"web_fetch":             {},
	"load_skill":            {},
	"run_command":           {},
}

func AllowsPlanTool(name string) bool {
	_, ok := planTools[name]
	return ok
}

func Parse(raw string) (Mode, error) {
	switch Mode(strings.ToLower(strings.TrimSpace(raw))) {
	case "", Agent:
		return Agent, nil
	case Plan:
		return Plan, nil
	default:
		return "", fmt.Errorf("agent mode must be %q or %q", Agent, Plan)
	}
}
