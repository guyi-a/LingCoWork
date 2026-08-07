package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/guyi-a/Interview-Agent/internal/agent"
	"github.com/guyi-a/Interview-Agent/internal/agent/browserbridge"
	"github.com/guyi-a/Interview-Agent/internal/agent/browseruse"
	"github.com/guyi-a/Interview-Agent/internal/agent/llm"
	"github.com/guyi-a/Interview-Agent/internal/agent/skills"
	"github.com/guyi-a/Interview-Agent/internal/agent/tools"
	"github.com/guyi-a/Interview-Agent/internal/approval"
	"github.com/guyi-a/Interview-Agent/internal/compaction"
	"github.com/guyi-a/Interview-Agent/internal/config"
	"github.com/guyi-a/Interview-Agent/internal/handler"
	"github.com/guyi-a/Interview-Agent/internal/instructions"
	"github.com/guyi-a/Interview-Agent/internal/mcp"
	ragembedding "github.com/guyi-a/Interview-Agent/internal/rag/embedding"
	ragretriever "github.com/guyi-a/Interview-Agent/internal/rag/retriever"
	ragstore "github.com/guyi-a/Interview-Agent/internal/rag/store"
	"github.com/guyi-a/Interview-Agent/internal/repository"
	"github.com/guyi-a/Interview-Agent/internal/service"
	"github.com/guyi-a/Interview-Agent/internal/skillhub"
	"github.com/guyi-a/Interview-Agent/internal/stream"
	"github.com/guyi-a/Interview-Agent/internal/websearch"
)

const (
	dbPath          = "data/interview.db"
	workspaceRoot   = ".workspace"
	instructionRoot = ".lingcowork/instructions"
	addr            = ":9001"
	shutdownTimeout = 5 * time.Second
)

func corsMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		origin := c.GetHeader("Origin")
		if origin == "http://localhost:5173" || origin == "http://127.0.0.1:5173" {
			c.Header("Access-Control-Allow-Origin", origin)
			c.Header("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
			c.Header("Access-Control-Allow-Headers", "Content-Type")
			c.Header("Access-Control-Max-Age", "600")
		}
		if c.Request.Method == http.MethodOptions {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}
		c.Next()
	}
}

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config.Load: %v", err)
	}
	log.Printf("LLM cfg: model=%s base=%s thinking=%v effort=%s",
		cfg.LLM.Model, cfg.LLM.BaseURL, cfg.LLM.EnableThinking, cfg.LLM.ReasoningEffort)

	ctx := context.Background()

	// dbPath 现在指向 data/ 下的文件；确保目录存在，避免第一次跑挂
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		log.Fatalf("mkdir db dir: %v", err)
	}

	db, err := repository.NewDB(dbPath)
	if err != nil {
		log.Fatalf("repository.NewDB: %v", err)
	}
	convRepo := repository.NewConversationRepo(db)
	msgRepo := repository.NewMessageRepo(db)
	projectRepo := repository.NewProjectRepo(db)
	checkpointRepo := repository.NewCheckpointRepo(db)
	pendingApprovalRepo := repository.NewPendingApprovalRepo(db)
	compactionRepo := repository.NewCompactionRepo(db)
	compactor := compaction.New(compactionRepo, cfg.Compaction)
	// Zero means "no fold coming", which the UI reads as: show the raw token
	// count with no denominator instead of a progress readout toward a
	// threshold that will never fire.
	contextLimit := 0
	if compactor != nil {
		contextLimit = compaction.ThresholdTokens(cfg.Compaction)
	}
	if compactor == nil {
		log.Printf("context compaction disabled (missing DEEPSEEK_API_KEY or COMPACTION_ENABLED=false); long conversations send full history")
	} else {
		log.Printf("context compaction enabled: model=%s threshold=%d tokens keep_last_turns=%d",
			cfg.Compaction.Model, compaction.ThresholdTokens(cfg.Compaction), cfg.Compaction.KeepLastUserTurns)
	}
	approvalModes := approval.NewModeStore()
	classifier := approval.NewClassifier(cfg.ApprovalFast)
	if classifier == nil {
		log.Printf("approval classifier disabled (missing DEEPSEEK_API_KEY or fast-model config); auto mode falls back to fast-path rules only")
	} else {
		log.Printf("approval classifier enabled: model=%s timeout=%ds", cfg.ApprovalFast.Model, cfg.ApprovalFast.TimeoutSeconds)
	}

	absWorkspaceRoot, err := filepath.Abs(workspaceRoot)
	if err != nil {
		log.Fatalf("resolve workspace root: %v", err)
	}
	if err := os.MkdirAll(absWorkspaceRoot, 0o755); err != nil {
		log.Fatalf("mkdir workspace root: %v", err)
	}

	cm, err := llm.NewChatModel(ctx, cfg.LLM)
	if err != nil {
		log.Fatalf("llm.NewChatModel: %v", err)
	}

	browserMgr := browseruse.NewManager(browseruse.Config{
		Headless: false,
		Channel:  os.Getenv("PLAYWRIGHT_CHANNEL"),
	})
	defer browserMgr.Shutdown()

	bridgeSvc := browserbridge.NewService(browserbridge.NewRegistry())

	skillLoader, err := skills.NewLoader(filepath.Dir(dbPath))
	if err != nil {
		log.Fatalf("skills.NewLoader: %v", err)
	}
	log.Printf("skills: 释放到 %s，用户技能目录 %s", skillLoader.RootPath(), skillLoader.UserDir())

	// Skill Hub：连内网 kskill 注册中心的技能市场。安装目录就是 loader 的
	// 用户技能目录，装完 SkillsIndexMiddleware 下一轮 Refresh 就能看到。
	// 分类用主模型归类，结果缓存在 data/ 下；分类失败前端隐藏分类栏。
	skillHubRegistry := skillhub.NewRegistry("")
	skillHubSvc := skillhub.NewService(
		skillHubRegistry,
		skillLoader.UserDir(),
		skillLoader.ReservedBuiltinNames,
	)
	skillHubCats := skillhub.NewCategories(
		skillHubRegistry,
		filepath.Join(filepath.Dir(dbPath), "skillhub-categories.json"),
		skillhub.NewModelClassifier(cm),
	)
	log.Printf("skillhub: registry=%s install_dir=%s", skillHubRegistry.Base(), skillLoader.UserDir())

	ts, effects, err := tools.Builtin(ctx, tools.Deps{
		WorkspaceRoot:    absWorkspaceRoot,
		ProjectRepo:      projectRepo,
		ConversationRepo: convRepo,
		BrowserUseMgr:    browserMgr,
		BridgeService:    bridgeSvc,
		SkillLoader:      skillLoader,
		RAGRetriever:     buildRAGRetriever(cfg),
		SearchService:    buildSearchService(cfg),
	})
	if err != nil {
		log.Fatalf("tools.Builtin: %v", err)
	}

	// MCP servers are optional and third-party, so nothing here is fatal: a
	// misconfigured or unreachable server is recorded and skipped rather than
	// taking the application down with it. Connect returns immediately and
	// dials in the background — the supervisor reads its remote tools once
	// per run, so a server that finishes connecting after boot is simply
	// available from the next turn.
	// The redirect URI is derived from addr rather than hardcoded, because an
	// authorization server matches it byte for byte against what the client
	// registered: the two drifting apart would break every re-authorization
	// with an error pointing at the provider, not at us.
	mcpAuth := mcp.NewAuthorizer(
		mcp.NewDBCredentialStore(repository.NewMCPCredentialRepo(db)),
		"http://localhost"+addr+"/mcp/oauth/callback",
	)
	mcpMgr := mcp.New(loadMCPConfig(), tools.BuiltinToolNames(), mcpAuth)
	mcpMgr.SetEffectRegistry(effects)
	mcpMgr.Connect(ctx)
	defer mcpMgr.Close()

	ag, err := agent.NewInterviewADKAgent(ctx, cm, ts, mcpMgr.ToolProvider(), skillLoader, checkpointRepo, convRepo, projectRepo, approvalModes, classifier, effects)
	if err != nil {
		log.Fatalf("agent.NewInterviewADKAgent: %v", err)
	}

	manager := stream.NewManager()
	instructionStore := instructions.NewStore(instructionRoot)
	pendingApprovals := approval.NewPendingStore(pendingApprovalRepo)
	if rows, err := pendingApprovalRepo.ListAll(ctx); err != nil {
		log.Printf("restore pending approvals: %v", err)
	} else {
		pendingApprovals.Restore(rows)
		log.Printf("restored %d pending approval(s) from DB", len(rows))
	}
	chatService := service.NewChatService(ag.Runner, ag.RootName, manager, convRepo, msgRepo, projectRepo, instructionStore, pendingApprovals, approvalModes, cfg.LLM.Multimodal, compactor)
	convService := service.NewConversationService(convRepo, msgRepo, compactionRepo, manager, browserMgr)
	projectService := service.NewProjectService(projectRepo, convRepo, manager, browserMgr, absWorkspaceRoot)
	workspaceService := service.NewWorkspaceService(convRepo, projectRepo)

	chatHandler := handler.NewChatHandler(chatService)
	approvalHandler := handler.NewApprovalHandler(chatService)
	convHandler := handler.NewConversationHandler(convService, contextLimit)
	projectHandler := handler.NewProjectHandler(projectService)
	workspaceHandler := handler.NewWorkspaceHandler(workspaceService)
	mcpHandler := handler.NewMCPHandler(mcpMgr)
	skillHubHandler := handler.NewSkillHubHandler(skillHubSvc, skillHubCats)
	instructionHandler := handler.NewInstructionHandler(instructionStore)

	r := gin.Default()
	r.Use(corsMiddleware())
	chatHandler.Register(r)
	approvalHandler.Register(r)
	convHandler.Register(r)
	projectHandler.Register(r)
	workspaceHandler.Register(r)
	mcpHandler.Register(r)
	skillHubHandler.Register(r)
	instructionHandler.Register(r)
	browserbridge.Register(r, bridgeSvc)

	srv := &http.Server{Addr: addr, Handler: r}

	serverErr := make(chan error, 1)
	go func() {
		log.Printf("listening on %s db=%s", addr, dbPath)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErr <- err
		}
		close(serverErr)
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	select {
	case err := <-serverErr:
		if err != nil {
			log.Fatalf("server error: %v", err)
		}
	case sig := <-quit:
		log.Printf("received %s, shutting down...", sig)
	}

	manager.ShutdownAll()
	// Closing an MCP connection also reaps the stdio server's child process.
	// Left to the deferred close, a hung HTTP shutdown would keep those
	// processes alive for the whole grace period.
	mcpMgr.Close()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Printf("http shutdown: %v", err)
	}

	if sqlDB, err := db.DB(); err == nil {
		_ = sqlDB.Close()
	}

	log.Print("shutdown complete")
}

// buildRAGRetriever 构造 vec+bm25 hybrid retriever。任何一步失败都返回 nil，
// tools.Builtin 会因此跳过 rag_search 工具的注册 —— agent 感知不到 RAG 存在。
// 这样 RAG 相关配置缺失只影响面试题库检索能力，不阻塞主流程启动。
func buildRAGRetriever(cfg *config.Config) ragretriever.Retriever {
	if !cfg.Embedding.Enabled() {
		log.Printf("rag: EMBEDDING_API_KEY 未配置，rag_search 工具未启用")
		return nil
	}
	dbPath := cfg.Rag.DBPath
	if dbPath == "" {
		dbPath = "data/rag.db"
	}
	if _, err := os.Stat(dbPath); err != nil {
		log.Printf("rag: 找不到 %s，rag_search 工具未启用（先跑 `go run ./cmd/rag-index`）: %v", dbPath, err)
		return nil
	}
	ragDB, err := ragstore.Open(dbPath)
	if err != nil {
		log.Printf("rag: 打开 %s 失败，rag_search 工具未启用: %v", dbPath, err)
		return nil
	}
	emb := ragembedding.New(cfg.Embedding)
	log.Printf("rag: 启用 rag_search，db=%s model=%s", dbPath, cfg.Embedding.Model)
	return ragretriever.NewHybrid(
		ragretriever.NewBruteForce(ragDB, emb),
		ragretriever.NewBM25(ragDB),
	)
}

// buildSearchService 构造联网搜索的 Service。没配任何 Tavily/Bocha key 就
// 返 nil，tools.Builtin 因此跳过 web_search 工具注册 —— agent 感知不到"联网
// 搜索"能力。web_fetch 独立注册，不依赖 key。
// loadMCPConfig reads the MCP server list, never failing the boot.
//
// An unreadable or malformed config means no MCP servers, which is the same
// state as not having configured any. Refusing to start the application over
// a broken optional integration would be the wrong trade.
func loadMCPConfig() *mcp.Config {
	cfg, err := mcp.LoadConfig()
	if err != nil {
		log.Printf("mcp: %v; continuing with no MCP servers", err)
		return &mcp.Config{}
	}
	switch {
	case !cfg.Exists:
		log.Printf("mcp: no config at %s; no MCP servers", cfg.Path)
	default:
		log.Printf("mcp: %d server(s) configured in %s", len(cfg.Servers), cfg.Path)
	}
	return cfg
}

func buildSearchService(cfg *config.Config) *websearch.Service {
	if !cfg.Search.Enabled() {
		log.Printf("websearch: TAVILY_API_KEY / BOCHA_API_KEY 均未配置，web_search 工具未启用")
		return nil
	}
	var enabled []string
	if cfg.Search.TavilyAPIKey != "" {
		enabled = append(enabled, "tavily")
	}
	if cfg.Search.BochaAPIKey != "" {
		enabled = append(enabled, "bocha")
	}
	log.Printf("websearch: 启用 web_search，providers=%v", enabled)
	return websearch.NewService(websearch.Config{
		TavilyAPIKey: cfg.Search.TavilyAPIKey,
		BochaAPIKey:  cfg.Search.BochaAPIKey,
	})
}
