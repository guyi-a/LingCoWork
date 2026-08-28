package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"

	"github.com/guyi-a/Interview-Agent/internal/repository"
	"github.com/guyi-a/Interview-Agent/internal/repository/model"
	"github.com/guyi-a/Interview-Agent/internal/service"
)

func TestWorkspaceTerminalRunsInteractiveShellInWorkspace(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx := context.Background()
	db, err := repository.NewDB(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("NewDB: %v", err)
	}
	projectRepo := repository.NewProjectRepo(db)
	convRepo := repository.NewConversationRepo(db)
	root := t.TempDir()
	if err := projectRepo.Create(ctx, &model.Project{
		ID: "project", Name: "project", Workspace: root,
	}); err != nil {
		t.Fatalf("create project: %v", err)
	}
	if err := convRepo.Upsert(ctx, "conversation"); err != nil {
		t.Fatalf("create conversation: %v", err)
	}
	if err := convRepo.SetProjectID(ctx, "conversation", "project"); err != nil {
		t.Fatalf("bind project: %v", err)
	}

	router := gin.New()
	NewWorkspaceHandler(service.NewWorkspaceService(convRepo, projectRepo)).Register(router)
	server := httptest.NewServer(router)
	defer server.Close()

	url := "ws" + strings.TrimPrefix(server.URL, "http") +
		"/conversations/conversation/workspace/terminal?project_id=project&cols=80&rows=24"
	header := http.Header{"Origin": []string{"http://localhost:5173"}}
	conn, response, err := websocket.DefaultDialer.Dial(url, header)
	if err != nil {
		if response != nil {
			t.Fatalf("dial terminal: %v (status %s)", err, response.Status)
		}
		t.Fatalf("dial terminal: %v", err)
	}
	defer conn.Close()
	if err := conn.SetReadDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatalf("read deadline: %v", err)
	}

	_, readyRaw, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read ready: %v", err)
	}
	var ready struct {
		Type string `json:"type"`
		Cwd  string `json:"cwd"`
	}
	if err := json.Unmarshal(readyRaw, &ready); err != nil {
		t.Fatalf("decode ready: %v", err)
	}
	if ready.Type != "ready" {
		t.Fatalf("ready frame = %s", readyRaw)
	}
	canonicalRoot, _ := filepath.EvalSymlinks(root)
	if ready.Cwd != canonicalRoot {
		t.Fatalf("terminal cwd = %q, want %q", ready.Cwd, canonicalRoot)
	}
	if err := conn.WriteMessage(
		websocket.BinaryMessage,
		[]byte("printf '__PTY_OK__\\n'\nexit\n"),
	); err != nil {
		t.Fatalf("write command: %v", err)
	}

	var output strings.Builder
	sawExit := false
	for !sawExit {
		messageType, data, readErr := conn.ReadMessage()
		if readErr != nil {
			break
		}
		if messageType == websocket.BinaryMessage {
			output.Write(data)
			continue
		}
		var frame struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(data, &frame) == nil && frame.Type == "exit" {
			sawExit = true
		}
	}
	if !strings.Contains(output.String(), "__PTY_OK__") {
		t.Fatalf("terminal output missing marker: %q", output.String())
	}
	if !sawExit {
		t.Fatal("terminal did not emit exit frame")
	}
}

func TestClampTerminalDimension(t *testing.T) {
	if got := clampTerminalDimension(1, 20, 500); got != 20 {
		t.Fatalf("low clamp = %d", got)
	}
	if got := clampTerminalDimension(999, 20, 500); got != 500 {
		t.Fatalf("high clamp = %d", got)
	}
	if got := clampTerminalDimension(80, 20, 500); got != 80 {
		t.Fatalf("normal clamp = %d", got)
	}
}
