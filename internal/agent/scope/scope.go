package scope

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Resolve validates a path that will be mutated. Both existing targets and
// the nearest existing parent of new targets are resolved through symlinks,
// so a lexical path inside the workspace cannot write through a symlink to
// somewhere outside it.
//
// Returns the cleaned absolute path on success.
func Resolve(workspaceRoot, userPath string) (string, error) {
	if workspaceRoot == "" {
		return "", fmt.Errorf("workspace root is empty")
	}
	if userPath == "" {
		return "", fmt.Errorf("path is empty")
	}
	root, err := canonicalRoot(workspaceRoot)
	if err != nil {
		return "", err
	}

	var target string
	if filepath.IsAbs(userPath) {
		target = userPath
	} else {
		target = filepath.Join(root, userPath)
	}
	target = filepath.Clean(target)
	if !isWithin(root, target) {
		return "", fmt.Errorf("path %q escapes workspace %q", userPath, root)
	}

	resolvedBoundary, err := resolveExistingBoundary(target)
	if err != nil {
		return "", fmt.Errorf("resolve path %q: %w", userPath, err)
	}
	if !isWithin(root, resolvedBoundary) {
		return "", fmt.Errorf("path %q escapes workspace %q through a symlink", userPath, root)
	}

	return target, nil
}

// ResolveRead is the read-side counterpart to Resolve.
//
// Semantics:
//   - Absolute userPath: cleaned and returned as-is. workspaceRoot is not
//     consulted; may be empty (relevant when the conversation has no
//     workspace bound but the caller wants to read a local file).
//   - Relative userPath: resolved against workspaceRoot. Existing symlinks are
//     evaluated so the effect layer can correctly classify a read that lands
//     outside the workspace and ask for approval.
//
// This is intentionally more permissive than Resolve on the absolute-path
// case: read tools trust the caller (single-user local machine), write tools
// keep the workspace fence.
func ResolveRead(workspaceRoot, userPath string) (string, error) {
	if userPath == "" {
		return "", fmt.Errorf("path is empty")
	}
	if filepath.IsAbs(userPath) {
		return filepath.Clean(userPath), nil
	}
	if workspaceRoot == "" {
		return "", fmt.Errorf("relative path %q requires a workspace; pass an absolute path or bind a workspace first", userPath)
	}
	root, err := canonicalRoot(workspaceRoot)
	if err != nil {
		return "", err
	}
	target := filepath.Clean(filepath.Join(root, userPath))
	if !isWithin(root, target) {
		return "", fmt.Errorf("path %q escapes workspace %q", userPath, root)
	}
	resolved, err := filepath.EvalSymlinks(target)
	if err == nil {
		return filepath.Clean(resolved), nil
	}
	if !os.IsNotExist(err) {
		return "", fmt.Errorf("resolve path %q: %w", userPath, err)
	}
	return target, nil
}

func canonicalRoot(workspaceRoot string) (string, error) {
	absolute, err := filepath.Abs(filepath.Clean(workspaceRoot))
	if err != nil {
		return "", fmt.Errorf("normalize workspace root: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", fmt.Errorf("resolve workspace root: %w", err)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", fmt.Errorf("inspect workspace root: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("workspace root %q is not a directory", resolved)
	}
	return filepath.Clean(resolved), nil
}

// resolveExistingBoundary resolves target itself when it exists. For a new
// target it resolves the closest parent that already exists, which is the
// part of the path where a symlink could redirect a subsequent mkdir/write.
func resolveExistingBoundary(target string) (string, error) {
	current := target
	for {
		if _, err := os.Lstat(current); err == nil {
			resolved, err := filepath.EvalSymlinks(current)
			if err != nil {
				return "", err
			}
			return filepath.Clean(resolved), nil
		} else if !os.IsNotExist(err) {
			return "", err
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", fmt.Errorf("no existing parent for %q", target)
		}
		current = parent
	}
}

func isWithin(root, target string) bool {
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
