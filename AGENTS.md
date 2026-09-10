# AGENTS.md — Mnemo-Go

## Product

**Mnemo-Go** is a free multi-cloud desktop file manager built with Go + Wails v2.
Mythology: Mnemosyne — memory.

**Active providers** (13, login + `drive.Driver`):
`pikpak` · `onedrive` · `dropbox` · `pan123` · `lanzou` · `ilanzou` · `pan139` · `pan189` · `yike` · `aliopen` · `guangya` · `webdav` · `s3`

> `gofile`、`gdrive` 已按需求移除；`encryption`（加密文件名/加密流）不再支持。

Default login provider: **PikPak**.

## Quick reference

```bash
# 环境：Go ≥ 1.25（实测 1.26.6）、gcc（Windows）、Node ≥ 20
go mod tidy
wails dev          # 热重载开发
wails build        # 产物 build/bin/
go build ./...     # 编译检查
go test ./...      # 单元测试
```

## 技术栈

- **Go 1.25+** + **Wails v2.14**（桌面壳：Go 后端 + WebView2 前端）
- **Vue 3.5 + Vite 6**（前端为纯 JavaScript，非 TypeScript；样式 token 复用旧版）
- 原生 Go HTTP Range 分段下载器（`internal/transfer/dlengine`），不再依赖 aria2c
- **网页播放器**（HTML5 + HLS.js / dash.js），上游鉴权由 `internal/preview` 播放会话代理隔离
- 多份原子 JSON 文件持久化（`internal/store`），无 SQLite；账号凭据使用本地 AES-256-GCM 加密，Windows 新密钥绑定 DPAPI，旧格式可迁移；macOS/Linux 暂保留 0600 兼容密钥文件
- 纯 Go 无 cgo 外部依赖

## Build order

`wails build` → `build/bin/`。前端 `frontend/dist` 由 `wails build` 内嵌（`//go:embed all:frontend/dist`）。
`go build ./...` 仅编译检查后端。

## Secrets

OAuth client_id 等从 `config.LoadSecrets(dataDir)` 读取（`internal/config`）。OneDrive/Dropbox 的 OAuth PKCE 在各 provider 的 `auth.go` 实现，本地回调监听 `127.0.0.1:0` 随机端口。

## Architecture

| Directory | Purpose |
|---|---|
| `main.go` | Wails 应用装配（入口） |
| `internal/app/` | Wails 绑定层（前端调用的全部方法 + 事件） |
| `internal/drive/` | provider 插件契约（接口/能力/元数据/注册表）+ ops 门面 |
| `internal/drive/providers/` | 13 个网盘插件（每个一个包，init() 注册） |
| `internal/drive/driveutil/` | 通用工具（路径、冲突策略） |
| `internal/model/` | 统一数据模型（文件/账号/分享/任务/设置） |
| `internal/netx/` | HTTP 客户端/上传/哈希/限速 |
| `internal/store/` | 本地持久化（账号/设置/标签/收藏/任务，原子 JSON） |
| `internal/transfer/` | 下载管理器 + 上传队列 + 跨盘迁移 |
| `internal/transfer/dlengine/` | 原生 Go 分段下载器（Range + 断点续传） |
| `internal/transfer/migrate/` | 跨盘迁移引擎 |
| `internal/sync/` | 双向同步引擎 |
| `internal/preview/` | 本地 Range/播放会话代理（鉴权流、HLS/DASH、字幕，会话令牌保护） |
| `internal/config/` | secrets 加载、UserDataDir |
| `frontend/` | Vue 3 前端（Wails webview，纯 JS） |
| `docs/` | 架构/加盘指南/设计规范/网盘状态/迁移进度 |

**Drive plugin layer:** `internal/drive/` — `driver.go`(契约) + `ops.go`(门面) + `registry.go`(注册表) + `capabilities.go`(能力位)。UI/传输只走 `drive/ops`，禁止中央 `if (provider == …)`。

加盘：`internal/drive/providers/<id>/` 实现 `drive.Driver` → `Register(drive.Registration{...})` → `providers/all.go` 空白导入。详见 `docs/PROVIDER_GUIDE.md`。

## Out of scope (removed)

`gofile`、`gdrive`、`encryption`（加密文件名/加密流）已移除。媒体库 UI、媒体服务器、WebDAV server 等也不在范围内。

## Testing

`go test ./...`、`go test -race ./...`、`go vet ./...`。现有测试覆盖网盘驱动、传输、同步、预览、存储与 e2e（dlengine/webdav/provider mock）。前端使用 Vitest + Vue Test Utils，运行 `cd frontend && npm test`；浏览器工作区验收见 `frontend/workspace.browser.cjs`。发布前需通过 `gofmt -l .` 与版本一致性检查。

## Provider checklist

新增 provider 参见 `docs/PROVIDER_GUIDE.md` 和 `docs/providers/<id>.md`（13 个盘的功能详情与迁移状态）。
