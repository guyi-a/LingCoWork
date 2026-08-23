package memory

import (
	"fmt"
	"strings"
)

// RenderSnippet 渲染拼进 system instruction 的片段。
//
// 这段输出必须是**字节稳定**的：提示词缓存按前缀匹配，system prompt 在最前面，
// 片段里任何每轮抖动的内容都会让整个前缀每轮 miss。所以这里只有常量、逐字照抄
// 的文件内容，和会话内固定的工作区路径 —— 不放条数、不放剩余额度、不放时间戳，
// 也不对条目重排（顺序就是文件里的行序）。
//
// 记忆为空且没有工作区时返回空串：宁可什么都不说，也不要用"（暂无记忆）"这种
// 占位白占 token。
func RenderSnippet(userContent, projectContent, projectPath string) string {
	userContent = strings.TrimRight(userContent, "\n")
	projectContent = strings.TrimRight(projectContent, "\n")
	if userContent == "" && projectContent == "" && projectPath == "" {
		return ""
	}

	var b strings.Builder
	b.WriteString("## 长期记忆\n\n")
	b.WriteString("以下是跨对话保留的偏好与约定，视为用户已经确认过的，不必再问一遍。\n")

	if userContent != "" {
		b.WriteString("\n### 用户偏好\n\n")
		b.WriteString(userContent)
		b.WriteString("\n")
	}
	if projectContent != "" {
		b.WriteString("\n### 项目约定\n\n")
		b.WriteString(projectContent)
		b.WriteString("\n")
	}

	b.WriteString("\n写入规则：\n")
	b.WriteString("- 用户偏好（称呼、回答语言、代码风格、常用工具链）用 remember 工具写入。\n")
	if projectPath != "" {
		b.WriteString(fmt.Sprintf("- 项目约定（技术栈、构建命令、目录规范、不要动的文件）写入 %s。\n", projectPath))
	} else {
		b.WriteString("- 当前会话没有绑定工作区，只能写用户偏好；项目约定需要先建工作区。\n")
	}
	b.WriteString("- 只记跨对话仍然成立的偏好与约定；一次性的任务细节不要记。\n")
	b.WriteString("- 日期只说明这条什么时候记下的，不代表它过期了。\n")
	b.WriteString("- 一行一条，只写内容；日期分组（## 年-月-日）由系统维护，不要自己改。\n")
	b.WriteString(fmt.Sprintf("- 每级上限 %d KB。写满之后先合并旧条目腾出空间，不要硬追加。\n", MaxBytes>>10))
	return b.String()
}
