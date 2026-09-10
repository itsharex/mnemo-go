# 阿里云盘（aliopen）功能详情

> 调研范围：`internal/drive/providers/aliopen/aliopen.go`（单文件，约 2000 行）
> 当前证据：本仓库 `internal/drive/providers/aliopen`、统一能力注册表和本地自动化测试；本次未读取父目录旧项目。
> 状态：✅ 统一驱动已注册；可靠性以自动验证范围和已知限制为准，见 [Provider 状态总表](../PROVIDER_STATUS.md)。

---

## 能力声明（`init` 注册）

```
favorite, search, createShare, shareExpiration, sharePassword, shareHistory, importShare: true
copy, recycleBin, permanentDelete: true
trashView, trashRestore: false
SetHashes(["sha1"], ["sha1"])
SetConflictPolicies(["refuse", "rename", "skip", "overwrite"])
```

---

## 1. 登录（aliopen.go）

| 子功能 | 状态 | Go 证据 | 差距 |
|--------|:----:|---------|------|
| refresh_token 粘贴 | ✅ | `authRefreshToken` | 无 |
| client_id/client_secret | ✅ | `LoginConfig`，client_id 可选走 AList 公共换票 | 无 |
| token 刷新 | ✅ | `client.refresh`/`refreshToken` | 同步解析 `user_id`、`user_name`、`nick_name`、头像和默认盘 ID；已覆盖旋转 refresh_token、429 退避、401/业务码自动刷新并重试一次 |
| ensureDrive | ✅ | `ensureDrive`/`getDriveInfo` | 无 |
| **限速/重试** | ✅ | `aliOpenRateLimiter` + `apiPostWithRetry` | concurrency:2、最小间隔 220ms、429 退避、401/AccessTokenInvalid 重试一次 |

---

## 2. 文件列表（双盘 b:/r:）

收藏使用 Open 官方 `POST /adrive/v1.0/openFile/starredList`，按空间分别读取全部 `next_marker` 分页，备份盘与资源库仍使用 `b:`/`r:` 文件 ID。添加与取消调用 `POST /adrive/v1.0/openFile/update`，只传 `drive_id/file_id/starred`，不改变文件名。`scopedDriveID` 优先使用明确的 `backup_drive_id`，避免默认盘为资源库时错误访问备份盘。依据：[官方列表文档](https://www.yuque.com/aliyundrive/zpfszx/zqkqp6)、[官方更新文档](https://www.yuque.com/aliyundrive/zpfszx/dp9gn443hh8oksgd)。已覆盖双空间、分页、失败快照保护和读写请求模拟测试，未进行真实账号写入验证。

| 子功能 | 状态 | Go 证据 | 差距 |
|--------|:----:|---------|------|
| 双盘作用域 | ✅ | `parseRef`/`wrapRef`，b:/r: 前缀 | 无 |
| 根目录虚拟盘入口 | ✅ | `Driver.List` 备份盘+资源盘 | 无 |
| ListPage | ✅ | `ListPage` 调用 `/adrive/v1.0/openFile/list` limit=200 | 无重复游标死循环 |

---

## 3. 下载（aliopen.go）

| 子功能 | 状态 | Go 证据 | 差距 |
|--------|:----:|---------|------|
| GetDownloadURL | ✅ | `DownloadInfo` 调用 `getDownloadUrl` | 无 |
| DownloadMode | redirect | `GetDownloadURL` | 无 |
| **`.livp`/Live Photo 流回退** | ✅ | `DownloadInfo` 同时兼容 `streamsUrl`/`streams_url`，优先 `mov`、其次 `jpeg` | API 未提供流地址时回退普通 `url` |
| **expire_sec** | ✅ | `DownloadInfo` 传入调用方时长，`0` 默认 14400 秒 | 与旧版 4 小时默认值一致 |
| **过期时间回填** | ✅ | 解析 API `expiration`，缺失时从签名 URL 推导 | 统一返回 Unix 毫秒 |

---

## 4. 视频预览（aliopen.go）

✅ `GetVideoPreview` 复用 `DownloadInfo` 返回的原画或 Live Photo `mov` 流，交给统一播放会话代理和网页播放器。
> 阿里 Open 的真正转码清晰度接口 `/adrive/v1.0/openFile/getVideoPreviewPlayInfo` 仍未接入；当前产品约束是浏览器可解码容器在线播放，不支持的容器提示下载。

---

## 5. 上传（aliopen.go）

| 子功能 | 状态 | Go 证据 | 差距 |
|--------|:----:|---------|------|
| SHA1 秒传 | ✅ | `UploadOneFile`/`RapidUpload` 严格以 `rapid_upload` 或 `exist` 命中 | 未命中时返回的 pending `file_id` 不视为已完成 |
| CreateUploadFile | ✅ | `/adrive/v1.0/openFile/create` 传 `content_hash`、`pre_hash`、本地时间和冲突模式 | 无 |
| 分片上传 | ✅ | `aliOpenPartSize` 动态选择 20 MiB-5 GiB，并使用 `SectionReader` 流式 PUT | 无 |
| **CompleteUpload** | ✅ | `CompleteUpload` 传真实 `upload_id` | 无 |
| **断点续传** | ✅ | 先加载远端 `file_id/upload_id` 和已完成分片，再创建新任务；预签名地址按需重取 | 无 |
| **pre_hash 探测** | ✅ | `aliOpenPreHash` 读取前 1 KiB SHA1 | 无 |
| **proof_code** | ✅ | pre-hash 命中或创建响应不完整时按 access token 定位并读取 proof 区间 | 无 |
| **分片地址过期刷新** | ✅ | 每 50min 刷新当前分片，`401/403` 立即重取并重试 | 无 |
| **缺失分片地址补齐** | ✅ | `UploadOneFile` 在 create 返回空地址时按 100 片一批调用 `getUploadUrl` | 无 |
| **同名冲突** | ✅ | `ConflictPolicy` 映射 `fail`/`auto_rename`/`ignore`；`skip` 上传前列目录确认 | `skip` 与远端并发创建之间仍由 `fail` 防止覆盖 |

---

## 6. 秒传（aliopen.go）

| 子功能 | 状态 | Go 证据 | 差距 |
|--------|:----:|---------|------|
| RapidUploadByHash | ✅ | 校验 sha1；`RapidUpload` 严格检查 `rapid_upload`/`exist` | 未命中时继续普通分片上传 |
| ResolveTransferHash | ✅ | `Detail` 读取 `content_hash` | 🟡 无 SHA1 格式校验 |

---

## 7. 文件操作（aliopen.go）

| 操作 | 状态 | Go 证据 | 差距 |
|------|:----:|---------|------|
| Mkdir | ✅ | `aliopen.go:326-341` create type=folder refuse | 无 |
| Rename | ✅ | `aliopen.go:343-350` update | 无 |
| Move | ✅ | `aliopen.go:367-374` move | 混合备份盘/资源盘选择明确报错 |
| Copy | ✅ | `aliopen.go:376-384` copy 含 to_drive_id | 混合备份盘/资源盘选择明确报错 |

---

## 8. 回收站（aliopen.go）

| 操作 | 状态 | Go 证据 | 差距 |
|------|:----:|---------|------|
| Trash | ✅ | `aliopen.go:352-361` recyclebin/trash | 无 |
| Delete | ✅ | `aliopen.go:363-372` delete | 无 |
| Restore | ❌ | `aliopen.go:774-775` NotSupported | 设计（旧版也无） |

---

## 9. 分享（aliopen.go）

| 子功能 | 状态 | Go 证据 | 差距 |
|--------|:----:|---------|------|
| CreateShare | ✅ | `aliopen.go:386-416` createShareLink | 无 |
| **分享导入 importShare** | ✅ | `aliopen.go:952-1027` ImportShareSession（getShareToken → listByShare → SaveShare `aliopen.go:1028-1055`） | 会话校验、分页游标保护、部分失败返回错误 |

---

## 10. 搜索（aliopen.go）

✅ `aliopen.go:303-316` `/adrive/v1.0/openFile/search` 双盘并行，资源盘失败静默降级。对齐。

---

## 11. RefreshAccount（aliopen.go）

✅ 刷新后保留账号资料，账号稳定 ID 使用 `user_id`，界面名称优先昵称；`GetSpaceInfo` 兼容数值和字符串容量字段并回填已用、总量、剩余。容量接口失败时保留上次成功值。

---

## P0 差距清单

1. ✅ **CompleteUpload 传真实 `upload_id`**：已修复
2. ✅ **分享导入已实现**：`ImportShareSession` + `SaveShare`
3. ✅ **上传断点续传已实现**：在创建新任务前加载并复用远端会话
4. ✅ **上传秒传探测已对齐旧版**：`pre_hash`、`proof_code`、`content_hash` 和本地时间字段
5. ✅ **API 统一 401/AccessTokenInvalid 自动刷新、并发限速与 429 退避**
6. ✅ **动态分片与流式上传**：20 MiB-5 GiB，避免大文件一次性分配整片内存
7. ✅ **Live Photo 下载/预览流回退**：兼容 `streamsUrl` 和 `streams_url`
8. ✅ 双盘批量操作与分享混选会明确拒绝，避免把文件路由到错误作用域
