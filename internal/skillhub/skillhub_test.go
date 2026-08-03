package skillhub

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func makeZip(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for name, content := range files {
		f, err := w.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

const goodSkillMD = `---
name: demo-skill
description: a demo skill from the registry
version: 1.2.0
---
follow these steps
`

// fakeRegistry serves the registry's {success,data,error} envelope plus a
// raw ZIP download endpoint.
func fakeRegistry(t *testing.T, bundle []byte) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	envelope := func(w http.ResponseWriter, data any) {
		blob, _ := json.Marshal(map[string]any{"success": true, "data": data})
		w.Header().Set("Content-Type", "application/json")
		w.Write(blob)
	}
	mux.HandleFunc("/api/skills", func(w http.ResponseWriter, r *http.Request) {
		envelope(w, map[string]any{
			"items": []map[string]any{{
				"id": "1", "slug": "demo-skill", "fullSlug": "@team/demo-skill",
				"name": "demo-skill", "description": "a demo skill",
				// 实测注册中心的可选字段会返回 null
				"owner": nil, "latestVersion": nil, "tags": nil,
			}},
			"authorProfiles": map[string]any{},
			"total":          1, "page": 1, "pageSize": 20,
		})
	})
	mux.HandleFunc("/api/skills/@team/demo-skill/readme", func(w http.ResponseWriter, r *http.Request) {
		envelope(w, map[string]any{"content": "# Demo"})
	})
	mux.HandleFunc("/api/skills/@team/demo-skill/versions", func(w http.ResponseWriter, r *http.Request) {
		envelope(w, []map[string]any{{"version": "1.2.0", "isLatest": true}})
	})
	mux.HandleFunc("/api/skills/@team/demo-skill/download", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/zip")
		w.Write(bundle)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"success":false,"error":{"message":"not found"}}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func newTestService(t *testing.T, bundle []byte, reserved ...string) *Service {
	t.Helper()
	srv := fakeRegistry(t, bundle)
	dir := filepath.Join(t.TempDir(), "skills")
	return NewService(NewRegistry(srv.URL), dir, func() []string { return reserved })
}

func TestCatalogNormalizesNulls(t *testing.T) {
	s := newTestService(t, nil)
	page, err := s.Registry.Catalog(context.Background(), "", 1, 20)
	if err != nil {
		t.Fatalf("Catalog: %v", err)
	}
	if len(page.Items) != 1 || page.Total != 1 {
		t.Fatalf("unexpected page: %+v", page)
	}
	item := page.Items[0]
	if item.Owner != "" || item.LatestVersion != "" || item.Tags != nil {
		t.Fatalf("null fields should decode to zero values: %+v", item)
	}
}

func TestRegistryErrorEnvelope(t *testing.T) {
	s := newTestService(t, nil)
	_, err := s.Registry.Readme(context.Background(), "no-such-skill")
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("want registry error message, got %v", err)
	}
}

func TestSlugValidationBlocksPathInjection(t *testing.T) {
	s := newTestService(t, nil)
	for _, slug := range []string{"../etc", "a/b/c", "@Bad/slug", "slug/", "", "@scope/../x"} {
		if _, err := s.Registry.Readme(context.Background(), slug); err == nil {
			t.Errorf("slug %q should be rejected", slug)
		}
	}
}

func TestInstallHappyPath(t *testing.T) {
	bundle := makeZip(t, map[string]string{
		"SKILL.md":       goodSkillMD,
		"REFERENCE.md":   "extra docs",
		"scripts/run.py": "print('hi')",
	})
	s := newTestService(t, bundle)

	installed, err := s.Install(context.Background(), "@team/demo-skill", "")
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if installed.Name != "demo-skill" {
		t.Fatalf("Name = %q, want demo-skill", installed.Name)
	}
	if installed.Version != "1.2.0" {
		t.Fatalf("Version = %q, want 1.2.0 (from frontmatter)", installed.Version)
	}
	for _, rel := range []string{"SKILL.md", "REFERENCE.md", "scripts/run.py", MarkerFile, ".registry"} {
		if _, err := os.Stat(filepath.Join(installed.Directory, rel)); err != nil {
			t.Errorf("missing %s after install: %v", rel, err)
		}
	}

	list := s.ListInstalled()
	if len(list) != 1 || list[0].FullSlug != "@team/demo-skill" || list[0].Version != "1.2.0" {
		t.Fatalf("ListInstalled = %+v", list)
	}

	// Reinstall over the existing copy must succeed (backup swap path).
	if _, err := s.Install(context.Background(), "@team/demo-skill", ""); err != nil {
		t.Fatalf("reinstall: %v", err)
	}
	// No staging or backup leftovers.
	entries, _ := os.ReadDir(s.SkillsDir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".staging-") || strings.HasPrefix(e.Name(), ".replace-old-") {
			t.Errorf("leftover temp dir %s", e.Name())
		}
	}

	if err := s.Uninstall("@team/demo-skill"); err != nil {
		t.Fatalf("Uninstall: %v", err)
	}
	if got := s.ListInstalled(); len(got) != 0 {
		t.Fatalf("still installed after uninstall: %+v", got)
	}
}

func TestInstallRejectsReservedName(t *testing.T) {
	bundle := makeZip(t, map[string]string{"SKILL.md": goodSkillMD})
	s := newTestService(t, bundle, "demo-skill")
	if _, err := s.Install(context.Background(), "@team/demo-skill", ""); err == nil {
		t.Fatal("installing over a builtin name must fail")
	}
	if _, err := os.Stat(s.SkillsDir); err == nil {
		entries, _ := os.ReadDir(s.SkillsDir)
		if len(entries) != 0 {
			t.Fatalf("reserved-name install left files behind: %v", entries)
		}
	}
}

func TestInstallRejectsMissingSkillMD(t *testing.T) {
	bundle := makeZip(t, map[string]string{"README.md": "no skill here"})
	s := newTestService(t, bundle)
	if _, err := s.Install(context.Background(), "@team/demo-skill", ""); err == nil || !strings.Contains(err.Error(), "SKILL.md") {
		t.Fatalf("want missing-SKILL.md error, got %v", err)
	}
}

func TestInstallRejectsInvalidName(t *testing.T) {
	badMD := "---\nname: Bad_Name\ndescription: x\n---\nbody\n"
	bundle := makeZip(t, map[string]string{"SKILL.md": badMD})
	s := newTestService(t, bundle)
	if _, err := s.Install(context.Background(), "@team/demo-skill", ""); err == nil {
		t.Fatal("invalid frontmatter name must fail install")
	}
}

func TestInstallRejectsEmptyDescription(t *testing.T) {
	// 与 loader 共用同一份校验：description 为空的包装不进去，
	// 免得装完 loader 又拒绝发现它。
	md := "---\nname: demo-skill\ndescription:\n---\nbody\n"
	bundle := makeZip(t, map[string]string{"SKILL.md": md})
	s := newTestService(t, bundle)
	if _, err := s.Install(context.Background(), "@team/demo-skill", ""); err == nil {
		t.Fatal("empty description must fail install")
	}
}

func TestUnzipBoundedZipSlip(t *testing.T) {
	// zip.Writer refuses to create ../ entries via Create, so write the
	// header manually.
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	f, err := w.CreateHeader(&zip.FileHeader{Name: "../evil.txt"})
	if err != nil {
		t.Fatal(err)
	}
	f.Write([]byte("pwned"))
	w.Close()

	if _, err := unzipBounded(buf.Bytes()); err == nil {
		t.Fatal("zip-slip entry must condemn the whole bundle")
	}
}

func TestUnzipBoundedTooManyFiles(t *testing.T) {
	files := map[string]string{}
	for i := 0; i <= maxBundleFiles; i++ {
		files[fmt.Sprintf("f%d.txt", i)] = "x"
	}
	if _, err := unzipBounded(makeZip(t, files)); err == nil {
		t.Fatal("entry-count cap must trip")
	}
}

func TestUnzipBoundedFileTooLarge(t *testing.T) {
	big := strings.Repeat("a", maxUncompressedFileBytes+1)
	if _, err := unzipBounded(makeZip(t, map[string]string{"big.txt": big})); err == nil {
		t.Fatal("per-file cap must trip")
	}
}

func TestUnzipBoundedCaseCollision(t *testing.T) {
	bundle := makeZip(t, map[string]string{"Readme.md": "a", "readme.md": "b"})
	if _, err := unzipBounded(bundle); err == nil {
		t.Fatal("case-insensitive collision must be rejected")
	}
}

func TestDownloadTooLargeBundle(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(bytes.Repeat([]byte("a"), maxBundleBytes+1))
	}))
	t.Cleanup(srv.Close)
	reg := NewRegistry(srv.URL)
	if _, err := reg.DownloadBundle(context.Background(), "demo-skill", ""); err == nil {
		t.Fatal("oversized download must fail")
	}
}

func TestVersionFrom(t *testing.T) {
	cases := []struct {
		md   string
		want string
	}{
		{"---\nname: a\nversion: 2.0.0\n---\nbody", "2.0.0"},
		{"---\nname: a\nmetadata:\n  version: 3.1.4\nversion: 2.0.0\n---\nbody", "3.1.4"},
		{"---\nname: a\nversion: \"1.0.0\"\n---\nbody", "1.0.0"},
		{"---\nname: a\n---\nbody", ""},
		{"no frontmatter", ""},
	}
	for _, c := range cases {
		if got := versionFrom(c.md); got != c.want {
			t.Errorf("versionFrom(%q) = %q, want %q", c.md, got, c.want)
		}
	}
}

func TestListInstalledFallsBackWithoutMarker(t *testing.T) {
	s := newTestService(t, nil)
	dir := filepath.Join(s.SkillsDir, "hand-copied")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	md := "---\nname: hand-copied\ndescription: x\nversion: 0.9.0\n---\nbody\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(md), 0o644); err != nil {
		t.Fatal(err)
	}
	list := s.ListInstalled()
	if len(list) != 1 {
		t.Fatalf("ListInstalled = %+v", list)
	}
	if list[0].FullSlug != "hand-copied" || list[0].Version != "0.9.0" {
		t.Fatalf("fallback fields wrong: %+v", list[0])
	}
}

func TestUninstallUnknownSlug(t *testing.T) {
	s := newTestService(t, nil)
	if err := s.Uninstall("@team/never-installed"); err == nil {
		t.Fatal("uninstalling something not installed must error")
	}
}
