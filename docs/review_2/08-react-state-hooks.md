# React：State 与 Hooks 入门

> 面向 Agent / 后端背景、刚接触 React 的同学。  
> 三件套基础见 [06-frontend-basics.md](06-frontend-basics.md)；场景题答法见 [07-frontend-scenarios.md §六](07-frontend-scenarios.md)。  
> 下文例子来自 LingCoWork `web/`。

**怎么用本文：**

1. 先读 **§一～§三**，搞清 props / state / 重渲染
2. 按 **§四** 逐个过常用 hook
3. **§五～§六** 对照项目代码（`useChatStream`、`approval-store`）
4. 考前扫 **§八 速记**

---

## 一、React 解决什么问题

原生 JS 改页面：

```javascript
let count = 0;
btn.addEventListener("click", () => {
  count++;
  span.textContent = String(count);  // 你要自己找 DOM、自己改
});
```

React 的思路：**描述「数据 → 界面应该长什么样」**，数据变了 React 帮你更新 DOM。

```tsx
function Counter() {
  const [count, setCount] = useState(0);
  return (
    <button onClick={() => setCount(c => c + 1)}>{count}</button>
  );
}
```

| 原生 JS | React |
|---------|-------|
| 改变量 + 手动改 DOM | 改 **state**，组件函数重新执行，输出新 JSX |
| 状态散落各处 | 状态和 UI 绑在一个组件里 |
| 长列表要自己 diff | React 做 Virtual DOM diff（了解即可，不必手写） |

---

## 二、三个核心概念

### 1. 组件（Component）

可复用的 UI 单元，本质是一个**函数**（或 class，新项目几乎都用函数）：

```tsx
function UserBadge({ name }: { name: string }) {
  return <span className="badge">{name}</span>;
}
```

### 2. Props — 父传子的只读参数

```tsx
<MessageBody content={text} streaming={isLive} />
```

- 从外面传进来，子组件**不能改 props**
- 类比：函数参数
- props 变了 → 子组件重渲染

### 3. State — 组件自己记住、会变的数据

```tsx
const [open, setOpen] = useState(false);
```

- 组件内部持有，用 `setXxx` 修改
- state 变了 → **本组件**重渲染，通常连带子树一起更新
- 类比：函数里会变的局部变量，但改法会触发「整段 UI 重算」

**常问：props 和 state 区别？**

| | props | state |
|---|-------|-------|
| 来源 | 父组件 | 组件自己 |
| 能否修改 | 只读 | 用 setter 改 |
| 典型用途 | 配置、展示数据 | 开关、输入、loading、列表 |

---

## 三、一次「改 state」背后发生了什么

```
用户点击 → setOpen(true)
    ↓
React 把新 state 记入队列（可能批量合并多次 setState）
    ↓
重新调用组件函数 Counter()，得到新 JSX
    ↓
和上一帧 JSX diff，只改 DOM 里真正变化的部分
    ↓
屏幕更新
```

**重要习惯：**

```tsx
// ❌ 直接改变量，React 不知道
open = true;

// ✅ 必须用 setter
setOpen(true);
setCount(c => c + 1);  // 依赖旧值时用 functional update
```

**functional update 何时用？**  
连续多次基于旧值更新，或闭包里拿到的 state 可能是旧的：

```tsx
setTurns(prev => [...prev, newTurn]);  // useChatStream 里常见
```

---

## 四、Hooks 是什么

**Hook** = 以 `use` 开头的函数，给**函数组件**挂上 state、副作用、缓存等能力。

React 要求：

1. **只在组件或自定义 hook 顶层调用**（不能写在 `if/for` 里）
2. **每次渲染 hook 调用顺序必须一致**

下面按面试频率排列。

---

### 4.1 `useState` — 记可变数据

```tsx
const [value, setValue] = useState(initial);
```

| 场景 | 例子 |
|------|------|
| 开关 | 下拉菜单 `open`（`ApprovalModeDropdown`） |
| 表单 | 输入框文字、选中项 |
| 异步 | `loading`、`error`、消息列表 `turns` |
| 流式 | `streaming` 是否在收 SSE |

**不要把「能算出来的东西」也塞进 state**（见 §4.2 误用）。

---

### 4.2 `useEffect` — 对接「外部世界」

组件渲染到屏幕**之后**执行。适合：

- 发 HTTP 请求
- 订阅 SSE / 事件监听
- 操作 DOM（focus、scroll）
- 定时器

```tsx
useEffect(() => {
  // 挂载后 / 依赖变化后执行
  document.addEventListener("keydown", onKey);

  return () => {
    // cleanup：卸载前 / 下次 effect 前执行
    document.removeEventListener("keydown", onKey);
  };
}, [open]);  // 依赖数组：这些值变了才重新跑
```

**依赖数组怎么理解：**

| 写法 | 含义 |
|------|------|
| `[]` | 只在**首次挂载**跑 once（+ 卸载 cleanup） |
| `[id]` | `id` 变了：先 cleanup 旧的，再跑新的 |
| 不写第二参数 | **每次渲染**都跑（几乎别这么写） |

**典型误用：**

```tsx
// ❌ 把「过滤后的列表」存 state，又在 effect 里 set
const [filtered, setFiltered] = useState([]);
useEffect(() => {
  setFiltered(items.filter(x => x.active));
}, [items]);

// ✅ 渲染时直接算（derived state）
const filtered = items.filter(x => x.active);
```

**竞态**：快速切换 conversationId 时，旧请求后返回会覆盖新数据。  
`useChatStream` 的做法：`cancelled` 标志 + `AbortController` + cleanup 里 abort。

---

### 4.3 `useRef` — 可变盒子，不触发重渲染

```tsx
const ref = useRef(initialValue);
ref.current = newValue;  // 改 current 不会重渲染
```

两种用途：

**① DOM 引用**

```tsx
const rootRef = useRef<HTMLDivElement>(null);
// rootRef.current 指向真实 DOM，用于 contains()、focus() 等
```

**② 跨渲染保存可变值（不想触发 UI 更新时）**

```tsx
const abortRef = useRef<AbortController | null>(null);
abortRef.current = controller;
```

**为什么需要 ref 解决 stale closure？**

`useEffect` / `useCallback` 闭包里的 state 可能是旧快照。  
回调里若需要**最新**的 `turns`、store action，常见模式：

```tsx
const turnsRef = useRef(turns);
turnsRef.current = turns;  // 每次渲染同步最新值
```

---

### 4.4 `useCallback` — 缓存函数引用

```tsx
const send = useCallback(async (text: string) => {
  // ...
}, [streaming, conversationID]);
```

- 依赖不变 → 返回**同一个函数对象**
- 配合 `memo` 子组件，避免「父重渲染 → 子 props 函数引用变了 → 子也重渲染」

`useChatStream` 的 `runStreamingResponse`、`send` 都用 `useCallback` 包一层。

---

### 4.5 `useMemo` — 缓存计算结果

```tsx
const sorted = useMemo(() => heavySort(items), [items]);
```

- 依赖不变 → 复用上次计算结果
- **别滥用**：简单 `filter/map` 不必 memo；列表很大或计算贵时才值得

---

### 4.6 `memo` — 包组件，props 没变就跳过渲染

```tsx
export const MessageBody = memo(
  ({ content, streaming }: Props) => <Streamdown ... />,
  (prev, next) =>
    prev.content === next.content &&
    prev.streaming === next.streaming,
);
```

流式聊天里 `content` 每来一帧就变，但**其他消息**的 `MessageBody` props 不变 → 不必跟着整页重绘 Markdown。

---

### 4.7 Hooks 速查表

| Hook | 一句话 | 项目例子 |
|------|--------|----------|
| `useState` | 可变数据，改了重渲染 | `turns`、`open`、`loading` |
| `useEffect` | 副作用 + cleanup | 拉历史、绑 Esc、abort SSE |
| `useRef` | 可变值 / DOM，不触发渲染 | `abortRef`、`rootRef` |
| `useCallback` | 稳定函数引用 | `send`、`runStreamingResponse` |
| `useMemo` | 稳定计算结果 | `Transcript` 里派生数据 |
| `memo` | 稳定子组件 | `MessageBody` |

---

## 五、自定义 Hook

当一坨逻辑（多个 state + effect + 回调）要在多处复用，或单个组件太长时，抽成 **`useXxx`**：

```tsx
export function useChatStream(conversationID: string) {
  const [turns, setTurns] = useState<ChatTurn[]>([]);
  const [streaming, setStreaming] = useState(false);
  const abortRef = useRef<AbortController | null>(null);

  useEffect(() => {
    const controller = new AbortController();
    // 拉历史 → probe resume → 读 SSE stream → 更新 turns
    return () => {
      controller.abort();
    };
  }, [conversationID]);

  const send = useCallback(async (text: string) => { ... }, [...]);

  return { turns, streaming, loading, send, cancel };
}
```

**规则**：自定义 hook 内部可以调用其他 hook；名字必须以 `use` 开头。

**路由页怎么用：**

```tsx
const { turns, send, streaming } = useChatStream(conversationId);
```

页面组件只关心 UI，流式协议细节藏在 hook 里。

---

## 六、状态放哪里

```
只本组件用          → useState
多个兄弟都要读       → 提升到最近共同父组件
跨页面 / 跨很远      → Context 或全局 store
```

LingCoWork 的分工：

| 数据 | 放哪 | 文件 |
|------|------|------|
| 消息列表、流式 | `useChatStream` 内 `useState` | `hooks/useChatStream.ts` |
| 待审批工具调用 | Zustand | `features/chat/approval-store.ts` |
| 工作区文件列表 | Zustand | `features/workspace/store` |
| 下拉菜单开闭 | 组件本地 `useState` | `ApprovalModeDropdown.tsx` |

**Zustand 和 useState 区别：**

- `useState`：跟着某个组件生命周期走，卸载可能丢
- Zustand：模块级 store，任何组件 `useApprovalStore(s => s.pending)` 都能订阅；切换 conversation 用 id 做 key 隔离

```tsx
export const useApprovalStore = create<ApprovalStore>((set, get) => ({
  pending: {},
  add: (convId, item) => { ... },
}));
```

面试答 **「状态放父还是子」**：谁用谁持有；多个子要用 → 提升；跨路由/多 feature → store。

---

## 七、和场景题 / 项目的对应

| 07 题号 | 主题 | 本文 |
|---------|------|------|
| F1 | 状态提升 / store | §六 |
| F2 | useEffect 误用 | §4.2 |
| F3 | 列表 key | §八.3 |
| F5 | useChatStream | §五 + `hooks/useChatStream.ts` |

**和「防抖 C1」的关系**：  
聊天 **发送** 是点按钮调 `send()`，不是输入时 debounce。  
`useState` 管输入框文字；**发 API** 在 `send` 里一次性触发。

**和 SSE 的关系**：  
`send` → `postChat` → `runStreamingResponse` 读 body stream → 每帧 `setTurns` 追加 content → `MessageBody` 重渲染（`streaming` 模式）。

---

## 八、面试速记

### 1. 三句话版

> React 用组件描述 UI。props 是父传子的只读参数，state 是组件内部可变数据，用 setter 改。Hooks 是函数组件的工具箱：`useState` 记状态，`useEffect` 管副作用和 cleanup，`useRef` 存不触发渲染的可变值。

### 2. useEffect cleanup 为什么要 abort

> 组件卸载或 conversation 切换时，旧 SSE 还在读会 setState 到已卸载组件或错误会话。cleanup 里 `controller.abort()` + `cancelled = true` 丢弃过期结果。

### 3. 列表 key

> 用稳定 `id`，别用 index。中间插入/删除时 index 会变，React 错复用 DOM，输入框内容错位。

### 4. 性能三板斧（够用即可）

1. 列表项 `key={item.id}`
2. 重渲染贵的子组件 → `memo` + 稳定 props（`useCallback` / `useMemo`）
3. 超长列表 → 虚拟列表（见 07 §E1），不是 memo 能 alone 解决的

### 5. 被问「你 React 熟吗」

> 项目里用函数组件 + hooks 交付聊天页：本地 state 管 UI 开关，自定义 `useChatStream` 管 SSE 流式和 cleanup，跨会话审批用 Zustand。能讲清 props/state、effect 依赖和 cleanup、为何 MessageBody 要 memo。没做过大型状态机或 React Native，但日常开发和改 bug 够用。

---

## 九、建议阅读顺序（对照代码）

1. `web/src/features/chat/ApprovalModeDropdown.tsx` — 最小 `useState` + `useEffect`（Esc、点外关闭）
2. `web/src/features/chat/MessageBody.tsx` — `memo` + 流式 Markdown
3. `web/src/hooks/useChatStream.ts` — 自定义 hook 全貌（mount 拉历史、SSE、abort）
4. `web/src/features/chat/approval-store.ts` — Zustand 全局状态

---

## 关联文档

- 三件套 + Tailwind：[06-frontend-basics.md](06-frontend-basics.md)（§五 有 React 概览，细节看本文）
- React 场景题：[07-frontend-scenarios.md §六](07-frontend-scenarios.md)
- 流式 SSE 协议：[02-streaming.md](02-streaming.md)
- 题目清单：[00-common-questions.md](00-common-questions.md)
