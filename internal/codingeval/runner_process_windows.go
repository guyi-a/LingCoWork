//go:build windows

package codingeval

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"time"
)

const createNoWindow = 0x08000000

func newShellCommand(command string) *exec.Cmd {
	if shellPath := gitBashPath(); shellPath != "" {
		command = `python3() { python "$@"; }; ` + command
		return hiddenCommand(shellPath, "-c", command)
	}
	if shellPath, err := exec.LookPath("powershell.exe"); err == nil {
		return hiddenCommand(shellPath, "-NoProfile", "-NonInteractive", "-Command", command)
	}
	return hiddenCommand("cmd.exe", "/D", "/S", "/C", command)
}

func gitBashPath() string {
	if shellPath, err := exec.LookPath("sh.exe"); err == nil {
		return shellPath
	}
	gitPath, err := exec.LookPath("git.exe")
	if err != nil {
		return ""
	}
	shellPath := filepath.Clean(filepath.Join(filepath.Dir(gitPath), "..", "bin", "sh.exe"))
	if info, err := os.Stat(shellPath); err == nil && !info.IsDir() {
		return shellPath
	}
	return ""
}

func hiddenCommand(name string, args ...string) *exec.Cmd {
	cmd := exec.Command(name, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: createNoWindow,
	}
	return cmd
}

func killShellCommand(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	killer := hiddenCommand(
		"taskkill.exe",
		"/PID", strconv.Itoa(cmd.Process.Pid),
		"/T",
		"/F",
	)
	if err := killer.Start(); err != nil {
		_ = cmd.Process.Kill()
		return
	}
	go func() {
		finished := make(chan error, 1)
		go func() { finished <- killer.Wait() }()
		select {
		case err := <-finished:
			if err != nil {
				_ = cmd.Process.Kill()
			}
		case <-time.After(500 * time.Millisecond):
			_ = cmd.Process.Kill()
		}
	}()
}
