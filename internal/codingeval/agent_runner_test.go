package codingeval

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestRunAgentUsesIsolatedWorkspaceAndAgentAPI(t *testing.T) {
	var workspace string
	mux := http.NewServeMux()
	mux.HandleFunc("/projects", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("project method=%s", r.Method)
		}
		var body struct {
			Path string `json:"path"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		workspace = body.Path
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"project":{"id":"project"}}`)
	})
	mux.HandleFunc("/chat/eval-test", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Message      string `json:"message"`
			ApprovalMode string `json:"approval_mode"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body.Message == "" || body.ApprovalMode != "auto" {
			t.Fatalf("chat body=%+v", body)
		}
		if err := os.WriteFile(filepath.Join(workspace, "input.txt"), []byte("new\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"type\":\"done\"}\n\n")
	})
	mux.HandleFunc("/projects/project", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	task := validTestTask()
	task.Prompt = "change input.txt to new"
	// Make the generated conversation id deterministic for this HTTP fixture.
	original := newConversationID
	newConversationID = func() string { return "eval-test" }
	defer func() { newConversationID = original }()

	artifactDir := filepath.Join(t.TempDir(), "artifacts")
	result, err := RunAgent(t.Context(), task, AgentRunOptions{
		BaseURL:     server.URL,
		ArtifactDir: artifactDir,
		Experiment:  &ExperimentRun{Experiment: "test", Variant: "candidate", Iteration: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Driver != "lingcowork-agent" || result.Status != "passed" ||
		result.Action.ExitCode != 0 || !result.Score.Passed {
		t.Fatalf("result=%#v", result)
	}
	if result.Artifacts == nil || result.Artifacts.Events == "" {
		t.Fatalf("artifacts=%#v", result.Artifacts)
	}
	eventData, err := os.ReadFile(filepath.FromSlash(result.Artifacts.Events))
	if err != nil {
		t.Fatal(err)
	}
	var event AgentEvent
	if err := json.Unmarshal(eventData[:len(eventData)-1], &event); err != nil {
		t.Fatal(err)
	}
	if event.Type != "done" || event.ReceivedAt.IsZero() {
		t.Fatalf("event=%#v", event)
	}
}
