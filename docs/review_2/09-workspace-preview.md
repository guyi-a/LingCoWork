# 项目复习 · 右侧 Workspace 预览面板

> Agent 把报告、代码、表格、PDF、Word、PPT 等产物写进 workspace 后，用户可以直接在会话右侧
> 打开查看。
>
> 它不是一套在线编辑器，而是：**自研面板壳 + 文件树 + 文件类型分发器 + 多个只读 renderer。**
>
> 代码位置：`web/src/features/workspace/`。

---

## 一、面试官问“用了什么组件”，先这样答

> 整个面板没有使用一个大而全的文件预览组件。面板布局、文件树和文件分发器是我们用 React
> 自己写的，状态用 Zustand。具体渲染按类型拆开：代码用 Shiki，Markdown 用 Streamdown，
> PDF 交给浏览器内置 viewer，docx 用 docx-preview，pptx 用
> @aiden0z/pptx-renderer，图片和音视频使用浏览器原生标签，CSV 用原生 table。

必须记住这张表：

| 文件或功能 | 组件 / 方案 |
|---|---|
| 面板壳 | 自研 React `WorkspacePanel` |
| 面板状态 | Zustand |
| 文件树 | 自研递归 `WorkspaceTree` |
| 文件分发 | 自研 `FilePreview` |
| Markdown | Streamdown + remark-gfm + remark-math + KaTeX |
| 代码 / 普通文本 | Shiki |
| PDF | 浏览器原生 `<iframe>` |
| docx | `docx-preview` |
| pptx | `@aiden0z/pptx-renderer` |
| CSV / TSV | 自己解析 + 原生 `<table>` |
| 图片 | 原生 `<img>` |
| 音视频 | 原生 `<video>` / `<audio>` |

一句话记忆：

```text
壳、树、分发器自己写；
代码 Shiki，Markdown Streamdown；
PDF 用浏览器，Word 用 docx-preview，PPT 用 pptx-renderer；
图片和音视频用原生标签。
```

## 二、整个面板怎么实现

整体可以拆成四层：

```text
第一层：WorkspacePanel
负责右侧面板的开关、宽度拖拽，以及显示文件树还是文件预览

第二层：WorkspaceTree
从后端获取文件列表，组装成目录树；点击文件后把 path 写进 Zustand

第三层：FilePreview
读取当前 path，根据扩展名和后端返回的 kind 选择 renderer

第四层：各类型 Renderer
Markdown、代码、PDF、Word、PPT、图片、音视频分别渲染
```

完整链路：

```text
Agent 用 write_file 等工具把产物写入 workspace
  ↓
前端刷新文件列表
  ↓
用户在 WorkspaceTree 中点击文件
  ↓
Zustand 保存 previewPath
  ↓
FilePreview 判断文件类型
  ↓
选择对应 renderer
  ↓
从 /file、/inline 或 /download 获取内容并展示
```

这个设计最重要的地方是：

> 不让一个通用组件勉强处理全部格式，而是先统一分发，再让每种格式使用最适合自己的渲染方式。

## 三、面板本身用了什么

### WorkspacePanel：自己写的 React 侧栏

面板默认宽度 320px，只显示文件树。

打开文件后，树会被预览区域替换，而不是左右并排。用户可以拖动左侧分隔条，把预览宽度调整到
360～760px。

拖拽没有使用 `react-resizable-panels`，而是直接监听 Pointer Events：

```text
pointerdown 记录鼠标位置和初始宽度
pointermove 计算位移，更新宽度
pointerup   结束拖拽
```

### Zustand：保存面板状态

主要保存：

```text
panelOpen      面板是否打开
previewPath    当前预览哪个文件
previewWidth   预览宽度
filesVersion   用于触发文件树刷新
```

只持久化 `panelOpen` 和 `previewWidth`，不持久化 `previewPath`。

因为页面刷新后，原来的文件可能已经被 Agent 删除或移动。重新回到文件树比打开一个失效路径更
稳妥。

### WorkspaceTree：自己写的递归树

没有使用 Ant Design Tree 或第三方文件管理器。

后端返回扁平路径：

```text
src
src/main.ts
src/components
src/components/App.tsx
README.md
```

前端先按路径组装成树，再由递归 `TreeItem` 渲染。每个目录节点自己维护展开状态。

`node_modules`、`.git`、`dist`、`.venv` 等重目录默认折叠，避免一打开就被依赖和构建产物
淹没。

Agent 流式工作期间，文件树每两秒刷新一次，所以刚生成的报告能及时出现在面板中。

## 四、FilePreview 怎么分流

`FilePreview` 是总分发器。

```text
用户点击文件
  ↓
先按扩展名判断是否为 PDF / Office / 音视频
  ├─ 是：直接使用 /inline 文件流
  └─ 否：调用 /file 获取 kind 和文本内容
          ├─ markdown → MarkdownRenderer → MessageBody → Streamdown
          ├─ csv/tsv  → TablePreview
          ├─ text     → CodePreview
          ├─ image    → ImageRenderer
          └─ 其他     → UnsupportedRenderer
```

为什么前端和后端都要分类？

- 前端要在发请求前决定走 `/file` 还是 `/inline`。
- 后端要判断文件能不能当文本读取，以及控制 MIME、大小和安全边界。

这两张分类表必须保持一致，这是当前实现的一个维护成本。

## 五、三条取数路径

不同文件不应该都塞进 JSON。

| 接口 | 返回内容 | 使用场景 |
|---|---|---|
| `/workspace/file` | JSON：`kind`、`content`、大小等 | Markdown、代码、文本、CSV |
| `/workspace/inline` | 原始文件流，`Content-Disposition: inline` | PDF、docx、pptx、音视频 |
| `/workspace/download` | 原始文件流，`Content-Disposition: attachment` | 图片、下载和不支持的格式 |

文本最多读取 512KB，超过后设置 `truncated: true`，前端提示用户下载完整文件。

后端还会检查文件前 512 字节。如果发现 NUL 字节，即使扩展名是 `.md` 或 `.txt`，也会降级为
二进制文件，避免把伪装成文本的二进制内容直接渲染。

所有路径都要经过 workspace 边界校验。用户传入 `../../.ssh/id_rsa` 时，解析后的绝对路径不在
当前 workspace 内，后端返回 403。

## 六、各种文件具体怎么渲染

### Markdown：Streamdown

Markdown 复用聊天消息的 `MessageBody`，底层使用 Streamdown：

```text
Streamdown
+ remark-gfm       表格、任务列表、删除线
+ remark-math      识别数学公式
+ rehype-katex     渲染公式
```

这样聊天气泡和 workspace 中的 Markdown 报告不会出现两套不同语法。

### 代码：Shiki

代码预览使用 Shiki，根据文件名判断语言，生成带行号的高亮 HTML。

没有使用 Monaco 或 CodeMirror，因为这里是**只读预览**：

```text
不需要编辑
不需要自动补全
不需要语法诊断
不需要撤销重做
```

使用完整编辑器会增加包体和运行开销。Shiki 只负责语法着色，更符合需求。

如果 Shiki 高亮失败，就退回普通 `<pre>`，至少保证内容仍然可读。

### PDF：浏览器 iframe

PDF 没有使用 pdf.js React 组件，而是：

```tsx
<iframe src={inlineURL} />
```

后端返回正确的 `application/pdf` 和 inline 文件流，浏览器内置 PDF viewer 负责分页、缩放、
搜索和下载。

这样开发成本最低，也不需要自己维护 PDF 渲染状态。

缺点是不同浏览器的工具栏和体验可能不完全一致。

### docx：docx-preview

前端从 `/inline` 拉取 ArrayBuffer，再动态加载 `docx-preview`：

```text
fetch ArrayBuffer
  ↓
import("docx-preview")
  ↓
renderAsync(buffer, container)
  ↓
渲染为浏览器 DOM
```

它可以保留标题、段落、列表、表格和图片等常用排版。

动态 import 的意义是：用户没有打开 Word 文件时，这个库不会进入首屏主包。

项目还处理了 Symbol / Wingdings 项目符号在部分系统上显示成方框的问题，将私有字符映射为
普通 Unicode 符号。

### pptx：@aiden0z/pptx-renderer

流程和 docx 类似：

```text
fetch ArrayBuffer
  ↓
动态 import("@aiden0z/pptx-renderer")
  ↓
PptxViewer.open()
  ↓
渲染幻灯片列表
```

大量幻灯片不会一次全部渲染，而是使用 windowed list，按批次渲染可见区域附近的页面。

切换文件或组件卸载时必须调用 `destroy()`，否则上一个 viewer 创建的 DOM 和监听器可能残留。

### CSV / TSV：原生 table

CSV 没使用专业表格库，而是前端按逗号或制表符拆分，再渲染原生 `<table>`。

最多展示 500 行、50 列，避免一次生成过多 DOM。

它只适合简单 CSV，不支持带引号的逗号、多行单元格等完整 CSV 语法。复杂表格提示用户下载。

### 图片和音视频：浏览器原生能力

```text
图片   <img>
视频   <video controls>
音频   <audio controls>
```

这些格式浏览器本身已经有成熟支持，没有必要再包一层第三方播放器。

## 七、为什么不用一个统一预览组件

看起来接一个“大一统文件预览库”更简单，但实际有几个问题：

1. 不同格式的数据入口不同：文本适合 JSON，二进制适合文件流。
2. PDF、图片、音视频浏览器本身就能处理，额外库反而增加成本。
3. Office 渲染库通常只擅长一种格式。
4. 一个大组件会让所有格式一起进入主包，影响首屏加载。
5. 不同格式的失败和降级策略不同。

因此采用：

> 统一外壳和分发协议，renderer 按格式独立实现。

以后增加 xlsx，只需要：

```text
增加扩展名识别
增加 XlsxPreview
在 FilePreview 注册分支
后端补充对应 MIME 白名单
```

不会影响现有 PDF、Word 和代码预览。

## 八、inline 为什么必须使用白名单

`/inline` 的含义是让浏览器在应用页面中嵌入 workspace 文件。

如果任意 HTML、SVG 或 JavaScript 都能 inline，工作区里的恶意文件可能在应用 origin 下执行
脚本，形成 XSS。

所以后端只允许明确支持的格式：

```text
PDF
docx
pptx
指定的音视频格式
```

同时：

- `Content-Type` 根据白名单扩展名确定，不依赖浏览器猜测。
- 返回 `X-Content-Type-Options: nosniff`。
- HTML、JavaScript 和 SVG 不进入任意 inline 渲染路径。

docx 和 pptx 本质上是 ZIP 包，只作为字节交给对应客户端库解析，不让浏览器当网页执行。

## 九、它和 Agent 是什么关系

预览面板不参与 Agent 的 ReAct 循环。

```text
Agent：通过文件工具把产物写入 workspace
面板：读取已经落盘的文件，提供给用户查看
```

因此：

- 文件树刷新只是为了及时看到 Agent 新生成的产物。
- 预览失败不会作为工具错误回灌给模型。
- 面板是人查看产物的窗口，不是 Agent 的工作记忆。
- 面板没有编辑、diff、接受或拒绝修改功能。

## 十、面试怎么讲

### 一分钟版本

> 右侧是 workspace 的只读文件预览面板。面板壳、文件树和分发器是 React 自己实现的，状态用
> Zustand。用户点击文件后，FilePreview 会按扩展名和后端 kind 分流：Markdown 用 Streamdown，
> 代码用 Shiki，PDF 交给浏览器 iframe，docx 用 docx-preview，pptx 用 pptx-renderer，图片和
> 音视频用原生标签。文本走 JSON 接口并限制到 512KB，二进制文件走 inline 文件流；后端还做
> workspace 路径校验和 inline MIME 白名单，防止越界读取和任意 HTML 在应用本源执行。

### 被问“为什么不用 Monaco”

> 这是只读预览，不需要编辑、补全和诊断。Monaco 太重，Shiki 已经能提供语言识别、语法高亮和
> 行号，高亮失败还能退回 pre。

### 被问“PDF 用了什么组件”

> 没有额外 PDF 组件。后端提供 inline 文件流，前端放进 iframe，使用浏览器内置 PDF viewer。

### 被问“Word 和 PPT 怎么预览”

> 两者都在浏览器端解析。docx 用 docx-preview 转成 DOM，pptx 用
> @aiden0z/pptx-renderer 渲染幻灯片；库都采用动态 import，只有用户真正打开对应文件时才加载。

### 被问“为什么不服务端统一转 PDF”

> 服务端转码会引入 LibreOffice 等重依赖，还要处理进程隔离、字体、并发和缓存。当前是本地桌面
> 应用，客户端直接解析链路更短；代价是不同格式需要维护独立 renderer，复杂 Office 排版也不能
> 保证完全还原。

### 被问“有什么不足”

> 后端 kind 和前端扩展名分流是两张表，增加格式时要同步维护；CSV 只是简单 split，不支持完整
> CSV 语法；暂时没有 xlsx 和旧版 doc/ppt；代码只有预览，没有编辑和 diff。
