package memory

import (
	"strings"
	"testing"
)

func TestSnippetEmptyWhenNothingToSay(t *testing.T) {
	if got := RenderSnippet("", "", ""); got != "" {
		t.Errorf("snippet = %q, want empty", got)
	}
}

// 注入的片段必须字节稳定：同样的输入渲染两次要完全一致，否则提示词缓存每轮都
// 会 miss。
func TestSnippetIsByteStable(t *testing.T) {
	user := "- [2026-08-23] 回答用中文\n"
	project := "- [2026-08-23] 构建用 make build\n"
	first := RenderSnippet(user, project, "/ws/memory.md")
	for i := 0; i < 5; i++ {
		if got := RenderSnippet(user, project, "/ws/memory.md"); got != first {
			t.Fatalf("render %d differs from the first", i)
		}
	}
}

// 条目顺序必须是文件里的原始行序，不能重排 —— 重排会让内容没变的记忆也换一个
// 字节表示。
func TestSnippetPreservesEntryOrder(t *testing.T) {
	user := "- [2026-08-23] 乙\n- [2026-08-22] 甲\n"
	got := RenderSnippet(user, "", "")
	if !strings.Contains(got, "- [2026-08-23] 乙\n- [2026-08-22] 甲") {
		t.Errorf("entries were reordered or rewritten:\n%s", got)
	}
}

func TestSnippetOmitsEmptyLevels(t *testing.T) {
	got := RenderSnippet("- [2026-08-23] 回答用中文\n", "", "/ws/memory.md")
	if strings.Contains(got, "### 项目约定") {
		t.Errorf("empty project level still rendered a section:\n%s", got)
	}
	if !strings.Contains(got, "### 用户偏好") {
		t.Errorf("user level missing:\n%s", got)
	}
}

// 有工作区时要把项目记忆的路径写出来，否则模型不知道项目约定该写到哪。
func TestSnippetNamesProjectPathWhenBound(t *testing.T) {
	got := RenderSnippet("", "", "/ws/memory.md")
	if !strings.Contains(got, "/ws/memory.md") {
		t.Errorf("project memory path missing:\n%s", got)
	}

	unbound := RenderSnippet("- [2026-08-23] 甲\n", "", "")
	if strings.Contains(unbound, "写入 ") {
		t.Errorf("unbound conversation should not point at a project file:\n%s", unbound)
	}
	if !strings.Contains(unbound, "用户选择工作区文件夹") {
		t.Errorf("unbound conversation should explain why project memory is unavailable:\n%s", unbound)
	}
}
