package service

import (
	"context"
	"time"

	"github.com/guyi-a/Interview-Agent/internal/memory"
)

// MemoryService 是长期记忆的读写门面，给 HTTP 层用。
//
// 两级的差别只在"文件在哪"：用户级路径进程启动就定了，项目级要先解析出会话的
// 工作区。定位之后走的都是同一个 Store，也就是跟注入中间件和 remember 工具同一
// 把锁 —— 记忆是唯一会被用户和 Agent 同时写的东西，锁分家等于没锁。
type MemoryService struct {
	registry  *memory.Registry
	userPath  string
	workspace *WorkspaceService
}

func NewMemoryService(registry *memory.Registry, userPath string, workspace *WorkspaceService) *MemoryService {
	return &MemoryService{registry: registry, userPath: userPath, workspace: workspace}
}

type MemoryResult struct {
	Scope   string `json:"scope"`
	Path    string `json:"path"`
	Content string `json:"content"`
	// Hash 是乐观锁的凭据：前端保存时带回来，对不上说明这期间 Agent 写过，
	// 服务端拒绝而不是让用户的版本盖掉它。
	Hash  string `json:"hash"`
	Bytes int    `json:"bytes"`
	Limit int    `json:"limit"`
}

func (s *MemoryService) ReadUser() (*MemoryResult, error) {
	store := s.registry.For(s.userPath)
	doc, err := store.Read()
	if err != nil {
		return nil, err
	}
	return result("user", store.Path(), doc), nil
}

func (s *MemoryService) WriteUser(content, expectedHash string) (*MemoryResult, error) {
	store := s.registry.For(s.userPath)
	doc, err := store.WriteChecked(content, expectedHash, time.Now())
	if err != nil {
		return nil, err
	}
	return result("user", store.Path(), doc), nil
}

// ReadProject 读会话所属工作区的记忆。没绑工作区时返回 ErrNoWorkspace —— 项目级
// 记忆跟着工作区走，没有工作区就没有这一级，而不是返回一个空文件。
func (s *MemoryService) ReadProject(ctx context.Context, convID, projectID string) (*MemoryResult, error) {
	store, err := s.projectStore(ctx, convID, projectID)
	if err != nil {
		return nil, err
	}
	doc, err := store.Read()
	if err != nil {
		return nil, err
	}
	return result("project", store.Path(), doc), nil
}

func (s *MemoryService) WriteProject(ctx context.Context, convID, projectID, content, expectedHash string) (*MemoryResult, error) {
	store, err := s.projectStore(ctx, convID, projectID)
	if err != nil {
		return nil, err
	}
	doc, err := store.WriteChecked(content, expectedHash, time.Now())
	if err != nil {
		return nil, err
	}
	return result("project", store.Path(), doc), nil
}

func (s *MemoryService) projectStore(ctx context.Context, convID, projectID string) (*memory.Store, error) {
	root, _, err := s.workspace.Root(ctx, convID, projectID)
	if err != nil {
		return nil, err
	}
	store := s.registry.For(memory.ProjectPath(root))
	if store == nil {
		return nil, ErrNoWorkspace
	}
	return store, nil
}

func result(scope, path string, doc memory.Doc) *MemoryResult {
	return &MemoryResult{
		Scope:   scope,
		Path:    path,
		Content: doc.Content,
		Hash:    doc.Hash,
		Bytes:   doc.Bytes,
		Limit:   doc.Limit,
	}
}
