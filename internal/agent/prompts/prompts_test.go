package prompts

import (
	"strings"
	"testing"
)

func TestGeneralContainsPermanentCodingLoop(t *testing.T) {
	for _, required := range []string{
		"## Coding Loop",
		"git status --short",
		"glob → grep → read_file",
		"委派 explore",
		"重新 read_file",
		"apply_patch",
		"运行仓库已有的 formatter",
		"validation_kind",
		"go test -json",
		"git diff -- <相关路径>",
		"commit、push",
		"只有用户明确要求",
	} {
		if !strings.Contains(General, required) {
			t.Errorf("General missing %q", required)
		}
	}
}

func TestSupervisorContainsDelegationMatrix(t *testing.T) {
	for _, required := range []string{
		"默认自己回答",
		"### explore",
		"### deep_research",
		"### job_search",
		"### resume_analyzer",
		"### question_planner",
		"## rag_search",
		"重新 read_file",
	} {
		if !strings.Contains(Supervisor, required) {
			t.Errorf("Supervisor missing %q", required)
		}
	}
}

func TestEveryAgentPromptIncludesSharedCoreDiscipline(t *testing.T) {
	prompts := map[string]string{
		"general":          General,
		"deep_research":    DeepResearch,
		"job_search":       JobSearch,
		"resume_analyzer":  ResumeAnalyzer,
		"question_planner": QuestionPlanner,
		"explore":          Explore,
	}
	for name, prompt := range prompts {
		if !strings.Contains(prompt, "未实际执行工具时") ||
			!strings.Contains(prompt, "同一时刻只调用一个工具") {
			t.Errorf("%s does not include Core discipline", name)
		}
	}
}

func TestExplorePromptDefinesReadMostlyContractAndOutput(t *testing.T) {
	for _, required := range []string{
		"不修改文件",
		"受限 run_command",
		"## 结论摘要",
		"## 关键位置",
		"## 调用关系与模块边界",
		"## 建议主 Agent 重新读取",
		"## 未确认与风险",
		"接近迭代上限时",
		"立即停止工具调用并输出结论",
	} {
		if !strings.Contains(Explore, required) {
			t.Errorf("Explore missing %q", required)
		}
	}
}

func TestPlanAndTodoPromptsDefineLifecycle(t *testing.T) {
	for _, required := range []string{
		"只读 Plan 模式",
		"不得修改文件",
		"调用 create_plan",
		"同一 checkpoint",
	} {
		if !strings.Contains(Plan, required) {
			t.Errorf("Plan missing %q", required)
		}
	}
	for _, required := range []string{
		"## 结构化 Todo",
		"todo_write",
		"最多一个任务为 in_progress",
		"必要验证真实完成后",
		"cancelled",
	} {
		if !strings.Contains(General, required) {
			t.Errorf("General missing %q", required)
		}
	}
}
