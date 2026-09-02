//go:build windows

package tools

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"

	"github.com/guyi-a/Interview-Agent/internal/agent/scope"
	"github.com/guyi-a/Interview-Agent/internal/validation"
)

func newRunCommandTool(d *fsDeps) (tool.BaseTool, error) {
	fn := func(ctx context.Context, in *RunCommandInput) (*RunCommandOutput, error) {
		command := strings.TrimSpace(in.Command)
		if command == "" {
			return nil, fmt.Errorf("command is required")
		}
		if err := validation.ValidateKind(in.ValidationKind); err != nil {
			return nil, err
		}

		ws, err := d.resolveWorkspace(ctx)
		if err != nil {
			return nil, err
		}
		cwd := strings.TrimSpace(in.Cwd)
		if cwd == "" {
			cwd = ws
		} else {
			abs, err := scope.Resolve(ws, cwd)
			if err != nil {
				return nil, fmt.Errorf("cwd: %w", err)
			}
			cwd = abs
		}
		fi, err := os.Stat(cwd)
		if err != nil {
			return nil, fmt.Errorf("cwd %q: %w", in.Cwd, err)
		}
		if !fi.IsDir() {
			return nil, fmt.Errorf("cwd %q is not a directory", in.Cwd)
		}

		timeoutSec := in.Timeout
		if timeoutSec <= 0 {
			timeoutSec = defaultShellTimeoutSec
		}
		if timeoutSec > maxShellTimeoutSec {
			timeoutSec = maxShellTimeoutSec
		}

		return runShell(ctx, command, cwd, time.Duration(timeoutSec)*time.Second)
	}

	return utils.InferTool(
		"run_command",
		"Execute a shell command line via PowerShell/cmd.exe. Supports pipes, redirects, subshells and env prefixes. Runs inside the current workspace (cwd must resolve inside it — absolute paths outside are rejected). Normal commands require approval in manual and accept-write modes; destructive commands require explicit approval even in auto mode. stdout and stderr are captured separately, each truncated to 64 KiB; when you expect large output, redirect to a file and read it back with read_file. Timeout defaults to 60s, max 300s; on timeout the process is killed.",
		fn,
	)
}

// runShell Windows 特定实现
func runShell(ctx context.Context, command, cwd string, timeout time.Duration) (*RunCommandOutput, error) {
	// Windows 使用 PowerShell 或 cmd.exe 执行命令
	shell := "powershell"
	if _, err := exec.LookPath(shell); err != nil {
		shell = "cmd"
	}

	var cmd *exec.Cmd
	if shell == "powershell" {
		// PowerShell: 使用 -Command 参数
		cmd = exec.Command(shell, "-Command", command)
	} else {
		// cmd.exe: 使用 /C 参数
		cmd = exec.Command(shell, "/C", command)
	}
	cmd.Dir = cwd

	stdoutBuf := &boundedBuffer{max: maxShellOutputBytes}
	stderrBuf := &boundedBuffer{max: maxShellOutputBytes}
	cmd.Stdout = stdoutBuf
	cmd.Stderr = stderrBuf

	start := time.Now()
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start %s: %w", shell, err)
	}

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	var timedOut bool
	var waitErr error
	select {
	case <-time.After(timeout):
		timedOut = true
		// Windows: 直接杀进程
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		waitErr = <-done
	case <-ctx.Done():
		// 请求侧取消
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		waitErr = <-done
	case err := <-done:
		waitErr = err
	}
	duration := time.Since(start)

	exitCode := 0
	if waitErr != nil {
		if ee, ok := waitErr.(*exec.ExitError); ok {
			exitCode = ee.ExitCode()
		} else {
			return nil, fmt.Errorf("wait: %w", waitErr)
		}
	}
	if timedOut {
		exitCode = -1
	}

	stdout, stdoutTrunc := stdoutBuf.Result()
	stderr, stderrTrunc := stderrBuf.Result()

	return &RunCommandOutput{
		ExitCode:        exitCode,
		DurationMs:      duration.Milliseconds(),
		Stdout:          stdout,
		Stderr:          stderr,
		StdoutTruncated: stdoutTrunc,
		StderrTruncated: stderrTrunc,
		TimedOut:        timedOut,
		Cwd:             cwd,
	}, nil
}

// Windows 版本的 boundedBuffer
type boundedBuffer struct {
	max     int
	buf     bytes.Buffer
	dropped bool
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	remaining := b.max - b.buf.Len()
	if remaining <= 0 {
		b.dropped = true
		return len(p), nil
	}
	if len(p) <= remaining {
		return b.buf.Write(p)
	}
	if _, err := b.buf.Write(p[:remaining]); err != nil {
		return 0, err
	}
	b.dropped = true
	return len(p), nil
}

func (b *boundedBuffer) Result() (string, bool) {
	return b.buf.String(), b.dropped
}
