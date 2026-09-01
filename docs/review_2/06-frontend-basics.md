# 最小必要 + 看懂 LingCoWork `web/`

> 面向 Agent / 后端背景、没系统学前端的同学。  
> **目标不是变成前端专家**，而是：能读项目代码、能答二面基础追问（盒模型、flex、事件循环、fetch/SSE）。

---

## 怎么用本文（先看这个）

| 步骤 | 做什么 |
|------|--------|
| 1 | 读 **§二 一张图**，建立浏览器 → React 的整体感 |
| 2 | 读 **§三～§五** HTML / CSS / JS，每节末尾有「面试一句」 |
| 3 | 打开 **§六 项目导读**，对照 `web/src` 真实文件走一遍 |
| 4 | 考前扫 **§七 速记**；场景题答法去 [07-frontend-scenarios.md](07-frontend-scenarios.md) |
| 5 | React state / hooks / 自定义 hook → [08-react-state-hooks.md](08-react-state-hooks.md) |

**本文不负责什么（别在这里找）：**

| 想学什么 | 去哪 |
|----------|------|
| 「设计稿怎么落地」「长列表怎么优化」等场景题 | [07-frontend-scenarios.md](07-frontend-scenarios.md) |
| `useState` / `useEffect` / `useChatStream` 详解 | [08-react-state-hooks.md](08-react-state-hooks.md) |
| 跨 tab 视频互斥、304 缓存、Electron IPC 展开 | [05-concept-notes.md](05-concept-notes.md) |
| SSE 流式协议 | [02-streaming.md](02-streaming.md)、[interview-notes/01-sse-implementation.md](../interview-notes/01-sse-implementation.md) |
| 右侧 workspace 按类型预览 | [09-workspace-preview.md](09-workspace-preview.md) |

---

## 二、一张图：从 URL 到你在改的 React 组件

```
用户打开页面
  → 浏览器下载 HTML，建 DOM 树（页面结构）
  → 加载 CSS，算样式，和 DOM 合成渲染树 → 布局 → 绘制
  → 执行 JS（含 React 打包后的 bundle）
  → React 根据 state 输出 JSX，更新 DOM
  → 用户操作 → 事件 → 改 state / 调 API → 再渲染
```

**三件套分工：**

| | 管什么 | 在项目里对应 |
|---|--------|--------------|
| **HTML** | 结构上「有什么」 | JSX 里的 `<div>`、`<button>`、`<video>`… |
| **CSS** | 长什么样、怎么摆 | Tailwind 的 `className="flex gap-2 …"` |
| **JS** | 交互、异步、请求 | `onClick`、`fetch`、`useChatStream` 里的 SSE 解析 |

React、Tailwind、TypeScript 都是**叠在三件套上的工程工具**，不是第四件套。

---

## 三、HTML：只记面试和读代码需要的

### 3.1 标签与语义

```html
<header>…</header>   <!-- 页头区域，不是随便的 div -->
<nav>…</nav>           <!-- 导航 -->
<main>…</main>         <!-- 主内容 -->
<button>发送</button>  <!-- 真按钮：键盘可用、无障碍 -->
<div>…</div>           <!-- 无语义容器，块级 -->
<span>…</span>         <!-- 无语义容器，行内 -->
```

**面试一句**：`div` 块级独占一行；`span` 行内不换行。按钮用 `<button>`，别用 `<div onClick>`。

### 3.2 JSX 和 HTML 的三处不同（读 `web/` 必知）

```tsx
// 1. class → className（class 是 JS 保留字）
<div className="flex gap-2">

// 2. 事件是驼峰 + 传函数引用
<button onClick={handleSend}>发送</button>

// 3. 自闭合标签在 TSX 里要闭合
<input type="text" />
```

### 3.3 项目里常见标签

| 标签 | 在哪见到 | 干嘛 |
|------|----------|------|
| `<aside>` | `Sidebar.tsx` | 侧边栏语义 |
| `<main>` | `App.tsx` | 主内容区 |
| `<button type="button">` | 各处 | 避免在 form 里误触提交 |
| `aria-label` | 折叠侧栏按钮 | 无障碍：读屏能知道按钮干啥 |

---

## 四、CSS：能看懂 Tailwind + 答布局题

LingCoWork `web/` 用 **Tailwind CSS v4**，几乎不写独立 `.css` 文件（主题变量在 `style.css`）。

### 4.1 盒模型（必考）

每个元素都是一个盒子，从外到内：`margin` → `border` → `padding` → `content`。

```css
box-sizing: border-box;  /* width 含 padding+border，项目里默认就是这个 */
```

**面试一句**：相邻块级元素垂直 `margin` 会**折叠**，20+20 往往还是 20，不是 40。

### 4.2 Flex：项目里 90% 的布局靠它

整页骨架在 `App.tsx`：

```tsx
<div className="flex h-full w-full gap-3 …">
  <Sidebar />   {/* 左：固定宽 280px，shrink-0 */}
  <main className="min-w-0 flex flex-1 flex-col overflow-hidden …">
    <Outlet />  {/* 右：占满剩余，内部再 flex-col */}
  </main>
</div>
```

对照记忆：

| Tailwind | CSS 含义 | 为啥项目里要 |
|----------|----------|--------------|
| `flex` | `display: flex` | 横向排 sidebar + main |
| `flex-col` | 纵向排 | 聊天页：Transcript 上、输入框下 |
| `flex-1` | `flex: 1` | 占满剩余高度/宽度 |
| `shrink-0` | 不缩小 | 侧栏别被挤扁 |
| `min-w-0` / `min-h-0` | 允许 flex 子项缩小 | **不传会溢出**：长文本把布局撑破 |
| `overflow-y-auto` | 超出滚动 | 消息列表 `Transcript` |
| `gap-3` | 子元素间距 | 替代手写 margin |

聊天页 `conversation.tsx` 里是 **横向 flex + 左栏纵向 flex**：

```
ConversationHeader
└─ flex-1 min-h-0 flex          ← 左：聊天 | 右：WorkspacePanel
   ├─ flex-1 flex-col            ← 上：Transcript | 下：PromptInput
   └─ WorkspacePanel
```

**面试一句**：一条线上的对齐、均分、居中 → **Flex**；要同时定行和列的棋盘格 → **Grid**。我们项目整页分栏用 flex 就够。

### 4.3 定位与层级（次常考）

| `position` | 行为 |
|------------|------|
| `static` | 默认，正常流 |
| `relative` | 相对自己偏移，仍占位 |
| `absolute` | 相对最近非 `static` 祖先，脱离文档流 |
| `fixed` | 相对视口固定 |

`z-index` 只在非 `static` 元素上比大小。审批浮层 `PendingInterruptDock` 用 `relative` 包一层，就是为了给内部绝对定位的卡片当参照。

### 4.4 层叠：多条 CSS 谁赢

优先级：`inline style` > `#id` > `.class` > 标签；同级后写覆盖先写。  
Tailwind 类名冲突时项目用 `tailwind-merge`（`cn()`）合并，避免 `p-2` 和 `p-4` 同时生效不知道听谁的。

### 4.5 隐藏三兄弟

| | 占位 | 能点到 |
|---|------|--------|
| `display: none` | 否 | 否 |
| `visibility: hidden` | 是 | 否 |
| `opacity: 0` | 是 | **能**（除非 `pointer-events-none`） |

---

## 五、JavaScript：交互与异步

### 5.1 和 React 的分工

原生写法：找 DOM → 改 DOM。  
React 写法：**改 state → 组件重跑 → React 更新 DOM**。  
所以读 `web/` 时，找 `useState` / `setTurns`，别找 `document.querySelector`。

### 5.2 事件：冒泡、捕获、委托

```
点击子元素：捕获（根→子）→ 目标 → 冒泡（子→根）
```

**事件委托**：在父节点绑一次 `click`，用 `event.target` 判断点的是哪个子项——长列表省内存。  
场景题展开：[07 §二 B1](07-frontend-scenarios.md)。

### 5.3 事件循环（必背一题）

JS 单线程。同步代码跑完 → 清空**微任务**（`Promise.then`）→ 取一个**宏任务**（`setTimeout`、I/O、点击）。

```javascript
console.log(1);
setTimeout(() => console.log(2), 0);
Promise.resolve().then(() => console.log(3));
console.log(4);
// 1 → 4 → 3 → 2
```

**面试一句**：和 Go 多 goroutine 不同，浏览器靠事件循环 + 异步 API；**tab 之间不共享内存**，同步状态用 `BroadcastChannel` 或 `localStorage` 事件（见 [07 §四](07-frontend-scenarios.md)）。

### 5.4 项目里真在用的浏览器 API

| API | 项目里干什么 |
|-----|--------------|
| `fetch` | `lib/api.ts` 调 Go 后端 REST |
| `ReadableStream` + 手动读 chunk | `useChatStream.ts` 解析 SSE（**没用** `EventSource`，因为要 POST + 自定义 header） |
| `AbortController` | 切会话、取消流式请求 |
| `localStorage` | 侧栏折叠等待 UI 偏好（若有） |
| Electron `preload` IPC | 选文件、存粘贴图（`electronAPI.pickFiles`） |

流式细节别在本篇展开 → [02-streaming.md](02-streaming.md)。

---

## 六、项目导读：按文件学三件套

建议顺序打开 `LingCoWork/web/src/`：

### 6.1 路由与页面骨架

| 文件 | 学到什么 |
|------|----------|
| `main.tsx` | React Router：`/` 首页，`/c/:id` 会话页 |
| `App.tsx` | **Flex 整页布局**：Sidebar + main |
| `routes/conversation.tsx` | 页面组合：Header + Transcript + 输入框 + Workspace；**不手写 DOM**，拼组件 |
| `features/workspace/` | 右侧预览：按扩展名分流，`/file` vs `/inline`（[09-workspace-preview.md](09-workspace-preview.md)） |

### 6.2 聊天 UI（三件套 + React 交界）

| 文件 | 学到什么 |
|------|----------|
| `features/chat/Transcript.tsx` | `overflow-y-auto` 滚动区；`useEffect` 滚到底 |
| `features/chat/PromptInput.tsx` | 表单、`onSubmit`、`textarea` |
| `features/chat/MessageBody.tsx` | 消息渲染（Markdown） |
| `hooks/useChatStream.ts` | **自定义 hook**：SSE、state、副作用全在这（详解见 [08 §五](08-react-state-hooks.md)） |

### 6.3 侧栏与样式

| 文件 | 学到什么 |
|------|----------|
| `features/sidebar/Sidebar.tsx` | `w-[280px] shrink-0` 固定宽；`data-collapsed` 做折叠动画 |
| 任意组件的 `className` | 对照 §4.2 表，**练「读 Tailwind」** |

### 6.4 和 klingwork-app 的差别（面试可能问）

| | LingCoWork `web/` | klingwork-app (Electron) |
|---|-------------------|---------------------------|
| 渲染 | 浏览器 / Electron 渲染进程 | 同上 |
| 和后端通信 | `fetch` → Go HTTP API | IPC + agent 事件流 |
| 读本地文件 | 经 Electron preload | 同类 |

Electron：**渲染进程** ≈ 浏览器；**主进程** ≈ 有 Node/文件权限的后端。前端代码仍在渲染进程里写 React。

---

## 七、考前速记（一页纸）

### HTML（4 条）

1. 语义化：`header` / `main` / `button`  
2. `div` 块级 vs `span` 行内  
3. JSX：`className`、驼峰事件  
4. 真 `<button>`，别 div 假按钮  

### CSS（6 条）

1. 盒模型 + `border-box`  
2. **Flex**：`flex` / `flex-col` / `flex-1` / `shrink-0` / `min-h-0`  
3. Flex vs Grid：一线 vs 棋盘  
4. `overflow-y-auto` 做滚动区  
5. margin 折叠  
6. `display:none` vs `visibility` vs `opacity`  

### JS（5 条）

1. 事件循环：`1 4 3 2`  
2. 冒泡 + 事件委托  
3. `fetch` + `async/await`  
4. tab 不共享内存 → `BroadcastChannel`  
5. React：改 state，不手写 DOM  

### 被问「你前端怎么样」

> 系统学的是后端和 Agent。前端以三件套为基础，能读 React + Tailwind 交付的界面。LingCoWork 里独立改过聊天页：布局是 flex 分栏，流式用 `fetch` 读 SSE，逻辑抽在 `useChatStream` 自定义 hook 里。盒模型、flex、事件循环、跨 tab 通信能讲；复杂动画和兼容性不是最深的一块。

---

## 八、学习路径（最小投入）

1. **15 分钟**：MDN 盒模型 + Flexbox 各扫一眼（链接见旧版 MDN，不必全读）  
2. **30 分钟**：按 §六 打开 `App.tsx` → `conversation.tsx` → `Transcript.tsx`，对照 §4.2 认 class  
3. **10 分钟**：手写输出 `1 4 3 2`，能解释为啥  
4. **按需**：场景题 [07](07-frontend-scenarios.md)；hooks [08](08-react-state-hooks.md)

**暂不必深啃**：Webpack 原理、手写 Virtual DOM、CSS 动画大师课。

---

## 关联文档

- 场景题大全：[07-frontend-scenarios.md](07-frontend-scenarios.md)  
- React state / hooks / `useChatStream`：[08-react-state-hooks.md](08-react-state-hooks.md)  
- 跨 tab、304、Electron：[05-concept-notes.md](05-concept-notes.md)  
- 流式 SSE：[02-streaming.md](02-streaming.md)  
- 题目清单：[00-common-questions.md](00-common-questions.md)
