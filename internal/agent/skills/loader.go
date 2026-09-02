// Package skills provides a lazy-loaded skill registry.
//
// Each subdirectory of internal/agent/skills/ that contains a SKILL.md is a
// skill. SKILL.md must start with a minimal YAML frontmatter:
//
//	---
//	name: browser-use
//	description: <one-line trigger hints>
//	---
//	<markdown body>
//
// The system prompt only carries the (name, description) index — the body
// is fetched on demand by the load_skill tool.
//
// 除了 SKILL.md 之外，一个 skill 目录可以放辅助文件（REFERENCE.md / FORMS.md
// 之类），以及 scripts/ 子目录里放 agent 可直接执行的 python 脚本。整个 skill
// 目录在启动时释放到磁盘（默认 <dataDir>/skills/builtin/<name>/），agent 通过
// read_file / run_command 访问这些辅助文件和脚本。
//
// 除内置技能外，Loader 还扫描用户技能目录（.lingcowork/skills/，与 mcp.json
// 同级）：Skill Hub 从注册中心装下来的技能落在那里。两个来源合成一份索引，
// 对模型不可见地一视同仁。索引不再是启动时的快照 —— Refresh 重扫两个根，
// 每轮 agent 运行开始时调用，装完的技能下一轮就能被看见。
package skills

import (
	"embed"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"unicode/utf8"
)

//go:embed all:*
var skillsFS embed.FS

// Source distinguishes where a skill was discovered. The model never sees
// this — it exists so installers know which names are reserved and the UI
// can say where a skill came from.
type Source string

const (
	SourceBuiltin Source = "builtin"
	SourceUser    Source = "user"
)

type Skill struct {
	Name        string
	Description string
	Body        string
	// Path 是 skill 目录在磁盘上的绝对路径。agent 用这个路径去 read 辅助文件
	// 或 run_command uv run <path>/scripts/xxx.py。
	Path   string
	Source Source
}

// UserDirName is where installed skills live, relative to the directory that
// also holds mcp.json. Kept next to the MCP config deliberately: both are
// user-owned, per-project state that survives restarts, unlike data/ which
// the app treats as rebuildable.
const UserDirName = "skills"

// namePattern is the Agent Skills spec's rule for a skill name, which is
// also its directory name: lowercase alphanumerics and single hyphens.
var namePattern = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

const (
	maxNameLength        = 64
	maxDescriptionLength = 4096
)

// ValidateName is the single source of truth for what a skill (directory)
// name may look like. Exported so the installer rejects a bad name with the
// exact same rule discovery uses — notably before joining it into a
// filesystem path — instead of copying the regex and letting the two drift.
func ValidateName(name string) error {
	if name == "" {
		return errors.New("skill name is empty")
	}
	if utf8.RuneCountInString(name) > maxNameLength {
		return fmt.Errorf("skill name %q is longer than %d characters", name, maxNameLength)
	}
	if !namePattern.MatchString(name) {
		return fmt.Errorf("skill name %q must be lowercase alphanumerics and single hyphens", name)
	}
	return nil
}

// ValidateMetadata judges a parsed SKILL.md against the directory it lives
// in. Discovery and the Skill Hub installer both use it, so whatever
// installs is guaranteed discoverable — and an installer failure can say
// which rule the package broke.
func ValidateMetadata(s Skill, directoryName string) error {
	if err := ValidateName(s.Name); err != nil {
		return err
	}
	if s.Name != directoryName {
		return fmt.Errorf("SKILL.md declares name %q but the directory is %q", s.Name, directoryName)
	}
	if strings.TrimSpace(s.Description) == "" {
		return errors.New("SKILL.md has an empty description")
	}
	if utf8.RuneCountInString(s.Description) > maxDescriptionLength {
		return fmt.Errorf("SKILL.md description is longer than %d characters", maxDescriptionLength)
	}
	return nil
}

type Loader struct {
	mu          sync.RWMutex
	skills      map[string]Skill
	builtinPath string // <dataDir>/skills/builtin
	userPath    string // <project>/.lingcowork/skills
}

// NewLoader 释放 embed 内容到 dataDir/skills/builtin，然后扫内置与用户两个
// 目录建索引。builtin 目录每次启动**清空**重建，避免旧脚本残留；用户目录
// （.lingcowork/skills/）只扫描，绝不清空 —— 那里放的是 Skill Hub 装的技能。
// dataDir 为空时走当前工作目录下的 data/。
func NewLoader(dataDir string) (*Loader, error) {
	return NewLoaderAt(dataDir, ResolveUserSkillsDir())
}

// NewLoaderAt is NewLoader with an explicit user-skills directory — tests
// point it at a temp dir so they don't pick up whatever the developer has
// installed under the repo's real .lingcowork/.
func NewLoaderAt(dataDir, userDir string) (*Loader, error) {
	if dataDir == "" {
		dataDir = "data"
	}
	builtinPath, err := filepath.Abs(filepath.Join(dataDir, "skills", "builtin"))
	if err != nil {
		return nil, fmt.Errorf("skills: resolve root: %w", err)
	}

	if err := os.RemoveAll(builtinPath); err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("skills: clean %s: %w", builtinPath, err)
	}
	if err := os.MkdirAll(builtinPath, 0o755); err != nil {
		return nil, fmt.Errorf("skills: mkdir %s: %w", builtinPath, err)
	}
	if err := extractEmbed(skillsFS, builtinPath); err != nil {
		return nil, fmt.Errorf("skills: extract: %w", err)
	}

	l := &Loader{
		builtinPath: builtinPath,
		userPath:    userDir,
	}
	if err := l.Refresh(); err != nil {
		return nil, err
	}
	return l, nil
}

// ResolveUserSkillsDir finds the installed-skills directory by walking up
// from the working directory looking for .lingcowork/ — the same walk the
// MCP config uses, because the server is started from various depths and a
// fixed relative path only works from the repo root. When no .lingcowork/
// exists yet, the returned path is where it would go, so the installer can
// create it.
func ResolveUserSkillsDir() string {
	dir, err := os.Getwd()
	if err != nil {
		return filepath.Join(".lingcowork", UserDirName)
	}
	start := dir
	for range 8 {
		candidate := filepath.Join(dir, ".lingcowork")
		if fi, err := os.Stat(candidate); err == nil && fi.IsDir() {
			return filepath.Join(candidate, UserDirName)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return filepath.Join(start, ".lingcowork", UserDirName)
}

// Refresh rescans both roots and swaps in the new index.
//
// Builtin first, then user, first-name-wins: an installed skill must not
// shadow a builtin one out of existence (the installer refuses those names
// too, but a hand-copied directory bypasses the installer). A malformed
// builtin skill is a hard error — those ship with the binary and a
// violation is our bug. A malformed user skill is logged and skipped: one
// broken download must not take the whole index down.
func (l *Loader) Refresh() error {
	out := map[string]Skill{}
	if err := scanRoot(l.builtinPath, SourceBuiltin, out); err != nil {
		return err
	}
	// scanRoot for the user directory never returns an error by
	// construction; a missing directory simply contributes nothing.
	_ = scanRoot(l.userPath, SourceUser, out)

	l.mu.Lock()
	l.skills = out
	l.mu.Unlock()
	return nil
}

func scanRoot(root string, source Source, out map[string]Skill) error {
	if root == "" {
		return nil
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		if source == SourceBuiltin {
			return fmt.Errorf("skills: read root: %w", err)
		}
		return nil
	}
	for _, e := range entries {
		if !e.IsDir() && e.Type()&fs.ModeSymlink == 0 {
			continue
		}
		skillDir := filepath.Join(root, e.Name())
		mdPath := filepath.Join(skillDir, "SKILL.md")
		raw, err := os.ReadFile(mdPath)
		if err != nil {
			// 子目录里没有 SKILL.md 就跳过 —— 便于放非 skill 的辅助目录，
			// 也覆盖安装器的 .staging-* 残留。
			continue
		}
		sk, err := parseSkill(string(raw))
		if err == nil {
			err = ValidateMetadata(sk, e.Name())
		}
		if err != nil {
			if source == SourceBuiltin {
				return fmt.Errorf("skills: %s: %w", mdPath, err)
			}
			log.Printf("skills: skipping %s: %v", skillDir, err)
			continue
		}
		if _, taken := out[sk.Name]; taken {
			if source == SourceBuiltin {
				return fmt.Errorf("skills: duplicate name %q", sk.Name)
			}
			log.Printf("skills: %s shadowed by an earlier skill of the same name", skillDir)
			continue
		}
		sk.Path = skillDir
		sk.Source = source
		out[sk.Name] = sk
	}
	return nil
}

// RootPath 返回内置 skill 目录在磁盘上的绝对路径。用于诊断和 UI 展示。
func (l *Loader) RootPath() string { return l.builtinPath }

// UserDir 返回用户（Skill Hub 安装）技能目录。目录可能尚不存在。
func (l *Loader) UserDir() string { return l.userPath }

// ReservedBuiltinNames lists the names an installed skill must not take —
// discovery gives builtin priority, so a same-named install would be
// invisible, which is worse than refusing it with a reason.
func (l *Loader) ReservedBuiltinNames() []string {
	l.mu.RLock()
	defer l.mu.RUnlock()
	var names []string
	for n, s := range l.skills {
		if s.Source == SourceBuiltin {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	return names
}

// Index returns the (name, description, source) list sorted alphabetically
// by name. Callers stitch it into the system prompt so the LLM knows what
// skills are available without paying the token cost of every body upfront.
func (l *Loader) Index() []Skill {
	l.mu.RLock()
	defer l.mu.RUnlock()
	names := make([]string, 0, len(l.skills))
	for n := range l.skills {
		names = append(names, n)
	}
	sort.Strings(names)
	out := make([]Skill, len(names))
	for i, n := range names {
		s := l.skills[n]
		out[i] = Skill{Name: s.Name, Description: s.Description, Source: s.Source}
	}
	return out
}

// Load returns the full SKILL.md body for the given name (with Path filled).
func (l *Loader) Load(name string) (Skill, error) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	s, ok := l.skills[name]
	if !ok {
		return Skill{}, ErrNotFound
	}
	return s, nil
}

// Names lists just the available skill names — used by the tool schema hint.
func (l *Loader) Names() []string {
	l.mu.RLock()
	defer l.mu.RUnlock()
	names := make([]string, 0, len(l.skills))
	for n := range l.skills {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

var ErrNotFound = errors.New("skill not found")

// extractEmbed 把 embed.FS 里的所有文件递归复制到 dst 目录。保留原目录结构。
// 权限：目录 0755，文件按扩展名决定 —— .py / .sh 给 0755（可执行），其他 0644。
func extractEmbed(src embed.FS, dst string) error {
	return fs.WalkDir(src, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == "." {
			return nil
		}
		// 跳过 loader.go 本身（跟 SKILL.md 一起在 embed 根，但不是 skill 内容）
		if !d.IsDir() && filepath.Dir(path) == "." {
			return nil
		}
		target := filepath.Join(dst, path)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		in, err := src.Open(path)
		if err != nil {
			return err
		}
		defer in.Close()
		mode := os.FileMode(0o644)
		switch strings.ToLower(filepath.Ext(path)) {
		case ".py", ".sh":
			mode = 0o755
		}
		out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
		if err != nil {
			return err
		}
		if _, err := io.Copy(out, in); err != nil {
			out.Close()
			return err
		}
		return out.Close()
	})
}

// ParseDocument parses a SKILL.md document without judging it against any
// directory. The installer uses it to learn the declared name before the
// target directory exists; everything else should go through the scan.
func ParseDocument(content string) (Skill, error) {
	return parseSkill(content)
}

// parseSkill splits a SKILL.md string into its YAML frontmatter and body.
// Only name and description keys are recognised; nothing fancier because
// the format is intentionally minimal.
// Line endings are normalised to \n first: SKILL.md files checked out on
// Windows with core.autocrlf=true arrive as CRLF, and a parser that only
// matched "---\n" rejected every builtin skill with "missing frontmatter
// delimiter". A UTF-8 BOM is stripped for the same reason — editors on
// Windows add one silently and it would hide the opening delimiter.
func parseSkill(s string) (Skill, error) {
	s = strings.TrimPrefix(s, "\ufeff")
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")

	if !strings.HasPrefix(s, "---\n") {
		return Skill{}, errors.New("missing frontmatter delimiter")
	}
	front, body, ok := strings.Cut(s[len("---\n"):], "\n---\n")
	if !ok {
		return Skill{}, errors.New("unterminated frontmatter")
	}

	sk := Skill{Body: body}
	// last tracks which recognised key an indented line would continue.
	// Registry-published skills routinely write the description as a YAML
	// folded scalar over several lines; reading only the first line would
	// put a truncated description in the model's index.
	var last *string
	for line := range strings.SplitSeq(front, "\n") {
		if last != nil && line != "" && (line[0] == ' ' || line[0] == '\t') {
			if cont := strings.TrimSpace(line); cont != "" {
				*last = strings.TrimSpace(*last + " " + cont)
			}
			continue
		}
		last = nil
		key, val, ok := splitKV(line)
		if !ok {
			continue
		}
		switch key {
		case "name":
			sk.Name = val
			last = &sk.Name
		case "description":
			sk.Description = val
			last = &sk.Description
		}
	}
	// A folded scalar leaves the key's own line empty ("description: >-"),
	// so strip the YAML folding indicator if it ended up as the prefix.
	for _, indicator := range []string{">-", ">", "|-", "|"} {
		if strings.HasPrefix(sk.Description, indicator+" ") {
			sk.Description = strings.TrimSpace(sk.Description[len(indicator):])
			break
		}
	}
	return sk, nil
}

// splitKV parses "key: value" (possibly with quotes around value). Ignores
// blank / comment lines.
func splitKV(line string) (string, string, bool) {
	line = strings.TrimSpace(line)
	if line == "" || strings.HasPrefix(line, "#") {
		return "", "", false
	}
	k, v, ok := strings.Cut(line, ":")
	if !ok {
		return "", "", false
	}
	k = strings.TrimSpace(k)
	v = strings.TrimSpace(v)
	if len(v) >= 2 && (v[0] == '"' && v[len(v)-1] == '"' || v[0] == '\'' && v[len(v)-1] == '\'') {
		v = v[1 : len(v)-1]
	}
	return k, v, true
}
