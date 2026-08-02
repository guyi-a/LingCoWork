package tools

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"

	"github.com/guyi-a/Interview-Agent/internal/rag/retriever"
)

type ragSearchInput struct {
	Query string `json:"query" jsonschema:"description=Search query. Chinese or English keywords\\, phrases\\, or questions. Examples: 'MySQL 索引失效'\\, 'goroutine 调度'\\, 'Redis 持久化机制'\\, 'kafka 消息丢失'. Longer / more specific queries yield more accurate top results."`
	TopK  int    `json:"top_k,omitempty" jsonschema:"description=How many top results to return. Default 5\\, max 20. Use 3-5 when just checking one concept\\, 8-15 when exploring or picking questions across a domain."`
}

type ragSearchHit struct {
	Path    string  `json:"path"`    // 相对路径（去掉绝对前缀），便于 LLM 引用
	Ord     int     `json:"ord"`     // chunk 在原 markdown 里的顺序
	Score   float64 `json:"score"`   // 融合后的相关性分数（越大越相关）
	Content string  `json:"content"` // chunk 全文，含 "章节：/问题：/答案：" 前缀
}

type ragSearchOutput struct {
	Query string         `json:"query"`
	Count int            `json:"count"`
	Hits  []ragSearchHit `json:"hits"`
	// Notice: 遇到 count==0 时给 LLM 一句解释性提示，避免模型误以为"题库空"。
	Notice string `json:"notice,omitempty"`
}

func newRAGSearchTool(r retriever.Retriever) (tool.BaseTool, error) {
	if r == nil {
		return nil, errors.New("nil retriever")
	}
	fn := func(ctx context.Context, in *ragSearchInput) (*ragSearchOutput, error) {
		if in == nil || strings.TrimSpace(in.Query) == "" {
			return nil, fmt.Errorf("query is required")
		}
		hits, err := r.Search(ctx, in.Query, in.TopK)
		if err != nil {
			return nil, err
		}
		out := &ragSearchOutput{
			Query: in.Query,
			Count: len(hits),
			Hits:  make([]ragSearchHit, len(hits)),
		}
		for i, h := range hits {
			out.Hits[i] = ragSearchHit{
				Path:    shortenRAGPath(h.Path),
				Ord:     h.Ord,
				Score:   h.Score,
				Content: h.Content,
			}
		}
		if len(hits) == 0 {
			out.Notice = "题库中没有找到与 query 匹配的 chunk。可能是 query 太窄（如某个具体项目名）、题库不覆盖这个主题，或该主题在题库里用了不同表述（尝试改写关键词/中英切换后再搜）。"
		}
		return out, nil
	}

	return utils.InferTool(
		"rag_search",
		"在本地面试题库（Redis / MySQL / Go / MQ / 分布式方向的 Q&A markdown）里做**向量 + BM25 混合检索**，返回 top-K 个最相关的题目 chunk。每个 chunk 内容形如：\n"+
			"    章节：3. 事务面试题\n"+
			"    问题：2.3 说一下 MySQL 的四种隔离级别？\n"+
			"    答案：...\n"+
			"**什么时候用**：\n"+
			"- 用户问某个具体技术概念/面试题时，先搜再答（例：'解释一下缓存穿透'、'Go 的 GMP 是什么'）\n"+
			"- 需要为候选人挑面试题（例：'根据这份简历给我出 5 道 Redis 中级题'）\n"+
			"- 想引用题库里的**权威答案**而不是自己现编时\n"+
			"**什么时候不用**：\n"+
			"- 通用编程/代码任务（写代码、debug、code review）—— 直接答\n"+
			"- 已加载的用户文件相关操作 —— 用 read_file / extract_document_text\n"+
			"- 简单打招呼、闲聊 —— 直接答\n"+
			"**query 写法建议**：中文短语最好 2 字以上、英文关键词 3 字符以上、中英混排（'goroutine 调度'）都可以；越具体命中越准。同一次对话里可以多次调用（不同 query）来覆盖多个方向。",
		fn,
	)
}

// shortenRAGPath：绝对路径 → 相对易读路径（去掉到 docs/rag_docs/ 的前缀），
// 展示给 LLM 的路径太长会污染上下文且没价值。
func shortenRAGPath(p string) string {
	const marker = "/docs/rag_docs/"
	if i := strings.Index(p, marker); i >= 0 {
		return "docs/rag_docs/" + p[i+len(marker):]
	}
	// fallback：取最后两段
	parts := strings.Split(p, "/")
	if len(parts) <= 2 {
		return p
	}
	return strings.Join(parts[len(parts)-2:], "/")
}
