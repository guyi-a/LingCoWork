# 场景题科普：可能被问什么、怎么答

> 面向 Agent / 后端背景、前端基础薄弱的同学。  
> 基础概念见 [06-frontend-basics.md](06-frontend-basics.md)；已考过的展开见 [05-concept-notes.md](05-concept-notes.md)。  
> 题目清单打 ✔ 的在 [00-common-questions.md](00-common-questions.md)。

**怎么用本文：**

1. 先扫 **§一 分类地图**，知道场景题从哪几个方向出
2. 每类看 **「考点 + 答法骨架」**，不用背代码
3. 标了 **✔ 已考** 的优先熟读
4. 和 Agent 项目相关的（流式、SSE、Electron）对照 LingCoWork / klingwork 代码理解

---

## 一、场景题分类地图

面试官给一段**产品描述**，让你说**怎么做**——通常落在下面八类：


| 类别              | 典型话术            | 主要考                      |
| --------------- | --------------- | ------------------------ |
| **A. 布局与样式**    | 「设计稿这样排」「两栏自适应」 | CSS flex/grid、盒模型        |
| **B. DOM 与事件**  | 「点击、拖拽、表单」      | 事件流、委托、默认行为              |
| **C. 异步与网络**    | 「搜索联想、上传、轮询」    | Promise、防抖、fetch、SSE     |
| **D. 跨页面/跨标签**  | 「多 tab 同步状态」    | BroadcastChannel、storage |
| **E. 性能**       | 「长列表卡顿」         | 虚拟列表、防抖、重排               |
| **F. React 组件** | 「状态放哪、怎么复用」     | state/props、hooks        |
| **G. 浏览器机制**    | 「304、同源、缓存」     | HTTP、安全模型                |
| **H. 与后端协作**    | 「联调、错误处理」       | REST/SSE、错误边界            |


百度二面已出现：**D（视频互斥）**、**G（304 缓存）**、间接 **F（设计稿落地）**。布局与样式基础见 [06-frontend-basics.md §三](06-frontend-basics.md)，本文不展开。

---

## 二、B. DOM 与事件场景



### B1. 列表里上千个按钮，怎么绑点击？

**答法**：**事件委托**——在父 `ul` 上监听一次 `click`，`event.target` 判断是否点中 `li`，读 `data-id`。

**好处**：动态增删项不用重新绑监听器；内存更少。

---



### B2. 表单提交：前端要做什么？

**答法骨架**：

1. 阻止默认提交 `preventDefault()`（若用 fetch 发）
2. **校验**：必填、格式、长度（HTML5 `required` 或 JS）
3. 禁用按钮防重复提交
4. 请求中 loading；成功/失败提示
5. 错误信息展示在字段旁

---



### B3. 拖拽上传文件

**答法**：监听 `dragover`（`preventDefault` 才能 drop）、`drop` 取 `event.dataTransfer.files`；Electron 另走 preload 暴露路径（见 [05 §六](05-concept-notes.md)）。

---



### B4. 复制聊天选中文字 + 浮层工具栏 ✔ 项目相关

klingwork `SelectionToolbar`：监听 `mouseup` / `selectionchange`，找最近 `data-message-id` 祖先，算选区位置浮层 — 考点 **Selection API + 事件 + 定位**。

---



## 三、C. 异步与网络场景



### C1. 搜索框输入联想（debounce）✔ 极高频

**场景**：用户打字，每键不打 API，停 300ms 再请求。

```javascript
function debounce(fn, ms) {
  let t;
  return (...args) => {
    clearTimeout(t);
    t = setTimeout(() => fn(...args), ms);
  };
}
input.addEventListener("input", debounce(() => fetchSuggestions(), 300));
```

**追问**：debounce vs throttle？  
→ debounce **停下来的那一次**；throttle **固定间隔最多一次**（滚动监听常用 throttle）。

---



### C2. 请求失败重试 / 超时

**答法**：`AbortController` 设 timeout；失败 **指数退避** 重试 1~2 次；最终 toast 错误；Agent 项目里 **和用户 abort 区分**（别当网络错误无限重试）。

---



### C3. 长轮询 vs WebSocket vs SSE ✔ 和项目强相关


| 方式        | 特点                    | 典型用途                       |
| --------- | --------------------- | -------------------------- |
| 长轮询       | 实现简单，开销大              | 老系统                        |
| WebSocket | 双向                    | 游戏、协作编辑                    |
| **SSE**   | 单向 server→client，HTTP | **AI 流式输出**（LingCoWork 聊天） |


答场景题：**聊天流式用 SSE**  enough；要客户端频繁上行再 WebSocket。

---



### C4. 同时发多个请求，怎么等全部完成？

`Promise.all([a, b, c])` — 一个失败全失败。  
要部分成功：`Promise.allSettled`。

---



### C5. 文件分片上传 / 大文件

**答法**：`File.slice` 分 chunk → 每片 POST → 服务端合并；断点续传带 uploaded 序号；进度条 `loaded/total`（`xhr.upload.onprogress` 或 fetch + ReadableStream）。

---



## 四、D. 跨页面 / 跨标签场景



### D1. 多 tab 视频互斥播放 ✔ 已考

见 [05-concept-notes.md §九](05-concept-notes.md)：

- 同页：`play` 事件 + Set 管所有 `<video>`  
- 跨 tab：`BroadcastChannel`（Go channel 类比正确）  
- 降级：`localStorage` + `storage` 事件

---



### D2. 多 tab 登录态同步

**答法**：

- 登录成功写 `localStorage` token → 其他 tab `storage` 事件刷新  
- 或 `BroadcastChannel` 广播 logout  
- 注意 **httpOnly cookie** 不会进 localStorage，看你们 auth 方案

---



### D3. 页面 A 改数据，页面 B 列表要更新

同 D2；或 **SharedWorker**（少用）；终极方案 **WebSocket 推送** 各 tab 刷新。

---



## 五、E. 性能场景



### E1. 聊天记录一万条，滚动卡 ✔ 和 Agent 项目相关

**场景**：Agent 聊天页历史很长（多轮 + 工具卡 + Markdown），滚动/流式更新时掉帧。

**先诊断卡在哪**（面试先讲 10 秒）：

| 瓶颈 | 原因 |
|------|------|
| **DOM 太多** | 一万条 = 一万棵子树，布局/绘制/滚动都贵 |
| **流式更新** | 每来一个 SSE chunk，`turns` 变 → 父组件重渲染 |
| **Markdown** | 把字符串 parse 成 AST 再生成 DOM，比纯文本贵一个数量级 |
| **图片/代码块** | 大图解码、高亮、数学公式（KaTeX）更吃 CPU |

下面四条按 **收益从大到小** 说；Agent 项目里 **Markdown 范围控制** 和 **虚拟列表** 最常考。

---

#### 1. 虚拟列表（收益最大，一万条必做）

**思路**：滚动容器里**只挂载视口附近 ± buffer 的 DOM**，离开视口的条目卸载或保留占位高度。

```
全量渲染：10000 条 × 每条约 50 个 DOM 节点 ≈ 50 万节点
虚拟列表：视口 ~15 条 + buffer ~10 条 ≈ 25 条在树上
```

**实现**：

- 库：`react-window`、`@tanstack/react-virtual`
- 自研：`scrollTop` + 每项 `estimatedHeight` + 累加 offset，算 `startIndex`/`endIndex`

**聊天列表难点**（可口述加分）：

- 消息**高度不固定**（Markdown、工具卡、折叠的思考块）→ 需要 **动态高度测量** 或「先估高、滚完再校正」
- **流式最后一条**高度一直在变 → 通常最后一条不走虚拟化，或单独渲染在列表底部
- **滚到底** / 新消息自动跟随：Agent 产品常见，虚拟列表要处理 `scrollToIndex` 和「用户上滑看历史时不强拉到底」

**LingCoWork 现状**：`Transcript.tsx` 仍是 `turns.map` **全量渲染**，会话一长会吃紧；优化方向是 `TranscriptEntry` 进虚拟列表，**最后一条 assistant 可例外**。

---

#### 2. 避免整列表跟着流式重渲染（React memo + 稳定 key）

**稳定 key**：`key={turn.id}`，别用 `index`。中间插入/删除时 index 会变，React 错复用 DOM，输入框内容错位、状态串台。

**memo 粒度**：按 **一条消息（或一个 segment）** 包一层，而不是只 memo 最内层文字。

LingCoWork 已做一半：

```tsx
// Transcript.tsx — 只有最后一条 assistant 带 streaming
streaming={streaming && i === turns.length - 1 && t.role === "assistant"}

// MessageBody.tsx — 自定义比较，content 不变就跳过 Markdown 重算
export const MessageBody = memo(
  ({ content, dense, streaming }) => ( ... ),
  (prev, next) =>
    prev.content === next.content &&
    prev.dense === next.dense &&
    prev.streaming === next.streaming,
);
```

**还要补的**（面试可说「下一步会怎么做」）：

| 问题 | 改法 |
|------|------|
| `TranscriptEntry` 未 `memo` | 包 `memo(TranscriptEntry)`，仅 `turn` 引用变的那条重渲染 |
| `allSubEvents` / `ownedToolIds` 每次 `turns` 变都新建 | 按 turn 切片下发，或 `useMemo` 到单条；别让历史消息的 props 每 chunk 都变 |
| 父组件传内联函数/对象 | `useCallback` / 稳定引用，否则 memo 失效 |

**原则**：流式时 **只让「正在变的那一条」重渲染**；上面 9999 条 props 不变 → `memo` 直接跳过。

---

#### 3. Markdown 重渲染范围缩小（Agent 聊天重点展开）

Markdown 是 Agent UI 最贵的一环：GFM 表格、代码块、公式都要 parse。

**策略 A — 流式 vs 静态分两档**

LingCoWork `MessageBody` 用 Streamdown：

```tsx
mode={streaming ? "streaming" : "static"}
```

- **streaming**：增量解析，只追最后一条的 tail，适合打字机
- **static**：消息 `done` 后切 static，**不再每 chunk 全量 re-parse**

面试句：> 历史消息一旦完成，就按静态 Markdown 缓存；只有当前 assistant 走 streaming 模式。

**策略 B — 缩小「谁带 streaming」**

只有 `Transcript` 里**最后一条 assistant** 传 `streaming={true}`，历史条全是 `static`。  
避免「一条 SSE 更新 → 一万条都带 streaming 重算 Markdown」。

**策略 C — 按 segment 切，不整 turn 一颗 Markdown**

Agent 一轮里可能是：`思考 → 正文 → tool_call → 再正文`。  
LingCoWork 用 `ReactSegment[]`：每个 segment 单独一个 `MessageBody`，工具卡和文字分开。  
好处：工具状态更新时，**不必重跑整段正文的 Markdown**。

**策略 D — 降频 / 截断**

| 手段 | 场景 |
|------|------|
| 流式 **节流**（如 50–100ms 合并一次 `setTurns`） | 减少 parse 次数 |
| 工具结果展示 **truncate**（项目里 tool content 截 1200 字） | 超长 shell 输出别全进 Markdown |
| 思考块 **默认折叠** | 少渲染、少 parse |
| 代码高亮 **延迟**（进视口再 highlight） | 长代码块 |

**策略 E — 别在列表里做昂贵插件**

`remark-math` + `rehype-katex` 很重。可对**历史消息**降级（纯文本预览 / 点击再渲染公式），或仅最后一条开 KaTeX。

**策略 F — 和虚拟列表配合**

虚拟列表卸载视口外 DOM 后，Markdown 的 parse 结果也一起卸掉；滚回来会 re-mount。可对**已完成的静态消息**做 parse 结果缓存（`Map<turnId, rendered>`），滚回视口时 O(1) 恢复。

---

#### 4. 图片 lazy load

聊天里的截图、附件预览：

```html
<img loading="lazy" decoding="async" />
```

- 视口外图片不解码，滚动更顺
- 占位 `width/height` 或 `aspect-ratio`，避免加载后 **layout shift** 把滚动条顶乱
- 大图缩略图 + 点击再看原图（LingCoWork `ImageTile` / workspace preview 同类思路）

---

#### 口述骨架（30 秒）

> 一万条聊天卡，首先是 **DOM 太多**，上 **虚拟列表** 只渲染视口。Agent 还有 **流式 SSE**，要 **稳定 key + memo**，保证只有最后一条 assistant 跟着 chunk 更新。最贵的是 **Markdown**：历史消息用 static、按 segment 拆开、工具结果截断，流式节流；图片 **lazy load**。我们项目 `MessageBody` 已 memo + streaming/static 分档，列表还是全量 map，下一步会上虚拟列表并把 `TranscriptEntry` memo 化。

**项目对照**：

| 文件 | 现状 |
|------|------|
| `Transcript.tsx` | 全量 `map`；`key={t.id}`；仅最后一条 `streaming` |
| `MessageBody.tsx` | `memo` + Streamdown `streaming`/`static` |
| `TranscriptEntry.tsx` | 按 segment 多个 `MessageBody`；工具内容 truncate |
| 缺口 | 虚拟列表、`TranscriptEntry` memo、props 稳定化 |

---



### E2. 输入框每个字都触发父组件全树渲染

**答法**：状态下沉；输入框独立组件；`useCallback`/`memo`；Controlled input 只更新局部 state。

---



### E3. 频繁改 DOM 样式导致卡顿

**答法**：批量改 class 而不是逐条改 style；用 `transform`/`opacity` 做动画（合成层，少触发 layout）；读布局属性（offsetHeight）和写样式 **交替** 会强制 sync layout — 批量读再批量写。

---



### E4. 首屏加载慢

**答法骨架**：代码分割（路由 lazy）、Tree shaking、CDN、压缩、gzip/brotli、图片 WebP、关键 CSS 内联、defer script — 讲 3~4 个即可，不必全背。

---



## 六、F. React 场景题

> 概念展开：[08-react-state-hooks.md](08-react-state-hooks.md)



### F1. 状态放父组件还是子组件？

**答法**：**谁需要这个数据谁持有，或最近的共同祖先**。  
只有输入框自己用的 → 子组件；多个兄弟要读 → 提升到父；跨很远 → Context / 状态库（Zustand 等 — LingCoWork `web/src/stores/`）。

---



### F2. useEffect 用来干什么？滥用会怎样？

**答法**：同步 **外部系统** — 请求、订阅、手动 DOM、定时器。  
滥用：把_derived state_ 全塞 effect 会导致重复请求、竞态；**能 render 算出来的不要放 state**。

---



### F3. 列表增删改，key 为什么不能用 index？

**答法**：中间插入/删除时 index 变，React **错复用 DOM**，输入框内容错位、动画乱。用 **稳定 id**。

---



### F4. 封装一个 Modal / Dialog

**答法要点**：`open` prop；portal 挂到 `document.body`；焦点 trap；Esc 关闭；`aria-modal`；点击遮罩关闭（可选）。

---



### F5. 自定义 Hook：useChatStream ✔ 项目相关

LingCoWork 流式：`fetch` +读 SSE body stream → 解析 event → 更新 messages state → cleanup abort on unmount。  
考点：**effect cleanup 里 abort**、**闭包 stale state** 用 ref 或 functional update。

---



## 七、G. 浏览器机制场景



### G1. 强缓存 vs 协商缓存，304 ✔ 已考

见 [05-concept-notes.md §七](05-concept-notes.md)。  
SSE 必须 `Cache-Control: no-cache` — 别被浏览器缓存成旧流。

---



### G2. 同源策略 / CORS

**场景**：前端 `localhost:5173` 调 API `localhost:8080`。

**答法**：跨 **源**（协议+域名+端口任一不同）→ 浏览器默认拦响应；服务端加 `Access-Control-Allow-Origin`；预检 OPTIONS（非简单请求）。

---



### G3. Cookie / localStorage / sessionStorage


|                | 生命周期    | 自动随请求发送 | 容量   |
| -------------- | ------- | ------- | ---- |
| Cookie         | 可设过期    | 是（同域）   | ~4KB |
| localStorage   | 永久除非清   | 否       | ~5MB |
| sessionStorage | 关 tab 没 | 否       | ~5MB |


---



### G4. XSS 怎么防？

**答法**：不拼 `innerHTML` 用户输入；React 默认转义文本；富文本用 DOMPurify；CSP 头；httpOnly cookie 存 token。

---



### G5. Electron 渲染进程为何不能直接读盘 ✔ 已考

见 [05-concept-notes.md §六](05-concept-notes.md)：`nodeIntegration: false` + preload IPC / 自定义协议。

---



## 八、H. 与后端协作场景



### H1. 前后端联调接口对不上

**答法**：OpenAPI/契约（你们 contracts/Zod）；Mock server；Network 面板看 request/response；错误码约定；id 类型 string vs number 对齐。

---



### H2. 流式接口前端怎么消费 ✔ 项目必考

```javascript
const res = await fetch("/api/chat/stream", { method: "POST", body, signal });
const reader = res.body.getReader();
const decoder = new TextDecoder();
while (true) {
  const { done, value } = await reader.read();
  if (done) break;
  const chunk = decoder.decode(value, { stream: true });
  // 按 SSE 规范 parse event/data 行
}
```

配合 **AbortController** 实现 Stop。

---



### H3. 鉴权 token 放哪？

**常见**：内存 + refresh；或 httpOnly cookie（防 XSS 偷）；localStorage 方便但 XSS 风险大 — **说 tradeoff**。

---



## 九、「万能答题骨架」（任何场景题）

面试官描述完，按这五步说，不会哑火：

```
1. 澄清需求：终端？数据量？要不要离线？实时性？
2. 技术选型：原生 / React / 要不要跨 tab
3. 数据结构：state 长什么样
4. 关键流程：用户操作 → 事件 → 请求 → 更新 UI
5. 边界与坑：错误、竞态、性能、安全
```

例 — **「做一个 todo 列表」**：

> Web 单页即可。state 用 `{ id, text, done }[]`。增删改本地 state，可选 persist localStorage。列表用稳定 key；项多用虚拟列表。若多人协作再上 WebSocket — 这题不需要。

---



## 十、自测清单（考前过一遍）


| #   | 场景              | 能否讲清思路 | 文档                               |
| --- | --------------- | ------ | -------------------------------- |
| 1   | 搜索 debounce     |        | §C1                              |
| 2   | 多 tab 视频互斥      | ✔      | §D1 / 05§九                       |
| 3   | 304 / 缓存        | ✔      | §G1 / 05§七                       |
| 4   | 长列表卡顿           |        | §E1                              |
| 5   | SSE 流式消费        |        | §H2                              |
| 6   | 事件循环输出顺序        |        | [06 §四.4](06-frontend-basics.md) |
| 7   | sidebar / 聊天页布局 |        | [06 §三](06-frontend-basics.md)   |
| 8   | flex vs grid    |        | [06 §三.3](06-frontend-basics.md) |
| 9   | Electron 读文件    | ✔      | §G5 / 05§六                       |
| 10  | CORS            |        | §G2                              |
| 11  | 事件委托            |        | §B1                              |


---



## 十一、和 00 清单的对应


| 00#   | 主题                    | 本文章节              |
| ----- | --------------------- | ----------------- |
| 15~18 | 流式 / markdown / chunk | §C3、§F5、§H2       |
| 19    | Electron 读盘           | §G5               |
| 20    | 304 缓存                | §G1               |
| 22    | 视频互斥                  | §D1               |
| 23    | Figma 还原              | [05 §十](05-concept-notes.md) |


---



## 关联文档

- [06-frontend-basics.md](06-frontend-basics.md) — 三件套基础  
- [05-concept-notes.md](05-concept-notes.md) — 已考展开  
- [02-streaming.md](02-streaming.md) — 项目流式实现  
- [00-common-questions.md](00-common-questions.md) — 题目清单
- [08-react-state-hooks.md](08-react-state-hooks.md) — state / hooks 详解

