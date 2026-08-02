package approval

import "testing"

// The destructive wall is the one rule that holds even in full_access, so
// every case here is a claim about what the user can never do by accident.

func TestIsDestructiveShellWrappers(t *testing.T) {
	// Wrappers park a harmless token in argv[0] and carry the real command
	// in the arguments. Before unwrapping, every one of these ran unprompted
	// in full_access.
	cases := []string{
		`ls | xargs rm -rf`,
		`xargs -I{} rm -rf {}`,
		`xargs -I {} rm -rf {}`,
		`find . -exec rm -rf {} \;`,
		`find . -name '*.log' -exec rm -rf {} +`,
		`find . -execdir rm -rf {} \;`,
		`bash -c "rm -rf /"`,
		`sh -c 'rm -rf ~'`,
		`zsh -c "git reset --hard"`,
		`bash -lc "rm -rf /usr"`,
		`env FOO=1 rm -rf x`,
		`nohup rm -rf /tmp`,
		`nice -n 10 rm -rf /etc`,
		`timeout 5 rm -rf /var`,
		`timeout 5 bash -c 'rm -rf x'`,
		`timeout --signal=KILL 5s rm -rf /bin`,
		`stdbuf -o0 rm -rf /root`,
		`setsid rm -rf /boot`,
		`find . -exec sh -c 'rm -rf "$1"' _ {} \;`,
	}
	for _, cmd := range cases {
		t.Run(cmd, func(t *testing.T) {
			got, reason := classifyShellCommand(cmd)
			if !got {
				t.Fatalf("classifyShellCommand(%q) = false, want destructive", cmd)
			}
			if reason == "" {
				t.Errorf("classifyShellCommand(%q) gave no reason", cmd)
			}
		})
	}
}

func TestIsDestructiveWrappersOverBenignInner(t *testing.T) {
	// Unwrapping must not turn the wrappers themselves into a blocklist —
	// these are the everyday uses and none of them lose data.
	cases := []string{
		`ls | xargs cat`,
		`xargs -n1 echo`,
		`find . -name '*.go' -exec grep -l TODO {} \;`,
		`bash -c "ls -la"`,
		`sh -c 'echo hello'`,
		`env FOO=1 go test ./...`,
		`nohup npm run build`,
		`timeout 30 go build ./...`,
		`nice -n 10 make`,
	}
	for _, cmd := range cases {
		t.Run(cmd, func(t *testing.T) {
			if got, reason := classifyShellCommand(cmd); got {
				t.Fatalf("classifyShellCommand(%q) = true (%s), want not destructive", cmd, reason)
			}
		})
	}
}

func TestIsDestructiveNestingDepth(t *testing.T) {
	// At the depth limit a wrapper is refused rather than followed: we stop
	// descending, and an unexamined wrapper is exactly what the wall is for.
	// Driving this through a hand-escaped shell string would really be a test
	// of the parser's unescaping, so call the guard directly.
	for _, cmd := range []string{"bash", "xargs", "find"} {
		t.Run(cmd, func(t *testing.T) {
			reason, bad := matchWrapped(cmd, []string{"-c", "ls"}, maxUnwrapDepth)
			if !bad {
				t.Fatalf("matchWrapped(%q, depth=%d) = false, want refused", cmd, maxUnwrapDepth)
			}
			if reason == "" {
				t.Error("refusal came with no reason")
			}
		})
	}

	// Below the limit the same wrapper is analysed normally.
	if _, bad := matchWrapped("bash", []string{"-c", "ls"}, 0); bad {
		t.Error("`bash -c ls` should be analysed and found benign at depth 0")
	}
}

func TestIsDestructiveRmGranularity(t *testing.T) {
	// Plain removes stay out of the wall on purpose: they still hit normal
	// approval, but full_access shouldn't prompt for every deleted file.
	tests := []struct {
		cmd  string
		want bool
	}{
		{`rm foo.txt`, false},
		{`rm a.txt b.txt`, false},
		{`rm -r build`, true},
		{`rm -rf node_modules`, true},
		{`rm -f /etc/hosts`, true},
		{`rm /`, true},
		{`rm ~/notes.md`, true},
		{`rm $HOME/notes.md`, true},
		{`rm /usr/local/bin/tool`, true},
	}
	for _, tt := range tests {
		t.Run(tt.cmd, func(t *testing.T) {
			if got, reason := classifyShellCommand(tt.cmd); got != tt.want {
				t.Fatalf("classifyShellCommand(%q) = %v (%s), want %v", tt.cmd, got, reason, tt.want)
			}
		})
	}
}

func TestIsDestructiveBaselineRules(t *testing.T) {
	tests := []struct {
		cmd  string
		want bool
	}{
		{`sudo ls`, true},
		{`git reset --hard`, true},
		{`git clean -fd`, true},
		{`git push --force origin main`, true},
		{`git checkout .`, true},
		{`chmod -R 777 .`, true},
		{`chown -R me:me .`, true},
		{`dd if=/dev/zero of=/dev/disk2`, true},
		{`dd if=disk.img`, false},
		{`mkfs.ext4 /dev/sda1`, true},
		{`truncate -s 0 app.log`, true},
		{`shred secret.txt`, true},
		{`kill 1234`, true},
		{`diskutil eraseDisk JHFS+ Untitled /dev/disk2`, true},
		{`git status`, false},
		{`git commit -m "wip"`, false},
		{`ls -la`, false},
		{`echo 'rm -rf /'`, false}, // quoted, not executed
	}
	for _, tt := range tests {
		t.Run(tt.cmd, func(t *testing.T) {
			if got, reason := classifyShellCommand(tt.cmd); got != tt.want {
				t.Fatalf("classifyShellCommand(%q) = %v (%s), want %v", tt.cmd, got, reason, tt.want)
			}
		})
	}
}

func TestIsDestructiveUnparseableFailsClosed(t *testing.T) {
	if got, _ := classifyShellCommand(`rm -rf "unterminated`); !got {
		t.Fatal("unparseable command should fail closed as destructive")
	}
}
