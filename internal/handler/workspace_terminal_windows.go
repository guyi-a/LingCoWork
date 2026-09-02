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

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
)

func terminatePTY(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = cmd.Process.Kill()
}

func loginShell() string {
	if shellPath := strings.TrimSpace(os.Getenv("COMSPEC")); filepath.IsAbs(shellPath) {
		if info, err := os.Stat(shellPath); err == nil && !info.IsDir() {
			return shellPath
		}
	}
	for _, candidate := range []string{
		"powershell.exe",
		"pwsh.exe",
		filepath.Join(os.Getenv("SystemRoot"), "System32", "WindowsPowerShell", "v1.0", "powershell.exe"),
	} {
		if _, err := exec.LookPath(candidate); err == nil {
			return candidate
		}
	}
	if shellPath := os.Getenv("COMSPEC"); shellPath != "" {
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

	// Force the console into UTF-8 before the shell prints anything.
	//
	// Windows shells default to the system OEM codepage (936/GBK on Chinese
	// installs), but the bytes are piped straight through to xterm.js, which
	// always decodes UTF-8. The result is mojibake: cmd's banner "版本" is
	// GBK B0 E6 B1 BE, which read as UTF-8 becomes "?汾".
	//
	// Setting the codepage in-shell (rather than transcoding the stream) also
	// fixes *user* commands: `go run` and friends emit UTF-8 natively, so a
	// blanket GBK->UTF-8 conversion would corrupt those instead.
	var cmd *exec.Cmd
	if strings.Contains(strings.ToLower(shellPath), "powershell") ||
		strings.Contains(strings.ToLower(shellPath), "pwsh") {
		// $OutputEncoding governs what PowerShell writes down a pipe;
		// OutputEncoding on [Console] governs native command output.
		const prelude = "" +
			"chcp 65001 > $null; " +
			"[Console]::OutputEncoding = [System.Text.UTF8Encoding]::new($false); " +
			"[Console]::InputEncoding  = [System.Text.UTF8Encoding]::new($false); " +
			"$OutputEncoding = [System.Text.UTF8Encoding]::new($false); " +
			"$PSDefaultParameterValues['*:Encoding'] = 'utf8'; " +
			"Clear-Host"
		cmd = exec.Command(shellPath, "-NoExit", "-Command", prelude)
	} else {
		// cmd.exe: /k runs chcp then stays interactive. Output is swallowed
		// so the session doesn't open with "Active code page: 65001".
		cmd = exec.Command(shellPath, "/k", "chcp 65001 > nul")
	}

	cmd.Dir = root
	cmd.Env = terminalEnvironment()

	stdin, err := cmd.StdinPipe()
	if err != nil {
		_ = conn.WriteJSON(gin.H{"type": "error", "message": err.Error()})
		return
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = conn.WriteJSON(gin.H{"type": "error", "message": err.Error()})
		return
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		_ = conn.WriteJSON(gin.H{"type": "error", "message": err.Error()})
		return
	}

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
			n, readErr := stdout.Read(buf)
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

	go func() {
		buf := make([]byte, 32*1024)
		for {
			n, readErr := stderr.Read(buf)
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
			if _, err := stdin.Write(data); err != nil {
				clientClosed = true
			}
		case websocket.TextMessage:
			var control terminalControl
			if json.Unmarshal(data, &control) == nil && control.Type == "resize" {
				// Windows 不支持终端大小调整
			}
		}
		if clientClosed {
			break
		}
	}

	if clientClosed {
		terminatePTY(cmd)
		_ = stdin.Close()
		_ = stdout.Close()
		_ = stderr.Close()
	}
	<-waitDone
	<-outputDone
}
