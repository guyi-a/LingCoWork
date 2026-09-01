package tools

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"

	"github.com/guyi-a/Interview-Agent/internal/agent/scope"
	"github.com/guyi-a/Interview-Agent/internal/validation"
)

const (
	// 单次调用 stdout / stderr 各自的字节上限。跟 write_file 的 64 KiB 一致，
	// 避开上游 SSE 流式协议在超大 tool_call args 上出现的序列化 bug。
	// agent 想要完整输出时，让它把命令 stdout 重定向到文件后用 read_file 分段读。
	maxShellOutputBytes = 64 * 1024
	// 单次 run_command 的默认超时。
	defaultShellTimeoutSec = 60
	// 单次 run_command 的最大超时上限；避免 agent 传超大值导致 UI 一直 pending。
	maxShellTimeoutSec = 300
)

type RunCommandInput struct {
	Command        string `json:"command" jsonschema:"description=Full shell command line executed via /bin/sh -c. Supports pipes / redirects / subshells / env-var prefixes (LANG=... foo). Non-empty."`
	Cwd            string `json:"cwd" jsonschema:"description=Working directory. Workspace-relative path or an absolute path INSIDE the workspace. Absolute paths outside the workspace are rejected — this tool runs code and must not roam. Default: workspace root."`
	Timeout        int    `json:"timeout" jsonschema:"description=Timeout in seconds. Default 60. Values above 300 are clamped down to 300. On timeout the entire process group is killed (SIGKILL)."`
	ValidationKind string `json:"validation_kind,omitempty" jsonschema:"description=Optional structured validation intent. One of test / build / lint / typecheck / format. Omit for ordinary commands."`
}

type RunCommandOutput struct {
	ExitCode        int    `json:"exit_code"` // -1 when the process group was killed by our timeout
	DurationMs      int64  `json:"duration_ms"`
	Stdout          string `json:"stdout"`
	Stderr          string `json:"stderr"`
	StdoutTruncated bool   `json:"stdout_truncated,omitempty"`
	StderrTruncated bool   `json:"stderr_truncated,omitempty"`
	TimedOut        bool   `json:"timed_out,omitempty"`
	Cwd             string `json:"cwd"`
}

func newRunCommandTool(d *fsDeps) (tool.BaseTool, error) {
	fn := func(ctx context.Context, in *RunCommandInput) (*RunCommandOutput, error) {
		command := strings.TrimSpace(in.Command)
		if command == "" {
			return nil, fmt.Errorf("command is required")
		}
		if err := validation.ValidateKind(in.ValidationKind); err != nil {
			return nil, err
		}

		// cwd 一律要求 workspace 内 —— 执行命令不同于读文件，绝对路径逃逸的
		// 影响面太大，用户已在 GPT review 里拍板收紧到这个边界。
		ws, err := d.resolveWorkspace(ctx)
		if err != nil {
			return nil, err
		}
		cwd := strings.TrimSpace(in.Cwd)
		if cwd == "" {
			cwd = ws
		} else {
			// scope.Resolve 允许 workspace-相对或落在 workspace 内的绝对路径，
			// 其他一律拒绝。
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
		"Execute a shell command line via /bin/sh -c. Supports pipes, redirects, subshells and env prefixes. Runs inside the current workspace (cwd must resolve inside it — absolute paths outside are rejected). Normal commands require approval in manual and accept-write modes; destructive commands (sudo / rm -rf / dd / mkfs / diskutil erase / git reset --hard / git push --force / curl | sh, etc.) require explicit approval even in auto mode. stdout and stderr are captured separately, each truncated to 64 KiB; when you expect large output, redirect to a file and read it back with read_file (which supports offset+limit for chunked reads). If output looks garbled for Chinese text, prefix the command with LANG=en_US.UTF-8. Timeout defaults to 60s, max 300s; on timeout the whole process group is killed with SIGKILL so no orphaned subprocess survives.",
		fn,
	)
}

// runShell forks /bin/sh -c <command> in its own process group, captures
// stdout/stderr into bounded buffers, and enforces the timeout by killing the
// entire group. Returning nil error means the command was successfully
// launched — a non-zero exit code lives in Output.ExitCode; a real error
// (fork failure, pipe setup) is what the error return is for.
func runShell(ctx context.Context, command, cwd string, timeout time.Duration) (*RunCommandOutput, error) {
	// 我们不用 exec.CommandContext 的自动 Kill 机制 —— 它只 kill 主子进程
	// (sh)，孙子进程（比如 sh 拉起来的 python）会成孤儿。手动管超时，超时时
	// 用 syscall.Kill(-pgid, SIGKILL) 把整棵进程组端掉。
	cmd := exec.Command("/bin/sh", "-c", command)
	cmd.Dir = cwd
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	stdoutBuf := &boundedBuffer{max: maxShellOutputBytes}
	stderrBuf := &boundedBuffer{max: maxShellOutputBytes}
	cmd.Stdout = stdoutBuf
	cmd.Stderr = stderrBuf

	start := time.Now()
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start /bin/sh: %w", err)
	}

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	var timedOut bool
	var waitErr error
	select {
	case <-time.After(timeout):
		timedOut = true
		// 负 pid 传给 Kill = 杀整个进程组。Setpgid 让 sh 成为 pgid = 自己的
		// pid 的组长，所有它 fork 出来的孙子进程都属于这个组。
		if cmd.Process != nil {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
		waitErr = <-done // Wait 会因为 SIGKILL 返回一个 ExitError，正常收尾
	case <-ctx.Done():
		// 请求侧取消（比如整个 run 被 abort），同样清理进程组。
		if cmd.Process != nil {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
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
			// pipe / IO 层的真错误 —— 少见但要暴露出来。
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

// boundedBuffer 是一个只保留前 max 字节的 io.Writer。超出的部分丢弃，只记
// 一个 truncated 标记 —— 相比 bytes.Buffer 无上限，agent 在跑 `cat huge.pdf`
// 之类的情况下不会撑爆 process memory 或 SSE 帧。
type boundedBuffer struct {
	max     int
	buf     bytes.Buffer
	dropped bool
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	remaining := b.max - b.buf.Len()
	if remaining <= 0 {
		b.dropped = true
		return len(p), nil // 假装写成功，让上游继续读子进程管道
	}
	if len(p) <= remaining {
		return b.buf.Write(p)
	}
	// 只吸收前 remaining 字节，剩下丢掉但依旧返回 len(p) 让 pipe 不会阻塞。
	if _, err := b.buf.Write(p[:remaining]); err != nil {
		return 0, err
	}
	b.dropped = true
	return len(p), nil
}

func (b *boundedBuffer) Result() (string, bool) {
	return b.buf.String(), b.dropped
}
