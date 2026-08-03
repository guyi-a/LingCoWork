package skills

import (
	"os"
	"path/filepath"
	"testing"
)

func writeSkill(t *testing.T, root, dir, frontName, desc string) {
	t.Helper()
	d := filepath.Join(root, dir)
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "---\nname: " + frontName + "\ndescription: " + desc + "\n---\nbody of " + frontName + "\n"
	if err := os.WriteFile(filepath.Join(d, "SKILL.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func newTestLoader(t *testing.T) (*Loader, string) {
	t.Helper()
	dataDir := t.TempDir()
	userDir := filepath.Join(t.TempDir(), "skills")
	l, err := NewLoaderAt(dataDir, userDir)
	if err != nil {
		t.Fatalf("NewLoaderAt: %v", err)
	}
	return l, userDir
}

func TestRefreshPicksUpUserSkills(t *testing.T) {
	l, userDir := newTestLoader(t)

	before := len(l.Index())
	if before == 0 {
		t.Fatal("expected builtin skills in the index")
	}
	if _, err := l.Load("my-skill"); err == nil {
		t.Fatal("my-skill should not exist yet")
	}

	writeSkill(t, userDir, "my-skill", "my-skill", "a user installed skill")
	if err := l.Refresh(); err != nil {
		t.Fatalf("Refresh: %v", err)
	}

	s, err := l.Load("my-skill")
	if err != nil {
		t.Fatalf("Load(my-skill): %v", err)
	}
	if s.Source != SourceUser {
		t.Fatalf("Source = %q, want user", s.Source)
	}
	if s.Body == "" || s.Path == "" {
		t.Fatalf("skill missing body/path: %+v", s)
	}
	if got := len(l.Index()); got != before+1 {
		t.Fatalf("index size = %d, want %d", got, before+1)
	}

	// Uninstall: gone after the next refresh.
	if err := os.RemoveAll(filepath.Join(userDir, "my-skill")); err != nil {
		t.Fatal(err)
	}
	if err := l.Refresh(); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if _, err := l.Load("my-skill"); err == nil {
		t.Fatal("my-skill should be gone after refresh")
	}
}

func TestBuiltinWinsOverUserSkill(t *testing.T) {
	l, userDir := newTestLoader(t)

	builtins := l.ReservedBuiltinNames()
	if len(builtins) == 0 {
		t.Fatal("no builtin names")
	}
	name := builtins[0]

	writeSkill(t, userDir, name, name, "attempt to shadow a builtin")
	if err := l.Refresh(); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	s, err := l.Load(name)
	if err != nil {
		t.Fatalf("Load(%s): %v", name, err)
	}
	if s.Source != SourceBuiltin {
		t.Fatalf("builtin %q shadowed by user skill", name)
	}
}

func TestMalformedUserSkillIsSkipped(t *testing.T) {
	l, userDir := newTestLoader(t)
	before := len(l.Index())

	// Declared name disagrees with the directory name.
	writeSkill(t, userDir, "dir-name", "other-name", "mismatched")
	// Invalid characters in the name.
	writeSkill(t, userDir, "Bad_Name", "Bad_Name", "bad name")
	// Empty description.
	writeSkill(t, userDir, "no-desc", "no-desc", "")

	if err := l.Refresh(); err != nil {
		t.Fatalf("Refresh should not fail on bad user skills: %v", err)
	}
	if got := len(l.Index()); got != before {
		t.Fatalf("index size = %d, want %d (bad skills must be skipped)", got, before)
	}
}

func TestParseDocumentFoldedDescription(t *testing.T) {
	// 注册中心发布的技能常把 description 写成 YAML 折行式多行文本
	//（kskill CLI 生成的就是这种），索引里必须拿到完整描述而不是第一行。
	md := "---\nname: ytech-service\ndescription: 接入和调用 ytech 服务。当需要上传本地输入，\n  拼装请求、调用接口、\n  轮询状态并下载结果时使用。\n---\nbody\n"
	sk, err := ParseDocument(md)
	if err != nil {
		t.Fatal(err)
	}
	want := "接入和调用 ytech 服务。当需要上传本地输入， 拼装请求、调用接口、 轮询状态并下载结果时使用。"
	if sk.Description != want {
		t.Fatalf("Description = %q, want %q", sk.Description, want)
	}
	if sk.Name != "ytech-service" {
		t.Fatalf("Name = %q", sk.Name)
	}

	folded := "---\nname: a\ndescription: >-\n  first line\n  second line\n---\nbody\n"
	sk, err = ParseDocument(folded)
	if err != nil {
		t.Fatal(err)
	}
	if sk.Description != "first line second line" {
		t.Fatalf("folded Description = %q", sk.Description)
	}
}

func TestValidateName(t *testing.T) {
	good := []string{"a", "pdf", "browser-use", "a1-b2-c3"}
	for _, n := range good {
		if err := ValidateName(n); err != nil {
			t.Errorf("ValidateName(%q) = %v, want nil", n, err)
		}
	}
	bad := []string{"", "-lead", "trail-", "double--dash", "Upper", "with space", "under_score", "中文"}
	for _, n := range bad {
		if err := ValidateName(n); err == nil {
			t.Errorf("ValidateName(%q) = nil, want error", n)
		}
	}
}
