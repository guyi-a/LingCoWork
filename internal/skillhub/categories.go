package skillhub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// Skill Hub 的任务分类：注册中心没有分类数据（实测约 95% 技能无 tag），
// 这里拉全量目录后用模型按「技能帮用户完成什么任务」归类。
//
//   - 归类结果按 fullSlug + 版本持久化在 JSON 缓存里，只对新增或版本变化的
//     技能增量调用模型。
//   - 全量目录短 TTL 内存缓存，分类筛选与分页在本地完成。
//   - 模型不可用/网络失败时整体报错，前端隐藏分类入口，浏览搜索不受影响。
//
// 分类表与提示词照搬 klingwork（同一个注册中心，没理由分出两套类目）。

// CategoryIDs 是任务导向的分类表；变更时把 taxonomyVersion +1 以作废旧缓存。
var CategoryIDs = []string{
	"docs-knowledge",
	"office-collab",
	"internal-systems",
	"dev-debug",
	"content-creation",
	"web-automation",
	"agent-meta",
	"other",
}

const (
	taxonomyVersion      = 1
	classifyBatchSize    = 40
	descriptionClipChars = 300
	catalogTTL           = 5 * time.Minute
	catalogPageSize      = 100
	catalogMaxPages      = 50
)

const classifySystemPrompt = `你是内部 Agent 技能目录的分类器。给定技能列表（name/description），按「这个技能帮助用户完成什么任务」归类，不要按技能的技术形态归类。

可选分类（id: 含义）：
- docs-knowledge: 文档、知识库、wiki、笔记的读写、检索、整理
- office-collab: 办公协同——邮件、日历、会议、IM 消息、审批、考勤、行政事务
- internal-systems: 打通或调用公司内部系统与服务——登录认证、内部 API、数据平台、效果服务
- dev-debug: 软件开发、调试、代码、CLI 工具、CI/CD、IDE 控制
- content-creation: 生成网页、PPT、图片、设计稿、文案、报告等交付内容
- web-automation: 网络搜索、浏览器自动化、抓取、批量文件处理
- agent-meta: 提升 Agent 自身能力的元技能——复盘、思维框架、执行质量保障
- other: 无法归入以上任何一类

规则：
1. 每个技能给 1 到 2 个分类 id，第一个必须是最贴近用户任务目标的。
2. 只输出严格 JSON 数组，元素形如 {"slug":"...","categories":["..."]}，不要输出任何其他文本或代码块围栏。`

// CategoryCount 是一个分类的技能数（含零值分类，前端自行隐藏）。
type CategoryCount struct {
	ID    string `json:"id"`
	Count int    `json:"count"`
}

// Classifier assigns 1–2 category ids to each skill, keyed by fullSlug.
type Classifier func(ctx context.Context, items []Skill) (map[string][]string, error)

// NewModelClassifier adapts the app's chat model into a Classifier.
func NewModelClassifier(cm model.BaseChatModel) Classifier {
	return func(ctx context.Context, items []Skill) (map[string][]string, error) {
		type entry struct {
			Slug        string `json:"slug"`
			Name        string `json:"name"`
			Description string `json:"description"`
		}
		payload := make([]entry, len(items))
		for i, s := range items {
			desc := s.Description
			if len([]rune(desc)) > descriptionClipChars {
				desc = string([]rune(desc)[:descriptionClipChars])
			}
			payload[i] = entry{Slug: s.FullSlug, Name: s.Name, Description: desc}
		}
		blob, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		msg, err := cm.Generate(ctx, []*schema.Message{
			schema.SystemMessage(classifySystemPrompt),
			schema.UserMessage(string(blob)),
		})
		if err != nil {
			return nil, fmt.Errorf("分类模型调用失败: %w", err)
		}
		return parseClassifierResponse(msg.Content)
	}
}

// parseClassifierResponse tolerates prose or fences around the JSON array —
// the outermost [...] is what gets parsed. Unknown category ids are dropped;
// an empty result for a skill falls back to "other".
func parseClassifierResponse(text string) (map[string][]string, error) {
	start := strings.Index(text, "[")
	end := strings.LastIndex(text, "]")
	if start < 0 || end <= start {
		return nil, errors.New("分类器输出不是 JSON 数组")
	}
	var parsed []struct {
		Slug       string   `json:"slug"`
		Categories []string `json:"categories"`
	}
	if err := json.Unmarshal([]byte(text[start:end+1]), &parsed); err != nil {
		return nil, errors.New("分类器输出无法解析")
	}
	out := map[string][]string{}
	for _, item := range parsed {
		if item.Slug == "" {
			continue
		}
		var valid []string
		for _, c := range item.Categories {
			if slices.Contains(CategoryIDs, c) && len(valid) < 2 {
				valid = append(valid, c)
			}
		}
		if len(valid) == 0 {
			valid = []string{"other"}
		}
		out[item.Slug] = valid
	}
	return out, nil
}

type categoryCacheEntry struct {
	// Key 是 latestVersion（缺省 updatedAt）；变化时重新归类。
	Key        string   `json:"key"`
	Categories []string `json:"categories"`
}

type categoryCacheFile struct {
	Taxonomy int                           `json:"taxonomy"`
	Entries  map[string]categoryCacheEntry `json:"entries"`
}

// Categories serves category counts and category-filtered listings over the
// full catalog. All entry points serialize on one mutex: a second request
// arriving mid-classification waits for the first instead of classifying the
// same batch twice.
type Categories struct {
	reg        *Registry
	cachePath  string
	classifier Classifier
	ttl        time.Duration

	mu          sync.Mutex
	catalogAt   time.Time
	catalog     []Skill
	profiles    map[string]AuthorProfile
	assignments map[string][]string
	cacheLoaded bool
	cache       categoryCacheFile
}

func NewCategories(reg *Registry, cachePath string, classifier Classifier) *Categories {
	return &Categories{
		reg:         reg,
		cachePath:   cachePath,
		classifier:  classifier,
		ttl:         catalogTTL,
		assignments: map[string][]string{},
		cache:       categoryCacheFile{Taxonomy: taxonomyVersion, Entries: map[string]categoryCacheEntry{}},
	}
}

// Counts 返回各分类的技能数。
func (c *Categories) Counts(ctx context.Context) ([]CategoryCount, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.ensureReady(ctx); err != nil {
		return nil, err
	}
	counts := map[string]int{}
	for _, s := range c.catalog {
		for _, cat := range c.assignments[s.FullSlug] {
			counts[cat]++
		}
	}
	out := make([]CategoryCount, len(CategoryIDs))
	for i, id := range CategoryIDs {
		out[i] = CategoryCount{ID: id, Count: counts[id]}
	}
	return out, nil
}

// ListByCategory 在全量目录上按分类（可叠加关键词）过滤并分页。
func (c *Categories) ListByCategory(ctx context.Context, category, q string, page, pageSize int) (Page, error) {
	if !slices.Contains(CategoryIDs, category) {
		return Page{}, fmt.Errorf("未知分类 %q", category)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.ensureReady(ctx); err != nil {
		return Page{}, err
	}
	query := strings.ToLower(strings.TrimSpace(q))
	var matches []Skill
	for _, s := range c.catalog {
		if !slices.Contains(c.assignments[s.FullSlug], category) {
			continue
		}
		if query != "" {
			haystack := strings.ToLower(strings.Join(append([]string{s.Name, s.Description, s.FullSlug}, s.Tags...), "\n"))
			if !strings.Contains(haystack, query) {
				continue
			}
		}
		matches = append(matches, s)
	}
	start := (page - 1) * pageSize
	items := []Skill{}
	if start < len(matches) {
		items = matches[start:min(start+pageSize, len(matches))]
	}
	return Page{
		Items:          items,
		AuthorProfiles: c.profiles,
		Total:          len(matches),
		Page:           page,
		PageSize:       pageSize,
	}, nil
}

// ensureReady loads the catalog (TTL cached) and makes sure every catalog
// entry has a category assignment. Callers hold c.mu.
func (c *Categories) ensureReady(ctx context.Context) error {
	if err := c.ensureCatalog(ctx); err != nil {
		return err
	}
	return c.ensureAssignments(ctx)
}

func (c *Categories) ensureCatalog(ctx context.Context) error {
	if c.catalog != nil && time.Since(c.catalogAt) < c.ttl {
		return nil
	}
	first, err := c.reg.Catalog(ctx, "", 1, catalogPageSize)
	if err != nil {
		return err
	}
	skills := first.Items
	profiles := map[string]AuthorProfile{}
	for k, v := range first.AuthorProfiles {
		profiles[k] = v
	}
	totalPages := (first.Total + catalogPageSize - 1) / catalogPageSize
	totalPages = min(max(totalPages, 1), catalogMaxPages)
	for p := 2; p <= totalPages; p++ {
		next, err := c.reg.Catalog(ctx, "", p, catalogPageSize)
		if err != nil {
			return err
		}
		skills = append(skills, next.Items...)
		for k, v := range next.AuthorProfiles {
			profiles[k] = v
		}
		if len(next.Items) == 0 {
			break
		}
	}
	c.catalog = skills
	c.profiles = profiles
	c.catalogAt = time.Now()
	return nil
}

func cacheKeyOf(s Skill) string {
	if s.LatestVersion != "" {
		return s.LatestVersion
	}
	return s.UpdatedAt
}

func (c *Categories) ensureAssignments(ctx context.Context) error {
	c.loadCache()
	var unclassified []Skill
	for _, s := range c.catalog {
		if _, ok := c.assignments[s.FullSlug]; ok {
			continue
		}
		if cached, ok := c.cache.Entries[s.FullSlug]; ok && cached.Key == cacheKeyOf(s) {
			c.assignments[s.FullSlug] = cached.Categories
			continue
		}
		unclassified = append(unclassified, s)
	}
	if len(unclassified) == 0 {
		return nil
	}
	if c.classifier == nil {
		return errors.New("分类器未配置")
	}
	for start := 0; start < len(unclassified); start += classifyBatchSize {
		batch := unclassified[start:min(start+classifyBatchSize, len(unclassified))]
		result, err := c.classifier(ctx, batch)
		if err != nil {
			return err
		}
		for _, s := range batch {
			categories := result[s.FullSlug]
			if len(categories) == 0 {
				// 模型漏答的技能兜底为 other，避免它们从分类视图里消失。
				categories = []string{"other"}
			}
			c.assignments[s.FullSlug] = categories
			c.cache.Entries[s.FullSlug] = categoryCacheEntry{Key: cacheKeyOf(s), Categories: categories}
		}
	}
	c.saveCache()
	return nil
}

func (c *Categories) loadCache() {
	if c.cacheLoaded {
		return
	}
	c.cacheLoaded = true
	raw, err := os.ReadFile(c.cachePath)
	if err != nil {
		return
	}
	var file categoryCacheFile
	// 缓存缺失或损坏时重新归类即可。
	if json.Unmarshal(raw, &file) == nil && file.Taxonomy == taxonomyVersion && file.Entries != nil {
		c.cache = file
	}
}

func (c *Categories) saveCache() {
	blob, err := json.MarshalIndent(c.cache, "", "  ")
	if err != nil {
		return
	}
	// 缓存写入失败不影响本次会话（内存中已有归类结果）。
	if err := os.MkdirAll(filepath.Dir(c.cachePath), 0o755); err != nil {
		return
	}
	_ = os.WriteFile(c.cachePath, blob, 0o644)
}
