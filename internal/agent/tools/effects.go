package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/guyi-a/Interview-Agent/internal/agent/scope"
	"github.com/guyi-a/Interview-Agent/internal/approval"
	"github.com/guyi-a/Interview-Agent/internal/effect"
)

// This file is the single place that says what each builtin tool DOES. The
// approval policy reads only the effect, never the tool name, so a tool whose
// deriver is missing or wrong is a security bug rather than a cosmetic one —
// TestEveryBuiltinToolHasADeriver exists to catch the missing half.

// registerEffects installs a deriver for every builtin tool.
//
// Registration is unconditional even though several tools only appear when
// their dependency is configured. A deriver for a tool that was never built
// is dead weight and nothing more, whereas a tool built without a deriver
// derives to KindUnknown and prompts the user on every call. Registering the
// superset makes the second case impossible.
func registerEffects(reg *effect.Registry, d *fsDeps) {
	// --- no side effect at all ---
	reg.Register("get_current_time", effect.Static(effect.Effect{Kind: effect.KindReadOnlyQuery}))
	reg.Register("rag_search", effect.Static(effect.Effect{Kind: effect.KindReadOnlyQuery}))
	// ask_user is the human interaction itself. Gating it would ask the user
	// for permission to ask the user something; the tool raises its own
	// question interrupt further down the stack.
	reg.Register("ask_user", effect.Static(effect.Effect{Kind: effect.KindUserInteraction}))

	// --- reads: may leave the workspace, so scope decides ---
	reg.Register("read_file", d.readEffect("path"))
	reg.Register("list_files", d.readEffect("path"))
	reg.Register("file_info", d.readEffect("path"))
	reg.Register("extract_document_text", d.readEffect("path"))

	// --- writes: scope.Resolve fences these into the workspace ---
	reg.Register("write_file", d.writeEffect("path"))
	reg.Register("edit_file", d.writeEffect("path"))
	reg.Register("edit_file_lines", d.writeEffect("path"))
	reg.Register("mkdir", d.structureEffect("path"))
	reg.Register("write_file_chunked", d.chunkedWriteEffect())
	reg.Register("create_workspace", createWorkspaceEffect)

	// --- destructive / transfer ---
	reg.Register("rm", d.rmEffect())
	reg.Register("mv", d.transferEffect(true))
	reg.Register("cp", d.transferEffect(false))

	// --- execution ---
	reg.Register("run_command", d.runCommandEffect())
	reg.Register("browser_use", browserEffect("browser_use"))
	reg.Register("browser_bridge", browserEffect("browser_bridge"))
	reg.Register("browser_use_install", effect.Static(effect.Effect{
		Kind:           effect.KindProcessExec,
		Classification: effect.Normal,
		Command:        "install the bundled browser runtime",
	}))

	// --- network ---
	reg.Register("web_search", webSearchEffect)
	reg.Register("web_fetch", webFetchEffect)

	// --- skills ---
	reg.Register("load_skill", loadSkillEffect)
}

// BuiltinToolNames lists every tool registerEffects covers, in the same order.
// Used by the drift test and by startup checks.
func BuiltinToolNames() []string {
	return []string{
		"get_current_time", "rag_search", "ask_user",
		"read_file", "list_files", "file_info", "extract_document_text",
		"write_file", "edit_file", "edit_file_lines", "mkdir",
		"write_file_chunked", "create_workspace",
		"rm", "mv", "cp",
		"run_command", "browser_use", "browser_bridge", "browser_use_install",
		"web_search", "web_fetch", "load_skill",
	}
}

// --- path scope ---

// scopeOfResolvedPath reports whether an already-resolved absolute path lands
// inside the workspace.
//
// This can't be folded into scope.Resolve/ResolveRead: ResolveRead hands back
// absolute paths untouched precisely so tools can reach outside the
// workspace, which means the caller still has to ask where the result landed.
// An absolute path pointing INTO the workspace is the case worth getting
// right — treating it as external would prompt on paths the agent is fully
// entitled to touch.
func scopeOfResolvedPath(workspaceRoot, absPath string) effect.Scope {
	if workspaceRoot == "" {
		return effect.ScopeExternal
	}
	root, err := filepath.Abs(workspaceRoot)
	if err != nil {
		return effect.ScopeExternal
	}
	target, err := filepath.Abs(absPath)
	if err != nil {
		return effect.ScopeExternal
	}
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return effect.ScopeExternal
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return effect.ScopeExternal
	}
	return effect.ScopeWorkspace
}

// readTarget resolves a path the way the read-side tools do and reports where
// it lands.
//
// A resolution failure is reported as in-workspace, which reads backwards
// until you list what can actually fail: an empty path, a relative path with
// no workspace mounted, or a relative path escaping the root. ResolveRead
// rejects all three and the tool then returns the same error, so the call
// touches nothing. Calling that "external" would make the user approve a
// request that was already refused.
func (d *fsDeps) readTarget(ctx context.Context, path string) (string, effect.Scope, string) {
	ws, _ := d.resolveWorkspace(ctx)
	abs, err := scope.ResolveRead(ws, path)
	if err != nil {
		return path, effect.ScopeWorkspace, "path does not resolve: " + err.Error()
	}
	return abs, scopeOfResolvedPath(ws, abs), ""
}

// writeTarget resolves a path the way the confined write tools do. Unlike
// readTarget, a failure here IS meaningful: scope.Resolve only fails when the
// write was aimed outside the workspace, and saying so is the honest summary
// even though the tool will refuse the call on its own.
func (d *fsDeps) writeTarget(ctx context.Context, path string) (string, effect.Scope, string) {
	ws, _ := d.resolveWorkspace(ctx)
	abs, err := scope.Resolve(ws, path)
	if err != nil {
		return path, effect.ScopeExternal, "outside the workspace: " + err.Error()
	}
	return abs, effect.ScopeWorkspace, ""
}

// --- derivers ---

func (d *fsDeps) readEffect(field string) effect.Deriver {
	return func(ctx context.Context, argsJSON string) (effect.Effect, error) {
		args, err := decodeArgs(argsJSON)
		if err != nil {
			return effect.Effect{}, err
		}
		abs, sc, note := d.readTarget(ctx, stringField(args, field))
		return effect.Effect{
			Kind:  effect.KindFileRead,
			Scope: sc,
			Path:  abs,
			Note:  note,
		}, nil
	}
}

func (d *fsDeps) writeEffect(field string) effect.Deriver {
	return func(ctx context.Context, argsJSON string) (effect.Effect, error) {
		args, err := decodeArgs(argsJSON)
		if err != nil {
			return effect.Effect{}, err
		}
		abs, sc, note := d.writeTarget(ctx, stringField(args, field))
		return effect.Effect{
			Kind:  effect.KindFileWrite,
			Scope: sc,
			Path:  abs,
			Note:  note,
		}, nil
	}
}

func (d *fsDeps) structureEffect(field string) effect.Deriver {
	return func(ctx context.Context, argsJSON string) (effect.Effect, error) {
		args, err := decodeArgs(argsJSON)
		if err != nil {
			return effect.Effect{}, err
		}
		abs, sc, note := d.writeTarget(ctx, stringField(args, field))
		return effect.Effect{
			Kind:  effect.KindFileStructure,
			Scope: sc,
			Path:  abs,
			Note:  note,
		}, nil
	}
}

// chunkedWriteEffect splits the chunked write protocol by mode. Only start
// names a target file and only start needs approval; append / finish / abort
// carry a session id and continue work the user already agreed to, so gating
// them again would put a prompt in front of every chunk of a long file.
func (d *fsDeps) chunkedWriteEffect() effect.Deriver {
	return func(ctx context.Context, argsJSON string) (effect.Effect, error) {
		var in ChunkedWriteInput
		if err := unmarshalArgs(argsJSON, &in); err != nil {
			return effect.Effect{}, err
		}
		// Resolve the mode the same way the tool does rather than comparing
		// the raw field. The tool trims and lower-cases it, and infers it from
		// the argument shape when it is missing, so a literal comparison here
		// would wave through " Start" — or a mode-less call carrying a path —
		// as a session continuation and then execute it as a real start.
		//
		// A mode that will not resolve is reported as harmless because the
		// tool rejects that same input before it touches the filesystem;
		// asking the user to approve a call that cannot write is just noise.
		mode, err := resolveChunkedMode(&in)
		if err != nil || mode != "start" {
			return effect.Effect{
				Kind: effect.KindFileStructure,
				Note: "continues a write session already approved at mode=start",
			}, nil
		}
		abs, sc, note := d.writeTarget(ctx, in.Path)
		return effect.Effect{
			Kind:  effect.KindFileWrite,
			Scope: sc,
			Path:  abs,
			Note:  note,
		}, nil
	}
}

// createWorkspaceEffect describes the bootstrap call. It only ever creates a
// fresh directory under the managed workspace root, and it is the call every
// other filesystem tool is waiting on — prompting for it would put an
// approval in front of the first useful thing a new conversation does.
func createWorkspaceEffect(_ context.Context, argsJSON string) (effect.Effect, error) {
	var in CreateWorkspaceInput
	if err := unmarshalArgs(argsJSON, &in); err != nil {
		return effect.Effect{}, err
	}
	return effect.Effect{
		Kind:  effect.KindFileStructure,
		Scope: effect.ScopeWorkspace,
		Path:  in.Slug,
	}, nil
}

// rmEffect marks a delete irreversible on the same terms as the shell wall:
// a recursive remove, or one aimed at a well-known dangerous path. A plain
// single-file delete stays ordinary, so full_access users don't get a prompt
// per file — it still needs approval in the other two modes.
func (d *fsDeps) rmEffect() effect.Deriver {
	return func(ctx context.Context, argsJSON string) (effect.Effect, error) {
		var in RmInput
		if err := unmarshalArgs(argsJSON, &in); err != nil {
			return effect.Effect{}, err
		}
		// rm resolves through ResolveRead, so an absolute path reaches
		// anywhere on the machine.
		abs, sc, note := d.readTarget(ctx, in.Path)
		// Check the raw argument as well as the resolved path: `~/x` and
		// `$HOME/x` are dangerous in the form the agent wrote them, and
		// resolution erases that.
		dangerous := approval.IsDangerousPath(in.Path) || approval.IsDangerousPath(abs)
		return effect.Effect{
			Kind:        effect.KindFileWrite,
			Scope:       sc,
			Path:        abs,
			Destructive: in.Recursive || dangerous,
			Note:        note,
		}, nil
	}
}

// transferEffect covers mv and cp. Both resolve each side independently
// through ResolveRead, so either end can sit outside the workspace, and the
// approval card needs to say WHICH end — `cp ./notes.md /tmp` and
// `cp /tmp/data.csv ./` have opposite risk profiles and a single worst-case
// scope collapses them into the same card.
func (d *fsDeps) transferEffect(deletesSource bool) effect.Deriver {
	return func(ctx context.Context, argsJSON string) (effect.Effect, error) {
		var in struct {
			Src string `json:"src"`
			Dst string `json:"dst"`
		}
		if err := unmarshalArgs(argsJSON, &in); err != nil {
			return effect.Effect{}, err
		}
		srcAbs, srcScope, srcNote := d.readTarget(ctx, in.Src)
		dstAbs, dstScope, dstNote := d.readTarget(ctx, in.Dst)
		return effect.Effect{
			Kind:      effect.KindFileTransfer,
			Scope:     effect.WorstScope(srcScope, dstScope),
			Path:      srcAbs,
			PathScope: srcScope,
			DestPath:  dstAbs,
			DestScope: dstScope,
			// mv unlinks the source, but a move is recoverable by moving it
			// back. Only genuinely unrecoverable operations get the flag that
			// overrides full_access.
			Note: joinNotes(srcNote, dstNote),
		}, nil
	}
}

func (d *fsDeps) runCommandEffect() effect.Deriver {
	return func(ctx context.Context, argsJSON string) (effect.Effect, error) {
		var in RunCommandInput
		if err := unmarshalArgs(argsJSON, &in); err != nil {
			return effect.Effect{}, err
		}
		class := approval.ClassifyShellCommand(in.Command)
		// run_command confines its cwd to the workspace, so the command runs
		// inside it by construction. The command body can still reach out
		// with an absolute path, which is what Classification is for.
		cwd, cwdScope, note := d.writeTarget(ctx, in.Cwd)
		if in.Cwd == "" {
			cwd, cwdScope, note = "", effect.ScopeWorkspace, ""
		}
		return effect.Effect{
			Kind:           effect.KindProcessExec,
			Scope:          cwdScope,
			Command:        in.Command,
			Cwd:            cwd,
			Classification: class,
			Destructive:    class == effect.Destructive,
			Note:           note,
		}, nil
	}
}

// browserEffect describes driving a browser. It is process-exec rather than
// network-request because the agent is operating a live session — cookies,
// logins and all — not fetching a URL.
func browserEffect(tool string) effect.Deriver {
	return func(_ context.Context, argsJSON string) (effect.Effect, error) {
		var in struct {
			Action string `json:"action"`
			URL    string `json:"url"`
			Script string `json:"script"`
		}
		if err := unmarshalArgs(argsJSON, &in); err != nil {
			return effect.Effect{}, err
		}
		cmd := in.Action
		if cmd == "" {
			cmd = tool
		}
		if in.Script != "" {
			cmd += ": " + in.Script
		}
		return effect.Effect{
			Kind:           effect.KindProcessExec,
			Classification: effect.Normal,
			Command:        cmd,
			URL:            in.URL,
		}, nil
	}
}

func webSearchEffect(_ context.Context, argsJSON string) (effect.Effect, error) {
	var in struct {
		Query string `json:"query"`
	}
	if err := unmarshalArgs(argsJSON, &in); err != nil {
		return effect.Effect{}, err
	}
	return effect.Effect{Kind: effect.KindNetwork, Note: in.Query}, nil
}

func webFetchEffect(_ context.Context, argsJSON string) (effect.Effect, error) {
	var in struct {
		URL string `json:"url"`
	}
	if err := unmarshalArgs(argsJSON, &in); err != nil {
		return effect.Effect{}, err
	}
	return effect.Effect{Kind: effect.KindNetwork, URL: in.URL}, nil
}

func loadSkillEffect(_ context.Context, argsJSON string) (effect.Effect, error) {
	var in struct {
		Name string `json:"name"`
	}
	if err := unmarshalArgs(argsJSON, &in); err != nil {
		return effect.Effect{}, err
	}
	return effect.Effect{Kind: effect.KindSkillLoad, Note: in.Name}, nil
}

// --- argument decoding ---

// unmarshalArgs fills a typed struct. Empty arguments are not an error: the
// resulting zero value derives an effect with empty paths, which the policy
// still handles, whereas failing here would push a well-formed call into
// KindUnknown.
func unmarshalArgs(argsJSON string, out any) error {
	if strings.TrimSpace(argsJSON) == "" {
		return nil
	}
	if err := json.Unmarshal([]byte(argsJSON), out); err != nil {
		return fmt.Errorf("unparseable arguments: %w", err)
	}
	return nil
}

// decodeArgs is the untyped form, for the derivers that only need one named
// string out of the payload.
func decodeArgs(argsJSON string) (map[string]any, error) {
	m := map[string]any{}
	if err := unmarshalArgs(argsJSON, &m); err != nil {
		return nil, err
	}
	return m, nil
}

func stringField(m map[string]any, key string) string {
	s, _ := m[key].(string)
	return s
}

func joinNotes(parts ...string) string {
	var kept []string
	for _, p := range parts {
		if p != "" {
			kept = append(kept, p)
		}
	}
	return strings.Join(kept, "; ")
}
