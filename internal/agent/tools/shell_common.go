package tools

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
