package approval

import (
	"encoding/json"
	"path/filepath"
	"regexp"
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

// pathIsSensitive returns (reason, true) for paths the LLM should look at
// instead of the fast path silently approving them. Everything checked here
// is CHEAP — no filesystem access, purely string-level.
//
// This used to also reject absolute paths, home-prefixed paths and `..`
// traversal. Those are gone because the effect's Scope now answers the same
// question by actually resolving the path against the workspace root, which
// is both stricter (a relative path that climbs out is caught) and less
// blunt (an absolute path pointing INTO the workspace is no longer treated
// as an escape).
func pathIsSensitive(path string) (string, bool) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "empty path", true
	}
	// Some names are load-bearing enough that we don't want an auto-approve
	// to slip past even inside the workspace. Match whole segments, so
	// "docs/env-setup.md" isn't flagged.
	cleaned := filepath.ToSlash(filepath.Clean(path))
	for _, seg := range strings.Split(cleaned, "/") {
		if reason, hit := sensitiveSegment(seg); hit {
			return reason, true
		}
	}
	return "", false
}

// PathIsSensitive exposes the same cheap path classifier to bounded workspace
// search tools. Search cannot ask for approval once per discovered file, so it
// omits known credential paths before reading them. Keeping one classifier
// prevents search and write approval from drifting apart.
func PathIsSensitive(path string) (string, bool) {
	return pathIsSensitive(path)
}

var (
	// Exact-match basenames or family prefixes for common credential /
	// system-config files. Match runs on individual path segments, so we
	// use a small set of literals plus a couple of family predicates.
	sensitiveExactBasename = map[string]bool{
		".npmrc":           true,
		".pypirc":          true,
		".netrc":           true,
		".gitconfig":       true,
		"credentials.json": true,
		"mcp.json":         true,
	}
	// Directory names that should not be silently written into.
	sensitiveDirname = map[string]bool{
		".ssh":    true,
		".aws":    true,
		".gcp":    true,
		".azure":  true,
		".gnupg":  true,
		".config": true,
		".git":    true,
	}
	sshKeyPrefixRE = regexp.MustCompile(`^id_(rsa|ed25519|ecdsa|dsa)`)
)

func sensitiveSegment(seg string) (string, bool) {
	if seg == "" {
		return "", false
	}
	if sensitiveDirname[seg] {
		return "sensitive directory: " + seg, true
	}
	if sensitiveExactBasename[seg] {
		return "sensitive basename: " + seg, true
	}
	if sshKeyPrefixRE.MatchString(seg) {
		return "ssh key file: " + seg, true
	}
	return "", false
}

const contentScanCap = 4 * 1024

var credentialSignatureRE = regexp.MustCompile(
	`-----BEGIN ` + // PEM key blocks (RSA/EC/OPENSSH/CERTIFICATE)
		`|AKIA[0-9A-Z]{16}` + // AWS access key id
		`|ASIA[0-9A-Z]{16}` + // AWS temporary access key
		`|ghp_[A-Za-z0-9]{20,}` + // GitHub personal access token
		`|gho_[A-Za-z0-9]{20,}` + // GitHub OAuth token
		`|sk_live_[A-Za-z0-9]{16,}` + // Stripe live secret key
		`|xox[baprs]-[A-Za-z0-9-]{10,}`, // Slack tokens
)

func contentLooksSensitive(content string) (string, bool) {
	if content == "" {
		return "", false
	}
	head := content
	if len(head) > contentScanCap {
		head = head[:contentScanCap]
	}
	if m := credentialSignatureRE.FindString(head); m != "" {
		// Keep the reason short — a leaked key in the reason string would
		// end up in the log; store only the prefix classifier.
		return "credential signature in content", true
	}
	return "", false
}

// isSafeShellCommand is the auto-mode fast path for run_command. Parses the
// command line with mvdan/sh and lets it through ONLY when EVERY sub-command
// (across && / || / ; / |) is on the read-only whitelist AND there are no
// output redirections and no dangerous flags. Anything else falls through to
// the LLM classifier (or human review if that's off).
//
// The whitelist is deliberately narrow. Tools that could write files or run
// arbitrary code (python / node / go / pandoc / marp / typst / ffmpeg / npm /
// pip / uv / cargo / make / brew ...) are NOT here — they can be safe in
// context, but that judgment belongs to the classifier, not this rule set.
func isSafeShellCommand(argsJSON string) (bool, string) {
	if argsJSON == "" {
		return false, "empty args"
	}
	var probe struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &probe); err != nil {
		return false, "unparseable args"
	}
	return isReadOnlyShellCommand(probe.Command)
}

// isReadOnlyShellCommand is the same judgment over a raw command string.
// Split out from isSafeShellCommand so callers holding the command already —
// effect derivation, ClassifyShellCommand — don't have to marshal it back
// into an arguments blob just to get it parsed again.
func isReadOnlyShellCommand(command string) (bool, string) {
	cmd := strings.TrimSpace(command)
	if cmd == "" {
		return false, "empty command"
	}
	file, err := syntax.NewParser().Parse(strings.NewReader(cmd), "")
	if err != nil {
		return false, "shell parse failed"
	}
	safe := true
	reason := "readonly_shell"
	syntax.Walk(file, func(node syntax.Node) bool {
		if !safe {
			return false
		}
		// Redirections live on Stmt, not CallExpr — walk Stmt first to catch
		// `... > out.txt` before we assess the command name.
		if stmt, ok := node.(*syntax.Stmt); ok {
			for _, r := range stmt.Redirs {
				if isWriteRedirect(r.Op) {
					safe = false
					reason = "has output redirection"
					return false
				}
			}
			return true
		}
		call, ok := node.(*syntax.CallExpr)
		if !ok {
			return true
		}
		if len(call.Args) == 0 {
			return true
		}
		words := extractWords(call.Args)
		if len(words) == 0 {
			return true // bare env-assignment only, no command yet
		}
		if ok, why := isReadOnlyInvocation(words); !ok {
			safe = false
			reason = why
			return false
		}
		return true
	})
	if !safe {
		return false, reason
	}
	return true, reason
}

// isWriteRedirect returns true for redirection ops that create / append to /
// truncate a target file. See mvdan/sh/v3/syntax RedirOperator constants.
func isWriteRedirect(op syntax.RedirOperator) bool {
	switch op {
	case syntax.RdrOut, syntax.AppOut, syntax.RdrAll, syntax.AppAll, syntax.ClbOut:
		return true
	}
	return false
}

// shellReadOnlyCommands is the set of first-token commands that are safe on
// their own. Some entries need per-argument checks (find/git) — those get
// special-cased in isReadOnlyInvocation before the map lookup falls through.
var shellReadOnlyCommands = map[string]bool{
	"pwd":      true,
	"ls":       true,
	"cat":      true,
	"head":     true,
	"tail":     true,
	"wc":       true,
	"file":     true,
	"stat":     true,
	"du":       true,
	"df":       true,
	"grep":     true,
	"rg":       true,
	"fd":       true,
	"jq":       true,
	"echo":     true,
	"printf":   true,
	"date":     true,
	"whoami":   true,
	"hostname": true,
	"uname":    true,
	"which":    true,
	"type":     true,
	"env":      true,
	"true":     true,
	"false":    true,
	"basename": true,
	"dirname":  true,
	"realpath": true,
	"sort":     true,
	"uniq":     true,
	"cut":      true,
	"tr":       true,
}

var exploreProbeCommands = map[string]bool{
	"pwd": true, "ls": true, "file": true, "stat": true, "wc": true,
	"rg": true, "jq": true, "head": true, "tail": true,
	"which": true, "type": true,
}

var exploreVersionCommands = map[string]bool{
	"go": true, "node": true, "npm": true, "pnpm": true,
	"python": true, "python3": true, "java": true, "javac": true,
	"cargo": true, "rustc": true,
}

var exploreGitSubcommands = map[string]bool{
	"status": true, "diff": true, "log": true, "show": true,
	"blame": true, "rev-parse": true, "ls-files": true,
	"ls-tree": true, "cat-file": true,
}

// IsExploreProbeCommand is stricter than the normal shell read-only fast path.
// Explore receives run_command for repository inspection, but it must not use
// that generic shell surface to write, execute project code, inspect arbitrary
// absolute paths or leak process credentials.
func IsExploreProbeCommand(command string) (bool, string) {
	command = strings.TrimSpace(command)
	if command == "" {
		return false, "empty command"
	}
	file, err := syntax.NewParser().Parse(strings.NewReader(command), "")
	if err != nil {
		return false, "shell parse failed"
	}
	allowed := true
	reason := "explore probe"
	syntax.Walk(file, func(node syntax.Node) bool {
		if !allowed {
			return false
		}
		if stmt, ok := node.(*syntax.Stmt); ok {
			if len(stmt.Redirs) > 0 {
				allowed = false
				reason = "redirection is not allowed"
				return false
			}
			return true
		}
		call, ok := node.(*syntax.CallExpr)
		if !ok {
			return true
		}
		words := extractWords(call.Args)
		if len(words) == 0 || words[0] == "" {
			allowed = false
			reason = "dynamic or missing command"
			return false
		}
		for _, word := range words {
			if word == "" || explorePathEscapesWorkspace(word) {
				allowed = false
				reason = "dynamic or external path argument"
				return false
			}
		}
		cmd := filepath.Base(words[0])
		args := words[1:]
		switch {
		case cmd == "git":
			if !isExploreGitProbe(args) {
				allowed = false
				reason = "git command is not an allowed probe"
				return false
			}
		case exploreProbeCommands[cmd]:
			// The generic shell parser already rejected redirection and every
			// argument was checked for absolute/traversal paths above.
		case exploreVersionCommands[cmd]:
			if len(args) != 1 || !helpFlagRE.MatchString(args[0]) {
				allowed = false
				reason = cmd + " is limited to --help/--version"
				return false
			}
		default:
			allowed = false
			reason = "command is not on the Explore probe allowlist: " + cmd
			return false
		}
		return true
	})
	return allowed, reason
}

func isExploreGitProbe(args []string) bool {
	if len(args) == 0 {
		return false
	}
	i := 0
	for i < len(args) && strings.HasPrefix(args[i], "-") {
		if args[i] != "--no-pager" {
			return false
		}
		i++
	}
	if i >= len(args) || !exploreGitSubcommands[args[i]] {
		return false
	}
	ok, _ := isReadOnlyGit(args)
	return ok
}

func explorePathEscapesWorkspace(word string) bool {
	if filepath.IsAbs(word) || strings.HasPrefix(word, "~") ||
		strings.Contains(word, "$HOME") {
		return true
	}
	if _, value, ok := strings.Cut(word, "="); ok && value != "" {
		if filepath.IsAbs(value) || value == ".." ||
			strings.HasPrefix(filepath.ToSlash(filepath.Clean(value)), "../") {
			return true
		}
	}
	cleaned := filepath.ToSlash(filepath.Clean(word))
	return cleaned == ".." || strings.HasPrefix(cleaned, "../") ||
		strings.Contains(cleaned, "/../")
}

// helpFlagRE catches `--help` / `-h` / `--version` / `-V` — any command
// invoked with just these is a read-only probe regardless of the command.
var helpFlagRE = regexp.MustCompile(`^(-h|--help|-V|--version)$`)

func isReadOnlyInvocation(words []string) (bool, string) {
	cmd := filepath.Base(words[0])
	args := words[1:]

	// Pure --help / --version probes are always safe.
	for _, a := range args {
		if helpFlagRE.MatchString(a) {
			return true, "help/version probe: " + cmd
		}
	}

	switch cmd {
	case "find":
		// find IS read-only unless the user passes an action that mutates or
		// executes: -delete / -exec / -execdir / -ok / -okdir / -fprint*.
		for _, a := range args {
			switch a {
			case "-delete", "-exec", "-execdir", "-ok", "-okdir":
				return false, "find action: " + a
			}
			if strings.HasPrefix(a, "-fprint") {
				return false, "find action: " + a
			}
		}
		return true, "find (readonly)"
	case "git":
		return isReadOnlyGit(args)
	}

	if shellReadOnlyCommands[cmd] {
		return true, "readonly command: " + cmd
	}
	return false, "command not on readonly whitelist: " + cmd
}

var gitReadOnlySubcommands = map[string]bool{
	"status":    true,
	"log":       true,
	"show":      true,
	"diff":      true,
	"blame":     true,
	"branch":    true, // listing; -D / -d would delete — see below
	"describe":  true,
	"rev-parse": true,
	"remote":    true,
	"config":    true, // read-only listing without value arg — we don't try to prove that here; leaving it out is safer
	"ls-files":  true,
	"ls-tree":   true,
	"cat-file":  true,
	"tag":       true, // listing; -d would delete
	"stash":     true, // 'stash' alone lists; 'stash push' writes — conservative below
	"reflog":    true,
	"shortlog":  true,
	"fsck":      true,
}

func isReadOnlyGit(args []string) (bool, string) {
	// Skip global flags: -C <dir>, -c key=val, plus long forms.
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
		break
	}
	if i >= len(args) {
		return false, "bare git invocation"
	}
	sub := args[i]
	rest := args[i+1:]
	if !gitReadOnlySubcommands[sub] {
		return false, "git " + sub + " not readonly"
	}
	// Sub-command-specific mutating flags.
	switch sub {
	case "branch", "tag":
		for _, a := range rest {
			if a == "-d" || a == "-D" || a == "--delete" {
				return false, "git " + sub + " " + a
			}
		}
	case "stash":
		// Only bare `git stash` (list) is read-only; anything else writes.
		if len(rest) > 0 {
			for _, a := range rest {
				if a == "list" || a == "show" {
					return true, "git stash " + a
				}
			}
			return false, "git stash with mutating subcommand"
		}
	case "config":
		// A safe read is `git config --get X` / `--list`; anything else may
		// write. Require an explicit read flag to whitelist.
		hasRead := false
		for _, a := range rest {
			if a == "--get" || a == "--list" || a == "-l" || a == "--get-all" ||
				a == "--get-regexp" {
				hasRead = true
				break
			}
		}
		if !hasRead {
			return false, "git config without a read flag"
		}
	}
	return true, "git " + sub + " (readonly)"
}
