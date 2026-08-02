package tools

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"

	"github.com/guyi-a/Interview-Agent/internal/agent/browseruse"
	"github.com/guyi-a/Interview-Agent/internal/agent/contextkey"
	"github.com/guyi-a/Interview-Agent/internal/agent/scope"
	"github.com/guyi-a/Interview-Agent/internal/repository"
)

// browser_use tool: one mega-tool with an `action` verb. Kept a single tool
// so the UI's tool chips don't get 10 separate cards per turn.
//
// action 分支：
//   open_tab(url)            → 新 tab 加载 url
//   list_pages               → 当前 session 打开的所有 tab
//   close_tab(page_id)       → 关闭指定 tab
//   read_state(page_id)      → 返回 markdown + 元素编号（拿 index 做后续操作用）
//   click(page_id, index)    → 点击 read_state 里的第 N 个元素
//   type(page_id, index, text) → 往输入框填 text（先清空）
//   press(page_id, key, index?) → 键盘事件；不传 index 时作用于整页

type browserUseInput struct {
	Action      string `json:"action" jsonschema:"description=One of: open_tab / list_pages / close_tab / focus_page / read_state / click / hover / dblclick / rightclick / type / press / scroll / wait_for / go_back / reload / extract / screenshot / execute_script / close_session"`
	PageID      string `json:"page_id,omitempty" jsonschema:"description=Page identifier returned by open_tab. Required for most actions."`
	URL         string `json:"url,omitempty" jsonschema:"description=Absolute URL for open_tab\\, must include scheme (http/https/file)."`
	Index       int    `json:"index,omitempty" jsonschema:"description=Element index from the last read_state. Required for click / hover / dblclick / rightclick / type / extract."`
	Text        string `json:"text,omitempty" jsonschema:"description=For type: what to fill into the element."`
	Key         string `json:"key,omitempty" jsonschema:"description=For press: 'Enter' / 'Tab' / 'Escape' / 'Control+A' etc."`
	DX          int    `json:"dx,omitempty" jsonschema:"description=For scroll: horizontal pixels to scroll by (positive=right)."`
	DY          int    `json:"dy,omitempty" jsonschema:"description=For scroll: vertical pixels to scroll by (positive=down)."`
	Selector    string `json:"selector,omitempty" jsonschema:"description=For wait_for: CSS selector to wait for. Mutually exclusive with a bare timeout wait."`
	TimeoutMS   int    `json:"timeout_ms,omitempty" jsonschema:"description=For wait_for: ceiling in milliseconds. Alone (no selector) means a plain idle wait."`
	SavePath    string `json:"save_path,omitempty" jsonschema:"description=For screenshot: destination path. Empty returns base64 (only for small images)."`
	FullPage    bool   `json:"full_page,omitempty" jsonschema:"description=For screenshot: capture the full scrolling page instead of just the viewport."`
	IncludeHTML bool   `json:"include_html,omitempty" jsonschema:"description=For extract: also return the element's innerHTML."`
	Script      string `json:"script,omitempty" jsonschema:"description=For execute_script: JS expression or async function body that returns a JSON-serialisable value."`
}

type browserUseOutput struct {
	OK          bool                  `json:"ok"`
	Message     string                `json:"message,omitempty"`
	Page        *browseruse.PageInfo  `json:"page,omitempty"`
	Pages       []browseruse.PageInfo `json:"pages,omitempty"`
	State       string                `json:"state,omitempty"`
	Text        string                `json:"text,omitempty"`
	HTML        string                `json:"html,omitempty"`
	Path        string                `json:"path,omitempty"`
	ImageBase64 string                `json:"image_base64,omitempty"`
	Value       interface{}           `json:"value,omitempty"`
}

func newBrowserUseTool(
	mgr *browseruse.Manager,
	convRepo *repository.ConversationRepo,
	projectRepo *repository.ProjectRepo,
) (tool.BaseTool, error) {
	if mgr == nil {
		return nil, errors.New("browser_use: manager is nil")
	}
	desc := "Real browser automation via an independent Chromium instance. " +
		"Typical flow: open_tab → read_state → click/type/press by index → " +
		"read_state again → ... → close_session when done. " +
		"Element indices from read_state expire the moment the DOM changes, " +
		"so re-read after every action. " +
		"Additional actions: hover / dblclick / rightclick (click variants), " +
		"scroll (dx/dy), wait_for (selector or timeout_ms), go_back / reload, " +
		"focus_page, extract (element text +optional innerHTML), screenshot " +
		"(save_path + full_page), execute_script (raw JS). " +
		"If chromium isn't installed, first call browser_use_install(step='check')."
	return utils.InferTool("browser_use", desc, func(ctx context.Context, in *browserUseInput) (*browserUseOutput, error) {
		return dispatchBrowserUse(ctx, mgr, convRepo, projectRepo, in)
	})
}

func dispatchBrowserUse(
	ctx context.Context,
	mgr *browseruse.Manager,
	convRepo *repository.ConversationRepo,
	projectRepo *repository.ProjectRepo,
	in *browserUseInput,
) (*browserUseOutput, error) {
	convID := contextkey.ConversationID(ctx)
	if convID == "" {
		return nil, errors.New("browser_use: no conversation in context")
	}

	// close_session is the one action that must NOT lazily boot a browser
	// just to tear it down. Handle it before any Session() call.
	if in.Action == "close_session" {
		mgr.CloseSession(convID)
		return &browserUseOutput{OK: true, Message: "browser session closed"}, nil
	}

	sess, err := mgr.Session(ctx, convID)
	if err != nil {
		if errors.Is(err, browseruse.ErrDriverMissing) {
			return &browserUseOutput{
				OK:      false,
				Message: "浏览器依赖尚未就绪。先调 browser_use_install(step='check') 检查，再按提示 install。",
			}, nil
		}
		return nil, err
	}

	switch in.Action {
	case "open_tab":
		if in.URL == "" {
			return &browserUseOutput{OK: false, Message: "open_tab 需要 url"}, nil
		}
		info, err := sess.OpenTab(in.URL)
		if err != nil {
			return &browserUseOutput{OK: false, Message: err.Error()}, nil
		}
		return &browserUseOutput{OK: true, Page: &info}, nil

	case "list_pages":
		return &browserUseOutput{OK: true, Pages: sess.ListPages()}, nil

	case "close_tab":
		if in.PageID == "" {
			return &browserUseOutput{OK: false, Message: "close_tab 需要 page_id"}, nil
		}
		if err := sess.CloseTab(in.PageID); err != nil {
			return &browserUseOutput{OK: false, Message: err.Error()}, nil
		}
		return &browserUseOutput{OK: true, Message: "closed"}, nil

	case "read_state":
		if in.PageID == "" {
			return &browserUseOutput{OK: false, Message: "read_state 需要 page_id"}, nil
		}
		md, err := sess.ReadState(in.PageID)
		if err != nil {
			return &browserUseOutput{OK: false, Message: err.Error()}, nil
		}
		return &browserUseOutput{OK: true, State: md}, nil

	case "click":
		if in.PageID == "" || in.Index == 0 {
			return &browserUseOutput{OK: false, Message: "click 需要 page_id 和 index"}, nil
		}
		if err := sess.Click(in.PageID, in.Index); err != nil {
			return &browserUseOutput{OK: false, Message: err.Error()}, nil
		}
		return &browserUseOutput{OK: true, Message: fmt.Sprintf("clicked index %d", in.Index)}, nil

	case "type":
		if in.PageID == "" || in.Index == 0 || in.Text == "" {
			return &browserUseOutput{OK: false, Message: "type 需要 page_id, index, text"}, nil
		}
		if err := sess.Type(in.PageID, in.Index, in.Text); err != nil {
			return &browserUseOutput{OK: false, Message: err.Error()}, nil
		}
		return &browserUseOutput{OK: true, Message: fmt.Sprintf("typed into index %d", in.Index)}, nil

	case "press":
		if in.PageID == "" || in.Key == "" {
			return &browserUseOutput{OK: false, Message: "press 需要 page_id 和 key"}, nil
		}
		if err := sess.Press(in.PageID, in.Key, in.Index); err != nil {
			return &browserUseOutput{OK: false, Message: err.Error()}, nil
		}
		return &browserUseOutput{OK: true, Message: "pressed " + in.Key}, nil

	case "hover", "dblclick", "rightclick":
		if in.PageID == "" || in.Index == 0 {
			return &browserUseOutput{OK: false, Message: in.Action + " 需要 page_id 和 index"}, nil
		}
		var err error
		switch in.Action {
		case "hover":
			err = sess.Hover(in.PageID, in.Index)
		case "dblclick":
			err = sess.Dblclick(in.PageID, in.Index)
		case "rightclick":
			err = sess.Rightclick(in.PageID, in.Index)
		}
		if err != nil {
			return &browserUseOutput{OK: false, Message: err.Error()}, nil
		}
		return &browserUseOutput{OK: true, Message: fmt.Sprintf("%s on index %d", in.Action, in.Index)}, nil

	case "scroll":
		if in.PageID == "" {
			return &browserUseOutput{OK: false, Message: "scroll 需要 page_id"}, nil
		}
		if err := sess.Scroll(in.PageID, in.DX, in.DY); err != nil {
			return &browserUseOutput{OK: false, Message: err.Error()}, nil
		}
		return &browserUseOutput{OK: true, Message: fmt.Sprintf("scrolled by (%d,%d)", in.DX, in.DY)}, nil

	case "wait_for":
		if in.PageID == "" {
			return &browserUseOutput{OK: false, Message: "wait_for 需要 page_id"}, nil
		}
		if err := sess.WaitFor(in.PageID, in.Selector, in.TimeoutMS); err != nil {
			return &browserUseOutput{OK: false, Message: err.Error()}, nil
		}
		return &browserUseOutput{OK: true, Message: "wait done"}, nil

	case "go_back":
		if in.PageID == "" {
			return &browserUseOutput{OK: false, Message: "go_back 需要 page_id"}, nil
		}
		if err := sess.GoBack(in.PageID); err != nil {
			return &browserUseOutput{OK: false, Message: err.Error()}, nil
		}
		return &browserUseOutput{OK: true, Message: "navigated back"}, nil

	case "reload":
		if in.PageID == "" {
			return &browserUseOutput{OK: false, Message: "reload 需要 page_id"}, nil
		}
		if err := sess.Reload(in.PageID); err != nil {
			return &browserUseOutput{OK: false, Message: err.Error()}, nil
		}
		return &browserUseOutput{OK: true, Message: "reloaded"}, nil

	case "focus_page":
		if in.PageID == "" {
			return &browserUseOutput{OK: false, Message: "focus_page 需要 page_id"}, nil
		}
		if err := sess.FocusPage(in.PageID); err != nil {
			return &browserUseOutput{OK: false, Message: err.Error()}, nil
		}
		return &browserUseOutput{OK: true, Message: "focused"}, nil

	case "extract":
		if in.PageID == "" || in.Index == 0 {
			return &browserUseOutput{OK: false, Message: "extract 需要 page_id 和 index"}, nil
		}
		text, html, err := sess.Extract(in.PageID, in.Index, in.IncludeHTML)
		if err != nil {
			return &browserUseOutput{OK: false, Message: err.Error()}, nil
		}
		return &browserUseOutput{OK: true, Text: text, HTML: html}, nil

	case "screenshot":
		if in.PageID == "" {
			return &browserUseOutput{OK: false, Message: "screenshot 需要 page_id"}, nil
		}
		absPath, err := resolveScreenshotPath(ctx, convRepo, projectRepo, in.SavePath)
		if err != nil {
			return &browserUseOutput{OK: false, Message: err.Error()}, nil
		}
		path, _, err := sess.Screenshot(in.PageID, absPath, in.FullPage)
		if err != nil {
			return &browserUseOutput{OK: false, Message: err.Error()}, nil
		}
		return &browserUseOutput{OK: true, Path: path}, nil

	case "execute_script":
		if in.PageID == "" || in.Script == "" {
			return &browserUseOutput{OK: false, Message: "execute_script 需要 page_id 和 script"}, nil
		}
		val, err := sess.ExecuteScript(in.PageID, in.Script)
		if err != nil {
			return &browserUseOutput{OK: false, Message: err.Error()}, nil
		}
		return &browserUseOutput{OK: true, Value: val}, nil

	default:
		return &browserUseOutput{OK: false, Message: "unknown action: " + in.Action}, nil
	}
}

// resolveScreenshotPath enforces workspace scope for browser screenshots:
//   - no workspace → return a self-heal hint asking for create_workspace
//   - save_path empty → auto-name under screenshots/<timestamp>.png
//   - save_path absolute → reject
//   - save_path relative → resolve inside workspace, prevent escape via ..
func resolveScreenshotPath(
	ctx context.Context,
	convRepo *repository.ConversationRepo,
	projectRepo *repository.ProjectRepo,
	savePath string,
) (string, error) {
	ws, err := resolveConversationWorkspace(ctx, convRepo, projectRepo)
	if err != nil {
		return "", fmt.Errorf("screenshot 需要工作区：%w。请先调 create_workspace 建一个", err)
	}

	if savePath == "" {
		name := fmt.Sprintf("screenshots/shot-%s.png", time.Now().Format("20060102-150405"))
		return filepath.Join(ws, name), nil
	}
	if filepath.IsAbs(savePath) {
		return "", fmt.Errorf("save_path 不接受绝对路径 %q；传相对路径，会落到 workspace 下", savePath)
	}
	// Anti-escape: use scope package so ".." can't punch out of ws.
	abs, err := scope.Resolve(ws, savePath)
	if err != nil {
		return "", fmt.Errorf("save_path 越界：%w", err)
	}
	// Default extension to .png if the model omits it.
	if !strings.Contains(filepath.Base(abs), ".") {
		abs += ".png"
	}
	return abs, nil
}

// --- browser_use_install ---

type installInput struct {
	Step string `json:"step" jsonschema:"description=One of: check (probe local state) / install (download driver + chromium; ~150MB\\, slow)."`
}

type installOutput struct {
	Step          string `json:"step"`
	DriverReady   bool   `json:"driver_ready"`
	BrowsersReady bool   `json:"browsers_ready"`
	Message       string `json:"message"`
}

func newBrowserUseInstallTool() (tool.BaseTool, error) {
	desc := "Probe / install the playwright driver + chromium browser used by browser_use. " +
		"Call step='check' first; if not ready, call step='install' (large one-time download). " +
		"Everything lands in the OS user cache — nothing goes into the current workspace."
	return utils.InferTool("browser_use_install", desc, func(ctx context.Context, in *installInput) (*installOutput, error) {
		switch in.Step {
		case "check":
			st := browseruse.CheckInstall()
			return &installOutput{
				Step:          "check",
				DriverReady:   st.DriverReady,
				BrowsersReady: st.BrowsersReady,
				Message:       st.Message,
			}, nil
		case "install":
			if err := browseruse.DoInstall(); err != nil {
				return &installOutput{Step: "install", Message: "install 失败：" + err.Error()}, nil
			}
			st := browseruse.CheckInstall()
			return &installOutput{
				Step:          "install",
				DriverReady:   st.DriverReady,
				BrowsersReady: st.BrowsersReady,
				Message:       st.Message,
			}, nil
		default:
			return &installOutput{Step: in.Step, Message: "step 必须是 check 或 install"}, nil
		}
	})
}

