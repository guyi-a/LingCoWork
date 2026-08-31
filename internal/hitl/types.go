// Package hitl 承载"跟用户对话式交互"的共享类型 —— 单选/多选反问的 Question /
// Answer / Answers、pending 队列的 Kind 枚举。工具层、stream 层、审批层、
// service / handler 都从这里取类型，避免相互 import 各自的私有包。
//
// 恢复流程需要通过 gob 把 Answers 塞进 ResumeParams.Targets，所以在 init 里
// 注册值类型。指针类型（作为 tool.Interrupt payload 的 stream.QuestionInfo）
// 在 stream 侧注册，跟 stream.ApprovalInfo 一起管理。
package hitl

import "encoding/gob"

// Question 是一次 ask_user 调用里的一个题目。
type Question struct {
	// ID 是可选的稳定标识；工具体在缺失时会补 q1/q2/...，供答案关联使用。
	ID string `json:"id,omitempty"`
	// Label 是可选的短标签，前端在多题场景下作为 tab 头部展示。
	Label string `json:"label,omitempty"`
	// Question 是问题正文，展示给用户。
	Question string `json:"question"`
	// Options 是候选选项字符串。想标记推荐项就在字符串末尾追加
	// "(Recommended)" 或 "（推荐）"，前端识别后自动抠掉后缀并高亮。
	Options []string `json:"options"`
	// MultiSelect 决定单选或多选。默认 false（单选，radio）；
	// true 时前端渲染 checkbox，至少选一个。
	MultiSelect bool `json:"multi_select,omitempty"`
}

// Answer 是用户对一个 Question 的答复。
type Answer struct {
	// QuestionID 对应 Question.ID。
	QuestionID string `json:"question_id"`
	// Selected 是用户勾选的选项文本（已经抠掉推荐后缀），单选场景长度为 1。
	Selected []string `json:"selected,omitempty"`
	// Custom 是用户在 "Other" 输入框填入的自定义答案。单选场景下 Selected
	// 为空且 Custom 非空表示走了 Other 路径；多选场景不给 Other。
	Custom string `json:"custom,omitempty"`
}

// Answers 是一次 ask_user 恢复时的全部答案 payload。通过 gob 塞进
// ResumeParams.Targets，工具体用 tool.GetResumeContext[Answers](ctx) 取回。
type Answers struct {
	// Cancelled 为 true 表示用户放弃回答（点了"暂不继续"）。此时
	// Items 为空，工具体返回固定的取消文案给 LLM。
	Cancelled bool `json:"cancelled,omitempty"`
	// Items 每项对应一个 Question 的答复。
	Items []Answer `json:"items,omitempty"`
}

// PlanDecision resumes create_plan after the user edits and accepts or
// cancels the persisted draft. PlanJSON is the canonical final snapshot from
// the database, not the model's original proposal.
type PlanDecision struct {
	Cancelled bool   `json:"cancelled,omitempty"`
	PlanJSON  string `json:"plan_json,omitempty"`
}

// PendingKind 区分一个中断是"等审批"还是"等用户回复"。
type PendingKind string

const (
	KindApproval PendingKind = "approval"
	KindQuestion PendingKind = "question"
	KindPlan     PendingKind = "plan"
)

func init() {
	// 只注册 ResumeParams.Targets 里会传的值类型。stream.QuestionInfo 作为
	// tool.Interrupt payload 需要单独注册（放在 stream 包里跟 ApprovalInfo
	// 一起）。
	gob.Register(Answers{})
	gob.Register(PlanDecision{})
}
