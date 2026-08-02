package tools

import (
	"context"
	"time"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"

	"github.com/guyi-a/Interview-Agent/internal/agent/browserbridge"
	"github.com/guyi-a/Interview-Agent/internal/agent/browseruse"
	"github.com/guyi-a/Interview-Agent/internal/agent/skills"
	"github.com/guyi-a/Interview-Agent/internal/effect"
	"github.com/guyi-a/Interview-Agent/internal/rag/retriever"
	"github.com/guyi-a/Interview-Agent/internal/repository"
	"github.com/guyi-a/Interview-Agent/internal/websearch"
)

// Deps groups the dependencies the workspace + fs tools need at registration
// time. They are captured into each tool's closure.
type Deps struct {
	WorkspaceRoot    string
	ProjectRepo      *repository.ProjectRepo
	ConversationRepo *repository.ConversationRepo
	BrowserUseMgr    *browseruse.Manager
	BridgeService    *browserbridge.Service
	SkillLoader      *skills.Loader
	// RAGRetriever 可为 nil：nil 时 rag_search 工具不注册，agent 感知不到 RAG 存在。
	RAGRetriever retriever.Retriever
	// SearchService 可为 nil：nil（用户没配任何 Tavily/Bocha key）时 web_search
	// 工具不注册，agent 感知不到联网搜索能力。web_fetch 独立注册（不依赖 key）。
	SearchService *websearch.Service
}

// Builtin returns the full set of tools wired up with the given deps, plus
// the effect registry describing what each of them does.
//
// The two travel together because they have to agree: the approval policy
// reads effects only, so a tool built here without a deriver derives to
// KindUnknown and prompts the user on every single call.
func Builtin(ctx context.Context, d Deps) ([]tool.BaseTool, *effect.Registry, error) {
	out := []tool.BaseTool{}
	reg := effect.NewRegistry()

	timeTool, err := utils.InferTool(
		"get_current_time",
		"Get the current wall-clock time. USE THIS whenever the user asks about now/today/current time or the answer depends on the current moment — NEVER guess, and NEVER answer from memory; the model's own knowledge of the current time is unreliable.",
		currentTime,
	)
	if err != nil {
		return nil, nil, err
	}
	out = append(out, timeTool)

	askTool, err := newAskUserTool()
	if err != nil {
		return nil, nil, err
	}
	out = append(out, askTool)

	wsTool, err := NewCreateWorkspaceTool(d.WorkspaceRoot, d.ProjectRepo, d.ConversationRepo)
	if err != nil {
		return nil, nil, err
	}
	out = append(out, wsTool)

	fs := &fsDeps{projectRepo: d.ProjectRepo, convRepo: d.ConversationRepo}
	registerEffects(reg, fs)
	for _, ctor := range []func(*fsDeps) (tool.BaseTool, error){
		newFileInfoTool,
		newListFilesTool,
		newReadFileTool,
		newExtractDocumentTextTool,
		newWriteFileTool,
		newChunkedWriteFileTool,
		newEditFileTool,
		newEditFileLinesTool,
		newMkdirTool,
		newRmTool,
		newMvTool,
		newCpTool,
		newRunCommandTool,
	} {
		t, err := ctor(fs)
		if err != nil {
			return nil, nil, err
		}
		out = append(out, t)
	}

	if d.BrowserUseMgr != nil {
		installTool, err := newBrowserUseInstallTool()
		if err != nil {
			return nil, nil, err
		}
		bu, err := newBrowserUseTool(d.BrowserUseMgr, d.ConversationRepo, d.ProjectRepo)
		if err != nil {
			return nil, nil, err
		}
		out = append(out, installTool, bu)
	}

	if d.BridgeService != nil {
		bb, err := newBrowserBridgeTool(d.BridgeService)
		if err != nil {
			return nil, nil, err
		}
		out = append(out, bb)
	}

	if d.SkillLoader != nil {
		ls, err := newLoadSkillTool(d.SkillLoader)
		if err != nil {
			return nil, nil, err
		}
		out = append(out, ls)
	}

	if d.RAGRetriever != nil {
		rag, err := newRAGSearchTool(d.RAGRetriever)
		if err != nil {
			return nil, nil, err
		}
		out = append(out, rag)
	}

	// web_search 依赖 SearchService（跟 Tavily/Bocha key 绑定）—— 没配 key
	// 就不注册，agent 感知不到"联网搜索"能力。web_fetch 独立注册，任何环境都能用。
	if d.SearchService != nil {
		ws, err := newWebSearchTool(d.SearchService)
		if err != nil {
			return nil, nil, err
		}
		out = append(out, ws)
	}

	wf, err := newWebFetchTool()
	if err != nil {
		return nil, nil, err
	}
	out = append(out, wf)

	return out, reg, nil
}

// --- get_current_time ---

type currentTimeInput struct {
	Timezone string `json:"timezone" jsonschema:"description=IANA timezone name like 'Asia/Shanghai' or 'UTC'. Empty for system local time."`
}

type currentTimeOutput struct {
	Time     string `json:"time"`
	Timezone string `json:"timezone"`
}

func currentTime(_ context.Context, in *currentTimeInput) (*currentTimeOutput, error) {
	loc := time.Local
	if in.Timezone != "" {
		if l, err := time.LoadLocation(in.Timezone); err == nil {
			loc = l
		}
	}
	now := time.Now().In(loc)
	return &currentTimeOutput{
		Time:     now.Format("2006-01-02 15:04:05"),
		Timezone: loc.String(),
	}, nil
}
