# Mnemo-Go 全量项目审查报告（2026-08-21）

> 初始审查为只读复核，基线为 `8546aa8`（feat: 彻底移除桌面传输悬浮球功能）+ 当时未提交的分享取消能力变更。
> 本文保留初始发现；后续修复状态以本文件的“后续修复验证”和各问题行的状态为准。上一轮基线见 [PROJECT_AUDIT_2026-08-20.md](PROJECT_AUDIT_2026-08-20.md)。

## 1. 审查结论

### 2026-09-10 补充审查修复

以下六项已在工作区修复，尚未提交：

| 项目 | 修复与回归 |
|---|---|
| 阿里断点上传 ID 互换 | 按解码器的 `uploadID, fileID` 顺序接收；回归检查恢复时实际完成请求中的两个 ID。 |
| 阿里同名同大小内容复用旧分片 | 会话键加入 SHA-1 与冲突策略，旧的不含内容身份的会话不再复用；覆盖内容替换、策略变化及正常恢复。 |
| 同步执行期间远端变化 | 逐项读取远端父目录复核文件身份、大小、时间、哈希和预期不存在；上传准备后再次检查，下载落盘前再检查来源；覆盖执行期间远端修改、新建及删除传播。未支持条件写入的 Provider 仍有最后检查与远端写请求之间的竞态窗口，不能宣称跨端原子同步。 |
| 下载 URL 解析无法取消 | 首次解析及过期重取均传递任务 Context；阻塞 Provider 回归验证暂停释放 worker。 |
| 分片上传绕过全局限速 | HTTP 发送路径统一消费共享额度，覆盖各盘分片和直传、S3 multipart；保留长度、SDK 签名输入与重试语义，避免直传进度重复计费；测试覆盖总速率、取消、动态设置、307 重放、123 实际 PUT 和 S3 SDK。 |
| 日常 CI 缺失 | 质量 job 增加所有分支 push 与 PR 触发；原生打包仅在手动或标签触发时运行。 |

验证采用失败回归、实现后针对性测试、全仓及竞态检查三个阶段；不访问真实网盘账号、不执行发布或安装更新。

- 上轮报告声称的关键修复经抽查**全部属实**（P0-01/02/03/04、F-24、F-27、F-28、F-39、P1-08 等）。
- 初始发现的 OneDrive 测试编译失败（N-01）已于 `0bb2725` 关闭，`go vet`、`go test` 与 CI quality 链路已恢复。
- DPAPI 密钥静默重置（N-02）、DPAPI 非原子写（N-07 的该部分）和分享记录跨网盘取消（N-08）已于 `f05a10b` 关闭并补回归。
- N-04（结构化错误分类）、N-06（全仓 gofmt/门禁）和 N-07（下载断点状态原子写）已于 `12b5cda` 关闭并完成回归；本轮审查新增的源码级问题均已关闭。
- N-03、N-05 为已记录的兼容/风控产品取舍；N-09 为已忽略的本地检查产物，不应作为后续代码待办。
- 迁移进度对照旧版 `../Mnemo` v0.1.18 核实后约为 **88%**（[MIGRATION_PROGRESS.md](MIGRATION_PROGRESS.md) 已同步修正过时声明）。

## 2. 初始验证基线

| 检查 | 结果 |
|---|---|
| `go build ./...` | ✅ 通过 |
| `go vet ./...` | ❌ `onedrive_test.go:166` 编译失败（N-01） |
| `go test ./...` | ❌ 仅 onedrive 包构建失败，其余 26 包全部通过 |
| `go test -race`（transfer/sync/store/vault/e2e） | ✅ 通过 |
| 前端 `npm test`（Vitest） | ✅ 16/16 通过 |
| 前端 `npm run build` | ✅ 通过（chunk >500kB 警告，非阻塞） |
| `gofmt -l` | ⚠️ 10 个文件未格式化 |
| `git diff --check` | 仅 CRLF 提示 |

### 后续修复验证

| 提交 | 已验证内容 | 结果 |
|---|---|---|
| `0bb2725` | 分享创建/取消、蓝奏下载、`go test ./...`、`go vet ./...`、`go build ./...`、Wails 生产打包 | 通过 |
| `f05a10b` | Vault fail-closed、分享记录 Provider 校验、`go test ./...`、`go vet ./...`、`go build ./...` | 通过 |
| `12b5cda` | OneDrive/阿里结构化错误、下载断点状态原子写、全仓 gofmt、CI 格式门禁、后端全量测试、前端 15 项测试与生产构建 | 通过 |
| 本次媒体预览修复 | 图片/音频沉浸式无实体标题栏、控件层级与对比、视频前后集禁用态、通用经典窗口三键、前端 16 项测试与生产构建 | 通过 |

## 3. 上轮修复抽查验证（全部属实）

| 项 | 验证方式 | 结果 |
|---|---|---|
| P0-01 同步路径逃逸 | 通读 `safeLocalPath`（`internal/sync/engine.go:186`）：NUL/绝对路径/卷名/`..`/符号链接/父非目录全拦截 | ✓ |
| P0-03 同名下载冲突 | `internal/transfer/manager.go:271` 原子分配 `name (N).ext`，最终文件与 `.part`/`.state.json` 均参与占用判断 | ✓ |
| P0-04 分段下载一致性 | 通读 `internal/transfer/dlengine/segment.go`：probe 与每段严格校验 Content-Range/Content-Length，ETag/Last-Modified + If-Range/If-Match | ✓ |
| F-24 状态文件脱敏 | state 只存 URL 的 SHA-256 指纹，`URL` 字段只读兼容且不再写入 | ✓ |
| F-27 外链协议 + CSP | `validateExternalBrowserURL` 仅放行 HTTPS 与本机回环；`frontend/index.html:6` 有完整 CSP | ✓ |
| F-28 上传 worker generation | `internal/transfer/upload.go` generation + 代际校验齐全 | ✓ |
| F-39 原子 JSON | store 统一 tmp + `renameWithRetry` + 0600 权限 | ✓ |
| OneDrive 分享权限往返 | `CreateLink` 存 Graph permission ID 为 ShareID，`DeletePermission` 按其删除，一致 | ✓ |

## 4. 新发现问题

严重度定义沿用上轮报告（P0 数据安全 / P1 明显风险 / P2 一致性与可维护性 / P3 工程卫生）。本轮无 P0。

### 4.1 P1 — 阻塞门禁

| 编号 | 问题 | 位置 | 说明 |
|---|---|---|---|
| N-01（已关闭） | onedrive 测试包编译失败 | `internal/drive/providers/onedrive/onedrive_test.go:166` | 已改为 `items[0].FileID`，随 `0bb2725` 提交；后续完整 `go test ./...`、`go vet ./...` 均通过。 |

### 4.2 P2 — 应尽快处理

| 编号 | 问题 | 位置 | 说明 |
|---|---|---|---|
| N-02（已关闭） | Vault 在 DPAPI 密钥损坏时静默重新生成密钥 | `internal/vault/vault.go` | `f05a10b` 改为 fail-closed：DPAPI 不可读且无有效旧密钥时直接报错，不生成替代密钥；存在有效旧密钥时继续使用但不覆盖损坏文件。已补两条回归。 |
| N-03（已记录，兼容取舍） | Dropbox 列目录不再包含挂载文件夹 | `internal/drive/providers/dropbox/dropbox.go:177` | `include_mounted_folders` 从 `true` 改为 `false`（规避部分账号服务端 500）。用户可见限制已写入 [KNOWN_ISSUES.md](KNOWN_ISSUES.md)，不作为后续无条件回归项。 |
| N-04（已关闭） | 错误分类依赖错误消息字符串匹配 | `onedrive.go` `isGraphAuthenticationFailure`；`aliopen.go` `aliOpenNotFound` | `12b5cda` 引入 `graphAPIError` / `aliOpenRequestError`，仅用结构化 HTTP 状态和服务端错误码决定刷新或端点回退；已补“格式化文本不得触发动作”的回归。 |
| N-05（已确认，风控取舍） | aliopen RefreshAccount 不再回写账号昵称 | `internal/drive/providers/aliopen/aliopen.go:2220` | `refreshAccountProfile` 调用被移除，以避免后台静默请求；按产品要求保持不自动获取名称，账号栏可显示已保存名称或 ID。 |

### 4.3 P3 — 工程卫生

| 编号 | 问题 | 说明 |
|---|---|---|
| N-06（已关闭） | gofmt 未格式化 10 个文件 | `12b5cda` 已全仓格式化，`gofmt -l .` 无输出；release quality job 新增 `Check Go formatting` 门禁。 |
| N-07（已关闭） | 两处非原子写 | `f05a10b` 原子写 DPAPI 密钥；`12b5cda` 让 dlengine `persistState` 使用临时文件、`Sync` 与 Windows 安全替换，回归覆盖初次写入、覆盖写入和临时文件清理。 |
| N-08（已关闭） | CancelShare 不校验 entry.Provider | `internal/app/drive.go` | `f05a10b` 在远端撤销前校验历史记录 Provider 与账号解析结果；旧版本没有 Provider 的记录继续兼容。 |
| N-09（已确认，无需改代码） | 仓库根目录两个 ~11MB 检查二进制 | `mnemo-darwin-check`/`mnemo-linux-check` 已在 `.gitignore`、未被 git 跟踪；保留为用户本地检查产物，不会进入提交或发布。 |

## 5. 分享取消能力复核

范围：`ShareCancellationDriver` 契约 + aliopen/dropbox/guangya/onedrive/pan123/pikpak 六盘实现 + store 删除接口 + 前端 ShareView 取消入口。

设计优点：

- 契约明确"仅删本地记录不算取消"；`App.CancelShare` 先云端撤销再清本地，本地清理失败时明确报"云端已取消但本地清理失败"。
- `DeleteShareHistory` 按账号隔离匹配，ShareID 优先、URL 兜底。
- 前端 capability 门控 + 危险确认弹窗 + 单条目进行中状态 + `share:history-changed` 事件刷新。
- 端点回退克制：阿里仅在明确 404 时回退消费级 API（避免重复建链）；PikPak 破坏性操作明确不做盲回退。

合入前置条件 N-01、N-04、N-06、N-07、N-08 均已关闭。N-03 已写入 [KNOWN_ISSUES.md](KNOWN_ISSUES.md)；N-05 按产品要求不再静默获取阿里账号名称，均保留为明确的产品取舍。

## 6. 迁移进度核实摘要

详细矩阵见 [MIGRATION_PROGRESS.md](MIGRATION_PROGRESS.md)（已同步修正）。要点：

- [MIGRATION_PROGRESS.md] 原 08-17 版本存在 5 处过时声明（前端测试框架、播放列表、日志设置页、DPAPI、store 加密），均已修正。
- 综合迁移进度由文档记载的 ~75% 修正为 **≈88%**。
- 剩余差距：瀑布流/相册模式、多音轨（网页播放器结构性缺失）、任务详情抽屉、设置页拆分、onedrive/dropbox 版本历史、dropbox 缩略图、Go filecache TTL/上限、真实云端账号发布前验证。

## 7. 建议处理顺序

1. 本报告中的源码级修复项已全部关闭；后续功能改动需继续保持对应单测和全仓格式门禁。
2. N-03、N-05 仅在真实服务端行为变化或产品方向调整时重新评估，不作为常规修复待办。
3. N-09 为本地文件整理建议，不影响构建或发布，也不纳入代码变更。

本报告为只读复核基线。每关闭一个编号应同时补对应自动化测试并更新本表状态。
