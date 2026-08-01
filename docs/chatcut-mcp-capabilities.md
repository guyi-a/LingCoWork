# ChatCut MCP 能力清单

已装好的 ChatCut MCP 工具集能力总览。

## 1. 项目管理
- 创建/复制/删除/恢复项目、切换目标项目
- 多时间线（Timeline）管理：创建、复制、切换、改画布比例（16:9 / 9:16 / 1:1）
- 项目/时间线预览：`show_preview` 在聊天里嵌入编辑器

## 2. 素材管理（媒体池）
- **导入**：本地文件、公网 URL、直接下载媒体到项目
- **组织**：文件夹式媒体池、重命名、移动素材
- **检查**：读取项目结构、抽帧看内容（`view_asset_frames`）
- 下载导出媒体给用户

## 3. 时间线编辑（核心）
- **轨道**：多视频/音频轨道，支持角色标签（anchor 主讲 / follower 背景乐自动 ducking）
- **片段**：增删改、分割（`split_item`）、trim、fade、位置、速度
- **原子批量**：一次 `edit_item` 里 `adds` + `updates` + `deletes` 原子提交
- Ripple 模式（插入时后面自动让位）

## 4. 基于转录文本的剪辑（Script 编辑，特色能力）
- 用 `read_script` 把整个时间线渲染成一份 `timeline.md`
- 直接编辑 markdown（`~~删除~~`、重排、复制）
- `apply_script` 提交到时间线
- 适合：删口误/嗯啊、挑最好的一条、精简一句话、做多版本
- `find_transcript` 定位某句话的时间戳
- `clean_script` 一键批量清理 filler（um / uh / 呃 / 额）和长停顿

## 5. 字幕（Captions）
- 一键开启，支持 20+ 预设风格（plain / tiktok / netflix / boyz-n-the-hood 等）
- 双语字幕、翻译（`language_mode`）
- 多源字幕（不同轨道分别显示）
- 单词级隐藏/替换（脏话打码、隐藏 filler）
- 位置、字号、字体、描边、阴影、高亮着色全部可调
- 用户自定义预设保存/复用

## 6. Motion Graphics（MG 动画）
- 用 React/JSX 代码即时创建 MG 资产（`create_motion_graphic_from_code`）
- 内置 Library 有大量成品 MG 可直接用（`browse_library`）
- 编辑 MG 属性（文字、颜色、logo 等 property overrides）
- 支持字体查询（`search_fonts`：Google Fonts 全库 + 中文字体如"得意黑"）
- MG → 透明 WebM 视频转换

## 7. AI 生成（付费能力）
- **视频**：Kling、Seedance2，支持首尾帧、多镜头 storyboard
- **语音（TTS）**：ElevenLabs + Doubao（豆包），大量预设声音、情感控制
- **音效**：ElevenLabs sound effect
- **音乐**：AI 生成配乐
- **Shader**：GLSL 效果/转场生成
- 全部异步 job + `track_progress` 轮询

## 8. Library（内置资源库）
- MG 模板、LUT 调色、Zoom 效果、转场、音效、社媒风格卡片
- `browse_library` 分类浏览
- 一键上时间线

## 9. 音频处理
- **AI 语音降噪/去回响**：`isolate_voice`（DeepFilterNet3）
- 自动 ducking：BGM 遇到人声自动压低

## 10. 导出
- 视频（H264 mp4 / VP8 WebM）
- 音频（MP3）
- 字幕（SRT / TXT）
- NLE XML（Premiere / DaVinci Resolve 交换格式）
- ProRes 4444 MG 单独导出

## 11. 高级功能
- **视觉分析**：Firecrawl 网页截图、抽帧看画面、看时间线渲染帧
- **模板**：整套模板导入（用 `manage_template`）
- **标记**：时间线注释 marker
- **设计风格**：品牌色/字体/logo 统一管理（Design Style）
- **多语言字幕/翻译**

---

## 能做什么样的完整视频

- **播客** → 精剪短片 + 字幕
- **长视频** → 抖音 / YouTube Shorts
- **讲解视频** → 加 MG 动画 / 字幕 / BGM
- **采访** → 双语字幕 + 去 filler
- **从零生成** → AI 生视频 + AI 配音 + AI 配乐 + 字幕
