package approval

import (
	"testing"

	"github.com/guyi-a/Interview-Agent/internal/effect"
)

func TestExploreProbeCommandAllowlist(t *testing.T) {
	for _, command := range []string{
		"git status --short",
		"git diff -- internal/agent",
		"git --no-pager log -5",
		"git show HEAD:go.mod",
		"pwd",
		"ls -la internal/agent",
		"file README.md",
		"stat go.mod",
		"wc -l README.md",
		`rg "ExploreAgentName" internal`,
		"jq '.name' package.json",
		"head -n 20 README.md",
		"tail -n 20 README.md",
		"go --version",
		"node --version",
		"which go",
	} {
		if ok, reason := IsExploreProbeCommand(command); !ok {
			t.Errorf("expected allow for %q: %s", command, reason)
		}
	}
}

func TestExploreProbeCommandRejectsExecutionAndEscapes(t *testing.T) {
	for _, command := range []string{
		"tee output.txt",
		"echo hello > output.txt",
		"ls /tmp",
		"ls ../other",
		"git -C /tmp status",
		"git --git-dir=/tmp/repo status",
		"git checkout main",
		"git branch -D feature",
		"go test ./...",
		"npm install",
		"python script.py",
		"env",
		"sh -c 'echo hello'",
		"rg needle . | tee result.txt",
		"ls $(pwd)",
	} {
		if ok, _ := IsExploreProbeCommand(command); ok {
			t.Errorf("unexpected allow for %q", command)
		}
	}
}

func TestTeeIsNotClassifiedAsHarmless(t *testing.T) {
	if got := ClassifyShellCommand("printf hello | tee output.txt"); got == effect.Harmless {
		t.Fatalf("tee classified as harmless")
	}
}
