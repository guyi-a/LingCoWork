# 方向四 · 浏览器操作（krow 的 browser-use）

> 对应：「Agent 要能自己上网点页面」。这篇只讲 **use** 这条独立浏览器路径。
> Bridge 见 [05-browser-bridge](05-browser-bridge.md)。更细的协议笔记见 `docs/interview-notes/04-browser-bridge.md`。
>
> 代码：`/Users/guyi/krow-agent/app/internal/tools/browser.py`  
> Skill：`app/skills/browser-use/SKILL.md`
>
> 先把「这是什么、命令怎么跑」讲完，再引入 session / profile。面试也按这个顺序，别一上来甩术语。

---

## 一、这玩意是啥

模型不会真的移动鼠标。要让 Agent 打开网页、点按钮、填表，必须有一个 **真的浏览器进程** 给它开着，再把「现在页上有哪些能点的东西」翻译成模型看得懂的文字。

krow 没有自研这一层，用的是开源 CLI **`browser-use`**：一条命令做一件事。

```text
browser-use open https://example.com
browser-use state          # 看看页上有哪些按钮，编成 1、2、3
browser-use click 5        # 点第 5 个
browser-use close
```

模型在对话里调工具 `browser_use(command="...")`，我们的包装器去项目 venv 里 spawn 这个二进制。前端只看到一张工具卡。

**为什么还要 Bridge：** 这条路径起的是 **另一个浏览器窗口**，不是你正在看的那个 Chrome。没登录、要验证码、用户要眼看着点，走扩展挂上真 Chrome。Skill 规定：先探 Bridge，连不上才用 use。

---

## 二、开源 browser-use 自己怎么做的

GitHub：[browser-use/browser-use](https://github.com/browser-use/browser-use)。它其实是 **两层产品**，面试别讲成「一个 Python 库」。

| 层 | 谁在想 | 干什么 |
|---|---|---|
| **Python Agent** | 库自己调 LLM | `Agent(task="去订机票").run()`：看页 → 选动作 → 再看，循环到做完 |
| **CLI**（`browser_use.skill_cli`） | **外面已经有模型**（Cursor / Claude Code / krow） | 模型只发 `open` / `state` / `click 5`；浏览器由 CLI 常驻 |

krow 用的是 **CLI**，没用它的 Agent 循环，也没用它的云端浏览器。决策在我们这边的对话循环里。

### 1. 命令短命，浏览器长命

CLI 前台故意只 import 标准库，启动要快。Playwright、DOM 扫描这些重的，丢给后台 **daemon**（一个 session 一个进程）。

```text
第一次  browser-use --session conv-xxx --headed open https://...
        → spawn  python -m browser_use.skill_cli.daemon --session conv-xxx
        → daemon 里才加载 Playwright，打开窗口
        → ~/.browser-use/ 写下 conv-xxx.sock / conv-xxx.pid / conv-xxx.state.json

以后    browser-use --session conv-xxx state
        browser-use --session conv-xxx click 5
        → 前台只活 ~50ms
        → Unix socket（Windows 是 TCP）把 JSON 动作发给同一个 daemon
        → 窗口一直开着

close   → daemon 关浏览器，自己也退
```

`--session` = daemon 的名字。不同名字 = 不同 socket = 不同浏览器。忘了带就落到默认 `default`，会点到别人的窗。这是 **CLI 自带的**，krow 包装器只是强制写成 `conv-{对话id}`。

### 2. 模型怎么「看见」页面

不是让模型写 CSS selector，也不是 CLI 自己再调一轮 LLM。

`state` 走库里同一套 DOM 扫描：从页面抽出可点击/可输入的节点，编成 `1、2、3…`（内部叫 selector map），再 `llm_representation()` 打成给模型看的树。`click 5` 在 daemon 里查编号 5 对应的节点，走 CDP 去点。

点完 DOM 变了，编号作废，要再 `state`。这是开源 CLI 的约定。

### 3. 浏览器三种开法（都在 daemon 里）

1. **默认**：Playwright 拉一份 Chromium，空白资料夹。
2. **`--profile Default`**：找到本机 Chrome 可执行文件和 User Data；**把 `Default` 拷到** `/tmp/browser-use-user-data-dir-xxx/`，再对着拷贝开。拷的时候跳过 `SingletonLock` 这类锁文件。Chrome 开着时 Windows 常拷失败（Cookie 被锁），Mac 有时能凑合。失败就提示关掉 Chrome，或改用下面这条。
3. **`--cdp-url` / `--connect`**：不新开窗口，挂到已经在跑的 Chrome（CDP）。这才是「进你正在用的那个进程」，和 Bridge 同类；krow 的 use 主路径不用这个。

另外还有他们自己的云端浏览器。krow 主路径是 **本地 daemon + 可选拷 profile**。

`profile list` 读的是 Chrome 的 `Local State`，列出「显示名 → 文件夹名」（比如 zhiling → `Default`）。`profile` 子命令后半截是另一个叫 `profile-use` 的 Go 程序，用来把 Cookie 同步到他们云上——**krow 没用那截**。

---

## 三、krow 包装器在它上面加了啥

裸 CLI 当生产工具不够用。开源项目已经解决了「命令怎么打进同一个浏览器」；我们补的是把它嵌进 Agent 产品里：

**1. 强制带上 `--session conv-{对话id}`**  
模型一次只发 `state`、`click 5`。CLI 忘带 session 就会进默认的 `default` daemon。包装器每次都拼上对话 id，并记住 `--headed` / `--profile`，后续命令自动复用。

**2. 安装拆开、装在项目里**  
CLI + Chromium 要下一两百 MB。`install_browser_use` 分成 check / install / chromium 三步，每步告诉模型下一步干什么，用户不至于对着黑屏以为卡死。二进制在 **这个 workspace 的 `.venv`**，卸项目能一起清掉。

**3. 默认看得见（`--headed`）**  
登录、验证码要人动手。看不见窗口，人没法接手。

**4. 关窗前把 Cookie 另存一份**  
`close` 前 `cookies export` 到项目里的 json；下次 `open` 若文件还在就先 import。给生成出来的自动化脚本重跑用，不是给「正在用的 Chrome」用的。

模型侧的操作循环和 LingCoWork 一样：**先看再点**。`state` 给出编号列表，模型说点第 N 个；编号是这一眼的快照，DOM 一变就作废，所以点完要再 `state`。

---

## 四、再讲 session / profile（Chrome 里的三层）

日常 Chrome 菜单里那个「zhiling / 已登录」，背后是磁盘上一个文件夹，例如：

`~/Library/Application Support/Google/Chrome/Default/`

里面分格：Cookie、密码、历史、扩展、**localStorage**。网页里的 `localStorage` 不是另一套系统，就是这份文件夹里的一格（按网站分开）。**sessionStorage** 才是标签一关就没。

操作浏览器时要分清三层，别都叫「状态」：

| 层 | 白话 | 关了还在吗 |
|---|---|---|
| **profile** | 你是谁。整袋行李（Cookie、登录） | 关浏览器还在磁盘上 |
| **session** | 这一次开着的窗。现在几个标签、停在哪一页 | 进程一关就没了 |
| **index** | 这一页按钮的临时编号 | 点一下、页面一变就作废 |

krow 包装器里对应：

- `--session conv-{对话id}`：**强制加上**。同一轮对话的 `open` / `click` 进同一个进程。
- `--profile <名字>`：第一次 `profile list` 再 `open` 时由模型带上，之后写进 `项目/.krow/browser-use/conv-xxx.json`，后续命令自动复用。

**`--profile` 不是两个 Chrome 一起写 `Default`。** Chrome 给正在用的那份文件夹加锁，同一时刻只能有一个主进程。browser-use 的做法是 **把 `Default` 拷到临时目录，对着拷贝再开一个浏览器**。登录能带过去，是因为 Cookie 被抄走了。

所以：这是当时的 **快照**。你后来在真 Chrome 里新登录的，不会自动同步过来。你 Chrome 开着时拷贝可能失败（文件被锁，Windows 更明显）。Agent 写坏的是临时拷贝，一般不会毁掉日常那份资料。

真正进「你正在看的那个窗口」、不拷贝的，是 Bridge（或 `--cdp-url` 连上已有进程）。

---

## 五、和 LingCoWork 的差别（被问到就说）

两边都是 **use + bridge**，工具形态也像（一个工具、`state` + 编号）。

LingCoWork 的 use 是 Playwright-Go：一个对话一个空的 `BrowserContext`，**有 session（对话里窗口接着用），没有 profile（不拷你的 Chrome 资料夹）**。所以 Boss 登不上，skill 写死走 bridge。

krow 的 use 多了 **拷 profile + Cookie 文件**。没装扩展时，兜底不再是完全空白的浏览器。

LingCoWork **可以**照做：拷 `Default` 到临时目录，再用 `LaunchPersistentContext` 开拷贝。不该直接去抢正在用的那份文件夹。做成 use 的增强兜底即可，不必取代 bridge。

---

## 六、面试怎么收

**一句话：** 开源 `browser-use` CLI 用后台 daemon + Playwright 把浏览器常驻，短命令经 socket 打进去；krow 包装器按对话强制 `--session`，需要登录时让 CLI 拷一份 Chrome profile。连不上用户正在用的窗口时用这条；要真登录、真眼看，走 Bridge。

**别讲混：**

- 不要说「krow 两个进程共用 Default」——CLI 是拷贝。
- 不要说「use 完全无状态」——daemon 就是会话；LingCoWork 缺的是登录态（不拷 profile）。
- 不要把 index 过期说成没登录。
- 不要说协议层是我们写的——daemon / `state` / `--profile` 拷贝都是开源项目的。

**你开口只锁两条（别把上面包装器四条全揽过来）：**

1. 首次安装拆成 check / install / chromium，进度可见；有系统 Chrome 就不下内核。
2. 上了 bridge 之后改 skill：先探 bridge 再决定走哪条；点完没变要切 tab；登录 / 验证码停下来交给人。

**诚实边界：** daemon / `state` / 拷 profile 是开源 CLI。你参与的是安装可视，以及和 bridge 配套的 skill。Bridge 见 [05](05-browser-bridge.md)。
