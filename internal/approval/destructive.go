package approval

import (
	"path/filepath"
	"regexp"
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

// This file is the destructive wall: the set of shell commands that route to
// a human no matter which approval mode the conversation is in, full_access
// included. A user who elevated the mode is saying they trust the agent's
// judgment, not that they want one click to be able to lose their data.
//
// 判断走 shell AST 解析：用 mvdan/sh 把 command 字符串 parse 成 AST，遍历
// 所有 CallExpr（跨 && || ; | 分段自然拆开），对每一条子命令做 pattern
// 匹配。parse 失败时保守返回 destructive（让 agent 明确拿到一个可诊断的
// 拦截）。
//
// The entry point is ClassifyShellCommand in shellclass.go; effect derivation
// calls it and sets Effect.Destructive from the result. Nothing here switches
// on a tool name — the wall is about what a command does.

// classifyShellCommand parses the command line and returns (destructive,
// reason) on the first match. Parse failure itself is treated as destructive
// with reason "unparseable" — safer than letting an unusual quoting pattern
// slip past the wall.
func classifyShellCommand(cmd string) (bool, string) {
	return classifyShellCommandAt(cmd, 0)
}

// maxUnwrapDepth bounds wrapper recursion. `bash -c "bash -c '...'"` nests
// legitimately a level or two; beyond that it's either obfuscation or a
// pathological input, and either way we stop descending rather than let a
// crafted string drive unbounded parsing.
const maxUnwrapDepth = 4

func classifyShellCommandAt(cmd string, depth int) (bool, string) {
	if depth > maxUnwrapDepth {
		return true, "wrapper nesting too deep to analyse"
	}
	file, err := syntax.NewParser().Parse(strings.NewReader(cmd), "")
	if err != nil {
		return true, "shell parse failed: " + err.Error()
	}
	var (
		hit    bool
		reason string
	)
	syntax.Walk(file, func(node syntax.Node) bool {
		if hit {
			return false
		}
		call, ok := node.(*syntax.CallExpr)
		if !ok || len(call.Args) == 0 {
			return true
		}
		// call.Args[0] 是命令名 —— 但当前 CallExpr 也可能仅是变量赋值
		// (VAR=val)；只有出现真正的命令 token 时才判断。
		words := extractWords(call.Args)
		if len(words) == 0 {
			return true
		}
		if r, bad := matchDestructiveAt(words, depth); bad {
			hit = true
			reason = r
			return false
		}
		return true
	})
	if hit {
		return true, reason
	}
	return false, ""
}

// extractWords flattens the WordParts of each Word in a CallExpr into plain
// strings we can pattern-match. Literals + double-quoted literals are copied
// as-is; anything dynamic (subshell, param expansion, arithmetic) becomes an
// empty string — good enough for the coarse matching we do here. Bare
// assignments like FOO=bar prefixing a command show up as their own Word and
// are skipped (they contain '=' before any command token).
func extractWords(args []*syntax.Word) []string {
	out := make([]string, 0, len(args))
	for _, w := range args {
		s := literalOf(w)
		if s == "" {
			out = append(out, "")
			continue
		}
		// FOO=bar prefix — skip until we see a real command token.
		if len(out) == 0 && strings.ContainsRune(s, '=') && isEnvAssignment(s) {
			continue
		}
		out = append(out, s)
	}
	return out
}

// literalOf pulls the concatenated literal string out of a Word. Non-literal
// parts (subshells, arithmetic, anything computed) turn the whole word into
// "" — we can't safely match against something we can't see.
//
// A plain variable reference is the exception: it renders back as its own
// source text, so `rm $HOME/notes.md` reaches the matcher as "$HOME/notes.md"
// and hits the dangerous-path rule that already names $HOME. Without this the
// rule is unreachable through the AST and only `~` is ever caught.
func literalOf(w *syntax.Word) string {
	var b strings.Builder
	for _, part := range w.Parts {
		switch p := part.(type) {
		case *syntax.Lit:
			b.WriteString(p.Value)
		case *syntax.SglQuoted:
			b.WriteString(p.Value)
		case *syntax.ParamExp:
			name := plainParamName(p)
			if name == "" {
				return ""
			}
			b.WriteString("$" + name)
		case *syntax.DblQuoted:
			// double-quoted: concat inner literals; give up on any dynamic bit.
			for _, inner := range p.Parts {
				switch q := inner.(type) {
				case *syntax.Lit:
					b.WriteString(q.Value)
				case *syntax.ParamExp:
					name := plainParamName(q)
					if name == "" {
						return ""
					}
					b.WriteString("$" + name)
				default:
					return ""
				}
			}
		default:
			return ""
		}
	}
	return b.String()
}

// plainParamName returns the variable name for a bare $NAME / ${NAME}, or ""
// for anything carrying an operator (${x:-y}, ${x#p}, ${a[i]}, ${#x}). Those
// compute a value we have no way to predict, so they stay opaque.
func plainParamName(p *syntax.ParamExp) string {
	if p == nil || p.Param == nil {
		return ""
	}
	if p.Excl || p.Length || p.Width || p.Exp != nil ||
		p.Index != nil || p.Slice != nil || p.Repl != nil || p.Names != 0 {
		return ""
	}
	return p.Param.Value
}

var envAssignRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=`)

func isEnvAssignment(s string) bool {
	return envAssignRE.MatchString(s)
}

// matchDestructive is the concrete pattern set. words[0] is the command; the
// rest are its arguments (literals only — see extractWords). Returns the
// human-readable reason on hit.
func matchDestructive(words []string) (string, bool) {
	return matchDestructiveAt(words, 0)
}

func matchDestructiveAt(words []string, depth int) (string, bool) {
	cmd := filepath.Base(words[0])
	args := words[1:]

	// Wrappers first. `ls | xargs rm -rf`, `bash -c "rm -rf /"` and
	// `find . -exec rm -rf {} \;` all park a harmless token in words[0] and
	// carry the real command in the arguments, so matching words[0] alone
	// walks them straight past the wall. Unwrap and judge the inner command.
	if reason, bad := matchWrapped(cmd, args, depth); bad {
		return reason, true
	}

	switch cmd {
	case "sudo", "su", "doas":
		return "privilege escalation: " + cmd, true
	case "dd":
		// Only DDs that write to something are dangerous; `dd if=X` alone
		// (reading) is not. Look for of=.
		for _, a := range args {
			if strings.HasPrefix(a, "of=") {
				return "dd of=... writes to a destination", true
			}
		}
	case "mkfs", "fdisk":
		return "disk-formatting tool: " + cmd, true
	case "diskutil":
		// diskutil erase*, secureErase, apfs eraseDisk, etc.
		for _, a := range args {
			la := strings.ToLower(a)
			if strings.HasPrefix(la, "erase") {
				return "diskutil " + a, true
			}
		}
	case "shred":
		return "shred permanently overwrites files", true
	case "rm":
		return matchRm(args)
	case "kill", "pkill", "killall":
		return "process-killing command: " + cmd, true
	case "chmod":
		return matchChmod(args)
	case "chown":
		return matchChown(args)
	case "truncate":
		// truncate -s 0 <file> — zeros out target files.
		for i, a := range args {
			if a == "-s" && i+1 < len(args) && strings.TrimLeft(args[i+1], "+") == "0" {
				return "truncate -s 0 zeros the target file", true
			}
			if a == "-s0" || a == "-s+0" {
				return "truncate -s0 zeros the target file", true
			}
		}
	case "git":
		return matchGit(args)
	}

	// mkfs.*, e.g. mkfs.ext4 / mkfs.apfs — filepath.Base loses the dot suffix
	// on the first switch only if the raw word already had a slash; fall
	// through here on the un-based name for safety.
	if strings.HasPrefix(cmd, "mkfs.") {
		return "disk-formatting tool: " + cmd, true
	}

	return "", false
}

// wrapperSpec describes how to skip a wrapper's own arguments to reach the
// command it will run.
type wrapperSpec struct {
	// valueFlags consume a separate following argument (xargs -I {}).
	// Attached forms (-I{}, --max-args=3) are handled generically.
	valueFlags map[string]bool
	// skipPositionals counts non-flag arguments belonging to the wrapper
	// itself before the inner command starts — timeout's duration.
	skipPositionals int
	// skipAssignments consumes leading VAR=value pairs, as env takes them.
	skipAssignments bool
}

// execWrappers run the arguments that follow them as a command in their own
// right. sudo / su / doas are deliberately absent: they are already flagged
// destructive on sight, so there is nothing left to learn from unwrapping.
var execWrappers = map[string]wrapperSpec{
	"xargs": {valueFlags: map[string]bool{
		"-I": true, "-i": true, "-n": true, "-L": true, "-P": true,
		"-s": true, "-a": true, "-d": true, "-E": true,
		"--replace": true, "--max-args": true, "--max-procs": true,
		"--max-lines": true, "--delimiter": true, "--arg-file": true,
	}},
	"nohup": {},
	"nice":  {valueFlags: map[string]bool{"-n": true, "--adjustment": true}},
	"timeout": {
		valueFlags: map[string]bool{
			"-s": true, "--signal": true, "-k": true, "--kill-after": true,
		},
		skipPositionals: 1,
	},
	"env": {
		skipAssignments: true,
		valueFlags: map[string]bool{
			"-u": true, "--unset": true, "-C": true, "--chdir": true, "-S": true,
		},
	},
	"stdbuf":   {valueFlags: map[string]bool{"-i": true, "-o": true, "-e": true}},
	"setsid":   {},
	"ionice":   {valueFlags: map[string]bool{"-c": true, "-n": true, "-p": true}},
	"chroot":   {skipPositionals: 1},
	"time":     {},
	"watch":    {valueFlags: map[string]bool{"-n": true, "-d": true}},
	"parallel": {valueFlags: map[string]bool{"-j": true, "-n": true}},
}

// shellWrappers take a command STRING via -c and interpret it themselves, so
// the payload has to go back through the parser rather than be treated as
// tokens.
var shellWrappers = map[string]bool{
	"bash": true, "sh": true, "zsh": true, "dash": true, "ksh": true, "fish": true,
}

// matchWrapped checks the command hidden inside a wrapper invocation.
func matchWrapped(cmd string, args []string, depth int) (string, bool) {
	if depth >= maxUnwrapDepth {
		// Refuse rather than descend. A wrapper we decline to look inside is
		// exactly the case the wall exists for.
		if shellWrappers[cmd] || isExecWrapper(cmd) || cmd == "find" {
			return "wrapper nesting too deep to analyse: " + cmd, true
		}
		return "", false
	}

	if shellWrappers[cmd] {
		script := shellStringArg(args)
		if script == "" {
			return "", false
		}
		if bad, reason := classifyShellCommandAt(script, depth+1); bad {
			return cmd + " -c → " + reason, true
		}
		return "", false
	}

	if spec, ok := execWrappers[cmd]; ok {
		inner := innerOfExecWrapper(spec, args)
		if len(inner) == 0 {
			return "", false
		}
		if reason, bad := matchDestructiveAt(inner, depth+1); bad {
			return cmd + " → " + reason, true
		}
		return "", false
	}

	if cmd == "find" {
		inner := findExecInner(args)
		if len(inner) == 0 {
			return "", false
		}
		if reason, bad := matchDestructiveAt(inner, depth+1); bad {
			return "find -exec → " + reason, true
		}
	}

	return "", false
}

func isExecWrapper(cmd string) bool {
	_, ok := execWrappers[cmd]
	return ok
}

// shellStringArg pulls the argument of -c out of a shell invocation.
// Combined short flags are honoured too: `bash -lc "..."` is as valid as
// `bash -c "..."`.
func shellStringArg(args []string) string {
	for i, a := range args {
		if !strings.HasPrefix(a, "-") || a == "-" || a == "--" {
			continue
		}
		if strings.HasPrefix(a, "--") {
			continue
		}
		if strings.ContainsRune(a[1:], 'c') && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

// innerOfExecWrapper skips the wrapper's own flags and positionals and
// returns the remaining tokens — the command it is about to run.
func innerOfExecWrapper(spec wrapperSpec, args []string) []string {
	i := 0
	skipped := 0
	for i < len(args) {
		a := args[i]
		if a == "--" {
			i++
			break
		}
		if spec.skipAssignments && isEnvAssignment(a) {
			i++
			continue
		}
		if strings.HasPrefix(a, "-") && a != "-" {
			// --flag=value / -I{} carry their value inline; a bare
			// value-taking flag eats the next token.
			if strings.ContainsRune(a, '=') {
				i++
				continue
			}
			if spec.valueFlags[a] {
				i += 2
				continue
			}
			if len(a) > 2 && spec.valueFlags[a[:2]] {
				i++
				continue
			}
			i++
			continue
		}
		if skipped < spec.skipPositionals {
			skipped++
			i++
			continue
		}
		break
	}
	if i >= len(args) {
		return nil
	}
	return args[i:]
}

// findExecInner returns the command tokens of a find action. The action runs
// from just after -exec up to the terminating ';' or '+'. The terminator
// reaches us as ";" when the shell escape was consumed by the parser and as
// "\\;" when it survived, so both spellings are accepted.
func findExecInner(args []string) []string {
	for i, a := range args {
		switch a {
		case "-exec", "-execdir", "-ok", "-okdir":
			rest := args[i+1:]
			for j, t := range rest {
				if t == ";" || t == `\;` || t == "+" {
					return rest[:j]
				}
			}
			return rest
		}
	}
	return nil
}

// matchRm flags recursive removes and any rm targeting well-known dangerous
// paths (/, /tmp/**, $HOME/*, workspace root, etc.). Plain `rm foo.txt` is
// NOT flagged as destructive — it still gets caught by normal approval.
func matchRm(args []string) (string, bool) {
	recursive := false
	force := false
	targets := make([]string, 0, len(args))
	for _, a := range args {
		if strings.HasPrefix(a, "-") && !strings.HasPrefix(a, "--") {
			flags := a[1:]
			if strings.ContainsAny(flags, "rR") {
				recursive = true
			}
			if strings.ContainsRune(flags, 'f') {
				force = true
			}
			continue
		}
		switch a {
		case "--recursive", "--force":
			if a == "--recursive" {
				recursive = true
			} else {
				force = true
			}
			continue
		}
		targets = append(targets, a)
	}
	if recursive {
		if force {
			return "rm -rf", true
		}
		return "rm -r", true
	}
	for _, t := range targets {
		if isDangerousPathTarget(t) {
			return "rm targeting " + t, true
		}
	}
	return "", false
}

// matchChmod flags recursive world-writable / world-anything -R changes and
// the classic chmod 777.
func matchChmod(args []string) (string, bool) {
	recursive := false
	for _, a := range args {
		if a == "-R" || a == "--recursive" {
			recursive = true
			continue
		}
		if a == "777" || a == "0777" {
			if recursive {
				return "chmod -R 777", true
			}
			// Non-recursive 777 on one file is less scary; still flag it.
			return "chmod 777", true
		}
	}
	return "", false
}

// matchChown flags `chown -R` — recursive ownership changes are almost never
// what an agent should do on its own.
func matchChown(args []string) (string, bool) {
	for _, a := range args {
		if a == "-R" || a == "--recursive" {
			return "chown -R", true
		}
	}
	return "", false
}

// matchGit flags the small set of git operations that lose work: hard reset,
// clean -fd, force push. Regular commits / adds / status / diff are safe.
func matchGit(args []string) (string, bool) {
	if len(args) == 0 {
		return "", false
	}
	// Skip global flags like `-C dir` / `-c key=val` to find the subcommand.
	sub := ""
	i := 0
	for i < len(args) {
		a := args[i]
		if a == "-C" || a == "-c" {
			i += 2
			continue
		}
		if strings.HasPrefix(a, "--") || strings.HasPrefix(a, "-") {
			i++
			continue
		}
		sub = a
		i++
		break
	}
	rest := args[i:]
	switch sub {
	case "reset":
		for _, a := range rest {
			if a == "--hard" {
				return "git reset --hard", true
			}
		}
	case "clean":
		for _, a := range rest {
			flags := strings.TrimPrefix(a, "-")
			if strings.HasPrefix(a, "-") && strings.ContainsRune(flags, 'f') &&
				(strings.ContainsRune(flags, 'd') || strings.ContainsRune(flags, 'x')) {
				return "git clean -fd/-fdx", true
			}
			if a == "--force" {
				return "git clean --force", true
			}
		}
	case "push":
		for _, a := range rest {
			if a == "--force" || a == "-f" || a == "--force-with-lease" {
				return "git push --force", true
			}
		}
	case "checkout", "restore":
		// git checkout . / git restore . 会丢工作区改动
		for _, a := range rest {
			if a == "." {
				return "git " + sub + " . discards working-tree changes", true
			}
		}
	}
	return "", false
}

// IsDangerousPath reports whether a path is one of the well-known targets
// that must never be removed on the agent's own initiative. Exported for
// effect derivation, which applies the same bar to the rm TOOL that the
// shell wall applies to `rm` on a command line — the two should not disagree
// about what counts as irreversible.
func IsDangerousPath(target string) bool {
	return isDangerousPathTarget(target)
}

// isDangerousPathTarget catches literal targets that shouldn't be blown
// away even with a bare rm: /, /*, /**, ~, $HOME, /tmp, /usr, /etc, etc.
// Purely string-level — the fast approval layer never touches the FS.
func isDangerousPathTarget(t string) bool {
	if t == "" {
		return false
	}
	// Strip a single leading -- (rm -- /path). We don't strip further to keep
	// paths recognisable in the reason string above.
	if t == "--" {
		return false
	}
	if t == "/" || t == "/*" || t == "/**" {
		return true
	}
	if t == "~" || t == "$HOME" || strings.HasPrefix(t, "~/") || strings.HasPrefix(t, "$HOME/") {
		return true
	}
	// Common system directories a coding agent has no business touching.
	dangerous := []string{
		"/bin", "/sbin", "/usr", "/etc", "/var", "/System", "/Library",
		"/Applications", "/private", "/boot", "/root", "/dev", "/proc",
	}
	for _, d := range dangerous {
		if t == d || strings.HasPrefix(t, d+"/") {
			return true
		}
	}
	return false
}
