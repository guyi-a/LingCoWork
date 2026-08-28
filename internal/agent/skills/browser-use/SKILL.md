---
name: browser-use
description: 浏览器任务的**兜底路径**——起一个独立的 Chromium 完成任务。**只在** browser-bridge 的 extension_status.ready=false（用户没装扩展或扩展没连上）时用这条。触发条件：先 load browser-bridge → extension_status → ready=false 才 load 这个 skill。也用于需要截图落盘 / 独立环境的场景。适用工具：browser_use / browser_use_install。不适用：ready=true 时（应该走 browser-bridge）。
---

# Skill: browser-use（浏览器自动化）

驱动一个独立 Chromium 完成需要真实浏览器渲染的任务。

## 工作流（按顺序）

1. **环境检查**（首次使用）：`browser_use_install(step='check')`。返回 `driver_ready=false` 时按提示调 `browser_use_install(step='install')`。安装完成前不要调 `browser_use`。
2. **打开页面**：`browser_use(action='open_tab', url='https://...')` → 记住返回的 `page_id`
3. **观察状态**：`browser_use(action='read_state', page_id=...)` → 拿到一份编号的可交互元素列表（button / a / input / textarea / select）
4. **执行动作**：按 index 调 `click` / `hover` / `dblclick` / `rightclick` / `type` / `press` / `extract`；页面级动作用 `scroll` / `go_back` / `reload` / `wait_for` / `screenshot` / `execute_script`；多 tab 用 `focus_page` 切换
5. **验证生效**：**每次交互后再 read_state 一次**，看 URL / 页面元素是否变化。exit=0 只代表命令跑了，不代表业务动作成功
6. **收尾**：任务做完 `browser_use(action='close_session')` 释放浏览器。用户可能想继续观察 → 问一下再关

## Action 速查

| Action | 参数 | 用途 |
|---|---|---|
| `open_tab` | url | 新开 tab 加载 URL，返回 page_id |
| `list_pages` | — | 列当前 session 所有 tab |
| `focus_page` | page_id | 把 tab 提到前台 |
| `close_tab` | page_id | 关一个 tab；最后一个自动 close_session |
| `close_session` | — | 释放浏览器 |
| `read_state` | page_id | 拿编号 markdown |
| `click` / `hover` / `dblclick` / `rightclick` | page_id, index | 点击变体 |
| `type` | page_id, index, text | 填输入框（先清空） |
| `press` | page_id, key, index? | 键盘按键，index 省略作用于整页 |
| `scroll` | page_id, dx, dy | 相对滚动，正 dy = 向下 |
| `wait_for` | page_id, selector 或 timeout_ms | 等某元素或纯等 |
| `go_back` / `reload` | page_id | 浏览器导航 |
| `extract` | page_id, index, include_html? | 拿元素文本（可选 HTML） |
| `screenshot` | page_id, save_path?, full_page? | 截图；save_path 空则返回 base64（仅限小图） |
| `execute_script` | page_id, script | 跑任意 JS，返回值必须 JSON 可序列化 |

## 硬性纪律

### 一 · 操作成功 ≠ 业务成功

- 点了"登录"按钮不等于登录成功 —— 可能弹了验证码 / 密码错 / 2FA
- 点了"提交"不等于表单接收 —— 可能字段校验红
- `type` 完只是注入 DOM，不代表触发提交

**每个关键动作后必须再 read_state**，用以下证据之一确认：
1. URL 变成预期下游页
2. 页面出现明确成功标识（订单号、"已提交"、用户头像等）
3. 出现明确失败信号（错误提示、红框）—— 别装看不见

证据不齐不能报"已完成"。

### 二 · 用户阻断 = 立刻交回，不绕过

遇到这些场景**立刻停手**，把状态告诉用户：

- 登录页要账号密码 → 不要凭空猜/试/注册
- CAPTCHA / reCAPTCHA / 图形验证码 → 不要尝试自动识别
- 2FA / 短信码 / 邮箱链接 → 等用户提供
- 银行 / 支付 / 实名页 → 不要替用户操作
- "我同意" 法律条款 → 必须用户自己点

汇报格式：当前 URL + 阻断原因 + 你需要用户做什么。

### 三 · index 只在最近一次 read_state 之后有效

`read_state` 给的编号是 DOM 快照下的临时指针，页面一动全部失效。

- **每次操作前重新 read_state 拿最新 index**
- 不要相信几步之前的 index
- click / type / press 之后想再操作 → 再读一次

### 四 · page_id 有生命周期

- `close_tab` 到最后一个 tab **会自动 close_session**，之后所有 page_id 作废
- 用户前端删除会话也会 close_session
- 报 "page_id not found" 时不要重试相同 page_id，重新 `open_tab` 或用 `list_pages` 拿现有 page

### 五 · screenshot 强制工作区

- 截图会写入**当前会话绑定的工作区**，落到 `workspace/screenshots/shot-<timestamp>.png`（不传 `save_path` 时的默认）
- 用户明确指定 `save_path` 必须是**相对路径**，会拼到 workspace 前面；绝对路径会被拒绝
- **未绑定工作区时截图会失败** —— 请用户先在 LingCoWork 中选择工作区文件夹再截图
- 只是想让自己"看到"页面 → 用 `read_state`（返回编号 markdown），不要为了观察去截图
- 只有真正要**存给用户看**的图才该 screenshot

### 六 · 不同 Chrome 进程

我们启动的是 playwright 控制的独立 Chromium 进程，跟用户日常 Chrome 完全隔离：

- 看不到用户已登录的 cookies / bookmarks
- 需要登录的站点得让用户自己登（走纪律二）
- 用户从 Dock 主动关 chromium 是**没用的**（playwright 会拦），必须走 `close_session` 或后端进程退出

## 键盘键名（press action 用）

单键：`Enter` / `Escape` / `Tab` / `Backspace` / `ArrowUp` / `ArrowDown` / `ArrowLeft` / `ArrowRight` / `PageUp` / `PageDown` / `Home` / `End`

组合键用 `+` 连接：`Control+A` / `Meta+K`（macOS 的 Cmd 用 `Meta`）/ `Shift+Tab`

## 失败处理

| 现象 | 措施 |
|---|---|
| tool 报 "chromium 未安装" | 先 `browser_use_install(step='check')`，再按提示 install |
| `read_state` 空 / 报 no page | session 可能没了，重新 `open_tab` |
| `click` 报 "index 越界" 或 "index 无效" | 页面变了，重新 read_state 拿新 index |
| 操作后页面无变化、URL 未变 | 可能是新开了 tab（`list_pages` 检查），或按钮 disabled；也可能页面加载慢，等几秒再 read_state |
| 超时 | 页面卡死或网络坏，别盲重试；把状况告诉用户 |
| tool 报 "page_id not found" | session 已关，重新 `open_tab` |

## 不该做

- 用不存在的 action（清单见上面 "Action 速查"）
- 一次 `open_tab` 多个 URL —— 一次一个
- 猜 page_id 或 index
- 用 `execute_script` 做 `click` / `type` / `scroll` 能做的事 —— 优先原生 action，只有真需要复杂 DOM 抽取时才用 script
- `screenshot` 不传 `save_path` 时返回 base64，页面大就爆炸；抓页面内容应该走 `read_state` + `extract`
- 让用户重复登录相同网站 —— cookies 会被 session 保留到 close_session 为止

## 交付总结（跟用户报告用）

- 做了什么（打开哪个网站、抓到什么、点了什么按钮）
- 关键的 URL / 页面元素证据
- 浏览器状态：still open / 已关闭
- 是否需要用户接手（登录、验证码、2FA 等）
