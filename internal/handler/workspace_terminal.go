package handler

import (
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/creack/pty"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
)

const maxTerminalInputBytes = 64 * 1024

var terminalUpgrader = websocket.Upgrader{
	ReadBufferSize:  16 * 1024,
	WriteBufferSize: 16 * 1024,
	CheckOrigin: func(r *http.Request) bool {
		origin := r.Header.Get("Origin")
		return origin == "" || origin == "null" ||
			strings.HasPrefix(origin, "lingcowork://") ||
			origin == "http://localhost:5173" ||
			origin == "http://127.0.0.1:5173"
	},
}

type terminalControl struct {
	Type string `json:"type"`
	Cols int    `json:"cols,omitempty"`
	Rows int    `json:"rows,omitempty"`
}

func (h *WorkspaceHandler) Terminal(c *gin.Context) {
	root, _, err := h.svc.Root(
		c.Request.Context(),
		c.Param("id"),
		"",
	)
	if err != nil {
		writeWorkspaceError(c, err)
		return
	}
	root, err = filepath.EvalSymlinks(filepath.Clean(root))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "resolve workspace"})
		return
	}
	conn, err := terminalUpgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		return
	}
	defer conn.Close()
	conn.SetReadLimit(maxTerminalInputBytes)

	shellPath := loginShell()
	cmd := exec.Command(shellPath, "-l")
	cmd.Dir = root
	cmd.Env = terminalEnvironment()
	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{
		Cols: uint16(clampTerminalDimension(queryInt(c, "cols", 100), 20, 500)),
		Rows: uint16(clampTerminalDimension(queryInt(c, "rows", 30), 5, 200)),
	})
	if err != nil {
		_ = conn.WriteJSON(gin.H{"type": "error", "message": err.Error()})
		return
	}
	defer ptmx.Close()

	var writeMu sync.Mutex
	writeJSON := func(value any) error {
		writeMu.Lock()
		defer writeMu.Unlock()
		return conn.WriteJSON(value)
	}
	writeBinary := func(data []byte) error {
		writeMu.Lock()
		defer writeMu.Unlock()
		return conn.WriteMessage(websocket.BinaryMessage, data)
	}
	if err := writeJSON(gin.H{
		"type": "ready", "cwd": root, "shell": shellPath,
	}); err != nil {
		terminatePTY(cmd)
		return
	}

	outputDone := make(chan struct{})
	go func() {
		defer close(outputDone)
		buf := make([]byte, 32*1024)
		for {
			n, readErr := ptmx.Read(buf)
			if n > 0 {
				if err := writeBinary(append([]byte(nil), buf[:n]...)); err != nil {
					return
				}
			}
			if readErr != nil {
				return
			}
		}
	}()

	waitDone := make(chan int, 1)
	go func() {
		waitErr := cmd.Wait()
		exitCode := 0
		if waitErr != nil {
			if exitErr, ok := waitErr.(*exec.ExitError); ok {
				exitCode = exitErr.ExitCode()
			} else {
				exitCode = -1
			}
		}
		<-outputDone
		_ = writeJSON(gin.H{"type": "exit", "exit_code": exitCode})
		_ = conn.WriteControl(
			websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseNormalClosure, "shell exited"),
			time.Now().Add(time.Second),
		)
		waitDone <- exitCode
	}()

	clientClosed := false
	for {
		messageType, data, readErr := conn.ReadMessage()
		if readErr != nil {
			clientClosed = true
			break
		}
		switch messageType {
		case websocket.BinaryMessage:
			if len(data) > maxTerminalInputBytes {
				continue
			}
			if _, err := ptmx.Write(data); err != nil {
				clientClosed = true
			}
		case websocket.TextMessage:
			var control terminalControl
			if json.Unmarshal(data, &control) == nil && control.Type == "resize" {
				_ = pty.Setsize(ptmx, &pty.Winsize{
					Cols: uint16(clampTerminalDimension(control.Cols, 20, 500)),
					Rows: uint16(clampTerminalDimension(control.Rows, 5, 200)),
				})
			}
		}
		if clientClosed {
			break
		}
	}

	if clientClosed {
		terminatePTY(cmd)
		_ = ptmx.Close()
	}
	<-waitDone
	<-outputDone
}

func loginShell() string {
	if shellPath := strings.TrimSpace(os.Getenv("SHELL")); filepath.IsAbs(shellPath) {
		if info, err := os.Stat(shellPath); err == nil && !info.IsDir() {
			return shellPath
		}
	}
	for _, candidate := range []string{"/bin/zsh", "/bin/bash", "/bin/sh"} {
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate
		}
	}
	return "/bin/sh"
}

func terminalEnvironment() []string {
	env := append([]string(nil), os.Environ()...)
	env = append(env, "TERM=xterm-256color", "COLORTERM=truecolor")
	return env
}

func terminatePTY(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGHUP)
	_ = cmd.Process.Kill()
}

func queryInt(c *gin.Context, key string, fallback int) int {
	value, err := strconv.Atoi(c.Query(key))
	if err != nil {
		return fallback
	}
	return value
}

func clampTerminalDimension(value, minimum, maximum int) int {
	if value < minimum {
		return minimum
	}
	if value > maximum {
		return maximum
	}
	return value
}
