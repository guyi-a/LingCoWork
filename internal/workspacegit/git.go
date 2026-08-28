package workspacegit

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

var (
	ErrNotRepository     = errors.New("workspace is not a Git repository")
	ErrNotRepositoryRoot = errors.New("workspace must be the Git repository root")
)

const commandTimeout = 15 * time.Second

type StatusEntry struct {
	IndexStatus byte
	WorkStatus  byte
	Path        string
	OldPath     string
}

type NumStatEntry struct {
	Path      string
	OldPath   string
	Additions int
	Deletions int
	Binary    bool
}

func RepositoryRoot(ctx context.Context, workspace string) (string, error) {
	stdout, _, err := Run(ctx, workspace, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", ErrNotRepository
	}
	root := strings.TrimSpace(string(stdout))
	if root == "" {
		return "", ErrNotRepository
	}
	rootCanonical, err := filepath.EvalSymlinks(filepath.Clean(root))
	if err != nil {
		return "", fmt.Errorf("resolve Git root: %w", err)
	}
	workspaceCanonical, err := filepath.EvalSymlinks(filepath.Clean(workspace))
	if err != nil {
		return "", fmt.Errorf("resolve workspace: %w", err)
	}
	if rootCanonical != workspaceCanonical {
		return "", ErrNotRepositoryRoot
	}
	return rootCanonical, nil
}

func Status(ctx context.Context, workspace string) ([]StatusEntry, error) {
	root, err := RepositoryRoot(ctx, workspace)
	if err != nil {
		return nil, err
	}
	stdout, stderr, err := Run(
		ctx,
		root,
		"-c", "core.quotepath=false",
		"status", "--porcelain=v1", "-z", "--untracked-files=all",
	)
	if err != nil {
		return nil, fmt.Errorf("git status: %w: %s", err, strings.TrimSpace(string(stderr)))
	}
	return parseStatus(stdout)
}

func HeadFile(ctx context.Context, workspace, relPath string) ([]byte, bool, error) {
	root, err := RepositoryRoot(ctx, workspace)
	if err != nil {
		return nil, false, err
	}
	spec := "HEAD:" + filepath.ToSlash(relPath)
	stdout, _, err := Run(ctx, root, "show", "--no-textconv", spec)
	if err != nil {
		return nil, false, nil
	}
	return stdout, true, nil
}

func Head(ctx context.Context, workspace string) (string, bool, error) {
	root, err := RepositoryRoot(ctx, workspace)
	if err != nil {
		return "", false, err
	}
	stdout, _, err := Run(ctx, root, "rev-parse", "--verify", "HEAD")
	if err != nil {
		return "", false, nil
	}
	return strings.TrimSpace(string(stdout)), true, nil
}

func NumStat(ctx context.Context, workspace string) ([]NumStatEntry, error) {
	root, err := RepositoryRoot(ctx, workspace)
	if err != nil {
		return nil, err
	}
	if _, exists, err := Head(ctx, root); err != nil || !exists {
		return nil, err
	}
	stdout, stderr, err := Run(
		ctx,
		root,
		"-c", "core.quotepath=false",
		"diff", "--numstat", "-z", "--find-renames", "HEAD", "--",
	)
	if err != nil {
		return nil, fmt.Errorf("git diff --numstat: %w: %s", err, strings.TrimSpace(string(stderr)))
	}
	return parseNumStat(stdout)
}

func Diff(ctx context.Context, workspace, relPath string) ([]byte, error) {
	root, err := RepositoryRoot(ctx, workspace)
	if err != nil {
		return nil, err
	}
	stdout, stderr, err := Run(
		ctx,
		root,
		"-c", "core.quotepath=false",
		"diff", "--no-ext-diff", "--find-renames", "--unified=3",
		"HEAD", "--", filepath.ToSlash(relPath),
	)
	if err != nil {
		return nil, fmt.Errorf("git diff: %w: %s", err, strings.TrimSpace(string(stderr)))
	}
	return stdout, nil
}

func NoIndexDiff(
	ctx context.Context,
	relPath string,
	oldContent, newContent []byte,
	oldExists, newExists bool,
) ([]byte, error) {
	dir, err := os.MkdirTemp("", "lingcowork-diff-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	oldPath := filepath.Join(dir, "old")
	newPath := filepath.Join(dir, "new")
	if err := os.WriteFile(oldPath, oldContent, 0o600); err != nil {
		return nil, err
	}
	if err := os.WriteFile(newPath, newContent, 0o600); err != nil {
		return nil, err
	}
	stdout, stderr, runErr := Run(
		ctx,
		dir,
		"diff", "--no-index", "--no-ext-diff", "--unified=3", "--", oldPath, newPath,
	)
	if runErr != nil {
		var exitErr *exec.ExitError
		if !errors.As(runErr, &exitErr) || exitErr.ExitCode() != 1 {
			return nil, fmt.Errorf("git diff --no-index: %w: %s", runErr, strings.TrimSpace(string(stderr)))
		}
	}
	text := string(stdout)
	hunk := strings.Index(text, "@@")
	if hunk < 0 {
		return nil, nil
	}
	oldLabel := "a/" + filepath.ToSlash(relPath)
	newLabel := "b/" + filepath.ToSlash(relPath)
	if !oldExists {
		oldLabel = "/dev/null"
	}
	if !newExists {
		newLabel = "/dev/null"
	}
	return []byte("--- " + oldLabel + "\n+++ " + newLabel + "\n" + text[hunk:]), nil
}

func Run(ctx context.Context, cwd string, args ...string) (stdout, stderr []byte, err error) {
	commandCtx, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()
	cmd := exec.CommandContext(commandCtx, "git", args...)
	cmd.Dir = cwd
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	err = cmd.Run()
	if commandCtx.Err() != nil {
		err = commandCtx.Err()
	}
	return outBuf.Bytes(), errBuf.Bytes(), err
}

func parseStatus(raw []byte) ([]StatusEntry, error) {
	records := bytes.Split(raw, []byte{0})
	out := make([]StatusEntry, 0, len(records))
	for i := 0; i < len(records); i++ {
		record := records[i]
		if len(record) == 0 {
			continue
		}
		if len(record) < 4 || record[2] != ' ' {
			return nil, fmt.Errorf("unexpected git status record %q", record)
		}
		entry := StatusEntry{
			IndexStatus: record[0],
			WorkStatus:  record[1],
			Path:        filepath.ToSlash(string(record[3:])),
		}
		if entry.IndexStatus == 'R' || entry.IndexStatus == 'C' ||
			entry.WorkStatus == 'R' || entry.WorkStatus == 'C' {
			i++
			if i >= len(records) || len(records[i]) == 0 {
				return nil, fmt.Errorf("rename status missing source path")
			}
			entry.OldPath = filepath.ToSlash(string(records[i]))
		}
		out = append(out, entry)
	}
	return out, nil
}

func parseNumStat(raw []byte) ([]NumStatEntry, error) {
	var out []NumStatEntry
	for len(raw) > 0 {
		tab1 := bytes.IndexByte(raw, '\t')
		if tab1 < 0 {
			return nil, fmt.Errorf("invalid numstat additions field")
		}
		tab2Rel := bytes.IndexByte(raw[tab1+1:], '\t')
		if tab2Rel < 0 {
			return nil, fmt.Errorf("invalid numstat deletions field")
		}
		tab2 := tab1 + 1 + tab2Rel
		addText := string(raw[:tab1])
		delText := string(raw[tab1+1 : tab2])
		raw = raw[tab2+1:]
		entry := NumStatEntry{}
		if addText == "-" || delText == "-" {
			entry.Binary = true
		} else {
			if _, err := fmt.Sscanf(addText, "%d", &entry.Additions); err != nil {
				return nil, fmt.Errorf("invalid numstat additions %q", addText)
			}
			if _, err := fmt.Sscanf(delText, "%d", &entry.Deletions); err != nil {
				return nil, fmt.Errorf("invalid numstat deletions %q", delText)
			}
		}
		if len(raw) == 0 {
			return nil, fmt.Errorf("numstat path missing")
		}
		if raw[0] == 0 {
			raw = raw[1:]
			oldEnd := bytes.IndexByte(raw, 0)
			if oldEnd < 0 {
				return nil, fmt.Errorf("numstat rename source missing terminator")
			}
			entry.OldPath = filepath.ToSlash(string(raw[:oldEnd]))
			raw = raw[oldEnd+1:]
			newEnd := bytes.IndexByte(raw, 0)
			if newEnd < 0 {
				return nil, fmt.Errorf("numstat rename destination missing terminator")
			}
			entry.Path = filepath.ToSlash(string(raw[:newEnd]))
			raw = raw[newEnd+1:]
		} else {
			pathEnd := bytes.IndexByte(raw, 0)
			if pathEnd < 0 {
				return nil, fmt.Errorf("numstat path missing terminator")
			}
			entry.Path = filepath.ToSlash(string(raw[:pathEnd]))
			raw = raw[pathEnd+1:]
		}
		out = append(out, entry)
	}
	return out, nil
}
