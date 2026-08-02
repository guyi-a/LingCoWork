package approval

import (
	"testing"

	"github.com/guyi-a/Interview-Agent/internal/effect"
)

func TestClassifyShellCommand(t *testing.T) {
	tests := []struct {
		cmd  string
		want effect.Classification
	}{
		// Read-only across the whole pipeline.
		{`ls -la`, effect.Harmless},
		{`cat go.mod | grep module`, effect.Harmless},
		{`git status`, effect.Harmless},
		{`git log --oneline -5`, effect.Harmless},
		{`find . -name '*.go'`, effect.Harmless},
		{`go --version`, effect.Harmless},

		// Does something, but nothing irreversible.
		{`go build ./...`, effect.Normal},
		{`npm install`, effect.Normal},
		{`mkdir -p build`, effect.Normal},
		{`echo hi > out.txt`, effect.Normal}, // write redirection disqualifies harmless
		{`git commit -m "wip"`, effect.Normal},

		// Irreversible, including through a wrapper.
		{`rm -rf node_modules`, effect.Destructive},
		{`git reset --hard`, effect.Destructive},
		{`sudo ls`, effect.Destructive},
		{`ls | xargs rm -rf`, effect.Destructive},
		{`bash -c "rm -rf /"`, effect.Destructive},

		// One bad segment poisons an otherwise read-only pipeline.
		{`ls && rm -rf build`, effect.Destructive},

		// Unparseable fails closed.
		{`rm -rf "unterminated`, effect.Destructive},

		// Empty is neither harmless nor dangerous; the tool rejects it.
		{``, effect.Normal},
		{`   `, effect.Normal},
	}
	for _, tt := range tests {
		name := tt.cmd
		if name == "" {
			name = "(empty)"
		}
		t.Run(name, func(t *testing.T) {
			if got := ClassifyShellCommand(tt.cmd); got != tt.want {
				t.Fatalf("ClassifyShellCommand(%q) = %q, want %q", tt.cmd, got, tt.want)
			}
		})
	}
}
