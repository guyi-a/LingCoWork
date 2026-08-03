// Package skillhub talks to the internal kskill registry and installs the
// skill bundles it serves into the user skills directory
// (.lingcowork/skills/), where the skills loader discovers them on its next
// refresh — i.e. the next agent run.
//
// This is a Go port of klingwork-app's kskill integration
// (packages/integrations/src/kskill + app-skill-hub-ipc.ts), same endpoints,
// same envelope, same safety caps.
package skillhub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// DefaultRegistryURL is deliberately still http:// — the internal skills
// registry does not terminate TLS yet (klingwork ships the same default and
// the same TODO).
const DefaultRegistryURL = "http://skills.yyyyy.tech"

const requestTimeout = 10 * time.Second

// fullSlugPattern accepts "slug" or "@scope/slug", matching the kskill CLI's
// apiSegment rule. It's the choke point against path injection: a fullSlug
// is spliced into request paths, marker files, and uninstall lookups.
var fullSlugPattern = regexp.MustCompile(`^(@[a-z0-9][a-z0-9-]*/)?[a-z0-9][a-z0-9-]*$`)

// ValidFullSlug reports whether s is a well-formed registry slug.
func ValidFullSlug(s string) bool { return fullSlugPattern.MatchString(s) }

// RegistryBase resolves the registry URL: KSKILL_REGISTRY_URL (the kskill
// CLI convention allows a comma-separated list — take the first) or the
// internal default.
func RegistryBase() string {
	raw := strings.TrimSpace(os.Getenv("KSKILL_REGISTRY_URL"))
	if raw != "" {
		first := strings.TrimSpace(strings.Split(raw, ",")[0])
		if first != "" {
			return strings.TrimRight(first, "/")
		}
	}
	return DefaultRegistryURL
}

// AuthorProfile 是列表响应里 authorProfiles 映射的值：owner 用户名对应的
// 展示信息。
type AuthorProfile struct {
	Username    string `json:"username"`
	DisplayName string `json:"displayName,omitempty"`
	AvatarURL   string `json:"avatarUrl,omitempty"`
}

// Skill is one catalog entry. Optional fields come back as JSON null in
// practice (owner, latestVersion, …); Go's decoder treats null as a no-op so
// they simply keep their zero values — no pointer gymnastics needed.
type Skill struct {
	ID            string   `json:"id"`
	Scope         string   `json:"scope,omitempty"`
	Slug          string   `json:"slug"`
	FullSlug      string   `json:"fullSlug"`
	Name          string   `json:"name"`
	Description   string   `json:"description,omitempty"`
	Owner         string   `json:"owner,omitempty"`
	Tags          []string `json:"tags,omitempty"`
	IsTeam        bool     `json:"isTeam,omitempty"`
	IsEditorPick  bool     `json:"isEditorPick,omitempty"`
	Hotness       *Hotness `json:"hotness,omitempty"`
	LatestVersion string   `json:"latestVersion,omitempty"`
	UpdatedAt     string   `json:"updatedAt,omitempty"`
}

type Hotness struct {
	Installs  int `json:"installs,omitempty"`
	Downloads int `json:"downloads,omitempty"`
}

// Page is the /api/skills listing response.
type Page struct {
	Items          []Skill                  `json:"items"`
	AuthorProfiles map[string]AuthorProfile `json:"authorProfiles"`
	Total          int                      `json:"total"`
	Page           int                      `json:"page"`
	PageSize       int                      `json:"pageSize"`
}

// Files is the bundle's file listing (GET /api/skills/:slug/files).
type Files struct {
	Files   []string `json:"files"`
	Version string   `json:"version,omitempty"`
}

// Version is one published version (GET /api/skills/:slug/versions).
type Version struct {
	Version    string `json:"version"`
	Changelog  string `json:"changelog,omitempty"`
	BundleSize int    `json:"bundleSize,omitempty"`
	IsLatest   bool   `json:"isLatest,omitempty"`
	CreatedAt  string `json:"createdAt,omitempty"`
}

// Registry is the HTTP client for the kskill registry.
type Registry struct {
	base string
	http *http.Client
}

// NewRegistry builds a client; empty base means "resolve via RegistryBase".
func NewRegistry(base string) *Registry {
	if base == "" {
		base = RegistryBase()
	}
	return &Registry{
		base: strings.TrimRight(base, "/"),
		http: &http.Client{Timeout: requestTimeout},
	}
}

func (r *Registry) Base() string { return r.base }

// get unwraps the registry's {success, data, error} envelope into out.
func (r *Registry) get(ctx context.Context, path string, params url.Values, out any) error {
	u := r.base + path
	if len(params) > 0 {
		u += "?" + params.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	resp, err := r.http.Do(req)
	if err != nil {
		return fmt.Errorf("注册中心不可达: %w", err)
	}
	defer resp.Body.Close()

	var envelope struct {
		Success bool            `json:"success"`
		Data    json.RawMessage `json:"data"`
		Error   *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&envelope); err != nil {
		return fmt.Errorf("注册中心返回了无法解析的响应 (HTTP %d)", resp.StatusCode)
	}
	if resp.StatusCode != http.StatusOK || !envelope.Success || envelope.Data == nil {
		if envelope.Error != nil && envelope.Error.Message != "" {
			return errors.New(envelope.Error.Message)
		}
		return fmt.Errorf("注册中心错误: HTTP %d", resp.StatusCode)
	}
	if err := json.Unmarshal(envelope.Data, out); err != nil {
		return fmt.Errorf("注册中心响应格式不符: %w", err)
	}
	return nil
}

// slugPath builds "/api/skills/<fullSlug><suffix>" after validating the slug.
// A scoped slug legitimately contains one "/" — it goes into the path raw,
// which is exactly why the pattern check must happen first.
func slugPath(fullSlug, suffix string) (string, error) {
	if !ValidFullSlug(fullSlug) {
		return "", fmt.Errorf("非法的技能 slug: %q", fullSlug)
	}
	return "/api/skills/" + fullSlug + suffix, nil
}

// Catalog lists/searches skills. q may be empty.
func (r *Registry) Catalog(ctx context.Context, q string, page, pageSize int) (Page, error) {
	params := url.Values{}
	params.Set("page", strconv.Itoa(page))
	params.Set("pageSize", strconv.Itoa(pageSize))
	if q != "" {
		params.Set("q", q)
	}
	var out Page
	if err := r.get(ctx, "/api/skills", params, &out); err != nil {
		return Page{}, err
	}
	if out.AuthorProfiles == nil {
		out.AuthorProfiles = map[string]AuthorProfile{}
	}
	if out.Items == nil {
		out.Items = []Skill{}
	}
	return out, nil
}

// Readme fetches the raw README markdown.
func (r *Registry) Readme(ctx context.Context, fullSlug string) (string, error) {
	path, err := slugPath(fullSlug, "/readme")
	if err != nil {
		return "", err
	}
	var out struct {
		Content string `json:"content"`
	}
	if err := r.get(ctx, path, nil, &out); err != nil {
		return "", err
	}
	return out.Content, nil
}

// FileList fetches the bundle's file listing.
func (r *Registry) FileList(ctx context.Context, fullSlug string) (Files, error) {
	path, err := slugPath(fullSlug, "/files")
	if err != nil {
		return Files{}, err
	}
	var out Files
	if err := r.get(ctx, path, nil, &out); err != nil {
		return Files{}, err
	}
	if out.Files == nil {
		out.Files = []string{}
	}
	return out, nil
}

// FileContent fetches one file's text content. filePath rides in a query
// parameter (encoded), so it carries no path-injection risk here.
func (r *Registry) FileContent(ctx context.Context, fullSlug, filePath string) (string, error) {
	path, err := slugPath(fullSlug, "/content")
	if err != nil {
		return "", err
	}
	params := url.Values{}
	params.Set("path", filePath)
	var out struct {
		Content string `json:"content"`
	}
	if err := r.get(ctx, path, params, &out); err != nil {
		return "", err
	}
	return out.Content, nil
}

// Versions fetches the published version history.
func (r *Registry) Versions(ctx context.Context, fullSlug string) ([]Version, error) {
	path, err := slugPath(fullSlug, "/versions")
	if err != nil {
		return nil, err
	}
	var out []Version
	if err := r.get(ctx, path, nil, &out); err != nil {
		return nil, err
	}
	if out == nil {
		out = []Version{}
	}
	return out, nil
}
