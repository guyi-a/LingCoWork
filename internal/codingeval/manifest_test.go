package codingeval

import (
	"strings"
	"testing"
)

func TestDefaultCatalog(t *testing.T) {
	catalog, err := DefaultCatalog()
	if err != nil {
		t.Fatal(err)
	}
	if got, want := len(catalog.Tasks), 30; got != want {
		t.Fatalf("task count = %d, want %d", got, want)
	}
	enabled, baseline := 0, 0
	for _, task := range catalog.Tasks {
		if task.Enabled {
			enabled++
			if task.EffectivePrompt() == "" {
				t.Fatalf("enabled task %s has no effective prompt", task.ID)
			}
		}
		if task.Baseline.Included {
			baseline++
		}
	}
	if enabled != 10 || baseline != 10 {
		t.Fatalf("enabled=%d baseline=%d, want 10/10", enabled, baseline)
	}
}

func TestDecodeCatalogRejectsUnknownField(t *testing.T) {
	input := `{"version":1,"unknown":true,"tasks":[]}`
	if _, err := DecodeCatalog(strings.NewReader(input)); err == nil {
		t.Fatal("expected unknown field error")
	}
}

func TestDisabledTaskCannotClaimBaseline(t *testing.T) {
	task := validTestTask()
	task.Enabled = false
	task.DisabledReason = "not calibrated"
	task.Baseline.Included = true
	if err := task.Validate(); err == nil {
		t.Fatal("expected disabled baseline validation error")
	}
}

func validTestTask() Task {
	return Task{
		ID: "test-task", Title: "Test", Description: "Test fixture", Type: "fixture",
		Enabled: true, Baseline: Baseline{Included: true, Reason: "test"},
		TimeoutSeconds: 5,
		Fixture:        Fixture{Files: map[string]string{"input.txt": "old\n"}, Command: "printf 'new\n' > input.txt"},
		Verify:         []Command{{Name: "content", Command: "grep -qx new input.txt"}},
		Scoring:        ScoringPolicy{ForbiddenPaths: []string{".git/**"}, MaxChangedFiles: 1, MaxAddedLines: 1, MaxDeletedLines: 1},
	}
}
