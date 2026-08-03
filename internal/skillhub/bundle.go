package skillhub

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"time"
)

// A skill bundle is a handful of text files. These caps keep a hostile or
// corrupt bundle — an oversized download, a zip bomb, thousands of entries —
// from exhausting memory/disk during download and decompression. Generous
// for real skills, they only trip on clearly abnormal input. Values match
// klingwork's.
const (
	maxBundleBytes            = 8 << 20
	maxUncompressedFileBytes  = 4 << 20
	maxUncompressedTotalBytes = 32 << 20
	maxBundleFiles            = 400

	downloadTimeout = 60 * time.Second
)

// DownloadBundle fetches a skill's ZIP from
// /api/skills/<slug>/download?version=. The declared content-length is
// checked as a cheap early bail, but it can be absent or lie, so the real
// byte count is what's enforced.
func (r *Registry) DownloadBundle(ctx context.Context, fullSlug, version string) ([]byte, error) {
	path, err := slugPath(fullSlug, "/download")
	if err != nil {
		return nil, err
	}
	u := r.base + path
	if version != "" {
		u += "?version=" + url.QueryEscape(version)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	client := &http.Client{Timeout: downloadTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("注册中心不可达: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		var failure struct {
			Error *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&failure) == nil &&
			failure.Error != nil && failure.Error.Message != "" {
			return nil, errors.New(failure.Error.Message)
		}
		return nil, fmt.Errorf("下载失败: HTTP %d", resp.StatusCode)
	}
	if resp.ContentLength > maxBundleBytes {
		return nil, errors.New("技能包超过大小上限")
	}
	// Read one byte past the cap so an oversized body is detected without
	// buffering all of it.
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBundleBytes+1))
	if err != nil {
		return nil, fmt.Errorf("下载技能包失败: %w", err)
	}
	if len(data) > maxBundleBytes {
		return nil, errors.New("技能包超过大小上限")
	}
	return data, nil
}

// unzipBounded decompresses the ZIP with hard limits on entry count and both
// per-file and total uncompressed size, failing the whole install the moment
// any cap is exceeded rather than materializing an unbounded amount of data.
//
// Directory entries are dropped (paths are recreated from file names). Any
// entry that would escape the extraction root (zip-slip) condemns the whole
// bundle — silently skipping the bad entry and installing the rest would
// mean trusting a package that already tried to attack us. Two names that
// collide case-insensitively are also rejected: on the case-insensitive
// filesystems macOS ships with, the second write would silently clobber the
// first.
func unzipBounded(bundle []byte) (map[string][]byte, error) {
	zr, err := zip.NewReader(bytes.NewReader(bundle), int64(len(bundle)))
	if err != nil {
		return nil, errors.New("技能包不是有效的 ZIP")
	}
	if len(zr.File) > maxBundleFiles {
		return nil, errors.New("技能包内文件数超过上限")
	}

	files := map[string][]byte{}
	lowered := map[string]string{}
	var total int64
	for _, f := range zr.File {
		name := strings.ReplaceAll(f.Name, `\`, "/")
		if strings.HasSuffix(name, "/") {
			continue
		}
		// filepath.IsLocal refuses absolute paths, drive letters, and any
		// ".." escape in one call — the exact zip-slip conditions.
		if !filepath.IsLocal(filepath.FromSlash(name)) {
			return nil, fmt.Errorf("技能包内路径 %q 试图逃出安装目录，整包拒绝", f.Name)
		}
		if prev, clash := lowered[strings.ToLower(name)]; clash && prev != name {
			return nil, fmt.Errorf("技能包内 %q 与 %q 仅大小写不同，在大小写不敏感的文件系统上会互相覆盖", name, prev)
		}
		lowered[strings.ToLower(name)] = name

		if f.UncompressedSize64 > maxUncompressedFileBytes {
			return nil, fmt.Errorf("技能包内文件 %q 超过单文件大小上限", name)
		}
		rc, err := f.Open()
		if err != nil {
			return nil, errors.New("技能包不是有效的 ZIP")
		}
		// The zip header's size field can under-report; cap the actual read.
		content, err := io.ReadAll(io.LimitReader(rc, maxUncompressedFileBytes+1))
		rc.Close()
		if err != nil {
			return nil, errors.New("技能包解压失败")
		}
		if len(content) > maxUncompressedFileBytes {
			return nil, fmt.Errorf("技能包内文件 %q 超过单文件大小上限", name)
		}
		total += int64(len(content))
		if total > maxUncompressedTotalBytes {
			return nil, errors.New("技能包解压后总大小超过上限")
		}
		files[name] = content
	}
	return files, nil
}
