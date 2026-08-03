package runtimectx

import (
	"context"
	"log"

	"github.com/cloudwego/eino/adk"

	"github.com/guyi-a/Interview-Agent/internal/agent/prompts"
	"github.com/guyi-a/Interview-Agent/internal/agent/skills"
)

// SkillsIndexMiddleware 每次 agent 运行开始时重扫技能目录并把当前索引拼进
// instruction。之前索引是构建 agent 时烤死在 instruction 里的快照 —— Skill Hub
// 装完的技能要重启才可见；改成运行时注入后，装完下一轮对话就能看到。
//
// 与 WorkspaceMiddleware 同一个模式：只重写 BeforeAgent，五个 agent 共用一个
// 实例。Refresh 是一次小目录扫描（十来个目录的 SKILL.md frontmatter），每轮
// 一次的代价可以忽略。
type SkillsIndexMiddleware struct {
	*adk.BaseChatModelAgentMiddleware
	loader *skills.Loader
}

func NewSkillsIndexMiddleware(loader *skills.Loader) *SkillsIndexMiddleware {
	return &SkillsIndexMiddleware{
		BaseChatModelAgentMiddleware: &adk.BaseChatModelAgentMiddleware{},
		loader:                       loader,
	}
}

func (m *SkillsIndexMiddleware) BeforeAgent(
	ctx context.Context,
	runCtx *adk.ChatModelAgentContext,
) (context.Context, *adk.ChatModelAgentContext, error) {
	if m.loader == nil {
		return ctx, runCtx, nil
	}
	// 刷新失败继续用上一次的索引：旧索引照样能干活，掐死整轮运行才是事故。
	if err := m.loader.Refresh(); err != nil {
		log.Printf("skills: refresh failed, serving stale index: %v", err)
	}
	runCtx.Instruction = prompts.WithSkillsIndex(runCtx.Instruction, m.loader)
	return ctx, runCtx, nil
}
