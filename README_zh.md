# Universal Local Video Recorder Daemon

[English](README.md) | **简体中文**

[![Go Version](https://img.shields.io/badge/Go-1.22+-00ADD8?style=flat&logo=go)](https://go.dev/)
[![Platform](https://img.shields.io/badge/Platform-Windows%20%7C%20macOS%20%7C%20Linux-blue)](#桌面构建)
[![DevContainer](https://img.shields.io/badge/DevContainer-Supported-007ACC?style=flat&logo=visualstudiocode)](.devcontainer/devcontainer.json)

本地视频录像服务。进程通过 FFmpeg 读取所选的内置、外接或虚拟摄像头，并持续提供低延迟 WebSocket 实时预览。只有明确开始录像后才会将 JPEG 帧写入临时存储，并可异步保存为 MP4；开始和保存录像均不会重启摄像头或 FFmpeg 进程。

## 功能

- 摄像头采集，采集进程异常后自动重连
- 可配置内存批次且线程安全的磁盘临时采集片段
- 显式录像会话、可配置自动超时以及超时数据清理
- WebSocket 实时 JPEG 帧预览，慢客户端不会阻塞采集
- 异步 H.264 MP4 导出，临时文件编码成功后原子发布
- 实时预览与导出文件可共用当前时间水印
- 导出队列、重复文件名自动编号与任务状态查询
- 支持中英文且可自动选择语言的本地配置和预览控制台
- Windows、macOS 和 Linux 原生托盘，菜单根据系统语言自动使用中文或英文，并支持可选的 headless 模式
- 配置原子保存、优雅退出和 Web Origin 白名单

## 系统架构

```text
┌─────────────────────────────────────────────────────────┐
│             Local Go Daemon Service Process             │
│                                                         │
│  ┌───────────────┐     ┌─────────────────────────────┐  │
│  │ OS SysTray UI │     │     Camera Video Stream     │  │
│  └───────────────┘     └──────────────┬──────────────┘  │
│                                       │                 │
│             ┌─────────────────────────┴───────┐         │
│             │                                 │         │
│             ▼                                 ▼         │
│  ┌────────────────────┐      ┌────────────────────────┐ │
│  │ WebSocket Service  │      │ Active Recording Only  │ │
│  │ (Live Web Preview) │      │ Memory + Temporary File│ │
│  └──────────┬─────────┘      └───────────┬────────────┘ │
│             │                            ▼              │
│             │                    ┌──────────────┐       │
│             │                    │ Export Task  │       │
│             │                    └──────┬───────┘       │
└─────────────┼─────────────────────────────────┼─────────┘
              │ WebSocket                       │ POST /api/v1/record/save
              ▼                                 │ (with fileName)
┌───────────────────────────────────────────────┴─────────┐
│                     Web Application                     │
└─────────────────────────────────────────────────────────┘
```

## 快速开始

要求 Go 1.22+ 和 FFmpeg 5+。FFmpeg 必须包含 `mjpeg` 解码器和 `libx264` 编码器。`drawtext` 滤镜为可选能力；缺少时会继续无水印采集，并在 Console 中显示提示。

```bash
go run -tags=headless ./cmd/recorder
```

默认从 `127.0.0.1:8800` 启动。访问 <http://127.0.0.1:8800/> 会自动跳转到 Console。如果默认端口被占用，会依次尝试 `8801`、`8802` 等后续端口，最终 URL 会写入日志并由托盘菜单打开。首次运行默认使用 30 FPS 的 FFmpeg 测试画面，因此没有摄像头也可以验证预览和导出。开发运行时若存在 `configs/config.json` 则使用该文件；打包后的应用会在系统用户配置目录中创建配置，例如 macOS 的 `~/Library/Application Support/video-recorder/config.json` 和 Windows 的 `%AppData%\video-recorder\config.json`。macOS 首次生成的配置默认将 `capture.ffmpegPath` 设为 `/opt/homebrew/bin/ffmpeg`，其他平台默认从 `PATH` 查找 `ffmpeg`。视频写入配置的存储目录。

控制台首次打开时根据 `navigator.languages`/`navigator.language` 自动选择中文或英文。页眉中的语言选择器可以手动覆盖并记住选择；切回“自动”会恢复浏览器语言检测。

使用其他配置文件：

```bash
go run -tags=headless ./cmd/recorder -config /path/to/config.json
```

### 摄像头设备

控制台会自动检测可用的内置、USB 外接和虚拟摄像头。选择设备后会尽力探测其像素格式、视频编码、分辨率和帧率，并在存在可用结果时应用完整的推荐硬件模式。硬件模式下拉框会将输入格式、分辨率和帧率作为一个受支持的组合；底层字段仍可编辑，探测不完整或硬件特殊时可以手工设置。连接或断开摄像头后，可使用列表旁的刷新按钮重新检测。

| 平台 | 保存的设备 ID 示例 | FFmpeg 输入 |
| --- | --- | --- |
| Linux | `/dev/video0` | `v4l2` |
| macOS | `0` | `avfoundation` |
| Windows | `@device_pnp_...` | `dshow` |

Linux 通过 `/dev/video*` 检测设备；macOS 和 Windows 使用 FFmpeg 的原生设备列表。能力探测始终针对当前选择的设备，因此 macOS 内置摄像头和 USB 外接摄像头可以返回不同参数。Windows 在 FFmpeg 提供 DirectShow alternative name 时保存该稳定标识，控制台仍显示易读的摄像头名称。DirectShow 原始画面模式使用 `pixelFormat`，MJPEG 等压缩模式使用 `videoCodec`，避免高带宽模式被错误地强制为不受支持的原始像素格式。

保存配置时会持久化所选设备 ID。仅当视频源、设备、像素格式、分辨率、帧率、JPEG 质量或 FFmpeg 路径等采集参数变化时，FFmpeg 才会重新连接；修改存储和 Web 配置不会重新连接摄像头。如果所选设备暂时不可用，服务每 2 秒重试并在状态接口显示最近错误。

## API

### 录像流程

应用启动后只进行实时预览，不会把帧写入临时存储。明确开始一次录像：

```http
POST /api/v1/capture/reset
```

该接口会丢弃尚未保存的当前录像并开始新的录像，但不会重启 FFmpeg 或重新打开摄像头。

保存当前录像：

```http
POST /api/v1/record/save?fileName=task_20260728_001
```

成功响应表示当前录像已原子加入保存队列。随后停止录像但继续实时预览；如需再次录像，必须重新调用 `POST /api/v1/capture/reset`。响应不表示文件已经写完：

```json
{
  "code": 200,
  "message": "video export task accepted; live preview continues",
  "data": {
    "fileName": "task_20260728_001.mp4",
    "jobId": "1785230534619-000001",
    "state": "queued"
  }
}
```

`fileName` 不允许目录分隔符、控制字符和 Windows 非法字符。已有文件不会被覆盖：`recording.mp4` 后续会依次保存为 `recording_01.mp4`、`recording_02.mp4`。没有活动录像或录像中还没有帧时返回 `409`，队列已满返回 `503`。

### 查询导出任务

```http
GET /api/v1/record/jobs/{jobId}
```

任务状态为 `queued`、`running`、`completed` 或 `failed`。

### 实时预览

```text
ws://127.0.0.1:<实际端口>/ws/live
```

每条 WebSocket binary message 是一张完整 JPEG，可直接交给 `createImageBitmap`、`Blob` 或 Canvas 渲染。客户端跟不上采集速率时只保留最新帧。

### 配置和状态

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| `GET` | `/` | 跳转到 `/console` |
| `GET` | `/console` | 本地控制台 |
| `GET` | `/api/v1/config` | 获取完整配置 |
| `PUT` | `/api/v1/config` | 校验并持久化；仅采集参数变化时重新连接 |
| `POST` | `/api/v1/config/reset` | 恢复并持久化默认采集参数 |
| `GET` | `/api/v1/cameras` | 检测可用摄像头设备 |
| `GET` | `/api/v1/cameras/capabilities?device=...&pixelFormat=...&videoCodec=...` | 尽力检测设备支持的完整输入模式 |
| `POST` | `/api/v1/storage/directory/select` | 打开系统存储目录选择框 |
| `GET` | `/api/v1/status` | 设备、录像、缓冲和预览连接状态 |
| `POST` | `/api/v1/capture/reset` | 丢弃尚未保存的录像并开始新的录像 |

Console 提供数值形式的服务端口配置。修改端口后需重启应用才会生效，其他采集和存储配置立即生效。自动端口回退仅用于默认端口 `8800`；明确配置的其他端口会严格绑定，不会静默切换。

控制台状态包括“设备不可用”“重连中”“实时预览”“录制中”和“超时停止录制”。“已录制时间”和“已录制帧数”只统计当前活动录像。“新的录制”会先清除当前内存批次和临时文件，再开始录像；“保存”在录像加入队列后停止录像，但 FFmpeg 和实时预览保持运行。达到配置的最长时长后会停止录像，并删除所有尚未保存的内存和临时文件数据；默认时长为 60 分钟。当 FFmpeg 包含 `drawtext` 时，实时预览和导出视频的右上角仅显示当前时间。

控制台通过操作系统目录选择框选择视频存储目录，并随其他配置保存系统返回的绝对路径。文件默认按天存放在 `yyyyMMdd` 子目录中，也可以选择按月存放到 `yyyyMM` 子目录或不分类。Linux 桌面环境的目录选择框需要安装 Zenity 或 KDialog；headless 环境可直接在 JSON 配置中设置 `storage.directory` 和 `storage.organization`。“重置配置”只恢复帧率、宽度、高度、内存缓冲时长和 JPEG 质量，其他设置和当前活动录像保持不变。

### Origin 白名单

控制台默认仅允许同源浏览器访问。外部业务 Web 应用需要将完整 Origin 加入 `server.allowedOrigins`：

```json
{
  "server": {
    "address": "127.0.0.1:8800",
    "allowedOrigins": ["https://app.example.com"]
  }
}
```

可以显式配置 `"*"`，但这会允许任意网页读取本机摄像头预览和调用导出接口，不建议用于生产环境。无 `Origin` 的本机程序和命令行请求不受此限制。

## 配置参考

```json
{
  "server": {
    "address": "127.0.0.1:8800",
    "allowedOrigins": []
  },
  "capture": {
    "source": "mock",
    "device": "",
    "pixelFormat": "",
    "videoCodec": "",
    "width": 1280,
    "height": 720,
    "fps": 30,
    "jpegQuality": 5,
    "bufferSeconds": 30,
    "ffmpegPath": "ffmpeg"
  },
  "recording": {
    "maxDurationMinutes": 60
  },
  "storage": {
    "directory": "recordings",
    "organization": "day"
  },
  "export": {
    "queueSize": 8
  }
}
```

`jpegQuality` 使用 FFmpeg 的量化范围，`2` 质量最高、`31` 最低。`bufferSeconds` 控制已录制 JPEG 帧在应用内存中累计多久后，批量追加一次到临时录像文件；它不限制最终保存视频的时长。默认 30 秒因此是应用层写入间隔，不保证磁盘每 30 秒物理刷盘：程序不会调用 `fsync`，实际落盘由操作系统控制。保存时会先写出剩余内存批次，再原子移交录像；开始新的录像和自动超时都会清除当前内存批次及对应临时文件。`recording.maxDurationMinutes` 控制该超时时间，默认为 60 分钟。

摄像头输入必须且只能设置 `capture.pixelFormat` 或 `capture.videoCodec` 中的一项。DirectShow 原始模式使用 `pixelFormat`，MJPEG 等压缩模式使用 `videoCodec`。

只有活动录像中的 JPEG 帧会保存在应用专属的系统临时目录中，直到保存、被新录像替换或超时。分辨率、帧率、画面复杂度、内存缓冲时长和已录制时间会影响内存及临时磁盘占用；正常退出时会删除当前临时目录。

`storage.organization` 可设为 `day`、`month` 或 `none`。例如存储目录为 `recordings` 时，文件将分别写入 `recordings/20260729`、`recordings/202607` 或直接写入 `recordings`。

## 桌面构建

Linux 桌面构建依赖 GTK 3 和 Ayatana AppIndicator 开发文件。在 Debian 或 Ubuntu 上：

```bash
sudo apt-get install gcc libgtk-3-dev libayatana-appindicator3-dev
go build -tags=desktop -o video-recorder-linux ./cmd/recorder
```

运行时需要安装 FFmpeg、`libayatana-appindicator3-1`，以及 Zenity 或 KDialog 之一。其他发行版需安装对应的 GTK 3 与 Ayatana AppIndicator 软件包。

Windows：

```bash
GOOS=windows GOARCH=amd64 go build -ldflags="-H windowsgui" -o video-recorder.exe ./cmd/recorder
```

macOS 原生托盘依赖 Cocoa，需要在 macOS 主机上构建：

```bash
GOOS=darwin GOARCH=arm64 go build -o video-recorder-mac ./cmd/recorder
```

无桌面托盘的构建使用 `-tags=headless`。Linux 未指定 `desktop` 标签时也默认使用 headless 模式，使服务器和容器构建无需 GTK 依赖。生产机器仍需安装 FFmpeg，或在配置中将 `capture.ffmpegPath` 指向随应用分发的 FFmpeg 可执行文件。

当程序所在目录不存在 `configs/config.json` 时，打包应用会在操作系统的用户配置目录创建配置。初始视频目录在 macOS 上为 `~/Movies/Video Recorder`，在 Windows 和 Linux 上为用户的 `Videos/Video Recorder` 目录。

## GitHub Actions 构建包

[构建工作流](.github/workflows/build.yml) 仅在手动触发或推送匹配 `v*` 的版本标签时运行。测试和静态检查通过后生成：

- `video-recorder-windows-amd64.zip`：包含 Windows GUI 可执行文件、默认配置、文档和许可证
- `video-recorder-linux-amd64.tar.gz`：包含 Linux 桌面可执行文件、默认配置、文档和许可证
- `video-recorder-macos-universal.zip`：包含同时支持 Intel 和 Apple Silicon 的临时签名 `.app`

三个压缩包会在工作流运行页面的 Artifacts 区域保留 14 天。推送 `v*` 标签时还会自动创建或更新 GitHub Release，并上传全部三个压缩包。

```bash
git tag v1.0.0
git push origin v1.0.0
```

发行包不内置 FFmpeg。目标机器需要安装 FFmpeg，或者将 `capture.ffmpegPath` 指向单独分发的可执行文件。Linux 制品面向兼容 Ubuntu 22.04 的 amd64 发行版，并依赖 GTK 3/Ayatana AppIndicator 运行库。macOS 制品使用临时签名但没有经过 Apple 公证；正式公开发行时应在工作流中配置 Developer ID 签名和公证凭据。

## 开发与验证

```bash
go test ./...
go test -race ./...
go vet ./...
```

DevContainer 已安装 FFmpeg，可直接运行上述命令。Linux 访问摄像头时还需将对应的 `/dev/video*` 设备映射进容器。

## 目录

```text
video-recorder/
├── .devcontainer/       # Go + FFmpeg 开发容器
├── cmd/recorder/        # 进程入口和生命周期
├── configs/             # 默认持久化配置
├── internal/api/        # HTTP 与 WebSocket
├── internal/config/     # 配置校验和原子存储
├── internal/service/    # 采集片段、预览和导出
├── internal/tray/       # 桌面与 headless 托盘适配
├── pkg/logger/          # 结构化日志
├── web/                 # 嵌入二进制的控制台
├── go.mod
├── README.md
└── README_zh.md
```

## License

[MIT](LICENSE)
