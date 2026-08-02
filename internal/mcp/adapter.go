package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
	"github.com/eino-contrib/jsonschema"
	"github.com/mark3labs/mcp-go/client"
	mcpgo "github.com/mark3labs/mcp-go/mcp"
)

// This is the translation layer between the MCP wire protocol and eino's tool
// interface. eino-ext ships one, but it discards ToolAnnotations (which the
// approval policy runs on) and hardcodes the remote tool name as the name the
// model sees (which collides with our builtins). Both would have to be worked
// around from the outside, and the workarounds come to more code than the
// translation itself.

// remoteTool is one MCP tool presented to the agent.
//
// It carries two names on purpose. publicName is prefixed and is what the
// model calls; remoteName is what the server published and is what goes back
// over the wire. Collapsing them is what forces the eino-ext version to be
// wrapped.
type remoteTool struct {
	cli        *client.Client
	server     string
	remoteName string
	info       *schema.ToolInfo
	// auth is nil for servers that do not use OAuth. When set, it is what a
	// mid-call token expiry is recovered through.
	auth *authRecovery
}

// authRecovery is the narrow slice of the manager and authorizer a live tool
// needs when its token stops working. An interface-free struct of funcs
// because there are exactly two operations and both are one line.
type authRecovery struct {
	refresh       func(ctx context.Context) bool
	markNeedsAuth func()
}

var _ tool.InvokableTool = (*remoteTool)(nil)

// listTools fetches every tool a server publishes.
//
// Client.ListTools follows nextCursor internally, so a paginated server comes
// back whole from this one call.
func listTools(ctx context.Context, cli *client.Client) ([]mcpgo.Tool, error) {
	res, err := cli.ListTools(ctx, mcpgo.ListToolsRequest{})
	if err != nil {
		return nil, fmt.Errorf("list tools: %w", err)
	}
	return res.Tools, nil
}

// newRemoteTool converts one MCP tool descriptor into an eino tool.
func newRemoteTool(cli *client.Client, server, publicName string, t mcpgo.Tool, auth *authRecovery) (*remoteTool, error) {
	params, err := convertSchema(t)
	if err != nil {
		return nil, fmt.Errorf("tool %q: %w", t.Name, err)
	}
	desc := strings.TrimSpace(t.Description)
	if desc == "" {
		// A tool with no description is nearly unusable to the model. Say
		// where it came from so at least the server name is a hint.
		desc = fmt.Sprintf("Tool %q provided by the %q MCP server.", t.Name, server)
	}
	return &remoteTool{
		cli:        cli,
		server:     server,
		remoteName: t.Name,
		auth:       auth,
		info: &schema.ToolInfo{
			Name:        publicName,
			Desc:        desc,
			ParamsOneOf: params,
		},
	}, nil
}

// convertSchema turns the server's JSON Schema into eino's representation.
//
// Round-tripping through JSON rather than mapping field by field is
// deliberate: the input schema is arbitrary JSON Schema and any hand-written
// mapping would quietly drop the keywords it didn't know about.
func convertSchema(t mcpgo.Tool) (*schema.ParamsOneOf, error) {
	raw := t.RawInputSchema
	if len(raw) == 0 {
		var err error
		raw, err = json.Marshal(t.InputSchema)
		if err != nil {
			return nil, fmt.Errorf("marshal input schema: %w", err)
		}
	}
	js := &jsonschema.Schema{}
	if err := json.Unmarshal(raw, js); err != nil {
		return nil, fmt.Errorf("parse input schema: %w", err)
	}
	return schema.NewParamsOneOfByJSONSchema(js), nil
}

func (t *remoteTool) Info(context.Context) (*schema.ToolInfo, error) {
	return t.info, nil
}

func (t *remoteTool) InvokableRun(ctx context.Context, argumentsInJSON string, _ ...tool.Option) (string, error) {
	req := mcpgo.CallToolRequest{
		Request: mcpgo.Request{Method: "tools/call"},
		Params: mcpgo.CallToolParams{
			Name:      t.remoteName,
			Arguments: rawArguments(argumentsInJSON),
		},
	}

	res, err := t.cli.CallTool(ctx, req)
	if err != nil && client.IsOAuthAuthorizationRequiredError(err) {
		res, err = t.retryAfterRefresh(ctx, req)
	}
	if err != nil {
		return "", fmt.Errorf("mcp server %q: %w", t.server, err)
	}

	out := flattenResult(res)
	if res.IsError {
		// The MCP spec routes tool-level failures through IsError rather than
		// a protocol error precisely so the model can see them and correct
		// itself. Returning a Go error is how that happens here: the toolerr
		// middleware turns it into a message on the next turn instead of
		// aborting the run.
		if out == "" {
			out = "the tool reported an error with no detail"
		}
		return "", fmt.Errorf("mcp tool %q failed: %s", t.remoteName, out)
	}
	return out, nil
}

// retryAfterRefresh handles a token the server stopped accepting.
//
// mcp-go refreshes only when it can see locally that a token has expired. A
// server that revoked the token early, or rotated its keys, just answers 401
// — and the transport turns that into an authorization-required error without
// trying the refresh token it is holding. So force one refresh and try again.
//
// Exactly one retry. If a freshly minted token is also rejected, the grant is
// gone and repeating the call would only spend the user's rate limit on it.
func (t *remoteTool) retryAfterRefresh(
	ctx context.Context,
	req mcpgo.CallToolRequest,
) (*mcpgo.CallToolResult, error) {
	if t.auth == nil {
		return nil, ErrNeedsAuthorization
	}
	if !t.auth.refresh(ctx) {
		t.auth.markNeedsAuth()
		return nil, fmt.Errorf(
			"authorization for the %q server has expired; "+
				"tell the user to re-authorize it on the connectors page", t.server)
	}
	res, err := t.cli.CallTool(ctx, req)
	if err != nil && client.IsOAuthAuthorizationRequiredError(err) {
		t.auth.markNeedsAuth()
		return nil, fmt.Errorf(
			"the %q server rejected a freshly refreshed token; "+
				"tell the user to re-authorize it on the connectors page", t.server)
	}
	return res, err
}

// rawArguments passes the model's argument JSON through untouched.
//
// An empty string is not valid JSON, and some models emit one for a tool that
// takes no arguments, so it becomes an empty object instead of a parse error
// on the server.
func rawArguments(argumentsInJSON string) any {
	if strings.TrimSpace(argumentsInJSON) == "" {
		return map[string]any{}
	}
	return json.RawMessage(argumentsInJSON)
}

// flattenResult renders a tool result as text for the model.
//
// Handing over the marshalled CallToolResult would work but wastes context on
// JSON scaffolding around what is almost always a single text block. Binary
// blocks cannot go down this path at all, so they are announced rather than
// inlined — a megabyte of base64 in the transcript helps nobody.
func flattenResult(res *mcpgo.CallToolResult) string {
	if res == nil {
		return ""
	}
	var parts []string
	for _, c := range res.Content {
		if text, ok := mcpgo.AsTextContent(c); ok {
			parts = append(parts, text.Text)
			continue
		}
		if img, ok := mcpgo.AsImageContent(c); ok {
			parts = append(parts, fmt.Sprintf("[image omitted: %s, %d bytes base64]", img.MIMEType, len(img.Data)))
			continue
		}
		if audio, ok := mcpgo.AsAudioContent(c); ok {
			parts = append(parts, fmt.Sprintf("[audio omitted: %s, %d bytes base64]", audio.MIMEType, len(audio.Data)))
			continue
		}
		if blob, err := json.Marshal(c); err == nil {
			parts = append(parts, string(blob))
		}
	}

	joined := strings.TrimSpace(strings.Join(parts, "\n"))
	if joined != "" {
		return joined
	}
	// A server may answer with structuredContent alone. The spec asks it to
	// also send an equivalent text block, but not every server does.
	if res.StructuredContent != nil {
		if blob, err := json.Marshal(res.StructuredContent); err == nil {
			return string(blob)
		}
	}
	return ""
}
