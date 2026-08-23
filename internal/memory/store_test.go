package memory

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	return NewStore(filepath.Join(t.TempDir(), "memory.md"))
}

var at = time.Date(2026, 8, 23, 10, 0, 0, 0, time.UTC)

func TestReadMissingFileIsEmptyNotError(t *testing.T) {
	doc, err := newTestStore(t).Read()
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if doc.Content != "" {
		t.Errorf("content = %q, want empty", doc.Content)
	}
	if doc.Hash == "" {
		t.Error("hash is empty; a client needs one to PUT the first version")
	}
	if doc.Limit != MaxBytes {
		t.Errorf("limit = %d, want %d", doc.Limit, MaxBytes)
	}
}

// 同一天的两条记忆共用一个日期标题，而不是每行重复一遍日期。
func TestAppendGroupsSameDayUnderOneHeading(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.Append("偏好 pnpm", at); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if _, err := s.Append("回答用中文", at); err != nil {
		t.Fatalf("Append: %v", err)
	}
	doc, err := s.Read()
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	want := "## 2026-08-23\n- 偏好 pnpm\n- 回答用中文\n"
	if doc.Content != want {
		t.Errorf("content = %q, want %q", doc.Content, want)
	}
}

func TestAppendOnAnotherDayStartsANewGroup(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.Append("偏好 pnpm", at); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if _, err := s.Append("回答用中文", at.AddDate(0, 0, 2)); err != nil {
		t.Fatalf("Append: %v", err)
	}
	doc, _ := s.Read()
	want := "## 2026-08-23\n- 偏好 pnpm\n\n## 2026-08-25\n- 回答用中文\n"
	if doc.Content != want {
		t.Errorf("content = %q, want %q", doc.Content, want)
	}
}

// 条目里的换行必须折成空格，否则一条记忆会被后续的整文件重写读成多条。
func TestAppendFlattensNewlinesInsideAnEntry(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.Append("第一行\n第二行", at); err != nil {
		t.Fatalf("Append: %v", err)
	}
	doc, _ := s.Read()
	// 一个日期标题 + 一条条目 = 两行。
	if got := strings.Count(doc.Content, "\n"); got != 2 {
		t.Errorf("expected a heading plus one entry, got %q", doc.Content)
	}
}

func TestAppendRejectsEmptyEntry(t *testing.T) {
	if _, err := newTestStore(t).Append("   \n  ", at); !errors.Is(err, ErrInvalid) {
		t.Errorf("err = %v, want ErrInvalid", err)
	}
}

func TestWriteRejectsOversizeContent(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.Write(strings.Repeat("x", MaxBytes+1), at); !errors.Is(err, ErrTooLarge) {
		t.Errorf("err = %v, want ErrTooLarge", err)
	}
	// 拒绝写入不该留下半个文件。
	if doc, _ := s.Read(); doc.Content != "" {
		t.Errorf("content = %q, want empty after a rejected write", doc.Content)
	}
}

func TestAppendRejectedWhenItWouldOverflow(t *testing.T) {
	s := newTestStore(t)
	filler := "## 2026-08-23\n- " + strings.Repeat("x", MaxBytes-48) + "\n"
	if _, err := s.Write(filler, at); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if _, err := s.Append("再加一条就超了这条会被拒绝", at); !errors.Is(err, ErrTooLarge) {
		t.Errorf("err = %v, want ErrTooLarge", err)
	}
	if doc, _ := s.Read(); doc.Content != filler {
		t.Errorf("a rejected append modified the file: %q", doc.Content)
	}
}

func TestWriteCheckedRejectsStaleHash(t *testing.T) {
	s := newTestStore(t)
	first, err := s.Write("一\n", at)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}

	// Agent 在用户编辑期间写了一次，用户手上的哈希就过期了。
	if _, err := s.Write("一\n二\n", at); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if _, err := s.WriteChecked("用户改的版本\n", first.Hash, at); !errors.Is(err, ErrConflict) {
		t.Errorf("err = %v, want ErrConflict", err)
	}

	current, _ := s.Read()
	if _, err := s.WriteChecked("用户改的版本\n", current.Hash, at); err != nil {
		t.Errorf("WriteChecked with fresh hash: %v", err)
	}
}

// 用户在编辑器里只写内容，日期该由我们补 —— 让人手打日期既啰嗦又容易写错，
// 而日期恰恰是我们唯一能可靠知道的东西。
func TestWriteGroupsLooseLinesUnderToday(t *testing.T) {
	s := newTestStore(t)
	doc, err := s.Write("回答用中文\n- 包管理器用 pnpm\n", at)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	want := "## 2026-08-23\n- 回答用中文\n- 包管理器用 pnpm\n"
	if doc.Content != want {
		t.Errorf("content = %q, want %q", doc.Content, want)
	}
}

// 保存永远不改写已有的日期分组，哪怕当天是别的日子。
//
// 代价是手敲在末尾的行会归到它前面那个日期下 —— 因为我们渲染出的条目和用户刚
// 敲的行在文本上没有区别，要想把裸行都改判成今天，就得连历史条目一起改判，那
// 等于每次保存都把全部日期刷成当天。宁可让一条的归属偏一组。
func TestWriteNeverRewritesExistingDates(t *testing.T) {
	s := newTestStore(t)
	later := at.AddDate(0, 1, 0)
	doc, err := s.Write("## 2026-01-05\n- 老条目\n\n新条目\n", later)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	want := "## 2026-01-05\n- 老条目\n- 新条目\n"
	if doc.Content != want {
		t.Errorf("content = %q, want %q", doc.Content, want)
	}
}

// 标题之前的裸行归到今天，而且今天那一组落在文件末尾 —— 跟 Append 的生长方向
// 一致，读下来就是从旧到新。
func TestWriteGroupsLeadingLinesUnderTodayAtTheEnd(t *testing.T) {
	s := newTestStore(t)
	doc, err := s.Write("新条目\n\n## 2026-01-05\n- 老条目\n", at)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	want := "## 2026-01-05\n- 老条目\n\n## 2026-08-23\n- 新条目\n"
	if doc.Content != want {
		t.Errorf("content = %q, want %q", doc.Content, want)
	}
}

// 连存两次结果必须一样，否则每次保存都会在文件里再叠一层结构。
func TestCanonicalizeIsIdempotent(t *testing.T) {
	inputs := []string{
		"回答用中文\n",
		"## 2026-01-05\n- 老条目\n\n## 2026-08-23\n- 新条目\n",
		"- [2026-01-05] 旧格式\n- [2026-01-06] 另一天\n",
	}
	for _, in := range inputs {
		first := Canonicalize(in, at)
		second := Canonicalize(first, at.AddDate(0, 0, 5))
		if second != first {
			t.Errorf("not idempotent for %q:\n first  = %q\n second = %q", in, first, second)
		}
	}
}

// 旧的逐行带日期格式要能自动迁移成分组格式，并且保住每条原本的日期。
func TestCanonicalizeMigratesLegacyDatedLines(t *testing.T) {
	got := Canonicalize("- [2026-08-23] 甲\n- [2026-08-22] 乙\n- [2026-08-23] 丙\n", at)
	want := "## 2026-08-23\n- 甲\n- 丙\n\n## 2026-08-22\n- 乙\n"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// 用户自己分的组保留原样，组内的条目不会被挪到日期组去。
func TestCanonicalizeKeepsUserHeadings(t *testing.T) {
	got := Canonicalize("## 工作偏好\n\n回答用中文\n", at)
	want := "## 工作偏好\n- 回答用中文\n"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// 方括号里不是日期的，那是用户写的正文，不该被提成分组标题。
func TestCanonicalizeLeavesNonDateBracketsInText(t *testing.T) {
	got := Canonicalize("[TODO] 买菜\n", at)
	want := "## 2026-08-23\n- [TODO] 买菜\n"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// 清空某天的条目之后不该留下一个光标题。
func TestCanonicalizeDropsEmptyGroups(t *testing.T) {
	if got := Canonicalize("## 2026-08-20\n\n## 2026-08-21\n- 还在\n", at); got != "## 2026-08-21\n- 还在\n" {
		t.Errorf("got %q", got)
	}
}

// 同一份内容在不同编辑器里读出来必须是同一个哈希，否则乐观锁会误报冲突。
func TestRestructureMakesHashStableAcrossLineEndings(t *testing.T) {
	a := docOf(Restructure("## 2026-08-23\r\n- 一\r\n- 二\r\n\r\n"))
	b := docOf(Restructure("## 2026-08-23\n- 一\n- 二"))
	if a.Hash != b.Hash {
		t.Errorf("hashes differ: %s vs %s", a.Hash, b.Hash)
	}
}

// 打开编辑器就该看到规整后的样子。迁移只在写入时做的话，老格式文件会一直卡在
// 老格式：用户看到的是旧样子，而内容没改动时保存按钮是禁用的，他连触发迁移的
// 机会都没有。
func TestReadMigratesLegacyFormatWithoutWriting(t *testing.T) {
	s := newTestStore(t)
	legacy := "- [2026-08-23] 甲\n- [2026-08-22] 乙\n"
	if err := os.WriteFile(s.Path(), []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}

	doc, err := s.Read()
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	want := "## 2026-08-23\n- 甲\n\n## 2026-08-22\n- 乙\n"
	if doc.Content != want {
		t.Errorf("content = %q, want %q", doc.Content, want)
	}

	// 读取不该写盘。
	raw, err := os.ReadFile(s.Path())
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != legacy {
		t.Errorf("a read rewrote the file: %q", raw)
	}

	// 哈希基于规整结果，所以拿它直接保存不会被误判成冲突。
	if _, err := s.WriteChecked(doc.Content, doc.Hash, at); err != nil {
		t.Errorf("saving what Read returned was rejected: %v", err)
	}
}

// 还没归到日期下的条目在读取时保持原样，不能凭空盖上"今天" —— 那会让读出来的
// 内容随日期变化，乐观锁的哈希跟着一起飘。
func TestRestructureLeavesUngroupedEntriesUndated(t *testing.T) {
	got := Restructure("回答用中文\n\n## 2026-01-05\n- 老条目\n")
	want := "- 回答用中文\n\n## 2026-01-05\n- 老条目\n"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestWriteIsAtomicAndOwnerOnly(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.Write("- 一\n", at); err != nil {
		t.Fatalf("Write: %v", err)
	}
	info, err := os.Stat(s.Path())
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("perm = %o, want 600", perm)
	}
	// 临时文件不该留在目录里。
	entries, err := os.ReadDir(filepath.Dir(s.Path()))
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("temporary file left behind: %s", e.Name())
		}
	}
}
