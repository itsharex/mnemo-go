<div align="center">

# 🧠 Mnemo

**一个免费的跨平台多云网盘桌面文件管理器**

把 13 家网盘装进同一个窗口 —— 浏览、传输、分享、同步、在线播放，一个应用搞定。

[![Release](https://img.shields.io/github/v/release/lllll081926i/mnemo-go?style=flat-square&color=7c6cf0)](https://github.com/lllll081926i/mnemo-go/releases/latest)
[![Build](https://img.shields.io/github/actions/workflow/status/lllll081926i/mnemo-go/release.yml?style=flat-square&label=release)](https://github.com/lllll081926i/mnemo-go/actions/workflows/release.yml)
[![Go](https://img.shields.io/badge/Go-1.25+-00ADD8?style=flat-square&logo=go&logoColor=white)](https://go.dev)
[![Wails](https://img.shields.io/badge/Wails-v2-e0342f?style=flat-square)](https://wails.io)
[![Platform](https://img.shields.io/badge/Windows%20%7C%20Linux-x64%2Farm64%20%7C%20macOS%20arm64-lightgrey?style=flat-square)](https://github.com/lllll081926i/mnemo-go/releases/latest)
[![License](https://img.shields.io/badge/License-GPL--3.0-blue?style=flat-square)](LICENSE)

</div>

---

## ✨ 功能特性

| | |
|---|---|
| 📁 **统一文件管理** | 目录树 / 列表 / 搜索 / 排序 / 批量操作 / 重命名 / 回收站 / 收藏 / 标签 |
| ⬇️ **原生分段下载** | 纯 Go Range 分段引擎，断点续传、多连接、限速，无需 aria2 |
| ⬆️ **队列上传** | 批量上传、冲突策略询问、哈希进度显示 |
| 🎬 **沉浸式播放器** | Netflix 风控制层、0–200% 音量增益、滚轮调亮度、截图、画中画、断点续播 |
| 💬 **全格式字幕** | SRT / VTT 文本轨、自研 PGS/SUP 图形字幕渲染、jassub(libass) 特效字幕 |
| 🎯 **画质切换** | 按网盘能力提供多档清晰度，HLS.js / dash.js 按需加载 |
| 🔀 **跨盘迁移** | 秒传 / 流式 / 临时文件回退，预检、逐项详情、恢复与目标校验 |
| 🔄 **双向同步** | push / pull / two-way，执行前预览、冲突处理、定时执行与删除保护 |
| 🔗 **分享管理** | 创建分享、历史记录、分享文案识别与兼容账号导入 |
| 🪟 **双栏与搜索** | 双栏独立浏览与复制、已浏览目录缓存搜索、按需跨账号在线搜索 |
| 💾 **偏好备份** | 外观、账号名称与图标、排序、收藏导出恢复，不包含登录凭据 |
| 🖥️ **托盘常驻** | 关闭最小化到托盘、单实例唤起、退出前下载任务确认 |
| 🎨 **界面与账号** | 浅色 / 深灰 / OLED 纯黑背景、账号状态、自定义图标和名称、拖拽排序 |
| 🔐 **登录安全** | OAuth PKCE / 账密 / Cookie / 短信验证码，日志自动脱敏 |

## 💾 支持的网盘（13 家）

| | | |
|---|---|---|
| 🟣 PikPak | 🔵 OneDrive | 📦 Dropbox |
| 🔢 123 云盘 | 🐱 蓝奏云 | ☁️ 蓝奏优享 |
| 📱 移动云盘 | 📠 天翼云盘 | 🚀 阿里云盘开放版 |
| 🌐 光压云 | 🌍 WebDAV | 🪣 S3 对象存储 |
| ⚡ 一刻相册 | | |

> WebDAV 预置坚果云、InfiniCLOUD、Nextcloud、ownCloud、Seafile、OpenList/AList、群晖、Koofr、Yandex Disk、pCloud 等模板，也支持自定义地址。模板地址仍需按服务商实际配置确认；启用双重验证的服务通常需要应用密码。S3 支持 AWS 及兼容对象存储。

## 📥 下载安装

前往 [**Releases**](https://github.com/lllll081926i/mnemo-go/releases/latest) 下载对应平台安装包：

| 平台 | 架构 | 安装包 |
|---|---|---|
| 🪟 Windows | x64 / arm64 | `Mnemo-windows-*-Setup.exe` |
| 🐧 Linux | x64 / arm64 | `.deb`、`.rpm`、`.pkg.tar.zst`（Arch）、`.AppImage` 或 `.tar.gz` |
| 🍎 macOS | Apple Silicon | `.dmg` 或 `.tar.gz` |

所有产物附带 `SHA256SUMS.txt` 校验文件。Windows 需要 WebView2 Runtime（Win10/11 一般已内置）；Linux 的 `.deb`、`.rpm`、Arch 包会声明 GTK3 / WebKitGTK 4.1 依赖，`.AppImage` 与 `.tar.gz` 仍需要系统提供这两项运行库。

应用启动时会自动检查更新，Windows 支持一键下载安装。

## 🛠️ 从源码构建

**环境**：Go 1.25+ · Node.js 20+ · Wails CLI v2.14 · 平台原生工具链

```bash
go mod download
(cd frontend && npm ci)

wails dev      # 热重载开发
wails build    # 构建到 build/bin/
go test ./...  # 单元测试
```

## 🏗️ 技术架构

```text
┌─────────────────────────────────────────┐
│  Vue 3 + Vite 前端（WebView 渲染）        │
├─────────────────────────────────────────┤
│  Wails v2 绑定层（internal/app）          │
├──────────┬──────────┬─────────┬─────────┤
│ 网盘插件层│ 传输引擎  │ 同步引擎 │ 播放代理 │
│ 13 家驱动 │ 分段下载  │ 双向同步 │ HLS/DASH│
├──────────┴──────────┴─────────┴─────────┤
│  原子 JSON 持久化（internal/store）       │
└─────────────────────────────────────────┘
```

- **插件化网盘驱动**：每个网盘一个包，实现 `drive.Driver` 契约后 `init()` 注册，UI 与传输只走统一门面，无 provider 特判
- **零外部依赖**：纯 Go 无 cgo（仅 macOS 托盘需要），下载引擎、字幕解析、播放代理全部自研
- **无数据库**：账号 / 设置 / 任务 / 收藏全部原子 JSON 文件存储

## 📚 文档

- [架构说明](docs/ARCHITECTURE.md) — 模块边界与数据流
- [网盘插件指南](docs/PROVIDER_GUIDE.md) — 如何接入新网盘
- [界面设计规范](docs/DESIGN.md) — 设计 token 与组件约定
- [发布记录](docs/releases/) — 各版本更新说明

## 📄 许可证

[GPL-3.0](LICENSE) © Mnemo
