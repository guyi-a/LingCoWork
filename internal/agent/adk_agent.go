package agent

import (
	"context"
	"fmt"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/prebuilt/deep"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"

	"github.com/guyi-a/Interview-Agent/internal/agent/checkpoint"
	"github.com/guyi-a/Interview-Agent/internal/agent/prompts"
	"github.com/guyi-a/Interview-Agent/internal/agent/runtimectx"
	"github.com/guyi-a/Interview-Agent/internal/agent/skills"
	"github.com/guyi-a/Interview-Agent/internal/agent/toolerr"
	"github.com/guyi-a/Interview-Agent/internal/approval"
	"github.com/guyi-a/Interview-Agent/internal/changes"
	"github.com/guyi-a/Interview-Agent/internal/effect"
	"github.com/guyi-a/Interview-Agent/internal/memory"
	"github.com/guyi-a/Interview-Agent/internal/repository"
	"github.com/guyi-a/Interview-Agent/internal/validation"
	"github.com/guyi-a/Interview-Agent/internal/workplan"
)

// 这两个 agent name 是稳定标识：
//   - SupervisorAgentName 用作 SSE 翻译的根 agent 白名单（adk_handler 只渲染根 agent 的事件）
//   - DeepResearchAgentName 同时是 sub-agent 的标识符，也是模型看到的工具名
const (
	SupervisorAgentName      = "supervisor"
	DeepResearchAgentName    = "deep_research"
	JobSearchAgentName       = "job_search"
	ResumeAnalyzerAgentName  = "resume_analyzer"
	QuestionPlannerAgentName = "question_planner"
	ExploreAgentName         = "explore"
)

// ADKBundle 把 root agent 和 runner 一起暴露给上层。
// runner 是给 ChatService 用的入口；rootAgent 只暴露 Name() 给 SSE handler
// 用来做"只渲染根 agent 事件"的判断。
type ADKBundle struct {
	Runner   *adk.Runner
	RootName string
}

// NewInterviewADKAgent assembles the root Agent plus five specialized
// sub-agents:
//
//	Runner
//	└── Supervisor (ChatModelAgent, root)
//	    ├── baseTools...
//	    ├── explore (workspace-read + guarded probes)
//	    └── business sub-agents (research/job/resume/questions)
//
// EmitInternalEvents=true 让 sub-agent 内部事件冒泡到 Runner 的 iter，
// adk_handler 会把它们翻译成带 agent 字段的 SSE 帧，UI 展示子 Agent
// 在干嘛，持久化时塞进 message.Extra.sub_events 数组（带
// parent_tool_call_id 链接回 root 的 deep_research 工具卡片）。
// dynamicTools supplies tools that only the root agent gets and that may not
// exist yet when this runs. MCP is both:
//
// Only the root agent, because a remote server's tool set is unbounded and
// unrelated to what a sub-agent was built to do — handing job_search a
// filesystem server would grow every sub-agent's prompt with tools none of
// them were designed around. The approval gate would still hold, so this is
// about exposure and context cost rather than a hole.
//
// Not yet existing, because an MCP server can come up long after boot: OAuth
// authorization is interactive, and the user clicks "authorize" while the app
// is already running. A slice captured here would be frozen on the agent's
// first run; a function is read every run. See DynamicToolsMiddleware.
func NewInterviewADKAgent(
	ctx context.Context,
	cm model.ToolCallingChatModel,
	baseTools []tool.BaseTool,
	dynamicTools func(context.Context) []tool.BaseTool,
	skillLoader *skills.Loader,
	checkpointRepo *repository.CheckpointRepo,
	convRepo *repository.ConversationRepo,
	projectRepo *repository.ProjectRepo,
	approvalModes *approval.ModeStore,
	classifier *approval.Classifier,
	effects *effect.Registry,
	memoryRegistry *memory.Registry,
	userMemoryPath string,
	changeTracker *changes.Tracker,
	workPlans *workplan.Service,
	validations *validation.Service,
) (*ADKBundle, error) {
	if cm == nil {
		return nil, fmt.Errorf("ToolCallingChatModel is nil")
	}
	if effects == nil {
		return nil, fmt.Errorf("effect registry is nil: without it every tool call derives to unknown and prompts the user")
	}
	supervisorInstruction := prompts.Supervisor
	deepResearchInstruction := prompts.DeepResearch

	// A sub-agent invoked as a tool has no side effect of its own — whatever
	// it goes on to do passes through its own copy of the approval middleware
	// below. Without these registrations each delegation would derive to
	// unknown and open with an approval card, which is a prompt for nothing.
	registerDelegateEffects(effects)
	subAgentTools, err := withoutRootStateTools(ctx, baseTools)
	if err != nil {
		return nil, fmt.Errorf("select sub-agent tools: %w", err)
	}

	// One middleware value for every agent in the topology. Constructing it
	// per agent would work today and drift tomorrow: a change made at one of
	// five call sites would leave the other four judging calls by the old
	// rules, and the sub-agents are exactly where that would go unnoticed.
	approvalMW := approval.Middleware(approvalModes, classifier, effects)
	toolMiddlewares := []compose.ToolMiddleware{
		toolerr.Middleware(),
		planGuard(convRepo),
		approvalMW,
	}
	if validations != nil {
		toolMiddlewares = append(toolMiddlewares, validations.Middleware())
	}
	if changeTracker != nil {
		toolMiddlewares = append(toolMiddlewares, changeTracker.Middleware())
	}

	// runtime middleware：每次 agent 运行开始时把当前 workspace 状态拼进 instruction。
	// 所有 sub-agent 共用同一个实例（无状态），保证主 agent 和 sub-agent 看到的
	// workspace 视图一致。
	workspaceMW := runtimectx.NewWorkspaceMiddleware(convRepo, projectRepo)

	// Skills 索引改成每轮注入（而不是构建时烤进 instruction）：Skill Hub 装完
	// 的技能下一轮就能出现在索引里。所有 agent 共用一个实例。
	skillsMW := runtimectx.NewSkillsIndexMiddleware(skillLoader)

	// 两级长期记忆同样每轮注入。挂在 skills 之后、workspace 之前：提示词缓存
	// 按前缀匹配，把变动频率低的排在前面，改动时被作废的前缀就更短。
	memoryMW := runtimectx.NewMemoryMiddleware(memoryRegistry, userMemoryPath, convRepo, projectRepo)

	// Only the supervisor gets this one — see the dynamicTools doc above.
	dynamicToolsMW := runtimectx.NewDynamicToolsMiddleware(dynamicTools)
	modeMW := runtimectx.NewModeMiddleware(convRepo)
	workPlanMW := runtimectx.NewWorkPlanMiddleware(workPlans)

	// 1) 后台研究员
	//    - 不带 Backend：继续用我们自己的 workspace/fs 工具（baseTools），不引入 ADK 原生 filesystem
	//    - WithoutWriteTodos: 默认 todos 中间件会强行注入一堆 tool/prompt，先关掉
	//    - WithoutGeneralSubAgent: 不让 deep agent 再 spawn 子 agent
	deepAgent, err := deep.New(ctx, &deep.Config{
		Name:                   DeepResearchAgentName,
		Description:            "后台研究员：处理需要多步分析、规划、生成结构化报告的复杂任务（项目分析、生成题库、写学习计划等）。不要用于普通一问一答。",
		ChatModel:              cm,
		Instruction:            deepResearchInstruction,
		MaxIteration:           50,
		WithoutWriteTodos:      true,
		WithoutGeneralSubAgent: true,
		ToolsConfig: adk.ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{
				Tools: subAgentTools,
				// toolerr is outermost: guard/tool failures become model
				// observations, while its interrupt-aware branch passes
				// approval/plan control flow through untouched.
				ToolCallMiddlewares: toolMiddlewares,
			},
		},
		Handlers: []adk.ChatModelAgentMiddleware{skillsMW, memoryMW},
	})
	if err != nil {
		return nil, fmt.Errorf("deep.New: %w", err)
	}

	// 2) 把 deep agent 包成 supervisor 的一个工具
	deepTool := adk.NewAgentTool(ctx, deepAgent)

	// 3) 招聘搜索员：跟 deep_research 平级的另一个 sub-agent。
	//    工具集共用 baseTools（主要用 browser_bridge + load_skill）。
	jobAgent, err := adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
		Name:        JobSearchAgentName,
		Description: "招聘信息搜索员：所有招聘/岗位/求职/找工作/Boss直聘类任务的**唯一入口**，supervisor 遇到这类请求必须走这里，不要自己调 browser_bridge 或 browser_use 硬走。给 request 传用户意图（岗位方向、城市、级别、想要几个），我会加载 bosszp skill、检查登录、抓取、返回结构化职位列表。",
		Instruction: prompts.JobSearch,
		Model:       cm,
		ToolsConfig: adk.ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{
				Tools:               subAgentTools,
				ToolCallMiddlewares: toolMiddlewares,
			},
		},
		Handlers:      []adk.ChatModelAgentMiddleware{skillsMW, memoryMW},
		MaxIterations: 50,
	})
	if err != nil {
		return nil, fmt.Errorf("adk.NewChatModelAgent(job_search): %w", err)
	}
	jobTool := adk.NewAgentTool(ctx, jobAgent)

	// 4) 简历自评员：帮"求职者本人"分析自己的简历 vs 目标 JD，识别匹配度、
	//    差距、面试要点。产出 self_review.md 供用户自查。
	resumeAgent, err := adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
		Name:        ResumeAnalyzerAgentName,
		Description: "求职者简历自评员。当用户（求职者）说'帮我看看这份简历怎么样'、'面 XX 岗合适吗'、'分析下我的简历跟 JD 的匹配度'时委派。传 request 说明简历路径 + JD（文本或路径）+ 目标岗位。会产出 reports/self_review.md（自评报告，用'你'称呼用户），返回路径。不要用于纯读文件、跟简历无关的技术问答。",
		Instruction: prompts.ResumeAnalyzer,
		Model:       cm,
		ToolsConfig: adk.ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{
				Tools:               subAgentTools,
				ToolCallMiddlewares: toolMiddlewares,
			},
		},
		Handlers:      []adk.ChatModelAgentMiddleware{skillsMW, memoryMW, workspaceMW},
		MaxIterations: 30,
	})
	if err != nil {
		return nil, fmt.Errorf("adk.NewChatModelAgent(resume_analyzer): %w", err)
	}
	resumeTool := adk.NewAgentTool(ctx, resumeAgent)

	// 5) 面试模拟题生成员：为"求职者本人"生成 TA 可能面试遇到的题目 + 参考答案。
	//    输出多个小文件（basic/experience/design/README），一次 write_file 一个，
	//    避开上游流式协议在大 tool_call args 时的序列化 bug。
	plannerAgent, err := adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
		Name:        QuestionPlannerAgentName,
		Description: "求职者面试模拟题生成员。当用户（求职者）说'根据我简历给我出些题练练'、'准备一套模拟面试题'、'给我一份复习题'时委派。传 request 说明简历自评报告路径 + JD + 可选偏好（题量/难度）。会产出 reports/questions/ 目录下多个 md（basic/experience/design/README）并返回索引路径。前置：必须先跑 resume_analyzer 生成自评报告。",
		Instruction: prompts.QuestionPlanner,
		Model:       cm,
		ToolsConfig: adk.ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{
				Tools:               subAgentTools,
				ToolCallMiddlewares: toolMiddlewares,
			},
		},
		Handlers:      []adk.ChatModelAgentMiddleware{skillsMW, memoryMW, workspaceMW},
		MaxIterations: 50,
	})
	if err != nil {
		return nil, fmt.Errorf("adk.NewChatModelAgent(question_planner): %w", err)
	}
	plannerTool := adk.NewAgentTool(ctx, plannerAgent)

	// 6) Read-mostly code explorer. Unlike the business sub-agents it gets a
	// deliberately tiny tool surface and a second effect guard. The prompt is
	// guidance; these two code-level boundaries are the actual permission
	// model.
	exploreTools, err := selectExploreTools(ctx, baseTools)
	if err != nil {
		return nil, err
	}
	exploreAgent, err := adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
		Name:        ExploreAgentName,
		Description: "只读代码库探索员：当实现位置未知、需要跨模块搜索或梳理调用链时使用。只读取当前 Workspace，并可运行受限 Git/文件探测命令；不修改文件、不测试、不构建。",
		Instruction: prompts.Explore,
		Model:       cm,
		ToolsConfig: adk.ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{
				Tools: exploreTools,
				ToolCallMiddlewares: []compose.ToolMiddleware{
					toolerr.Middleware(),
					exploreGuard(effects),
				},
			},
		},
		Handlers:      []adk.ChatModelAgentMiddleware{workspaceMW},
		MaxIterations: exploreMaxIterations,
	})
	if err != nil {
		return nil, fmt.Errorf("adk.NewChatModelAgent(explore): %w", err)
	}
	exploreTool := adk.NewAgentTool(ctx, exploreAgent)

	// 7) Supervisor 工具列表 = baseTools + 5 个 sub-agent tool。
	// MCP 工具不在这里，每轮由 dynamicToolsMW 注入。
	supervisorTools := make([]tool.BaseTool, 0, len(baseTools)+5)
	supervisorTools = append(supervisorTools, baseTools...)
	supervisorTools = append(
		supervisorTools,
		deepTool, jobTool, resumeTool, plannerTool, exploreTool,
	)

	supervisor, err := adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
		Name:        SupervisorAgentName,
		Description: "通用生产力与 Coding 主 Agent；直接执行任务，并在大范围代码探索或明确业务流程中委派对应子 Agent。",
		Instruction: supervisorInstruction,
		Model:       cm,
		ToolsConfig: adk.ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{
				Tools:               supervisorTools,
				ToolCallMiddlewares: toolMiddlewares,
			},
			// Bubble up sub-agent (deep_research) internal events to the
			// Runner's iter so the UI can show real-time progress.
			EmitInternalEvents: true,
		},
		Handlers:      []adk.ChatModelAgentMiddleware{skillsMW, memoryMW, workspaceMW, dynamicToolsMW, modeMW, workPlanMW},
		MaxIterations: 50,
	})
	if err != nil {
		return nil, fmt.Errorf("adk.NewChatModelAgent: %w", err)
	}

	runner := adk.NewRunner(ctx, adk.RunnerConfig{
		Agent:           supervisor,
		EnableStreaming: true,
		CheckPointStore: checkpoint.NewDBStore(checkpointRepo),
	})

	return &ADKBundle{
		Runner:   runner,
		RootName: SupervisorAgentName,
	}, nil
}

func registerDelegateEffects(effects *effect.Registry) {
	for _, name := range []string{
		DeepResearchAgentName, JobSearchAgentName,
		ResumeAnalyzerAgentName, QuestionPlannerAgentName, ExploreAgentName,
	} {
		effects.Register(name, effect.Static(effect.Effect{
			Kind:  effect.KindDelegate,
			Agent: name,
		}))
	}
}
