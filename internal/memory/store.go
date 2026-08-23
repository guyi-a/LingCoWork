// Package memory 存放跨对话保留的长期记忆。
//
// 两级：用户级是跨项目稳定的个人偏好，项目级是某个工作区的约定。每级一个
// markdown 文件，一行一条。
//
// Store 只认一个文件路径，不认目录 —— 用户级路径进程启动时就定了，项目级得
// 按会话解析出工作区才知道，两者生命周期不同，所以两级各持一个实例，而不是
// 一个 Store 带 scope 参数。
package memory

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

const (
	// MaxBytes 是单级记忆的硬上限。记忆每轮都进系统提示词，无上限增长等于
	// 慢性侵占上下文；写满之后由模型合并旧条目腾地方，而不是自动淘汰 ——
	// 该舍弃哪一条是判断题，不是先进先出。
	MaxBytes = 4 << 10

	// MaxEntryBytes 限制单条长度。一条占掉大半配额的记忆通常是把任务细节
	// 当成了长期偏好。
	MaxEntryBytes = 512

	// DateLayout 是条目前缀里的日期格式。
	DateLayout = "2006-01-02"
)

var (
	ErrTooLarge = errors.New("memory exceeds size limit")
	ErrInvalid  = errors.New("invalid memory entry")
	// ErrConflict 是乐观锁失败：读取之后文件被别人改过。用户在编辑器里改
	// 记忆的同时 Agent 也可能写入，没有这一层用户保存会静默吃掉 Agent 的
	// 写入。
	ErrConflict = errors.New("memory changed since it was read")
)

// Doc 是一次读取的结果。Hash 给乐观锁用，Bytes/Limit 给前端显示剩余额度用 ——
// 都不进注入的片段，那边必须字节稳定。
type Doc struct {
	Content string `json:"content"`
	Hash    string `json:"hash"`
	Bytes   int    `json:"bytes"`
	Limit   int    `json:"limit"`
}

type Store struct {
	path string
	mu   sync.RWMutex
}

func NewStore(path string) *Store {
	return &Store{path: filepath.Clean(path)}
}

func (s *Store) Path() string {
	return s.path
}

// Read 返回当前内容。文件不存在不是错误 —— 没写过记忆和记忆为空是同一件事。
func (s *Store) Read() (Doc, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.readLocked()
}

// Write 整文件替换。Agent 的重写动作和用户保存都走这里。
//
// at 是补日期用的时钟：写入方只负责内容，分组与日期由 Canonicalize 填。
func (s *Store) Write(content string, at time.Time) (Doc, error) {
	content = Canonicalize(content, at)
	if err := checkSize(content); err != nil {
		return Doc{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.writeLocked(content); err != nil {
		return Doc{}, err
	}
	return docOf(content), nil
}

// WriteChecked 是带乐观锁的整文件替换：expectedHash 与磁盘现状不符就拒绝，
// 让调用方重新加载。用户手动编辑走这条。
func (s *Store) WriteChecked(content, expectedHash string, at time.Time) (Doc, error) {
	content = Canonicalize(content, at)
	if err := checkSize(content); err != nil {
		return Doc{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	current, err := s.readLocked()
	if err != nil {
		return Doc{}, err
	}
	if expectedHash != current.Hash {
		return Doc{}, fmt.Errorf("%w: expected %s, on disk %s", ErrConflict, expectedHash, current.Hash)
	}
	if err := s.writeLocked(content); err != nil {
		return Doc{}, err
	}
	return docOf(content), nil
}

// Append 追加一条到 at 那天的分组下。读改写整个持在写锁内，避免两次追加互相
// 覆盖。
func (s *Store) Append(entry string, at time.Time) (Doc, error) {
	text, err := normalizeEntry(entry)
	if err != nil {
		return Doc{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	current, err := s.readLocked()
	if err != nil {
		return Doc{}, err
	}
	next := AppendEntry(current.Content, text, at)
	if err := checkSize(next); err != nil {
		return Doc{}, err
	}
	if err := s.writeLocked(next); err != nil {
		return Doc{}, err
	}
	return docOf(next), nil
}

// normalizeEntry 把一条记忆压成一行正文（不含项目符号）。换行折成空格是为了
// 守住"一行一条"这个不变量 —— 条目里带换行会让后续的整文件重写把一条读成多条。
func normalizeEntry(entry string) (string, error) {
	flat := entryText(entry)
	if flat == "" {
		return "", fmt.Errorf("%w: entry is empty", ErrInvalid)
	}
	if len(flat) > MaxEntryBytes {
		return "", fmt.Errorf("%w: entry is %d bytes, limit %d", ErrInvalid, len(flat), MaxEntryBytes)
	}
	return flat, nil
}

const headingPrefix = "## "

// legacyDatedEntry 认早先"每行一个日期前缀"的写法，用于迁移到分组格式。故意
// 只认真实的日期形状：`- [TODO] 买菜` 里的方括号是用户自己写的正文，把它提成
// 一个分组标题只会让人莫名其妙。
var legacyDatedEntry = regexp.MustCompile(`^-?\s*\[(\d{4}-\d{2}-\d{2})\]\s*(.*)$`)

type group struct {
	heading string
	entries []string
}

// Restructure 只做结构规整：按天分组、一行一条、丢空行和空分组，并把旧的逐行
// 带日期写法迁移过来。不需要时钟，所以读取路径也能用。
//
// 同一天的条目共用一个日期标题，而不是每行重复一遍日期：那份重复既占额度（每级
// 4KB，还要跟每一轮的系统提示词分摊），读起来也吵。
//
// 读取时就规整，是因为迁移只发生在写入的话，老格式的文件会一直停在老格式 ——
// 用户打开编辑器看到的还是旧样子，而内容没改动时保存按钮是禁用的，他连触发迁移
// 的机会都没有。
//
// 输出对同一输入必须逐字节相同：注入的片段是照抄这个结果的，而提示词缓存按前缀
// 匹配，这里抖一下等于每轮都 miss。分组顺序取文件里首次出现的顺序，不排序，这样
// 重排也不会凭空改变字节。
func Restructure(content string) string {
	groups, loose := parseGroups(content)
	if len(loose) > 0 {
		// 还没归到任何日期下的条目原样留在开头。读取时不该凭空给它们盖一个
		// "今天" —— 那会让读出来的内容随日期变化，乐观锁的哈希跟着一起飘。
		groups = append([]group{{entries: loose}}, groups...)
	}
	return renderGroups(groups)
}

// Canonicalize 是 Restructure 再加一步：把还没归组的条目落到 at 那一天。写入
// 路径用这个，因为"这条是什么时候记下的"只有在写的那一刻才知道。
func Canonicalize(content string, at time.Time) string {
	groups, loose := parseGroups(content)
	if len(loose) > 0 {
		var idx int
		groups, idx = ensureGroup(groups, at.Format(DateLayout))
		groups[idx].entries = append(groups[idx].entries, loose...)
	}
	return renderGroups(groups)
}

// AppendEntry 把一条记忆放进 at 那天的分组；那天还没有分组就在末尾新建一个。
func AppendEntry(content, entry string, at time.Time) string {
	groups, loose := parseGroups(content)
	today := at.Format(DateLayout)
	groups, idx := ensureGroup(groups, today)
	groups[idx].entries = append(groups[idx].entries, loose...)
	groups[idx].entries = append(groups[idx].entries, entry)
	return renderGroups(groups)
}

// parseGroups 把内容拆成有序分组。loose 只装文件开头、任何标题之前的条目 ——
// 空文件里用户直接敲内容就是这种情况。
//
// 标题之后的裸行一律归给那个标题，哪怕它是个旧日期。听起来该把它改判成今天，
// 但做不到：我们渲染出的条目和用户刚敲的行在文本上完全一样，要改判就得连历史
// 条目一起改判，于是每次保存都会把全部日期刷成当天 —— 而那个日期正是分组存在
// 的全部意义。宁可让手敲在末尾的一条归属偏一组。
func parseGroups(content string) (groups []group, loose []string) {
	current := -1
	for _, raw := range strings.Split(content, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "#") {
			heading := strings.TrimSpace(strings.TrimLeft(line, "#"))
			if heading == "" {
				continue
			}
			groups, current = ensureGroup(groups, heading)
			continue
		}
		// 旧格式的行按它自己带的日期归组，而不是跟着当前标题走 —— 它的日期
		// 就是它当初记下的时间，这是迁移时唯一该保留的信息。
		if m := legacyDatedEntry.FindStringSubmatch(line); m != nil {
			if text := entryText(m[2]); text != "" {
				var idx int
				groups, idx = ensureGroup(groups, m[1])
				groups[idx].entries = append(groups[idx].entries, text)
			}
			continue
		}
		text := entryText(line)
		if text == "" {
			continue
		}
		if current < 0 {
			loose = append(loose, text)
			continue
		}
		groups[current].entries = append(groups[current].entries, text)
	}
	return groups, loose
}

func ensureGroup(groups []group, heading string) ([]group, int) {
	for i := range groups {
		if groups[i].heading == heading {
			return groups, i
		}
	}
	return append(groups, group{heading: heading}), len(groups)
}

func renderGroups(groups []group) string {
	var b strings.Builder
	for _, g := range groups {
		// 空分组不落盘：用户把某天的条目清空之后，留一个光标题只是噪音。
		if len(g.entries) == 0 {
			continue
		}
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		// 无标题分组（Restructure 留下的未归组条目）直接出条目。
		if g.heading != "" {
			b.WriteString(headingPrefix)
			b.WriteString(g.heading)
			b.WriteString("\n")
		}
		for _, e := range g.entries {
			b.WriteString("- ")
			b.WriteString(e)
			b.WriteString("\n")
		}
	}
	return b.String()
}

// entryText 去掉行首的项目符号并把内部空白压成单空格。
func entryText(line string) string {
	return strings.Join(strings.Fields(strings.TrimLeft(strings.TrimSpace(line), "-*• \t")), " ")
}

func checkSize(content string) error {
	if len(content) > MaxBytes {
		return fmt.Errorf("%w: %d bytes, limit %d", ErrTooLarge, len(content), MaxBytes)
	}
	return nil
}

func docOf(content string) Doc {
	return Doc{
		Content: content,
		Hash:    hashOf(content),
		Bytes:   len(content),
		Limit:   MaxBytes,
	}
}

func hashOf(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}

func (s *Store) readLocked() (Doc, error) {
	info, err := os.Lstat(s.path)
	if errors.Is(err, fs.ErrNotExist) {
		return docOf(""), nil
	}
	if err != nil {
		return Doc{}, fmt.Errorf("stat memory file: %w", err)
	}
	// 拒绝符号链接等非普通文件：项目级记忆落在工作区里，而工作区的内容是
	// Agent 自己写出来的，不该让一个链接把写入引到工作区外面去。
	if !info.Mode().IsRegular() {
		return Doc{}, fmt.Errorf("%w: memory path is not a regular file", ErrInvalid)
	}
	data, err := os.ReadFile(s.path)
	if err != nil {
		return Doc{}, fmt.Errorf("read memory file: %w", err)
	}
	// 读出来就规整：编辑器、注入片段、模型看到的都是同一份整理过的内容，老格式
	// 的文件不用等到下一次写入才变样。哈希也基于规整结果，写入时的乐观锁比的是
	// 同一个东西，不会因为磁盘上还是旧字节而误判冲突。
	return docOf(Restructure(string(data))), nil
}

func (s *Store) writeLocked(content string) error {
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create memory directory: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".memory-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary memory file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fmt.Errorf("set temporary memory permissions: %w", err)
	}
	if _, err := tmp.WriteString(content); err != nil {
		tmp.Close()
		return fmt.Errorf("write temporary memory file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("sync temporary memory file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temporary memory file: %w", err)
	}
	if err := os.Rename(tmpName, s.path); err != nil {
		return fmt.Errorf("replace memory file: %w", err)
	}
	return nil
}
