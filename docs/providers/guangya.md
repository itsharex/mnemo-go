# 光鸭云盘（guangya）功能详情

> 调研范围：`internal/drive/providers/guangya/`（guangya.go ~693 行 + upload.go ~312 行）
> 当前证据：本仓库 `internal/drive/providers/guangya`、统一能力注册表和本地自动化测试；本次未读取父目录旧项目。
> 状态：✅ 统一驱动已注册；可靠性以自动验证范围和已知限制为准，见 [Provider 状态总表](../PROVIDER_STATUS.md)。

---

## 能力声明（guangya.go:36-43）

```
copy, permanentDelete, createShare, shareExpiration, sharePassword, combinedShare, shareHistory: true
SetHashes(["md5"], nil)
ShareExpirationOptions: [0, 1, 7, 30]
```

---

## 1. 登录（guangya.go）

| 子功能 | 状态 | Go 证据 | 差距 |
|--------|:----:|---------|------|
| 短信验证码 | ✅ | `guangya.go` 的 SendSms → verify → signin，含静默 captcha 初始化与失效重取 | 无 |
| refresh_token | ✅ | `guangya.go` 的 loginByRefreshToken | 无 |
| SendGuangyaSms | ✅ | Driver.SendSms 返回 verification/device/captcha token，前端会带回登录 | 无 |
| 请求级刷新 | ✅ | `guangya.go` client.post 遇 401/403 自动 refresh_token、重试一次并落库 | 无 |

---

## 2. 文件列表（guangya.go）

✅ `guangya.go:155-188` client.List POST /userres/v1/file/get_file_list page+pageSize=100 最多 200 页。对齐。

---

## 3. 下载（guangya.go）

✅ `guangya.go:267-288` DownloadInfo → `:398-415` GetDownloadURL proxy；使用旧版稳定的 `/nd.bizuserres.s/v1/get_res_download_url`，优先解析 `data.signedURL`，兼容 `downloadUrl`。

---

## 4. 视频预览（guangya.go）

✅ `guangya.go:422-433` GetVideoPreview 复用 GetDownloadURL origin，视频统一交给网页播放器会话代理。

---

## 5. 上传（upload.go）

| 子功能 | 状态 | Go 证据 | 差距 |
|--------|:----:|---------|------|
| OSS multipart | ✅ | `upload.go:144-196` get_res_center_token → S3 multipart queueSize=2 → waitUploadTask | 无 |
| 秒传 | ✅ | code=156 命中 | 无 |
| directPut 回退 | ✅ | `upload.go:186-189,226-252`，直传后校验 waitUploadTask 错误 | 无 |
| 本地文件尺寸 | ✅ | `upload.go:141-150` 先 `Stat` 再申请上传凭证 | 避免 `fileSize: 0` 破坏秒传/上传任务 |

> 500ms 限速（`guangya.go:145`）与旧版一致。

---

## 6. 文件操作（guangya.go）

| 操作 | 状态 | Go 证据 |
|------|:----:|---------|
| Mkdir | ✅ | `guangya.go:214-225` /userres/v1/file/create_dir + `:419-425` |
| Rename | ✅ | `guangya.go:227-229` /userres/v1/file/rename + `:427-434` |
| Move | ✅ | `guangya.go:231-233` /userres/v1/file/move_file + `:436-449` |
| Copy | ✅ | `guangya.go:235-237` /userres/v1/file/copy_file + `:451-464` |

---

## 7. 回收站

光鸭删除任务将文件移入回收站；`guangya.go` 使用 `get_file_list` 的 `dirType=4` 分页列举。`trash.go` 通过网页当前使用的 `POST /userres/v1/file/recycle_file` 还原，发送 `{fileIds}` 并查询 `/userres/v1/get_task_status`；仅任务状态为成功时返回成功。来源为官网网页脚本 `https://www.guangyapan.com/static/js/index.2795c2ad.js`，已完成模拟请求测试，尚待真实账号验证。普通 `delete_file` 仅能确定移入回收站，彻底删除能力仍关闭。

---

## 8. 分享（share.go）

✅ `POST /nd.bizuserres.s/v1/share_file`；支持多文件/文件夹、永久或 1/7/30 天、可选自定义提取码。响应必须返回分享链接，业务失败会保留服务端错误。

---

## 9. RefreshAccount（guangya.go）

✅ 登录和手动容量刷新调用 `/assets/v1/get_assets`，读取 `data.usedSpaceSize` / `data.totalSpaceSize`；兼容数字和字符串字节数，并携带 `Did` / `Dt` 设备头。业务请求收到 401/403 时仍会刷新会话再重试。

---

## 10. ProvideHashes（guangya.go）

✅ `guangya.go:42-43, 243-279` SetHashes(["md5"], nil)，详情兼容旧版 `data.fileInfo` 与列表形态并读取 MD5。`ResolveTransferHash` 支持 md5（`:500-513`）。

---

## 差距清单

无明显差距。登录、容量、请求级续期和上传链路均已接入。
