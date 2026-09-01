package approval

import (
	"context"
	"encoding/json"
	"log"
	"strings"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"

	"github.com/guyi-a/Interview-Agent/internal/agent/contextkey"
	"github.com/guyi-a/Interview-Agent/internal/effect"
	"github.com/guyi-a/Interview-Agent/internal/stream"
)

// Middleware wraps tool calls with the deterministic three-mode approval
// policy. Effects are re-derived on resume and bound to the decision digest,
// so an approved target cannot change between review and execution.
//
// Every agent in the process shares one Middleware value. Building a second
// one per sub-agent would work, but the supervisor and its sub-agents would
// then be judging calls against separately-constructed policy, and a future
// change that only reached one construction site would silently leave the
// others on the old rules.
//
// Wire it OUTSIDE toolerr.Middleware so the interrupt sentinel error bubbles
// straight up to the framework instead of being wrapped into a fake tool
// result:
//
//	ToolCallMiddlewares: []compose.ToolMiddleware{
//	    approvalMW,
//	    toolerr.Middleware(),
//	}
func Middleware(store *ModeStore, memory *Memory, registry *effect.Registry) compose.ToolMiddleware {
	interrupt := func(ctx context.Context, input *compose.ToolInput, e effect.Effect) error {
		_, rememberable := Fingerprint(e, input.Arguments)
		return tool.Interrupt(ctx, &stream.ApprovalInfo{
			Tool:         input.Name,
			Args:         input.Arguments,
			CallID:       input.CallID,
			EffectJSON:   e.JSON(),
			Rememberable: rememberable,
		})
	}
	return compose.ToolMiddleware{
		Invokable: func(next compose.InvokableToolEndpoint) compose.InvokableToolEndpoint {
			return func(ctx context.Context, input *compose.ToolInput) (*compose.ToolOutput, error) {
				decision := evaluate(ctx, store, memory, registry, input.Name, input.Arguments)
				switch decision.kind {
				case decisionPass:
					return next(ctx, input)
				case decisionDeny:
					return &compose.ToolOutput{Result: denialMessage(input.Name, decision.reason)}, nil
				default:
					return nil, interrupt(ctx, input, decision.effect)
				}
			}
		},
		Streamable: func(next compose.StreamableToolEndpoint) compose.StreamableToolEndpoint {
			return func(ctx context.Context, input *compose.ToolInput) (*compose.StreamToolOutput, error) {
				decision := evaluate(ctx, store, memory, registry, input.Name, input.Arguments)
				switch decision.kind {
				case decisionPass:
					return next(ctx, input)
				case decisionDeny:
					return &compose.StreamToolOutput{
						Result: schema.StreamReaderFromArray([]string{denialMessage(input.Name, decision.reason)}),
					}, nil
				default:
					return nil, interrupt(ctx, input, decision.effect)
				}
			}
		},
	}
}

// decisionKind partitions the middleware's answers into three categories
// the Invokable/Streamable wrappers can act on uniformly. Both wrappers
// share evaluate() to keep the decision chain in one place.
type decisionKind int

const (
	decisionInterrupt decisionKind = iota // prompt the user
	decisionPass                          // run the tool
	decisionDeny                          // return the user's rejection reason
)

type decision struct {
	kind   decisionKind
	reason string // for decisionDeny: user reason surfaced to the model
	effect effect.Effect
}

func evaluate(
	ctx context.Context,
	store *ModeStore,
	memory *Memory,
	registry *effect.Registry,
	toolName, argsJSON string,
) decision {
	e := registry.Derive(ctx, toolName, argsJSON)

	// A resume decision is authoritative only for the exact effect the user
	// reviewed. Missing payloads fail closed instead of implicitly approving
	// sibling interrupts.
	if resumed, dec, ok := resumeDecision(ctx); resumed {
		if !ok {
			return decision{kind: decisionInterrupt, effect: e}
		}
		if !dec.Approved {
			return decision{kind: decisionDeny, reason: dec.Reason, effect: e}
		}
		if dec.EffectDigest != "" && dec.EffectDigest != EffectDigest(e) {
			log.Printf("approval: target changed before execution for %s", toolName)
			return decision{kind: decisionInterrupt, effect: e}
		}
		return decision{kind: decisionPass, effect: e}
	}

	// The safety wall is shared by every mode.
	if must, why := MustAsk(e); must {
		log.Printf("approval: wall held %s (%s / %s)", toolName, e.Kind, why)
		return decision{kind: decisionInterrupt, effect: e}
	}
	if sensitive, why := IsSensitiveCall(e, argsJSON); sensitive {
		log.Printf("approval: sensitive call held %s (%s)", toolName, why)
		return decision{kind: decisionInterrupt, effect: e}
	}

	mode := store.Get(contextkey.ConversationID(ctx))
	if !ShouldAsk(mode, e) {
		return decision{kind: decisionPass, effect: e}
	}

	if fingerprint, ok := Fingerprint(e, argsJSON); ok {
		if rememberedInSnapshot(ctx, fingerprint) {
			log.Printf("approval: remembered grant allowed %s", toolName)
			return decision{kind: decisionPass, effect: e}
		}
		// Test and direct middleware callers may not install a run snapshot.
		if _, hasSnapshot := ctx.Value(memorySnapshotKey{}).(map[string]struct{}); !hasSnapshot {
			if _, live := memory.Allowed(contextkey.ConversationID(ctx))[fingerprint]; live {
				return decision{kind: decisionPass, effect: e}
			}
		}
	}

	return decision{kind: decisionInterrupt, effect: e}
}

// resumeDecision distills tool.GetInterruptState + tool.GetResumeContext
// into: (is this a resume?, decision, did the user attach one?).
func resumeDecision(ctx context.Context) (bool, Decision, bool) {
	interrupted, _, _ := tool.GetInterruptState[any](ctx)
	if !interrupted {
		return false, Decision{}, false
	}
	_, has, dec := tool.GetResumeContext[Decision](ctx)
	return true, dec, has
}

// denialMessage is what the model sees on its next ReAct turn when a call is
// rejected. Returns a stable JSON shape so the model can distinguish a user
// cancellation ("canceled":true) from a real tool error, and act on
// "instruction" instead of retrying the same call.
func denialMessage(toolName, reason string) string {
	payload := map[string]any{
		"canceled": true,
		"tool":     toolName,
	}
	reason = strings.TrimSpace(reason)
	if reason != "" {
		payload["reason"] = reason
		payload["instruction"] = "用户拒绝执行该工具并给出了 reason。请根据 reason 调整方案，不要原样重试同一工具调用。"
	} else {
		payload["instruction"] = "用户拒绝执行该工具但未说明理由。请向用户说明该操作已取消，并询问希望如何继续，或提出不执行该工具的替代方案。"
	}
	b, _ := json.Marshal(payload)
	return string(b)
}
