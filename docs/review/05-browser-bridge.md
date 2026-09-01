# 方向四 · 浏览器桥（krow 的 browser-bridge）

> 对应：「Agent 要能点用户正在用的那个 Chrome」。这篇只讲 **bridge**。
> 独立窗口那条见 [04-browser-use](04-browser-use.md)。更细的协议 / pending 表见 `docs/interview-notes/04-browser-bridge.md`。
>
> 代码：`/Users/guyi/krow-agent/app/internal/browser_bridge/`  
> 工具：`app/internal/tools/browser_bridge_tool.py`  
> Skill：`app/skills/browser-bridge/SKILL.md`
>
> LingCoWork 是同一套思路（Go + `chan`），代码在 `internal/agent/browserbridge/`。

---

## 一、这玩意是啥

use 起的是 **另一扇窗**。bridge 挂的是 **用户任务栏里那个 Chrome**。

用户装着扩展。扩展跟本机 agent 拉一条 WebSocket。模型调 `browser_bridge(...)`，后端经这条线发给扩展，扩展用 Chrome 自己的 API 去点、去填。一个进程、两个控制者：人和 Agent 都在操作同一扇窗。

```text
模型 → browser_bridge 工具
     → 本机 backend（krow：FastAPI；LingCoWork：Go）
     → WebSocket
     → Chrome 扩展
     → chrome.tabs / scripting … 动的是用户那个窗口
```

登录是真的：Cookie、SSO、已经打开的 Boss 页，都在。use 即使拷了 profile，也是另一份快照、另一扇窗；你后来在真 Chrome 里新登的，那边不会跟着变。要眼看着点、要接着用户已经打开的 tab，走这条。

代价：用户要装扩展。Skill 规定：先 `extension_status`，连上走 bridge；连不上才退回 use。两条不要混用。

---

## 二、简历主线一：客户端 WebSocket 端点开发

### 先说为什么要开发这个端点

Chrome 扩展虽然能调用 `chrome.tabs`、`chrome.scripting` 操作浏览器，但 Agent
无法直接调用扩展。两边需要一个本机通信端点，把 Agent 的同步工具调用转换成扩展能够
执行的 WebSocket 命令，再把异步结果送回来。

这里的“客户端端点”指的是**运行在用户电脑上的 krow 本地服务**，不是云端服务，
也不是 Chrome 扩展本身。我负责的是这段连接和转发链路：

```text
Agent 工具调用
  → 本地客户端端点
  → 找到目标 Chrome 与 tab
  → WebSocket 下发带 uuid 的命令
  → 扩展执行 Chrome API
  → 回包按 uuid 找到原调用
```

具体包含四件事：

1. 提供本机发现和 WebSocket 接入端点，让扩展能够找到并连接当前 krow 进程。
2. 处理 `hello` 握手，登记 `browser_id`，同步 tab 的新增、更新和关闭事件。
3. 维护浏览器与页面 Registry，让工具调用能够定位用户当前的 Chrome 和具体 tab。
4. 用 `uuid + Future` 等待命令结果；扩展回包后唤醒对应调用，断线时立即失败所有等待项。

这是一套自研的小协议，不是 MCP，也不是开源 browser-use。扩展里跑 gRPC 不现实，
所以使用 **WebSocket + JSON + uuid**。

### 1. 找到本机服务

扩展扫本地端口，带一个固定签名去 `ping`。对上了，后端给一个 **这次进程才有效** 的 token，再用 token 建 WS。

签名是双方写死的咒语，防的是误连别人的 localhost，不是防黑客。token 不落盘，backend 一重启就废。

### 2. 握手，再记 tab

WS 通了，扩展报 `hello`：这个 Chrome 资料夹是谁（`browser_id`）。后端 Registry 里记着：现在连着谁、开了哪些页。

之后用户开 tab、关 tab、切前台，扩展推 `page_updated` / `page_closed`。模型 `list_pages` 读的是这份 **内存镜像**，不用每次去问 Chrome。

两个 ID 别混：

- **session_id**：这一条 WS 连接。断了就没了。
- **browser_id**：这个 Chrome 资料夹。扩展重连往往还是它，agent 后续命令带这个。
- **page_id**：后端给 tab 发的稳定号。扩展内部用的是 Chrome 的 `tab_id`。用户把 tab 关了，page_id 作废，要重新 `list_pages`。

### 3. 命令怎么同步回来

WS 是异步的，工具调用要等结果。每条命令一个 uuid，后端塞进一张等待表：

- krow：`pending_results[id] = asyncio.Future`
- LingCoWork：`pending[id] = chan`

扩展回 `{type: "result", id: 那个uuid}`，再唤醒。断线就把表里没回来的全失败掉，别干等超时。

这是 RPC over WebSocket 的老做法。面试能画「发命令 → 塞表 → 等 → 按 id 唤醒」就够。别说成你发明了 WebSocket。

模型怎么「看见」页面，和 use 一样：`read_state` 吐编号，`click 5` 点第 5 个。编号是这一眼的快照，点完要再看。

---

## 三、简历主线二：Skill 打磨

### 为什么只有工具还不够

接通 Bridge 只代表“技术上能操作浏览器”，不代表模型会正确使用。
产品里同时存在 use 和 bridge 两条路径，如果不在 Skill 里约束，模型可能在扩展已连接时
仍然新开独立浏览器，也可能重复登录、点错 tab，甚至关闭用户原本打开的页面。

所以 Skill 打磨主要解决三类问题：

- **路径选择**：先探测扩展；可用就走 bridge，不可用才回退 use。
- **目标选择**：先列出 session 和 page，尽量复用用户已经打开的页面。
- **操作边界**：每次先读取页面再操作；登录和验证码交还用户；不擅自关闭用户 tab。

工具层不管「该不该走这条」。skill 写成硬规则：

1. 任何浏览器任务，先 `extension_status`。`ready=true` 整段走 bridge；`false` 立刻 load use，不要问用户「要不要装扩展」。
2. `list_sessions` 拿 `browser_id`，`list_pages` 先看用户是不是已经开了要用的页，能 `focus` 就别再新开。
3. 先看再点；登录 / 验证码 / 支付停下交给人。
4. **不要主动关用户的 tab**。那是他的 Chrome，不是我们拉起来的临时窗。
5. 已经走 bridge 了，不要同一条任务里再去调 use。

上了 bridge 之后，use 的 skill 也要改口：自己变成兜底，并补上「点完没变可能是新 tab」「登录是终点」。那是 [04](04-browser-use.md) 里 skill 调优要讲的，和这篇是一套事。

---

## 四、和 use 别讲混

| | use | bridge |
|---|---|---|
| 窗 | 新拉一个（系统 Chrome 或 Playwright Chromium） | 用户正在看的那个 |
| 谁执行 | 开源 CLI daemon + Playwright | 扩展 + Chrome API |
| 登录 | 拷 profile，是快照 | 真登录、真 SSO、真已开 tab |
| 用户眼看 | 另一扇窗 | 就是他的 Chrome |
| 没扩展时 | 能跑 | 跑不了 |

use 解决「没装扩展也能点页面」。bridge 解决「要进他正在用的窗口」。不是 bridge 淘汰 use。

krow 的 use 能拷 profile，所以「use 完全没登录」说重了。缺的是：**当前这扇窗、此刻的登录、人眼看着**。这些拷贝给不了。

产品里还有下载分段上传、扩展跨平台静默安装、抓 XHR 这些。机制知道有就行，面试别一串报完，也别揽成自己写的。

---

## 五、和 LingCoWork（被问到就说）

两边都是 **use + bridge**，工具长得像（`read_state` + 编号）。

LingCoWork 的 use **不拷 profile**，Boss 一类网站 skill 写死走 bridge。krow 的 use 能拷，没扩展时兜底不那么空白，但真登录、真眼看仍是 bridge。

协议同构：ping → token → WS → hello → 命令 uuid → 等待表。krow 用 Future，LingCoWork 用带缓冲 1 的 `chan`（接收协程回包时，调用方可能已经超时走了，无缓冲会把收包卡住）。

---

## 六、面试怎么收

**定场：**

> 浏览器我参与了两条路。use 是开源 CLI 开独立窗口，没扩展时兜底。bridge 是扩展挂到用户 Chrome，走 WebSocket。要登录、要接着已经打开的页、要人眼看着，走 bridge。

**use 只说你锁的两条**（见 04，别再加 daemon / Cookie 导出）：

1. 首次安装拆成 check / install / chromium，进度可见；有系统 Chrome 就不下内核。
2. 上了 bridge 之后改 skill：先探 bridge 再决定走哪条；点完页面没变要切 tab 看；登录 / 验证码停下来交给人。

**bridge 说机制 + 参与，不说「我写了整座桥」：**

> 我主要负责本地客户端的 WebSocket 端点：承接扩展连接和 hello 握手，
> 用 Registry 维护已连接的浏览器与 tab，再把 Agent 工具调用封装成带 uuid 的命令
> 下发给扩展，回包后按 id 唤醒等待中的调用。另一部分是 Skill 打磨：
> 先探测扩展状态，可用就走 bridge，不可用才回退 use；同时补上页面选择、
> index 刷新、登录交还用户和不关闭用户 tab 等规则。

**别讲混：**

- 不要说 use 和 bridge 共用同一份 `Default`。use 是拷贝；bridge 是同一个 Chrome 进程。
- 不要说「我写了 daemon」。那是开源 use。
- 不要把 index 过期说成断线。
- 不要把签名 ping 说成安全方案。它只防误连。
- 主线仍是 MCP / SkillHub。浏览器是参与过的能力，不是一个人的方向。
