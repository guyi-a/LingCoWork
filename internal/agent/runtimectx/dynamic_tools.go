package runtimectx

import (
	"context"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/tool"
)

// DynamicToolsMiddleware 每次 agent 运行开始时把当前可用的外部工具追加进工具表。
//
// 为什么必须走中间件：eino 在 agent 第一次运行时用 sync.Once 冻结工具表和
// 交给模型的 toolInfos（adk/chatmodel.go 的 prepareExecContext），构造时传进去的
// 工具此后就定型了。但 MCP 服务器可能在启动之后才连上——OAuth 授权本身就是
// 交互式的，用户点"授权"那一刻应用早已跑起来了。
//
// 而只要 agent 挂了任意一个 handler，eino 每轮都会从冻结的基准工具表复制一份
// runCtx.Tools 交给 BeforeAgent 改，改完重新跑 genToolInfos 并用 WithToolList
// 覆盖 ToolsNode 的派发表。所以从这里追加的工具，模型这一轮就能看见、也能真的
// 调到，不需要重建 agent、不需要换 Runner、更不会让已有的 checkpoint 对不上。
//
// 注意基准表每轮都是重新复制的，不是上一轮的结果，所以这里 append 不会累积。
// 反过来说：同一批工具**不能**既走构造参数又走这里，否则每轮都会重复一遍。
type DynamicToolsMiddleware struct {
	*adk.BaseChatModelAgentMiddleware
	provide func(context.Context) []tool.BaseTool
}

func NewDynamicToolsMiddleware(provide func(context.Context) []tool.BaseTool) *DynamicToolsMiddleware {
	return &DynamicToolsMiddleware{
		BaseChatModelAgentMiddleware: &adk.BaseChatModelAgentMiddleware{},
		provide:                      provide,
	}
}

func (m *DynamicToolsMiddleware) BeforeAgent(
	ctx context.Context,
	runCtx *adk.ChatModelAgentContext,
) (context.Context, *adk.ChatModelAgentContext, error) {
	if m.provide == nil {
		return ctx, runCtx, nil
	}
	extra := m.provide(ctx)
	if len(extra) == 0 {
		return ctx, runCtx, nil
	}
	runCtx.Tools = append(runCtx.Tools, extra...)
	return ctx, runCtx, nil
}
