package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/joho/godotenv"
)

type LLMConfig struct {
	APIKey    string
	BaseURL   string
	Model     string
	MaxTokens int
	// EnableThinking toggles DeepSeek thinking mode via
	// extra_body {"thinking":{"type":"enabled|disabled"}}.
	EnableThinking bool
	// ReasoningEffort is DeepSeek's reasoning_effort ("high" / "max";
	// "low"/"medium" are accepted for adapter compatibility and map to high
	// on DeepSeek's side). Empty → "high" when thinking is on.
	ReasoningEffort string
	// Multimodal reports whether the main model accepts native image
	// content blocks. Vision model names enable it by default; an explicit
	// LLM_MULTIMODAL value wins. When off, multimodal.BuildUserMessage
	// rewrites [image:] markers into [file:] so they flow through OCR.
	Multimodal bool
}

// ApprovalFastConfig points at an OpenAI-compatible endpoint used by the
// auto-mode approval classifier. Shares DEEPSEEK_API_KEY with the main LLM
// by default but keeps its own base URL / model so the classifier can stay
// on a cheaper non-thinking model. Missing APIKey disables the classifier
// entirely — auto mode then only has the fast-path rules to work with.
type ApprovalFastConfig struct {
	APIKey    string
	BaseURL   string
	Model     string
	MaxTokens int
	// TimeoutSeconds bounds a single classifier call. Anything over this
	// deadline is treated as failure → deny → human review (safe default).
	TimeoutSeconds int
}

func (c ApprovalFastConfig) Enabled() bool {
	return c.APIKey != "" && c.BaseURL != "" && c.Model != ""
}

// CompactionConfig drives cross-turn context compaction: when a
// conversation's estimated context crosses ThresholdTokens, the history is
// folded into a summary before the next run starts.
//
// Shares DEEPSEEK_API_KEY like ApprovalFastConfig, but keeps its own base
// URL / model so summarization can be pointed at a cheaper endpoint. Missing
// APIKey disables compaction entirely — conversations then behave exactly as
// they did before the feature existed.
type CompactionConfig struct {
	APIKey    string
	BaseURL   string
	Model     string
	MaxTokens int
	// TimeoutSeconds bounds one summarization call. Generous compared to the
	// approval classifier: the request carries an entire conversation.
	TimeoutSeconds int

	// WindowNominalTokens is the model's advertised context window and
	// WindowUsableRatio the fraction of it treated as reliably usable.
	WindowNominalTokens int
	WindowUsableRatio   float64
	// ReservedOutputTokens is held back for the model's own reply.
	ReservedOutputTokens int
	// BufferTokens is the safety margin so compaction fires before the
	// window is actually exhausted. It also absorbs what the estimator
	// deliberately doesn't count: system prompt and tool schemas.
	BufferTokens int

	// KeepLastUserTurns is how many recent user turns stay verbatim. 0 folds
	// everything already persisted; the turn being started is never folded
	// either way, since history is read before its user row is written.
	KeepLastUserTurns int
	// CharsPerToken is the fallback ratio for rows with no usage data.
	CharsPerToken int

	// ToolResultTruncateThresholdChars is the size above which a tool result
	// gets head/tail trimmed before the summarizer sees it;
	// ToolResultTruncateKeepChars is how much of each end survives.
	ToolResultTruncateThresholdChars int
	ToolResultTruncateKeepChars      int
}

func (c CompactionConfig) Enabled() bool {
	return c.APIKey != "" && c.BaseURL != "" && c.Model != ""
}

// EmbeddingConfig targets an OpenAI-compatible /embeddings endpoint used by
// the RAG layer to encode chunks and queries. Default deployment is Aliyun
// DashScope in "compatible-mode" (same wire shape as OpenAI's endpoint),
// but any OpenAI-compatible embedding service works.
//
// BatchSize caps how many inputs go in one request — DashScope's compatible
// mode currently limits text-embedding-v3 to 10 per call, so the client
// auto-chunks larger inputs. Dimensions is sent when the model supports
// truncated output (v3/v4); providers that ignore it just return native dim.
type EmbeddingConfig struct {
	APIKey         string
	BaseURL        string
	Model          string
	Dimensions     int
	BatchSize      int
	TimeoutSeconds int
}

func (c EmbeddingConfig) Enabled() bool {
	return c.APIKey != "" && c.BaseURL != "" && c.Model != ""
}

// RagConfig 只管 RAG 层路径/切分参数。embedding 相关继续走 EmbeddingConfig。
type RagConfig struct {
	DocsDir      string // markdown 源目录
	DBPath       string // rag.db 文件路径
	ChunkSize    int
	ChunkOverlap int
}

// SearchConfig 装联网搜索的 provider API keys。
// Tavily / Bocha 任何一个配了就能用；两个都没配就不注册 web_search 工具（agent 感知不到）。
// 环境变量名对齐 pentaloom：TAVILY_API_KEY / BOCHA_API_KEY，用户可以直接复用之前的配置。
type SearchConfig struct {
	TavilyAPIKey string
	BochaAPIKey  string
}

func (c SearchConfig) Enabled() bool {
	return c.TavilyAPIKey != "" || c.BochaAPIKey != ""
}

type Config struct {
	LLM          LLMConfig
	ApprovalFast ApprovalFastConfig
	Compaction   CompactionConfig
	Embedding    EmbeddingConfig
	Rag          RagConfig
	Search       SearchConfig
}

func Load() (*Config, error) {
	loadDotenv()

	deepseekKey := os.Getenv("DEEPSEEK_API_KEY")
	// Optional override so main agent and classifier can use different keys
	// later without renaming the shared default.
	llmKey := getEnv("LLM_API_KEY", deepseekKey)
	// COMPACTION_ENABLED=false is the off switch; otherwise compaction runs
	// whenever there's a key to summarize with.
	compactionKey := deepseekKey
	if !getEnvBool("COMPACTION_ENABLED", true) {
		compactionKey = ""
	}
	llmModel := getEnv("LLM_MODEL", "deepseek-v4-flash-vision-exp")

	cfg := &Config{
		LLM: LLMConfig{
			APIKey:          llmKey,
			BaseURL:         getEnv("LLM_BASE_URL", "https://api.deepseek.com"),
			Model:           llmModel,
			MaxTokens:       getEnvInt("LLM_MAX_TOKENS", 32000),
			EnableThinking:  getEnvBool("LLM_ENABLE_THINKING", true),
			ReasoningEffort: getEnv("LLM_REASONING_EFFORT", "high"),
			Multimodal:      getEnvBool("LLM_MULTIMODAL", isVisionModel(llmModel)),
		},
		ApprovalFast: ApprovalFastConfig{
			APIKey:         deepseekKey,
			BaseURL:        getEnv("APPROVAL_FAST_BASE_URL", "https://api.deepseek.com"),
			Model:          getEnv("APPROVAL_FAST_MODEL", "deepseek-v4-flash"),
			MaxTokens:      getEnvInt("APPROVAL_FAST_MAX_TOKENS", 512),
			TimeoutSeconds: getEnvInt("APPROVAL_FAST_TIMEOUT", 15),
		},
		Compaction: CompactionConfig{
			APIKey:         compactionKey,
			BaseURL:        getEnv("COMPACTION_BASE_URL", "https://api.deepseek.com"),
			Model:          getEnv("COMPACTION_MODEL", "deepseek-v4-flash"),
			MaxTokens:      getEnvInt("COMPACTION_MAX_TOKENS", 4096),
			TimeoutSeconds: getEnvInt("COMPACTION_TIMEOUT", 120),

			WindowNominalTokens:  getEnvInt("COMPACTION_WINDOW_TOKENS", 1000000),
			WindowUsableRatio:    getEnvFloat("COMPACTION_USABLE_RATIO", 0.90),
			ReservedOutputTokens: getEnvInt("COMPACTION_RESERVED_OUTPUT", 32000),
			BufferTokens:         getEnvInt("COMPACTION_BUFFER_TOKENS", 20000),

			KeepLastUserTurns: getEnvInt("COMPACTION_KEEP_LAST_TURNS", 0),
			CharsPerToken:     getEnvInt("COMPACTION_CHARS_PER_TOKEN", 4),

			ToolResultTruncateThresholdChars: getEnvInt("COMPACTION_TOOL_TRUNCATE_THRESHOLD", 48000),
			ToolResultTruncateKeepChars:      getEnvInt("COMPACTION_TOOL_TRUNCATE_KEEP", 8000),
		},
		Embedding: EmbeddingConfig{
			APIKey:         os.Getenv("EMBEDDING_API_KEY"),
			BaseURL:        getEnv("EMBEDDING_BASE_URL", "https://dashscope.aliyuncs.com/compatible-mode/v1"),
			Model:          getEnv("EMBEDDING_MODEL", "text-embedding-v3"),
			Dimensions:     getEnvInt("EMBEDDING_DIMENSIONS", 1024),
			BatchSize:      getEnvInt("EMBEDDING_BATCH_SIZE", 10),
			TimeoutSeconds: getEnvInt("EMBEDDING_TIMEOUT", 30),
		},
		Rag: RagConfig{
			DocsDir:      getEnv("RAG_DOCS_DIR", "docs/rag_docs"),
			DBPath:       getEnv("RAG_DB_PATH", "data/rag.db"),
			ChunkSize:    getEnvInt("RAG_CHUNK_SIZE", 500),
			ChunkOverlap: getEnvInt("RAG_CHUNK_OVERLAP", 80),
		},
		Search: SearchConfig{
			TavilyAPIKey: os.Getenv("TAVILY_API_KEY"),
			BochaAPIKey:  os.Getenv("BOCHA_API_KEY"),
		},
	}

	if cfg.LLM.APIKey == "" {
		return nil, fmt.Errorf("DEEPSEEK_API_KEY (or LLM_API_KEY) is required")
	}
	if cfg.LLM.BaseURL == "" {
		return nil, fmt.Errorf("LLM_BASE_URL is required")
	}
	if cfg.LLM.Model == "" {
		return nil, fmt.Errorf("LLM_MODEL is required")
	}
	return cfg, nil
}

func isVisionModel(model string) bool {
	return strings.Contains(strings.ToLower(strings.TrimSpace(model)), "vision")
}

func loadDotenv() {
	// Walk up from cwd until we find a .env or hit filesystem root.
	// Tests can live 3+ dirs deep (internal/rag/embedding/...) so a
	// fixed 2-level lookup wasn't enough. Cap the walk to avoid climbing
	// past the repo when run from an unexpected cwd.
	dir, err := os.Getwd()
	if err != nil {
		return
	}
	for range 8 {
		candidate := filepath.Join(dir, ".env")
		if _, err := os.Stat(candidate); err == nil {
			_ = godotenv.Overload(candidate)
			return
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return
		}
		dir = parent
	}
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func getEnvInt(key string, fallback int) int {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return fallback
	}
	return n
}

func getEnvFloat(key string, fallback float64) float64 {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return fallback
	}
	return f
}

func getEnvBool(key string, fallback bool) bool {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return fallback
	}
	return b
}
