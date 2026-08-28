package tools

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"

	"github.com/guyi-a/Interview-Agent/internal/agent/scope"
)

// --- rm ---

type RmInput struct {
	Path      string `json:"path" jsonschema:"description=Path to delete. Must be workspace-relative or an absolute path inside the selected workspace."`
	Recursive bool   `json:"recursive" jsonschema:"description=If true\\, recursively delete a directory and all its contents (rm -rf). If false (default)\\, only delete a file or an EMPTY directory."`
}

type RmOutput struct {
	Path    string `json:"path"`
	Removed string `json:"removed"` // "file" | "directory"
}

func newRmTool(d *fsDeps) (tool.BaseTool, error) {
	fn := func(ctx context.Context, in *RmInput) (*RmOutput, error) {
		if in.Path == "" {
			return nil, fmt.Errorf("path is required")
		}
		ws, err := d.resolveWorkspace(ctx)
		if err != nil {
			return nil, err
		}
		abs, err := scope.Resolve(ws, in.Path)
		if err != nil {
			return nil, err
		}
		if ws != "" && abs == strings.TrimSuffix(ws, string(filepath.Separator)) {
			return nil, fmt.Errorf("refusing to delete the workspace root")
		}
		st, err := os.Lstat(abs)
		if err != nil {
			if os.IsNotExist(err) {
				return nil, fmt.Errorf("path %q does not exist", in.Path)
			}
			return nil, fmt.Errorf("stat: %w", err)
		}
		kind := "file"
		if st.IsDir() {
			kind = "directory"
			if in.Recursive {
				if err := os.RemoveAll(abs); err != nil {
					return nil, fmt.Errorf("remove all: %w", err)
				}
			} else {
				if err := os.Remove(abs); err != nil {
					if isNotEmpty(err) {
						return nil, fmt.Errorf("directory %q is not empty; pass recursive=true to delete it and all its contents", in.Path)
					}
					return nil, fmt.Errorf("remove dir: %w", err)
				}
			}
		} else {
			if err := os.Remove(abs); err != nil {
				return nil, fmt.Errorf("remove file: %w", err)
			}
		}
		return &RmOutput{Path: abs, Removed: kind}, nil
	}
	return utils.InferTool(
		"rm",
		"Delete a file or directory inside the selected workspace. By default only deletes files or empty directories — pass recursive=true to delete a directory and all its contents (rm -rf semantics). Refuses paths outside the workspace, symlink escapes, and deletion of the workspace root. There is no trash / undo.",
		fn,
	)
}

// --- mv ---

type MvInput struct {
	Src string `json:"src" jsonschema:"description=Source path inside the selected workspace. Source is deleted after the move."`
	Dst string `json:"dst" jsonschema:"description=Destination path inside the selected workspace. If dst is an existing directory\\, src is moved INTO it (as dst/basename(src)). If dst does not exist\\, src is renamed to dst (parent directory is created if missing). If dst is an existing file\\, the call is REJECTED — delete dst first."`
}

type MvOutput struct {
	Src string `json:"src"`
	Dst string `json:"dst"`
}

func newMvTool(d *fsDeps) (tool.BaseTool, error) {
	fn := func(ctx context.Context, in *MvInput) (*MvOutput, error) {
		if in.Src == "" || in.Dst == "" {
			return nil, fmt.Errorf("src and dst are required")
		}
		ws, err := d.resolveWorkspace(ctx)
		if err != nil {
			return nil, err
		}
		srcAbs, err := scope.Resolve(ws, in.Src)
		if err != nil {
			return nil, fmt.Errorf("src: %w", err)
		}
		dstAbs, err := scope.Resolve(ws, in.Dst)
		if err != nil {
			return nil, fmt.Errorf("dst: %w", err)
		}
		if ws != "" {
			wsClean := strings.TrimSuffix(ws, string(filepath.Separator))
			if srcAbs == wsClean {
				return nil, fmt.Errorf("refusing to move the workspace root")
			}
		}
		if _, err := os.Lstat(srcAbs); err != nil {
			if os.IsNotExist(err) {
				return nil, fmt.Errorf("src %q does not exist", in.Src)
			}
			return nil, fmt.Errorf("stat src: %w", err)
		}

		finalDst := dstAbs
		if st, err := os.Lstat(dstAbs); err == nil {
			if st.IsDir() {
				finalDst = filepath.Join(dstAbs, filepath.Base(srcAbs))
				finalDst, err = scope.Resolve(ws, finalDst)
				if err != nil {
					return nil, fmt.Errorf("dst: %w", err)
				}
				if _, err := os.Lstat(finalDst); err == nil {
					return nil, fmt.Errorf("target %q already exists in destination directory; delete it first", filepath.Base(srcAbs))
				}
			} else {
				return nil, fmt.Errorf("dst %q already exists; delete it first (mv refuses to overwrite)", in.Dst)
			}
		} else if !os.IsNotExist(err) {
			return nil, fmt.Errorf("stat dst: %w", err)
		} else {
			if err := os.MkdirAll(filepath.Dir(finalDst), 0o755); err != nil {
				return nil, fmt.Errorf("mkdir parent: %w", err)
			}
		}

		if err := os.Rename(srcAbs, finalDst); err != nil {
			if isCrossDevice(err) {
				if err := copyPath(srcAbs, finalDst); err != nil {
					return nil, fmt.Errorf("cross-device copy: %w", err)
				}
				if err := os.RemoveAll(srcAbs); err != nil {
					return nil, fmt.Errorf("cross-device remove source: %w", err)
				}
			} else {
				return nil, fmt.Errorf("rename: %w", err)
			}
		}
		return &MvOutput{Src: srcAbs, Dst: finalDst}, nil
	}
	return utils.InferTool(
		"mv",
		"Move or rename a file or directory inside the selected workspace. If dst is an existing directory, src is moved into it. If dst does not exist, src is renamed to dst (parent dir is auto-created). Refuses paths outside the workspace, symlink escapes, overwrites, and moving the workspace root.",
		fn,
	)
}

// --- cp ---

type CpInput struct {
	Src string `json:"src" jsonschema:"description=Source path. Absolute local path (anywhere on the user's machine) or workspace-relative. Copied read-only\\, source is untouched."`
	Dst string `json:"dst" jsonschema:"description=Destination path inside the selected workspace. If dst is an existing directory\\, src is copied INTO it (as dst/basename(src)). If dst does not exist\\, src is copied to dst (parent directory is created if missing). If dst is an existing file\\, the call is REJECTED — delete dst first."`
}

type CpOutput struct {
	Src   string `json:"src"`
	Dst   string `json:"dst"`
	IsDir bool   `json:"is_dir"`
}

func newCpTool(d *fsDeps) (tool.BaseTool, error) {
	fn := func(ctx context.Context, in *CpInput) (*CpOutput, error) {
		if in.Src == "" || in.Dst == "" {
			return nil, fmt.Errorf("src and dst are required")
		}
		ws, err := d.resolveWorkspace(ctx)
		if err != nil {
			return nil, err
		}
		srcAbs, err := scope.ResolveRead(ws, in.Src)
		if err != nil {
			return nil, fmt.Errorf("src: %w", err)
		}
		dstAbs, err := scope.Resolve(ws, in.Dst)
		if err != nil {
			return nil, fmt.Errorf("dst: %w", err)
		}
		srcSt, err := os.Lstat(srcAbs)
		if err != nil {
			if os.IsNotExist(err) {
				return nil, fmt.Errorf("src %q does not exist", in.Src)
			}
			return nil, fmt.Errorf("stat src: %w", err)
		}

		finalDst := dstAbs
		if st, err := os.Lstat(dstAbs); err == nil {
			if st.IsDir() {
				finalDst = filepath.Join(dstAbs, filepath.Base(srcAbs))
				finalDst, err = scope.Resolve(ws, finalDst)
				if err != nil {
					return nil, fmt.Errorf("dst: %w", err)
				}
				if _, err := os.Lstat(finalDst); err == nil {
					return nil, fmt.Errorf("target %q already exists in destination directory; delete it first", filepath.Base(srcAbs))
				}
			} else {
				return nil, fmt.Errorf("dst %q already exists; delete it first (cp refuses to overwrite)", in.Dst)
			}
		} else if !os.IsNotExist(err) {
			return nil, fmt.Errorf("stat dst: %w", err)
		} else {
			if err := os.MkdirAll(filepath.Dir(finalDst), 0o755); err != nil {
				return nil, fmt.Errorf("mkdir parent: %w", err)
			}
		}

		if err := copyPath(srcAbs, finalDst); err != nil {
			return nil, fmt.Errorf("copy: %w", err)
		}
		return &CpOutput{Src: srcAbs, Dst: finalDst, IsDir: srcSt.IsDir()}, nil
	}
	return utils.InferTool(
		"cp",
		"Copy a file or directory into the selected workspace. The read-only source may be an absolute local path, but the destination must remain inside the workspace and cannot escape through symlinks. Directories are copied recursively. Refuses to overwrite an existing target.",
		fn,
	)
}

// --- helpers ---

// copyPath copies src to dst. If src is a directory, it recursively copies
// the entire tree. Parent of dst is expected to exist.
func copyPath(src, dst string) error {
	st, err := os.Lstat(src)
	if err != nil {
		return err
	}
	if st.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(src)
		if err != nil {
			return err
		}
		return os.Symlink(target, dst)
	}
	if st.IsDir() {
		return copyDir(src, dst, st.Mode())
	}
	return copyFile(src, dst, st.Mode())
}

func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode.Perm())
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

func copyDir(src, dst string, mode os.FileMode) error {
	if err := os.MkdirAll(dst, mode.Perm()); err != nil {
		return err
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, e := range entries {
		s := filepath.Join(src, e.Name())
		d := filepath.Join(dst, e.Name())
		if err := copyPath(s, d); err != nil {
			return err
		}
	}
	return nil
}

// isNotEmpty checks for "directory not empty" across platforms.
func isNotEmpty(err error) bool {
	var errno syscall.Errno
	if errors.As(err, &errno) {
		return errno == syscall.ENOTEMPTY
	}
	return false
}

// isCrossDevice checks for EXDEV (Rename across filesystems).
func isCrossDevice(err error) bool {
	var errno syscall.Errno
	if errors.As(err, &errno) {
		return errno == syscall.EXDEV
	}
	return false
}
