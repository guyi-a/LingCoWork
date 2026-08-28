package tools

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"

	"github.com/guyi-a/Interview-Agent/internal/memory"
)

// remember 只管用户级记忆。项目级那份落在工作区根，模型用现成的 write_file /
// apply_patch 就能改，审批门槛由 effects.go 的路径识别兜住 —— 再给它一个专用动作
// 只会多一条模型要记住的规则。
type rememberInput struct {
	Action string `json:"action" jsonschema:"description=append (add one entry) or rewrite (replace the whole file\\, used to merge or drop entries),enum=append,enum=rewrite"`
	Entry  string `json:"entry,omitempty" jsonschema:"description=For append: the preference itself\\, one line\\, phrased so it still makes sense months later. Do NOT write a date — it is filed under today's date group for you."`
	// 分组和日期由服务端补，模型和用户都只给内容。让模型自己写日期还得先调
	// get_current_time，而它对"今天"的判断本来就不可靠。
	Content string `json:"content,omitempty" jsonschema:"description=For rewrite: the complete new file. Entries are one per line under a '## YYYY-MM-DD' heading; keep the existing headings as they are and put anything new on its own line."`
}

type rememberOutput struct {
	OK      bool   `json:"ok"`
	Bytes   int    `json:"bytes,omitempty"`
	Limit   int    `json:"limit,omitempty"`
	Content string `json:"content,omitempty"`
	Message string `json:"message,omitempty"`
}

func newRememberTool(store *memory.Store) (tool.BaseTool, error) {
	if store == nil {
		return nil, errors.New("remember: memory store is nil")
	}
	// 什么该记、什么不该记全写在描述里。模型默认倾向于把任务里的每个细节都当成
	// 值得记的，而记忆每轮都进系统提示词 —— 噪音的代价是长期的，所以这里说"不
	// 要记什么"比说"记什么"更重要。
	desc := "Save a durable USER-LEVEL preference that should carry across all future conversations " +
		"(how to address them, what language to answer in, code style, preferred toolchain). " +
		"Call it only when the user states a preference outright, or after you have been corrected " +
		"on the same point more than once. " +
		"Do NOT record task details, file paths, one-off instructions, or anything specific to the " +
		"current project — project conventions belong in the workspace memory file named in the system prompt. " +
		"Use action=append to add one entry; use action=rewrite (with the complete new content) to merge or " +
		"drop entries, which is also how you make room once the file is full."
	return utils.InferTool("remember", desc, func(ctx context.Context, in *rememberInput) (*rememberOutput, error) {
		switch strings.ToLower(strings.TrimSpace(in.Action)) {
		case "append":
			doc, err := store.Append(in.Entry, time.Now())
			if err != nil {
				return rememberFailure(store, err), nil
			}
			return &rememberOutput{OK: true, Bytes: doc.Bytes, Limit: doc.Limit, Content: doc.Content}, nil

		case "rewrite":
			doc, err := store.Write(in.Content, time.Now())
			if err != nil {
				return rememberFailure(store, err), nil
			}
			return &rememberOutput{OK: true, Bytes: doc.Bytes, Limit: doc.Limit, Content: doc.Content}, nil

		default:
			return &rememberOutput{OK: false, Message: `action must be "append" or "rewrite"`}, nil
		}
	})
}

// rememberFailure 把写入失败翻成模型能据此行动的话。写满是唯一一个模型自己能
// 解决的失败 —— 它已经在上下文里看到了全部记忆，直接 rewrite 一份精简的即可，
// 所以这里把当前内容一并回给它，省一次读取。
func rememberFailure(store *memory.Store, err error) *rememberOutput {
	if errors.Is(err, memory.ErrTooLarge) {
		out := &rememberOutput{
			OK: false,
			Message: fmt.Sprintf(
				"memory is full (limit %d bytes). Merge or drop entries and resubmit with action=rewrite.",
				memory.MaxBytes,
			),
		}
		if doc, readErr := store.Read(); readErr == nil {
			out.Content = doc.Content
			out.Bytes = doc.Bytes
			out.Limit = doc.Limit
		}
		return out
	}
	return &rememberOutput{OK: false, Message: err.Error()}
}
