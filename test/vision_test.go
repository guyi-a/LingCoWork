package test

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/cloudwego/eino/schema"

	"github.com/guyi-a/Interview-Agent/internal/agent/llm"
	"github.com/guyi-a/Interview-Agent/internal/config"
)

// RUN_VISION_INTEGRATION=1 opts into a real, billable DeepSeek request.
// The default test suite skips this test.
func TestDeepSeekVisionStream(t *testing.T) {
	if os.Getenv("RUN_VISION_INTEGRATION") != "1" {
		t.Skip("set RUN_VISION_INTEGRATION=1 to call the real vision API")
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	cfg.LLM.Model = "deepseek-v4-flash-vision-exp"
	cfg.LLM.Multimodal = true

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	cm, err := llm.NewChatModel(ctx, cfg.LLM)
	if err != nil {
		t.Fatalf("llm.NewChatModel: %v", err)
	}

	// Valid 1x1 transparent PNG.
	b64 := "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII="
	reader, err := cm.Stream(ctx, []*schema.Message{{
		Role: schema.User,
		UserInputMultiContent: []schema.MessageInputPart{
			{Type: schema.ChatMessagePartTypeText, Text: "请用一句话说明这张图片非常小。"},
			{
				Type: schema.ChatMessagePartTypeImageURL,
				Image: &schema.MessageInputImage{
					MessagePartCommon: schema.MessagePartCommon{
						Base64Data: &b64,
						MIMEType:   "image/png",
					},
					Detail: schema.ImageURLDetailAuto,
				},
			},
		},
	}})
	if err != nil {
		t.Fatalf("vision stream: %v", err)
	}
	defer reader.Close()

	var content strings.Builder
	for {
		chunk, recvErr := reader.Recv()
		if errors.Is(recvErr, io.EOF) {
			break
		}
		if recvErr != nil {
			t.Fatalf("receive vision stream: %v", recvErr)
		}
		content.WriteString(chunk.Content)
	}
	if strings.TrimSpace(content.String()) == "" {
		t.Fatal("vision model returned no text")
	}

	withTools, err := cm.WithTools([]*schema.ToolInfo{{
		Name: "report_image_seen",
		Desc: "Call this no-argument tool after confirming that an image was provided.",
	}})
	if err != nil {
		t.Fatalf("bind vision tool: %v", err)
	}
	toolReply, err := withTools.Generate(ctx, []*schema.Message{{
		Role: schema.User,
		UserInputMultiContent: []schema.MessageInputPart{
			{
				Type: schema.ChatMessagePartTypeText,
				Text: "先查看图片，然后必须调用 report_image_seen 工具，不要直接回答。",
			},
			{
				Type: schema.ChatMessagePartTypeImageURL,
				Image: &schema.MessageInputImage{
					MessagePartCommon: schema.MessagePartCommon{
						Base64Data: &b64,
						MIMEType:   "image/png",
					},
					Detail: schema.ImageURLDetailAuto,
				},
			},
		},
	}})
	if err != nil {
		t.Fatalf("vision tool calling: %v", err)
	}
	if len(toolReply.ToolCalls) == 0 ||
		toolReply.ToolCalls[0].Function.Name != "report_image_seen" {
		t.Fatalf("vision model did not call tool: %#v", toolReply.ToolCalls)
	}
}
