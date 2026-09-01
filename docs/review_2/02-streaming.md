# 项目二 · 流式输出

> 对应简历第二条 bullet 的前半：「SSE 以服务端缓冲为权威源，断网、刷新都能重连续读、
> 不丢内容」。审批那半在 [03-approval.md](03-approval.md)。
>
> 代码位置：`internal/stream/`、`internal/handler/chat.go`、`internal/service/chat.go`。

---

## 一、前提：长任务随时会断

agent 一跑几分钟，用户会切页面、会断网、会刷新，服务进程还会重启。

所以流式输出**不能以「这条 HTTP 连接」为中心设计**。连接是易碎品，得有个比它活得更久
的东西托着。

## 二、三个角色

核心设计一句话：**agent 跑在自己的 goroutine 里，HTTP 请求只是一个订阅者。**

| 角色 | 生命周期 |
|---|---|
| agent goroutine | 从 `Start` 到跑完，不受连接影响 |
| `StreamBuffer` | 跟一次 run 绑定 |
| HTTP 请求 | 随时来、随时走 |

再加一句：**数据库是终态的唯一真相**。buffer 是过程态，DB 是结果态。这个区分后面到处
用得上。

## 三、StreamBuffer 的双重身份

```16:22:internal/stream/buffer.go
type StreamBuffer struct {
	mu          sync.RWMutex
	chunks      [][]byte
	subscribers []chan []byte
	status      Status
	cancel      context.CancelFunc
}
```

`chunks` 是**录像**，给后来连上的客户端回放；`subscribers` 是**广播**，每个正连着的
请求一个 channel。`Append` 同时做这两件事。

广播用 `select-default`，**满了就丢**——拿丢帧换生产者不阻塞，agent 不能因为某个慢
客户端卡住。实际不会触发（fetch 消费远快于模型产出），真丢了 DB 也能兜底。

## 四、唯一真正需要想清楚的并发问题

新客户端连上要做两件事：拿到已产出的历史、订阅接下来的新帧。**这两步必须原子。**

```92:112:internal/stream/buffer.go
	b.mu.Lock()
	history := make([][]byte, len(b.chunks))
	copy(history, b.chunks)
	if b.status == StatusComplete {
		b.mu.Unlock()
		// 已完成：只回放，不订阅
		...
	}

	sub := make(chan []byte, 64)
	b.subscribers = append(b.subscribers, sub)
	b.mu.Unlock()
```

先快照、放锁、再订阅 → 中间那一瞬产出的帧既不在历史也不会推给你，**丢帧**。
先订阅、再快照 → 那一瞬的帧同时出现在两处，**重帧**。

同一把锁里做完，边界严丝合缝：历史截止到 T，订阅从 T 开始。

## 五、两个端点，都返回 SSE

```23:27:internal/handler/chat.go
func (h *ChatHandler) Register(r *gin.Engine) {
	r.POST("/chat/:id", h.Chat)
	r.GET("/chat/:id", h.Resume)
	r.POST("/chat/:id/cancel", h.Cancel)
}
```

**注意 POST 也返回 SSE**，不是「POST 发消息、GET 收流」那种分离模式。

拆成两步的话中间有个窗口：POST 已返回、agent 已开始产 token，但 GET 还没连上，这段
时间的帧要么丢、要么得额外补发。合成一步就没这个问题。

前提是有 buffer 这层——**没有它，「POST 直接返流」和「断线可重连」没法同时成立**。

POST 进来还会先判 `IsStreaming`：已经在跑就直接给现有流，不再起一轮 agent。用户手抖
点两次不会触发两个 agent。

## 六、204 是「刷新不重复」的全部秘密

```86:89:internal/handler/chat.go
	if !h.chat.IsStreaming(id) {
		c.Status(http.StatusNoContent)
		return
	}
```

跑完的 buffer 还留在 manager 里，是给**原来那个 POST 客户端**读完最后的 `done` 帧。
但对刷新页面进来的新客户端来说，DB 里已经有完整消息了，再回放一遍就是显示两遍。

所以规则是：**还在跑才给流，跑完了让你读 DB**。同一个 buffer，对不同来源的客户端语义
不同。

## 七、断连为什么不影响 agent

agent 的 context 来自 `context.Background()`，**不继承 HTTP 请求的 context**：

```go
runCtx, cancel := context.WithCancel(context.Background())
buf.SetCancel(cancel)
go s.runAgent(runCtx, ...)
```

连接断了只是少个订阅者。只有显式 `POST /chat/:id/cancel` 才真的停。
**这一行 `context.Background()` 就是「断连不影响 agent」的全部实现。**

## 八、一个部署坑

`writeSSE` 里有个 `X-Accel-Buffering: no`，关掉 nginx 的响应缓冲。

不加这个头，nginx 会攒够一块才转发，流式效果直接消失——本地正常、上了带 nginx 的环境
变成「转半天然后一次性全出来」。

## 九、为什么是 SSE 而不是 WebSocket

被问过，而且是追着问的。答案分两层，第二层才是关键。

**第一层：数据是单向的。** 服务端往下推大量增量，客户端上行只有「发消息、点审批、
点停止」这几个低频动作，各自一个普通 POST 就够。WebSocket 的双向能力用不上，但它的
成本要照付：协议升级、心跳、连接状态自己管。而 SSE 就是纯 HTTP，代理和网关天然友好，
`curl` 就能调试。

**第二层：权威源在缓冲，不在连接上。** 这才是真正的理由。

流不是「连接的副产品」，而是服务端有一份 buffer，连接只是它的订阅者。断线重连就是
「再 GET 一次」，从 buffer 重放。这套模型和 HTTP 请求-响应天然契合——换成 WebSocket
反而要自己再造一遍「重连之后怎么补齐」，而那正是 buffer 已经解决的事。

**最有力的答法是指出项目里两种都用了。** 对话流用 SSE，浏览器桥用 WebSocket。桥那边
必须用，因为它是真双向而且是 RPC 语义：扩展要主动推 tab 变化事件，后端要下发命令并
等结果返回（uuid + 等待表）。「下发命令、等一个回复」SSE 根本做不了。

所以这不是「只会 SSE」，是按数据流方向选的：**单向推流用 SSE，双向 RPC 用 WebSocket。**

## 十、为什么不用浏览器原生 `EventSource`

改用 `fetch` + `ReadableStream`，三个原因：它不支持 POST；它断线自动重连但这里需要
精确控制重连时机；它不能配合 `AbortController` 取消。代价是自己写帧解析，二十行。

注意这意味着**放弃了 `EventSource` 自带的自动重连**，重连逻辑是自己写的。被问到
「SSE 不是自带重连吗」要能答上这一句。

## 十一、网络抖动与渲染平滑（**未实现**，被问过）

这块目前是缺口，而且被问过一次没答上来。

先把问题归位：**这是渲染层的事，换传输协议解决不了。** TCP 包的到达本来就是突发的
——卡 300 毫秒，然后一次到达两百个字符。用 WebSocket 一样抖。

现在前端是**收到就渲染**（`last.content + f.content` 直接 setState），所以网络抖动会
一比一反映到画面上：卡一下、突然吐一大段、再卡一下。豆包那种匀速打字机是前端做了缓冲。

正确做法是**抖动缓冲 + 定速播放**，就是视频播放器的老思路：

1. 收到的 chunk 不直接进渲染状态，先进一个待显示的字符缓冲
2. 用 `requestAnimationFrame` 循环，每帧从缓冲取若干字符追加到显示状态
3. 网络突发 → 缓冲变长，画面速度不变；网络卡顿 → 缓冲还有存货，继续匀速吐

**速率必须自适应**，这是最容易做错的地方。固定速率两头不讨好：网络快时缓冲越积越多、
画面严重滞后于模型；网络慢时缓冲照样会空。做法是按「目标多少毫秒内清空当前缓冲」反算
每帧字符数，并且收到 `done` 之后加速收尾，别让用户等动画。

顺带两点：rAF 循环天然把「每个 token 一次 setState」合并成「每帧一次」，顺手解决了
Markdown 全量重渲染掉帧；流式 Markdown 未闭合代码块的闪烁问题由 Streamdown 处理，
这块已经有了。

还有个相关的坑：断线重连时服务端是**全量重放**的，前端如果直接 append 会把已显示的
内容再打一遍。所以重连要按已收长度截断，这件事得和上面的缓冲一起做才完整。

## 十二、和审批的交汇点

审批弹出时，**当前这条 SSE 流正常收尾**——不是挂着一条不死不活的连接等用户。恢复时
新建一个 buffer，前端重新 GET 订阅。

所以一轮对话中断三次审批，就是四条 SSE 流、四个 HTTP 请求。

为什么不挂着等：用户可能几小时才回来，挂着的连接一直占资源和浏览器连接配额，中间还
可能被代理按空闲超时掐掉。**把「等人」从连接层挪到应用层**，连接就只负责真正在传数据
的那段。

这也是「权威源在缓冲不在连接」这个设计的红利：连接可以随时结束、随时重建，因为它本来
就不是权威。中断恢复的完整机制见 [03-approval.md](03-approval.md)。

## 十三、诚实的边界

**多实例部署不支持**。流缓冲在进程内存里。要扩展得把它外置（Redis Stream 之类）+ 做
会话亲和。桌面交付形态下单机是合理选择，但这是明确边界。

**前端没做渲染平滑**。网络抖动会直接反映到画面上，做法见第十一节。这是纯前端就能补的
改进，不影响协议选择——但要说清楚这是「还没做」，不是「做不了」。

**重放是全量的，不是按偏移续传**。断线重连会把整个缓冲重新推一遍。简单可靠，代价是长
会话重连时要重传一次。改进方向很清楚：加 `Last-Event-ID`，服务端按偏移续发。

## 十四、串起来的主线

**前提**是长任务随时会断，所以流式输出不能以连接为中心。

**核心**是把权威源从连接挪到服务端缓冲：agent 写 buffer，连接只是订阅者，**断线成了
正常路径而不是异常**。

**配套**有三处：`context.Background()` 派生 agent 的 context，所以断连不影响它跑；
GET 的 204 判定，所以刷新页面不会把内容显示两遍；快照与订阅在同一把锁里，所以既不丢帧
也不重帧。

**协议选择**是这个设计的直接结论：数据单向、权威源不在连接上，所以 SSE 够了；而项目里
真需要双向 RPC 的地方（浏览器桥）用的就是 WebSocket。

**已知缺口**是前端没做渲染平滑——网络抖动会一比一反映到画面上，这是渲染层的活，跟协议
无关。
