package memory

import (
	"path/filepath"
	"sync"
)

// FileName 是两级记忆共用的文件名。用户级落在应用数据目录，项目级落在工作区根。
const FileName = "memory.md"

// ProjectPath 是"项目级记忆在哪"的唯一定义。中间件靠它读、审批靠它认路径、
// HTTP 靠它写 —— 三处各写一遍 filepath.Join 就是等着它们哪天对不上。
// workspaceRoot 为空（临时对话）时返回空串。
func ProjectPath(workspaceRoot string) string {
	if workspaceRoot == "" {
		return ""
	}
	return filepath.Join(workspaceRoot, FileName)
}

// Registry 保证同一个文件只有一个 Store，因而只有一把锁。
//
// 用户级路径进程启动就知道，本可以直接 NewStore；项目级路径要按会话解析工作区，
// 每次请求新建一个实例就等于每次换一把锁，读改写之间的窗口不再互斥。两级都走
// 这里领实例，写入路径上就只有一把锁。
type Registry struct {
	mu     sync.Mutex
	stores map[string]*Store
}

func NewRegistry() *Registry {
	return &Registry{stores: make(map[string]*Store)}
}

// For 返回该路径的 Store，没有就建一个。path 为空返回 nil，调用方按"这一级
// 不可用"处理。
func (r *Registry) For(path string) *Store {
	if path == "" {
		return nil
	}
	path = filepath.Clean(path)

	r.mu.Lock()
	defer r.mu.Unlock()
	if s, ok := r.stores[path]; ok {
		return s
	}
	s := NewStore(path)
	r.stores[path] = s
	return s
}
