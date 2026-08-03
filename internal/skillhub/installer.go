package skillhub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/guyi-a/Interview-Agent/internal/agent/skills"
)

// MarkerFile 是 Hub 安装时写进技能目录的元数据文件，用于识别安装来源与版本。
// 与 klingwork 同名，两边装的技能可以互相识别。
const MarkerFile = ".skill-hub.json"

// Installed 描述本地已安装的一个技能。
type Installed struct {
	// Name 是目录名，也就是 loader 发现的技能名。
	Name string `json:"name"`
	// FullSlug 对应注册中心的 fullSlug；非 Hub 装的技能回退为目录名。
	FullSlug  string `json:"fullSlug"`
	Version   string `json:"version,omitempty"`
	Directory string `json:"directory"`
}

type marker struct {
	FullSlug    string  `json:"fullSlug"`
	Version     *string `json:"version"`
	Registry    string  `json:"registry"`
	InstalledAt string  `json:"installedAt"`
}

// Service bundles the registry client with the local install/uninstall
// operations. reservedNames is queried at install time (not captured) so it
// always reflects the loader's current builtin set.
type Service struct {
	Registry      *Registry
	SkillsDir     string
	ReservedNames func() []string
}

func NewService(reg *Registry, skillsDir string, reservedNames func() []string) *Service {
	if reservedNames == nil {
		reservedNames = func() []string { return nil }
	}
	return &Service{Registry: reg, SkillsDir: skillsDir, ReservedNames: reservedNames}
}

// Install downloads fullSlug (at version, or latest when empty), verifies
// the bundle, and lands it in the skills directory under the name SKILL.md
// declares. The write is staged and swapped so the target directory either
// holds the complete new version or whatever it held before — never a
// half-written skill.
func (s *Service) Install(ctx context.Context, fullSlug, requestedVersion string) (Installed, error) {
	if !ValidFullSlug(fullSlug) {
		return Installed{}, fmt.Errorf("非法的技能 slug: %q", fullSlug)
	}
	bundle, err := s.Registry.DownloadBundle(ctx, fullSlug, requestedVersion)
	if err != nil {
		return Installed{}, err
	}
	// 注册中心的 bundle 主格式是 ZIP；gzip/tar 是外部 registry 的旧格式，不支持。
	if len(bundle) >= 2 && bundle[0] == 0x1f && bundle[1] == 0x8b {
		return Installed{}, errors.New("技能包是 gzip 格式，只支持 ZIP")
	}
	files, err := unzipBounded(bundle)
	if err != nil {
		return Installed{}, err
	}
	skillMD, ok := files["SKILL.md"]
	if !ok {
		return Installed{}, errors.New("技能包缺少 SKILL.md")
	}

	// 目录名必须等于 frontmatter name，否则 loader 不会发现这个技能。
	// 校验规则与 loader 的 scan 完全同一份 —— 装得进去的必然发现得了。
	directoryName := declaredName(string(skillMD))
	if directoryName == "" {
		directoryName = slugTail(fullSlug)
	}
	if parsed, perr := skills.ParseDocument(string(skillMD)); perr != nil {
		return Installed{}, fmt.Errorf("SKILL.md 无法解析: %w", perr)
	} else if verr := skills.ValidateMetadata(parsed, directoryName); verr != nil {
		return Installed{}, fmt.Errorf("技能包不符合发现规则: %w", verr)
	}
	// 内置技能名不可被市场技能占用（loader 里内置优先，同名安装等于装了个
	// 隐身技能）。必须在任何破坏性文件操作之前拦下。
	if slices.Contains(s.ReservedNames(), directoryName) {
		return Installed{}, fmt.Errorf("%q 是内置技能的保留名，不能安装", directoryName)
	}

	version := requestedVersion
	if version == "" {
		version = versionFrom(string(skillMD))
	}

	target := filepath.Join(s.SkillsDir, directoryName)
	installed, err := s.stageAndSwap(files, fullSlug, directoryName, version, target)
	if err != nil {
		return Installed{}, err
	}
	return installed, nil
}

// stageAndSwap writes the whole bundle into a staging directory next to the
// target, then swaps it in with a backup of any existing install:
//
//	stage everything → move old install aside → rename staging into place →
//	drop the backup.
//
// If the final rename fails the backup is rolled back; if the rollback fails
// too, the backup directory is deliberately left behind for manual recovery
// — data loss is worse than a leftover .replace-old-* directory.
func (s *Service) stageAndSwap(files map[string][]byte, fullSlug, directoryName, version, target string) (_ Installed, err error) {
	if mkerr := os.MkdirAll(s.SkillsDir, 0o755); mkerr != nil {
		return Installed{}, fmt.Errorf("创建技能目录失败: %w", mkerr)
	}
	staging := filepath.Join(s.SkillsDir, ".staging-"+directoryName+"-"+uuid.NewString())
	backup := filepath.Join(s.SkillsDir, ".replace-old-"+directoryName+"-"+uuid.NewString())
	backedUp := false
	defer func() {
		os.RemoveAll(staging)
		if backedUp {
			os.RemoveAll(backup)
		}
	}()

	for relPath, content := range files {
		// unzipBounded already proved every path is local; FromSlash keeps
		// Windows happy.
		dst := filepath.Join(staging, filepath.FromSlash(relPath))
		if mkerr := os.MkdirAll(filepath.Dir(dst), 0o755); mkerr != nil {
			return Installed{}, fmt.Errorf("写入技能文件失败: %w", mkerr)
		}
		if werr := os.WriteFile(dst, content, 0o644); werr != nil {
			return Installed{}, fmt.Errorf("写入技能文件失败: %w", werr)
		}
	}

	// .registry 与 kskill CLI 兼容，便于 `kskill update` 识别安装来源。
	if werr := os.WriteFile(filepath.Join(staging, ".registry"), []byte(s.Registry.Base()), 0o644); werr != nil {
		return Installed{}, fmt.Errorf("写入安装元数据失败: %w", werr)
	}
	m := marker{
		FullSlug:    fullSlug,
		Registry:    s.Registry.Base(),
		InstalledAt: time.Now().UTC().Format(time.RFC3339),
	}
	if version != "" {
		m.Version = &version
	}
	markerJSON, _ := json.MarshalIndent(m, "", "  ")
	if werr := os.WriteFile(filepath.Join(staging, MarkerFile), markerJSON, 0o644); werr != nil {
		return Installed{}, fmt.Errorf("写入安装元数据失败: %w", werr)
	}

	// Move any existing install aside first so we can restore it if the swap
	// fails. Renaming onto a non-empty directory is unreliable across
	// platforms, so the target is only vacated after the backup succeeds.
	if _, serr := os.Stat(target); serr == nil {
		if rerr := os.Rename(target, backup); rerr != nil {
			return Installed{}, fmt.Errorf("挪开旧版本失败: %w", rerr)
		}
		backedUp = true
	}
	if rerr := os.Rename(staging, target); rerr != nil {
		if backedUp {
			if rberr := os.Rename(backup, target); rberr != nil {
				// Double failure: the only surviving copy is the backup dir.
				// Keep it for manual recovery instead of letting the defer
				// delete it.
				backedUp = false
				return Installed{}, fmt.Errorf("安装失败且回滚失败，旧版本保留在 %s: %w", backup, rerr)
			}
			backedUp = false
		}
		return Installed{}, fmt.Errorf("安装失败: %w", rerr)
	}

	return Installed{
		Name:      directoryName,
		FullSlug:  fullSlug,
		Version:   version,
		Directory: target,
	}, nil
}

func slugTail(fullSlug string) string {
	if i := strings.LastIndex(fullSlug, "/"); i >= 0 {
		return fullSlug[i+1:]
	}
	return fullSlug
}

// declaredName pulls the frontmatter name if it parses; empty otherwise so
// the caller can fall back to the slug tail.
func declaredName(skillMD string) string {
	parsed, err := skills.ParseDocument(skillMD)
	if err != nil {
		return ""
	}
	return parsed.Name
}

var (
	frontmatterPattern   = regexp.MustCompile(`(?s)^---\r?\n(.*?)\r?\n---(?:\r?\n|$)`)
	metadataBlockPattern = regexp.MustCompile(`(?m)^metadata:[ \t]*\r?\n((?:[ \t]+.+\r?\n?)*)`)
	metaVersionPattern   = regexp.MustCompile(`(?m)^[ \t]+version:[ \t]*["']?([^\s"']+)["']?[ \t]*$`)
	topVersionPattern    = regexp.MustCompile(`(?m)^version:[ \t]*["']?([^\s"']+)["']?[ \t]*$`)
)

// versionFrom mirrors the kskill CLI's precedence: frontmatter
// metadata.version first, then a top-level version key.
func versionFrom(skillMD string) string {
	fm := frontmatterPattern.FindStringSubmatch(skillMD)
	if fm == nil {
		return ""
	}
	front := fm[1]
	if block := metadataBlockPattern.FindStringSubmatch(front); block != nil {
		if v := metaVersionPattern.FindStringSubmatch(block[1]); v != nil {
			return v[1]
		}
	}
	if v := topVersionPattern.FindStringSubmatch(front); v != nil {
		return v[1]
	}
	return ""
}
