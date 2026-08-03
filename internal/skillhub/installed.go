package skillhub

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// ListInstalled scans the skills directory for installed skills. A missing
// directory is an empty list, not an error — nothing has been installed yet.
//
// Hub installs carry a .skill-hub.json marker with their fullSlug/version;
// skills that arrived some other way (hand-copied, kskill CLI) fall back to
// the directory name as fullSlug and the SKILL.md frontmatter (or the CLI's
// .version file) for the version.
func (s *Service) ListInstalled() []Installed {
	entries, err := os.ReadDir(s.SkillsDir)
	if err != nil {
		return []Installed{}
	}
	out := []Installed{}
	for _, e := range entries {
		if !e.IsDir() && e.Type()&fs.ModeSymlink == 0 {
			continue
		}
		dir := filepath.Join(s.SkillsDir, e.Name())
		skillMD, err := os.ReadFile(filepath.Join(dir, "SKILL.md"))
		if err != nil {
			continue // staging leftovers, stray files — not a skill
		}

		item := Installed{Name: e.Name(), FullSlug: e.Name(), Directory: dir}
		if raw, err := os.ReadFile(filepath.Join(dir, MarkerFile)); err == nil {
			var m marker
			if json.Unmarshal(raw, &m) == nil {
				if m.FullSlug != "" {
					item.FullSlug = m.FullSlug
				}
				if m.Version != nil {
					item.Version = *m.Version
				}
			}
		}
		if item.Version == "" {
			item.Version = versionFrom(string(skillMD))
		}
		if item.Version == "" {
			if raw, err := os.ReadFile(filepath.Join(dir, ".version")); err == nil {
				item.Version = strings.TrimSpace(string(raw))
			}
		}
		out = append(out, item)
	}
	return out
}

// Uninstall removes the installed skill whose fullSlug matches. The lookup
// goes through ListInstalled rather than joining the slug into a path — a
// scoped slug contains "/" and must never touch the filesystem directly.
func (s *Service) Uninstall(fullSlug string) error {
	if !ValidFullSlug(fullSlug) {
		return fmt.Errorf("非法的技能 slug: %q", fullSlug)
	}
	for _, item := range s.ListInstalled() {
		if item.FullSlug == fullSlug {
			if err := os.RemoveAll(item.Directory); err != nil {
				return fmt.Errorf("删除 %s 失败: %w", item.Directory, err)
			}
			return nil
		}
	}
	return fmt.Errorf("技能 %q 未安装", fullSlug)
}
