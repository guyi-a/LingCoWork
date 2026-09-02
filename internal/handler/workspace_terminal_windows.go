//go:build windows

package handler

import (
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	winpty "github.com/aymanbagabas/go-pty"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
)

func terminatePTY(cmd *winpty.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = cmd.Process.Kill()
}

func loginShell() string {
	for _, candidate := range []string{
		"pwsh.exe",
		"powershell.exe",
		filepath.Join(os.Getenv("SystemRoot"), "System32", "WindowsPowerShell", "v1.0", "powershell.exe"),
	} {
		if shellPath, err := exec.LookPath(candidate); err == nil {
			if absolutePath, err := filepath.Abs(shellPath); err == nil {
				return absolutePath
			}
			return shellPath
		}
	}
	if shellPath := strings.TrimSpace(os.Getenv("COMSPEC")); filepath.IsAbs(shellPath) {
		if info, err := os.Stat(shellPath); err == nil && !info.IsDir() {
			return shellPath
		}
	}
	if shellPath := strings.TrimSpace(os.Getenv("COMSPEC")); shellPath != "" {
		return shellPath
	}
	return "cmd.exe"
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
	var shellArgs []string
	if strings.Contains(strings.ToLower(shellPath), "powershell") ||
		strings.Contains(strings.ToLower(shellPath), "pwsh") {
		const prelude = "" +
			"chcp 65001 > $null; " +
			"[Console]::OutputEncoding = [System.Text.UTF8Encoding]::new($false); " +
			"[Console]::InputEncoding  = [System.Text.UTF8Encoding]::new($false); " +
			"$OutputEncoding = [System.Text.UTF8Encoding]::new($false); " +
			"$PSDefaultParameterValues['*:Encoding'] = 'utf8'; " +
			"Clear-Host"
		shellArgs = []string{"-NoExit", "-Command", prelude}
	} else {
		shellArgs = []string{"/k", "chcp 65001 > nul"}
	}

	ptmx, err := winpty.New()
	if err != nil {
		_ = conn.WriteJSON(gin.H{"type": "error", "message": err.Error()})
		return
	}
	var closePTYOnce sync.Once
	closePTY := func() {
		closePTYOnce.Do(func() {
			_ = ptmx.Close()
		})
	}
	defer closePTY()

	cols := clampTerminalDimension(queryInt(c, "cols", 100), 20, 500)
	rows := clampTerminalDimension(queryInt(c, "rows", 30), 5, 200)
	if err := ptmx.Resize(cols, rows); err != nil {
		_ = conn.WriteJSON(gin.H{"type": "error", "message": err.Error()})
		return
	}

	cmd := ptmx.Command(shellPath, shellArgs...)
	cmd.Dir = root
	cmd.Env = terminalEnvironment()

	if err := cmd.Start(); err != nil {
		_ = conn.WriteJSON(gin.H{"type": "error", "message": err.Error()})
		return
	}

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
		// Closing ConPTY releases its output pipe so the reader can finish.
		// go-pty's Windows Close is not idempotent: calling it twice closes the
		// same HPCON twice and can terminate the entire backend with
		// STATUS_HEAP_CORRUPTION (0xC0000374).
		closePTY()
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
				_ = ptmx.Resize(
					clampTerminalDimension(control.Cols, 20, 500),
					clampTerminalDimension(control.Rows, 5, 200),
				)
			}
		}
		if clientClosed {
			break
		}
	}

	if clientClosed {
		terminatePTY(cmd)
		closePTY()
	}
	<-waitDone
	<-outputDone
}
