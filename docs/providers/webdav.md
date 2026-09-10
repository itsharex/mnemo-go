# WebDAV 功能详情

> 调研范围：`internal/drive/providers/webdav/webdav.go` + `internal/provider/webdav/client.go`
> 当前证据：本仓库 `internal/drive/providers/webdav`、`internal/provider/webdav` 和本地 HTTP 自动化测试；本次未读取父目录旧项目。
> 状态：✅ 统一驱动已注册；可靠性以自动验证范围和已知限制为准，见 [Provider 状态总表](../PROVIDER_STATUS.md)。

---

## 能力声明（webdav.go:20-35）

```
mountedStorage, permanentDelete: true
recycleBin, trashView: false
UploadMode: direct
```

---

## 1. 连接配置（webdav.go / account.go）

新增连接仅执行 `PROPFIND Depth:0`，只请求 `resourcetype` 验证挂载目录；不扫描子目录、不查容量、不试写。总校验时间限制为 60 秒（包含认证协商），与日常请求超时一致。网络客户端优先使用应用显式代理，否则复用 Windows 手动系统代理适配；PAC/WPAD 不在该适配范围。

| 子功能 | 状态 | Go 证据 | 差距 |
|--------|:----:|---------|------|
| URL + 账密 / Bearer Token | ✅ | `webdav.go:43-49` clientOf 从 c.Token.Conn 取 Endpoint、账号、密码/令牌 | Basic、Digest、Bearer；客户端证书、NTLM 和网页登录流程不支持 |
| **认证协商** | ✅ | `client.go` 默认先保持 Basic 兼容；仅在服务端明确返回 Digest 挑战时重试一次，并缓存 nonce/计数 | Digest 支持 MD5、SHA-256、SHA-512-256（含 `-sess`）和 `qop=auth`；仅 `auth-int` 的服务端不支持 |
| 连接持久化 | ✅ | account.go Token.Conn *ConnConfig 持久化 | 无（服务端存储） |
| **rootPath 前缀** | ✅ | `ConnConfig.RootPath` + client 的 endpointPath/逻辑路径转换；旧 `BasePath` 自动兼容 | 无 |

---

## 2. 文件列表（webdav.go / client.go）

✅ `webdav.go:65-78` client.List → PROPFIND Depth:1 XML 解析。Go 版自研 PROPFIND；列表和 `Stat(Depth:0)` 均读取 `DAV:getcontentlength` 作为单文件字节数，不额外发送 HEAD 请求。

---

## 3. 下载（webdav.go）

✅ `webdav.go:98-114` GetDownloadURL Stat + client.DownloadURL（Endpoint+href 直链）。Basic/Bearer 使用静态请求头；Digest 为每一个 Range/播放/迁移请求计算新的 nonce-count，下载保持单连接以兼容严格服务端。

> ✅ 请求 URL 按路径段编码，空格、Unicode、`#`、`?` 等文件名不会被误解析为 URL 控制符。

---

## 4. 上传（webdav.go / client.go）

| 子功能 | 状态 | Go 证据 | 差距 |
|--------|:----:|---------|------|
| direct PUT | ✅ | `webdav.go:196-208` client.Put HTTP PUT Content-Length | 无 |
| **冲突策略** | ✅ | `webdav.go:256` ResolveConflictPolicy refuse/rename/overwrite | 无 |
| **进度回调** | ✅ | `webdav.go:275` NewProgressReader 实时 DownSize/DownProcess | 无 |
| **递归目录上传** | ✅ | `internal/transfer/upload.go` 目录遍历 + `ensureRemoteParent` | 队列上传会先创建远端目录 |

---

## 5. 文件操作（webdav.go / client.go）

| 操作 | 状态 | Go 证据 | 差距 |
|------|:----:|---------|------|
| Mkdir | ✅ | `webdav.go:116-121` client.Mkcol HTTP MKCOL | 🟡 无 recursive（WebDAV 协议限制） |
| Rename | ✅ | `webdav.go:123-133` Stat + client.Move | 无 |
| Move | ✅ | `webdav.go` 遍历 client.Move | 自身/子目录目标在请求前拒绝 |
| Copy | ✅ | `webdav.go` 遍历 client.Copy | 自身/子目录目标在请求前拒绝 |
| Delete | ✅ | 直接 Delete（永久） | 无 |

---

## 6. 回收站 / 分享 / 搜索

❌ 均无（设计）。永久删除。

---

## 差距清单

1. ✅ **上传冲突策略已实现**（`webdav.go:256` ResolveConflictPolicy refuse/rename/overwrite）
2. ✅ **进度回调已实现**（`webdav.go:275` NewProgressReader + DownSize/DownProcess）
3. ✅ 上传支持递归目录（队列遍历并逐级创建远端目录）
4. ✅ Move/Copy 已增加自身/子目录防护
5. ✅ URL 路径段按 RFC 规则编码
6. 🟡 当前没有独立的 provider 级递归列出 API；同步引擎通过统一目录接口递归遍历
7. 🟡 客户端证书、NTLM、Kerberos 和网页登录换 Cookie 等认证方式未实现；遇到这类 `WWW-Authenticate` 会保留脱敏诊断，而不会盲目重试。
