package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/guyi-a/Interview-Agent/internal/repository"
	"github.com/guyi-a/Interview-Agent/internal/repository/model"
	"github.com/guyi-a/Interview-Agent/internal/stream"
)

type messageJournal struct {
	mu                       sync.Mutex
	repo                     *repository.MessageRepo
	conversationID           string
	runID                    string
	assistantOrdinal         int
	lastAssistantID          uint64
	lastAssistantSeq         int
	lastAssistantFingerprint string
	lastEventWasAssistant    bool
}

func newMessageJournal(
	repo *repository.MessageRepo,
	conversationID, runID string,
) *messageJournal {
	return &messageJournal{
		repo: repo, conversationID: conversationID, runID: runID,
	}
}

func (j *messageJournal) AppendAssistant(
	ctx context.Context,
	record stream.AssistantTurnRecord,
) (int, bool, error) {
	if record.Content == "" && record.ReasoningContent == "" &&
		len(record.ToolCalls) == 0 {
		return 0, false, nil
	}
	rawCalls := ""
	if len(record.ToolCalls) > 0 {
		data, err := json.Marshal(record.ToolCalls)
		if err != nil {
			return 0, false, err
		}
		rawCalls = string(data)
	}
	fingerprint := record.Content + "\x00" + record.ReasoningContent + "\x00" + rawCalls
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.lastEventWasAssistant && j.lastAssistantFingerprint == fingerprint {
		return j.lastAssistantSeq, false, nil
	}
	j.assistantOrdinal++
	key := fmt.Sprintf("assistant:%s:%d", j.runID, j.assistantOrdinal)
	row := &model.Message{
		ConversationID:   j.conversationID,
		EventKey:         &key,
		RunID:            j.runID,
		Role:             "assistant",
		Content:          record.Content,
		ReasoningContent: record.ReasoningContent,
		ToolCalls:        rawCalls,
		TotalTokens:      record.TotalTokens,
	}
	persistCtx, cancel := journalContext(ctx)
	defer cancel()
	created, err := j.repo.AppendEvent(persistCtx, row)
	if err != nil {
		return 0, false, err
	}
	j.lastAssistantID = row.ID
	j.lastAssistantSeq = row.Seq
	j.lastAssistantFingerprint = fingerprint
	j.lastEventWasAssistant = true
	return row.Seq, created, nil
}

func (j *messageJournal) AppendToolResult(
	ctx context.Context,
	record stream.ToolResultRecord,
) (int, bool, error) {
	if record.CallID == "" {
		return 0, false, nil
	}
	j.mu.Lock()
	j.lastEventWasAssistant = false
	j.mu.Unlock()
	key := "tool:" + record.CallID
	content := record.Content
	if !record.OK {
		if content == "" {
			content = record.Error
		} else if record.Error != "" && !strings.Contains(content, record.Error) {
			content += " (" + record.Error + ")"
		}
		if content == "" {
			content = "[error] tool failed"
		}
	}
	row := &model.Message{
		ConversationID: j.conversationID,
		EventKey:       &key,
		RunID:          j.runID,
		Role:           "tool",
		Content:        content,
		ToolCallID:     record.CallID,
		ToolName:       record.Name,
	}
	if !record.OK || record.Cancelled {
		payload := map[string]any{"ok": record.OK}
		if record.Error != "" {
			payload["error"] = record.Error
		}
		if record.Cancelled {
			payload["cancelled"] = true
		}
		if data, err := json.Marshal(payload); err == nil {
			row.Extra = string(data)
		}
	}
	persistCtx, cancel := journalContext(ctx)
	defer cancel()
	created, err := j.repo.AppendEvent(persistCtx, row)
	return row.Seq, created, err
}

func (j *messageJournal) AppendPartialAssistant(
	ctx context.Context,
	content, reasoning string,
) error {
	if content == "" && reasoning == "" {
		return nil
	}
	_, _, err := j.AppendAssistant(ctx, stream.AssistantTurnRecord{
		Content: content, ReasoningContent: reasoning,
	})
	return err
}

func (j *messageJournal) UpdateLastAssistant(
	ctx context.Context,
	totalTokens int,
	extra string,
) error {
	j.mu.Lock()
	id := j.lastAssistantID
	j.mu.Unlock()
	if id == 0 {
		var err error
		persistCtx, cancel := journalContext(ctx)
		defer cancel()
		id, err = j.repo.LatestAssistantID(persistCtx, j.conversationID)
		if err != nil {
			return err
		}
	}
	persistCtx, cancel := journalContext(ctx)
	defer cancel()
	return j.repo.UpdateAssistantMetadata(persistCtx, id, totalTokens, extra)
}

var _ stream.MessageJournal = (*messageJournal)(nil)

func journalContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
}
