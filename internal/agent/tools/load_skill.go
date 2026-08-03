package tools

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"

	"github.com/guyi-a/Interview-Agent/internal/agent/skills"
)

type loadSkillInput struct {
	Name string `json:"name" jsonschema:"description=Skill name to load (must match one of the entries listed in the system prompt's Skills index)."`
}

type loadSkillOutput struct {
	OK          bool   `json:"ok"`
	Name        string `json:"name,omitempty"`
	Body        string `json:"body,omitempty"`
	SkillPath   string `json:"skill_path,omitempty"`  // skill 目录在磁盘上的绝对路径
	ScriptsPath string `json:"scripts_path,omitempty"` // <skill_path>/scripts，如果存在
	Message     string `json:"message,omitempty"`
}

func newLoadSkillTool(loader *skills.Loader) (tool.BaseTool, error) {
	if loader == nil {
		return nil, errors.New("load_skill: loader is nil")
	}
	// 可用技能名单不烤进 description：技能集是动态的（Skill Hub 随时装卸），
	// 而工具 schema 在 agent 首轮就冻结了。名单交给系统提示词里的 Skills 目录
	// （每轮由 SkillsIndexMiddleware 重新注入），错误提示则现查 loader。
	desc := "Load a skill: returns its instruction body plus its on-disk directory path. " +
		"Call this the moment a user request matches a skill's trigger description — its body carries the exact " +
		"tool syntax, discipline, and failure recipes for that task. " +
		"The skill directory may contain additional files (REFERENCE.md, FORMS.md, etc.) — read them with read_file as needed. " +
		"If the skill has a scripts/ subdirectory, run its scripts via `run_command uv run <scripts_path>/xxx.py <args>`. " +
		"Do NOT modify files inside the skill directory — copy to workspace/scripts/ first if you need to customize. " +
		"The system prompt's Skills 目录 section lists what is currently available."
	return utils.InferTool("load_skill", desc, func(ctx context.Context, in *loadSkillInput) (*loadSkillOutput, error) {
		if in.Name == "" {
			return &loadSkillOutput{OK: false, Message: "name is required"}, nil
		}
		s, err := loader.Load(in.Name)
		if err != nil {
			return &loadSkillOutput{
				OK:      false,
				Message: fmt.Sprintf("skill %q not found; available: %s", in.Name, strings.Join(loader.Names(), ", ")),
			}, nil
		}
		out := &loadSkillOutput{
			OK:        true,
			Name:      s.Name,
			Body:      s.Body,
			SkillPath: s.Path,
		}
		scriptsDir := filepath.Join(s.Path, "scripts")
		if fi, err := os.Stat(scriptsDir); err == nil && fi.IsDir() {
			out.ScriptsPath = scriptsDir
		}
		return out, nil
	})
}
