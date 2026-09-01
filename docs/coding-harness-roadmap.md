# LingCoWork 写代码 Harness 能力评估与路线

> 目的：把"LingCoWork 距离一个能自主写代码的 harness 还差多少"的分析、已落地的 `apply_patch` 容错、验证闭环设计、编辑器集成复杂度，以及对 DeepSeek Harness（`dsh`）的参考结论，汇总成一份可读的文档，作为后续 roadmap。
>
> 阅读对象：本仓库的维护者 / 想推进 LingCoWork coding 能力的同学。

---

## 0. 结论速览

| 问题 | 结论 |
| --- | --- |
| 现在能写代码吗 | 能。单 agent、单仓库、人陪跑已可用（~85–90%）。 |
| 能自主写代码吗 | 还不能（~60–70%）。缺确定性验证闭环、并发安全、git 生命周期、代码级精确理解。 |
| 最值得先做 | ① FS 编辑的 read-before-edit + version guard；② 模型面 `lsp` 工具；③ 工具结果剪枝。 |
| 编辑器集成 | **L3（VS Code 扩展桥）已决定往后靠**；LSP（L2）是独立且更省的高价值杠杆。 |
| dsh 借鉴什么 | 借鉴"设计模式"（能力缝 / 策略门 / read-before-edit / 单模型工具），**不要**照搬 Cordis 插件框架。 |

---

## 1. 现状：离"能写代码的 harness"还差多少

### 1.1 已经很强（骨架扎实）
- **读写/执行/验证工具全**：`read_file` / `glob` / `grep` / `list_files`、`write_file` / `write_file_chunked` / `apply_patch` / `rm` / `mv` / `cp`、`run_command`（`/bin/sh -c`、workspace 内 cwd、进程组 SIGKILL、超时、64KiB 截断）。
- **结构化验证**：`run_command` 可声明 test/build/lint/typecheck/format，诊断落 **Problems 面板**并**跳转到文件行**。
- **安全的审批**：按 **effect** 而非工具名决策、workspace 越界拒绝、`sudo` / `rm -rf` / `git reset --hard` / `git push --force` 等 **full_access 也硬拦**。
- **上下文管理**：RAG、跨轮压缩、记忆、子 agent、checkpoint/resume/reconcile。
- **工作区**：File 树 / Diff / Problems / PTY Terminal 预览，macOS 打包为独立 app。

### 1.2 还差的关键能力
1. **`apply_patch` 原本是精确匹配，没有 fuzzy 兜底**（已在本轮补上，见 §2）。
   - 原实现：`matchingLineRanges` 逐行精确相等、必须唯一（0 处或 >1 处都把整个 patch 拒绝）。模型读旧代码后上下文一漂移，patch 整体失败 → 重试循环。
2. **没有 git 生命周期抽象**：无 `git_add`/`git_commit`/`git_branch`/`git_checkout`/`git_revert` 这些一等工作流；agent 只能靠 `run_command` 去 shell git，且要过审批。
3. **没有确定性"改→测→修→验证到绿"闭环**（见 §3）：现在是模型驱动的 ReAct，harness 不强制"改完必须跑测试、失败必须修、收敛检测"。
4. **多 agent 并发改同一仓库**：无 per-workspace 写互斥、无 `index.lock` 重试、Diff/Changes 快照会交错。
5. **大型仓库"工作集"管理**：靠 RAG/压缩兜底，缺"只把相关文件放进上下文"的精确机制；成本/时间预算、失败自动分级也没有。
6. **编辑器 / LSP 集成**：见 §4。

### 1.3 量化
- 单 agent、单仓库、人陪跑：**约 85–90%**。
- 能自主写代码（无人逐拍确认、多仓并发、闭环到绿）：**约 60–70%**，缺的集中在上面的 1–5。

---

## 2. `apply_patch` fuzzy（已实现）

> 位置：`internal/agent/tools/patch.go` + `patch_test.go`。

### 2.1 改动
精确匹配找不到（0 处）时，走**保守 fuzzy**；精确能匹配 / 有多处歧义则维持原行为。

- **行匹配忽略行尾空白**：`lineEqForMatch` 用 `strings.TrimRight(a," \t") == ...`，保留行首缩进（避免滑到缩进不同的块）。
- **罕见行当锚点**：`lineDistinctValue` 按行在文件中的出现次数给权重（1 次=4、2 次=3、≤4 次=2、更多=1），函数名 / 唯一字符串这类行把匹配"钉"在正确位置。
- **三道防误改闸**（`bestMatchPosition` 返回后校验）：
  1. 最佳匹配**唯一且明显领先次优**（平局 → 拒绝）；
  2. **至少命中一个高权重锚点**（`hasAnchor`，权重 ≥2 的行）；
  3. **得分 ≥ 55% 总权重**。
  任一不满足 → 仍按 `context was not found` 拒绝，文件不动。
- 仍走**全 hunk 校验 + 原子写**（`atomicReplaceIfUnchanged`），避免半成品落盘。
- 结果新增 `fuzzy`（每个 hunk）+ `fuzzy_hunks` 计数，告诉模型"这步是模糊匹配，请核实"；工具描述同步更新。

### 2.2 测试
- `TestApplyContextPatchFuzzyWhitespaceTolerance`：行尾空白漂移 + 锚点 → 正确模糊应用，`fuzzy=true`。
- `TestApplyContextPatchFuzzyRejectsAmbiguous`：两处等强候选 → 拒绝（不猜）。

### 2.3 说明
- `go test ./internal/agent/tools/`、`go build ./internal/agent/...`、`go vet` 都 ✅（沙箱内需用工作区 GOCACHE，默认可写缓存目录可能被挡）。

---

## 3. "改→测→修→验证到绿"闭环（设计，未实现）

> **评审后更正：本节写偏"理想 harness"了，实际价值被高估。**
> 现在 agent 写完代码本来就会调 `run_command` 跑测试，"验证"能力已具备；缺的只是"由 harness 保证一定测到绿 + 防打转"。但那两点很轻，**不需要独立的验证循环引擎**，优先级也**偏低**（见 §6）。
> 真正值得做的只有两个轻触碰：① 模型"准备结束"但声明过的 verify 命令没重跑时，提醒一句；② 同一条失败重复 N 次时强制停。其余（harness 每步自动跑、失败结构化注入、预算/收敛引擎、plan-mode 编排）都可砍。

这是 **harness 级的确定性编排**，不是让模型自觉记得跑测试。

### 3.1 闭环契约
1. **声明验证命令 + 成功标准**：`run_command` 声明 test/build/lint/typecheck/format，并给"何时算过"（如 `go test ./...` 退出码 0）。harness 才知道目标。
2. **跑 → 解析失败 → 注入上下文**：命令跑完，把 **stdout/stderr 里的失败**（file:line + 错误，而非整段日志）正规化后**注入回模型上下文**。已有部分（结构化验证 → Problems + 跳转），缺"喂给模型让它修"这一环。
3. **改 → 复跑**：模型修完，harness 重跑验证命令，看是否转绿。
4. **预算 + 逃逸**：限制**最大迭代数 / 墙钟时间**，超了就停，并报告"改了 N 轮仍未过 + 最后失败"。
5. **收敛检测**：连续两轮**失败相同**（同文件同行同错）→ 卡死 → 提前终止，避免死循环。
6. **区分"修好"与"绕过"**：识别"把测试注释掉 / 删掉"这种假通过。

### 3.2 落地方式（适配现有架构）
- **轻量**：加一个 `run_verify` 工具（或给 `run_command` 加 `expect` 元数据）——跑命令、返回结构化失败、模型继续循环；harness 在 service 层记录**尝试次数 + 失败指纹**，撞收敛/预算就强制 stop，把"这次没搞完"写进总结。
- **更 harness 化**：在 service 加**验证循环**——模型每产出一次编辑，harness 自动跑测试，红了就把失败交给模型修，作为**连续性续行**（类似 resume）。"绿"由 harness 判，不是模型声称的。
- 关键收益：**确定性**。这块正好接上已有的结构化验证 + Problems + run_command。

---

## 4. 编辑器集成（复杂度评估 + 决策）

**决策：L3（VS Code 扩展桥）往后靠。** 下面给全谱系以便回头再决定。

| 档 | 是什么 | 复杂度 | 说明 |
| --- | --- | --- | --- |
| L0 | 内置面板（Files/Diff/Problems/Terminal） | 已有 | —— |
| L1 | 在编辑器打开（`code file:line`） | 低 | 一次 exec，几乎零成本，DX 直接提升。 |
| L2 | **LSP 集成**（gopls/tsserver 等） | 中 | **代码理解精度的最大杠杆**。DSH 已把它做得很干净（见 §5）。 |
| L3 | VS Code 扩展桥（agent 改动实时进编辑器，事件/LSP 回传） | 中高 | 骨架几天，稳（buffer 冲突协调、LSP、生命周期）要数周。Cursor/Claude Code 的护城河在这。 |
| L4 | 内嵌编辑器（Monaco/CodeMirror） | 中 | 接近"加一个编辑器"，嵌入式而非完整 IDE。 |

> 说明：LSP（L2）与 L3 是**两回事**。L3 往后靠**不影响** L2——L2 是 harness 自己"更懂代码"，成本中等、杠杆最大。

---

## 5. 参考 DeepSeek Harness（`dsh`）

`dsh` 是 DeepSeek 官方 agent harness，**一切皆插件**，用 Cordis 做依赖注入/事件/可逆效果，技术栈 TypeScript + Node。

### 5.1 强项（值得借鉴的"设计模式"）
- **FS 能力族（read-before-edit + version guard）**：`packages/fs` 拆成
  - `fs` Service Definition：provider 契约（执行世界路径、有界文本 IO、**带可选 version guard 的原子变更**）；
  - `fs-observation-policy` 策略门：**observed-state + read-before-edit + version-guarded write/edit**；
  - 模型面工具 `tool-fs` 只管 `read/write/edit` 的 schema/编排，不碰底层。
  这正好解决"多 agent 并发改同一仓库"。LingCoWork 现在只在 `apply_patch` 写前做了一次"文件未变"检查（`atomicReplaceIfUnchanged`），`write_file` 没有读前守卫。
- **模型面 `lsp` 工具（4 个动作）**：`packages/lsp` 只暴露 `goToDefinition` / `findReferences` / `goToImplementation` / `hover`，一基数 UTF-16 坐标，**providers 注册 capability 而非工具**，`tool-lsp` 独占模型面名字/schema/提示词。这是 L2 最干净的实现，可直接照着做。
- **compaction + model-free tool-result pruner**：`packages/compaction` 除摘要外，还有个跟模型无关的**工具结果剪枝器**——把巨大工具输出（长测试失败、大 diff）剪掉，只留"可重读"链接。对 coding harness 极有用。
- **分层原则：capability seam vs model-facing tool**："providers register capabilities, not tools; the model-facing tool owns the name/schema/prompt"。即能力缝与模型面工具分开，backend 可换而不动模型 schema。
- **step/turn 生命周期 + 能力事件**：`step`（一次模型请求 + 其调用的工具）/ `turn`（零或多个 step）有事件（turn/start、step/start、tools/pre-execute/execute/post-execute、tool/result、step/end、turn-stopping），是干净的扩展点模型。

### 5.2 不要借鉴的
- **Cordis 插件框架 / 事件总线 / bundle-profile 体系**：TS 依赖注入 + 可逆效果，与 Go + eino 完全不匹配，移植等于重写。
- **"每个环节都是插件"的极端插件化**：LingCoWork 是单体 Go 应用，不必为可替换而拆成几十个插件。

### 5.3 一个观察（对我们是机会）
浏览 dsh 的工具清单和 agent-loop，它**也没有"改→测→修→验证到绿"的确定性闭环**——验证靠 bash + 模型自觉，`goal-round-driver` 只做"同 session 目标续跑"，没有"红→喂失败→再跑→收敛检测"的强制编排。

**所以 §3 的验证闭环，LingCoWork 可以做得比 dsh 更好**——不是从 dsh 借，而是我们领先的一块。

---

## 6. 落地状态（按判断）

> 更新（基于评审）：**只做了第 1 项**（FS read-before-edit + version guard），其余几项**暂缓**——尤其是确定性验证闭环，目前 agent 写完代码本来就会跑测试，收益被判定为偏低，未列为当前目标。

1. **✅ 已实现：FS read-before-edit + version guard**（§5.1 第一项）
   - `read_file` 返回 `observed_state`（size + mtimeNs，git stat-cache 同款手法）；
   - `write_file` / `apply_patch` 新增可选 `observed_state` 入参：模型把读到的版本回传，工具在写前核对，不一致（文件变了/被删）→ **返回明确冲突错误**（"文件自你读取后已变，请重新 read_file 再改"）；
   - 抽了 `verifyObservedState` 供两处复用，并加测试（`TestObservedStateMatchesAndConflicts`、`TestApplyPatchRejectsStaleObservedState`）。
   - 位置：`internal/agent/tools/fs.go`、`patch.go`。
2. **（暂缓）模型面 `lsp` 工具**（§5.1 第二项，4 个动作）：不依赖 IDE 的代码理解，价值仍在，但未排期。
3. **（暂缓）tool-result pruner**（§5.1 第三项）。
4. **（暂缓 / 不做）确定性验证闭环**（§3）：按评审，当前 agent 已会自己跑测试，此项收益低，未列为目标。
5. **（可选）capability-seam 原则**（§5.1 第四项）：架构清整。

---

## 7. 附：多点（其他值得探索的参考）

- **沙箱 mode 栅栏**：dsh 的 `fs-sandbox` 按 per-call mode（read-only / workspace-write / danger-full-access）+"workspace root"围栏写操作、读放行。LingCoWork 已有 scope/approval，可考虑把"mode"概念形式化。
- **`goal`/`plan`/`todo`**：dsh 有持久目标（同 session，`goal-round-driver`），LingCoWork 已有 plan/todo，可对比扩展。
- **`guard` / 审批**：dsh 的策略门是事件制；LingCoWork 的 effect 审批是等价的，已较强。
