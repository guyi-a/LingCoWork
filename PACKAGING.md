# LingCoWork 桌面打包

## macOS 构建

要求 macOS、Go 1.26+、Node.js 20+ 和 pnpm。执行：

```bash
./scripts/package-macos.sh
```

脚本依次构建 React 前端、`darwin/arm64` Go 后端、Electron TypeScript，并用
Electron Builder 输出：

- `release/mac-arm64/LingCoWork.app`
- `release/LingCoWork-<version>-arm64.dmg`

## Windows 构建

要求 64 位 Windows、Go 1.26+、Node.js 20+ 和 pnpm。在 PowerShell 中执行：

```powershell
.\scripts\package-windows.ps1
```

脚本依次安装锁定版本的依赖、构建 React 前端、`windows/amd64` Go 后端和
Electron TypeScript，随后用 Electron Builder 输出：

- `release/win-unpacked/LingCoWork.exe`
- `release/LingCoWork-<version>-x64-setup.exe`

安装程序使用 NSIS，可选择安装目录，并创建开始菜单和桌面快捷方式。当前产物未做
Windows 代码签名，因此下载或首次运行时 SmartScreen 可能显示未知发布者警告。

安装包只包含编译产物、`.env.example` 和图标，不包含仓库 `.env`、数据库或工作区。

## 运行结构

Electron Main 从应用的 `Resources/web` 提供 `lingcowork://app`。macOS 从
`Resources/backend/lingcowork-api` 启动后端，Windows 从
`resources/backend/lingcowork-api.exe` 启动后端。窗口会等
`http://127.0.0.1:9001/healthz` 成功后再显示。

后端只监听回环地址。应用退出时，macOS 使用进程组信号清理后端，Windows 使用
`taskkill /T /F` 清理完整进程树。端口 9001 已被占用时只显示冲突，不会结束占用进程。

## 数据和配置

生产数据根目录是：

```text
~/Library/Application Support/LingCoWork
```

其中包括 `.env`、`data/`、`.workspace/`、`.lingcowork/`、`attachments/` 和
`logs/backend.log`。首次启动可导入已有 `.env`；如果不导入，应用会在此处创建模板，
填写 `DEEPSEEK_API_KEY` 后重新打开。

Windows 上对应的数据根目录是：

```text
%APPDATA%\LingCoWork
```

## 冒烟检查

1. 确保没有运行 `dev.sh`，从 `.app` 或 DMG 冷启动。
2. 确认主窗口可见，`curl http://127.0.0.1:9001/healthz` 返回 `{"status":"ok"}`。
3. 验证对话 SSE、审批恢复、文件选择、图片粘贴和本地文件预览。
4. 重启应用，确认会话、Memory、Workspace 和设置仍存在。
5. 退出应用，确认 9001 端口已释放，且没有残留 Go、MCP 或浏览器子进程。
6. 单独验证 MCP OAuth 回调、Chrome Bridge 和依赖本机 Node/Python/Chromium 的 Skills。

## 首版限制

- macOS 仅构建 Apple Silicon，不构建 Intel 或 Universal Binary。
- Windows 仅构建 x64，不构建 arm64。
- 未接入 Developer ID/Authenticode 签名、公证、自动更新或 CI 发布。
- 不捆绑 Node、Python、browser-use Chromium 或 API Key。
- GUI 启动时的 PATH 可能少于终端；依赖外部命令的 Skill 应返回明确的“未安装”错误。
