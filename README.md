# Universal Local Video Recorder Daemon

**English** | [简体中文](README_zh.md)

[![Go Version](https://img.shields.io/badge/Go-1.22+-00ADD8?style=flat&logo=go)](https://go.dev/)
[![Platform](https://img.shields.io/badge/Platform-Windows%20%7C%20macOS%20%7C%20Linux-blue)](#desktop-build)
[![DevContainer](https://img.shields.io/badge/DevContainer-Supported-007ACC?style=flat&logo=visualstudiocode)](.devcontainer/devcontainer.json)

A local video recording service. It uses FFmpeg with the selected built-in, external, or virtual camera and provides a continuous low-latency WebSocket preview. Recording starts only when explicitly requested, spools JPEG frames to temporary storage, and can be saved asynchronously as MP4 without restarting the camera or FFmpeg process.

## Features

- Camera capture with automatic process recovery
- Configurable in-memory batching with thread-safe, disk-backed capture segments
- Explicit recording sessions with a configurable automatic timeout and cleanup
- Live JPEG preview over WebSocket without backpressure from slow clients
- Asynchronous H.264 MP4 export with atomic publication after encoding
- Optional current-time video watermark shared by live preview and exported files
- Bounded export queue, automatic duplicate-name numbering, and job status tracking
- English and Chinese configuration console with automatic language selection
- Native Windows, macOS, and Linux tray with system-language Chinese/English menus and an optional headless mode
- Atomic configuration updates, graceful shutdown, and web Origin allowlist

## Architecture

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

## Quick Start

Go 1.22+ and FFmpeg 5+ are required. The FFmpeg build must include the `mjpeg` decoder and `libx264` encoder. When the optional `drawtext` filter is unavailable, capture continues without watermarks and the Console displays a warning.

```bash
go run -tags=headless ./cmd/recorder
```

The service starts at `127.0.0.1:9000` by default. Open <http://127.0.0.1:9000/> to be redirected to the Console. If that default port is occupied, it tries `9001`, `9002`, and subsequent ports until one is available; the selected URL is written to the log and opened by the tray menu. The initial configuration uses an FFmpeg test pattern at 30 FPS, so preview and export work without a camera. Configuration is stored in `configs/config.json`, and videos are written to `recordings/` by default.

On first load, the console selects English or Chinese from `navigator.languages`/`navigator.language`. The language selector in the header can persist a manual override; selecting Auto restores browser-language detection.

To use a different configuration file:

```bash
go run -tags=headless ./cmd/recorder -config /path/to/config.json
```

### Camera Devices

The console automatically detects available built-in, USB, and virtual cameras. Selecting a device probes its pixel formats, resolutions, and frame rates on a best-effort basis and applies a recommended concrete mode when one is available. The exact results depend on the device, driver, platform, and FFmpeg build. Every suggested field remains editable, so incomplete probes and unusual hardware can be configured manually. Use the refresh button beside the list after connecting or disconnecting a camera.

| Platform | Saved device ID example | FFmpeg input |
| --- | --- | --- |
| Linux | `/dev/video0` | `v4l2` |
| macOS | `0` | `avfoundation` |
| Windows | `@device_pnp_...` | `dshow` |

Linux detection enumerates `/dev/video*`; macOS and Windows detection use FFmpeg's native device listing. Capability probing always targets the selected device, so macOS built-in and USB cameras may expose different modes. On Windows, the stable DirectShow alternative name is saved when FFmpeg provides one, while the readable camera name remains visible in the console.

Saving settings persists the selected device ID. FFmpeg reconnects only when capture parameters such as the source, device, pixel format, resolution, frame rate, JPEG quality, or FFmpeg path change. Storage and web settings do not reconnect the camera. If the selected device is unavailable, the service retries every two seconds and exposes the latest error through the status endpoint.

## API

### Recording Workflow

The application starts in live-preview mode and does not write frames to temporary storage. Start a recording explicitly:

```http
POST /api/v1/capture/reset
```

This endpoint discards any unsaved active recording and starts a new one. It does not restart FFmpeg or reopen the camera.

Save the active recording:

```http
POST /api/v1/record/save?fileName=task_20260728_001
```

A successful response means the active recording was atomically queued for saving. Recording then stops while live preview continues; another `POST /api/v1/capture/reset` is required to record again. The response does not mean the output file is already complete:

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

`fileName` cannot contain path separators, control characters, or Windows-invalid characters. Existing files are never overwritten: `recording.mp4` is followed by `recording_01.mp4`, `recording_02.mp4`, and so on. Saving without an active recording or without recorded frames returns `409`; a full queue returns `503`.

### Query an Export Job

```http
GET /api/v1/record/jobs/{jobId}
```

Job states are `queued`, `running`, `completed`, and `failed`.

### Live Preview

```text
ws://127.0.0.1:<selected-port>/ws/live
```

Each WebSocket binary message contains one complete JPEG image suitable for `createImageBitmap`, `Blob`, or Canvas rendering. When a client cannot keep up with the capture rate, only its newest pending frame is retained.

### Configuration and Status

| Method | Path | Description |
| --- | --- | --- |
| `GET` | `/` | Redirect to `/console` |
| `GET` | `/console` | Local management console |
| `GET` | `/api/v1/config` | Read the complete configuration |
| `PUT` | `/api/v1/config` | Validate and persist; reconnect capture only when capture parameters change |
| `POST` | `/api/v1/config/reset` | Restore and persist the default capture parameters |
| `GET` | `/api/v1/cameras` | Detect available camera devices |
| `GET` | `/api/v1/cameras/capabilities?device=...&pixelFormat=...` | Best-effort detection of pixel formats, resolutions, and frame rates for a device |
| `POST` | `/api/v1/storage/directory/select` | Open the system storage-directory picker |
| `GET` | `/api/v1/status` | Device, recording, buffer, and preview-client status |
| `POST` | `/api/v1/capture/reset` | Discard any unsaved recording and start a new recording |

The Console exposes the service port as a numeric setting. Port changes take effect after restarting the application; capture and storage settings take effect immediately. Automatic port fallback applies only to the default port `9000`, while an explicitly configured non-default port remains strict.

The Console reports `Device unavailable`, `Reconnecting`, `Live preview`, `Recording`, or `Recording stopped: time limit`. Recorded duration and frame count apply only to the active recording. `New recording` discards the current in-memory batch and temporary file before starting again. `Save` stops recording after the segment is queued, while FFmpeg and live preview remain active. When the configured duration limit is reached, recording stops and all unsaved memory and temporary-file data is deleted. The default limit is 60 minutes. When FFmpeg provides `drawtext`, each preview and exported frame contains only the current time in the upper-right corner.

The console selects the video storage directory through the operating system's directory picker and saves the returned absolute path with the rest of the configuration. Files are organized by day (`yyyyMMdd`) by default; monthly (`yyyyMM`) and unclassified storage are also available. On Linux desktops the directory picker requires Zenity or KDialog; headless environments can set `storage.directory` and `storage.organization` directly in the JSON configuration. Reset Settings restores only frame rate, width, height, memory buffer duration, and JPEG quality; other settings and the active recording are retained.

### Origin Allowlist

The console permits same-origin browser access by default. Add the complete Origin of an external business web application to `server.allowedOrigins`:

```json
{
  "server": {
    "address": "127.0.0.1:9000",
    "allowedOrigins": ["https://app.example.com"]
  }
}
```

An explicit `"*"` permits any website to read the local camera preview and invoke exports, so it is not recommended in production. Local programs and command-line requests without an `Origin` header are unaffected.

## Configuration Reference

```json
{
  "server": {
    "address": "127.0.0.1:9000",
    "allowedOrigins": []
  },
  "capture": {
    "source": "mock",
    "device": "",
    "pixelFormat": "",
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

`jpegQuality` uses FFmpeg's quantizer scale: `2` is the highest quality and `31` the lowest. `bufferSeconds` controls how long recorded JPEG frames are batched in application memory before one append to the temporary recording file; it does not limit the saved video duration. The default 30 seconds is therefore an application write interval, not a guaranteed physical-disk flush interval: the recorder does not call `fsync`, and the operating system controls physical writeback. Saving flushes the remaining memory batch before atomically detaching the recording. Starting a new recording and automatic timeout both discard the current memory batch and its temporary file. `recording.maxDurationMinutes` controls that timeout and defaults to 60 minutes.

Only frames from an active recording are retained in the application-owned system temporary directory until they are saved, replaced, or timed out. Resolution, frame rate, scene complexity, buffer duration, and elapsed recording time affect memory and temporary disk usage. The active temporary directory is removed during graceful shutdown.

`storage.organization` accepts `day`, `month`, or `none`. For example, a base directory of `recordings` writes to `recordings/20260729`, `recordings/202607`, or directly to `recordings`, respectively.

## Desktop Build

Linux desktop builds require GTK 3 and Ayatana AppIndicator development files. On Debian or Ubuntu:

```bash
sudo apt-get install gcc libgtk-3-dev libayatana-appindicator3-dev
go build -tags=desktop -o video-recorder-linux ./cmd/recorder
```

At runtime, install FFmpeg, `libayatana-appindicator3-1`, and either Zenity or KDialog. Other distributions require the equivalent GTK 3 and Ayatana AppIndicator packages.

Windows:

```bash
GOOS=windows GOARCH=amd64 go build -ldflags="-H windowsgui" -o video-recorder.exe ./cmd/recorder
```

The native macOS tray uses Cocoa and must be built on a macOS host:

```bash
GOOS=darwin GOARCH=arm64 go build -o video-recorder-mac ./cmd/recorder
```

Use `-tags=headless` for a build without a desktop tray. Linux builds without the `desktop` tag also default to headless mode, keeping server and container builds free of GTK dependencies. FFmpeg must still be installed on production machines, or `capture.ffmpegPath` must point to an FFmpeg executable distributed with the application.

When no repository-local `configs/config.json` exists, packaged applications create their configuration under the operating system's user configuration directory. The initial video directory is `~/Movies/Video Recorder` on macOS and the user's `Videos/Video Recorder` directory on Windows and Linux.

## GitHub Actions Packages

The [build workflow](.github/workflows/build.yml) runs on manual dispatches and pushes of version tags matching `v*`. After tests and static analysis, it produces:

- `video-recorder-windows-amd64.zip`, containing the Windows GUI executable, default configuration, documentation, and license
- `video-recorder-linux-amd64.tar.gz`, containing the Linux desktop executable, default configuration, documentation, and license
- `video-recorder-macos-universal.zip`, containing an ad-hoc signed `.app` with both Intel and Apple Silicon architectures

All three archives are available from the workflow run's Artifacts section for 14 days. A `v*` tag also creates or updates a GitHub Release and attaches all three archives.

```bash
git tag v1.0.0
git push origin v1.0.0
```

FFmpeg is not bundled in these archives. Install it on the target machine or update `capture.ffmpegPath` to a separately distributed executable. The Linux artifact targets amd64 distributions compatible with Ubuntu 22.04 and requires GTK 3/Ayatana AppIndicator runtime libraries. The macOS artifact is ad-hoc signed but not Apple-notarized; a production public release should add Developer ID signing and notarization credentials to the workflow.

## Development and Verification

```bash
go test ./...
go test -race ./...
go vet ./...
```

The DevContainer includes FFmpeg. To access a camera from Linux, also map the appropriate `/dev/video*` device into the container.

## Project Layout

```text
video-recorder/
├── .devcontainer/       # Go + FFmpeg development container
├── cmd/recorder/        # Process entry point and lifecycle
├── configs/             # Default persisted configuration
├── internal/api/        # HTTP and WebSocket transport
├── internal/config/     # Validation and atomic config storage
├── internal/service/    # Capture segments, preview, and export
├── internal/tray/       # Desktop and headless tray adapters
├── pkg/logger/          # Structured logging
├── web/                 # Console embedded in the binary
├── go.mod
├── README.md
└── README_zh.md
```

## License

[MIT](LICENSE)
