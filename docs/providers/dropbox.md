# Dropbox 功能详情

> 调研范围：`internal/drive/providers/dropbox/`（onedrive.go 驱动, auth.go, dropbox.go 客户端, util.go）
> 当前证据：本仓库 `internal/drive/providers/dropbox`、统一能力注册表和本地自动化测试；本次未读取父目录旧项目。
> 状态：✅ 统一驱动已注册；可靠性以自动验证范围和已知限制为准，见 [Provider 状态总表](../PROVIDER_STATUS.md)。

---

## 能力声明（dropbox.go:32-39）

```
search, createShare, shareExpiration, sharePassword, shareHistory: true
SetHashes(["dropbox"], nil)
```
> ProvideHashes 已声明

---

## 1. 登录（auth.go）

| 子功能 | 状态 | Go 证据 | 差距 |
|--------|:----:|---------|------|
| OAuth PKCE S256 | ✅ | `auth.go` 无 secret 时发送 verifier/challenge | 内置 rclone confidential client 按旧版约定不发送 PKCE |
| 本地回调 | ✅ | `auth.go` 127.0.0.1:0 + state/来源校验 + 10min 超时 | Go 版内置 |
| Token 交换 | ✅ | `auth.go` POST dbTokenURL | 有 secret 发送 `client_secret`，无 secret 才发送 `code_verifier` |
| token_access_type=offline | ✅ | `auth.go:45` | 无 |
| app_key / secret | ✅ | 请求配置、release secrets 或内置 rclone 凭据 | 登录与刷新复用同一组凭据 |
| **Token 刷新** | ✅ | `dropbox.go:637-670` RefreshAccount 调 refreshDropboxToken | 无 |
| **账号信息/配额** | ✅ | `dropbox.go:650-670` 获取 get_current_account / get_space_usage | 无 |

默认使用与旧版及 `Example/rclone` 一致的 Dropbox app key/secret。登录后账号 ID 来自 `get_current_account.account_id`，并转换为 `dropbox_<account-id>` 与 `dropbox:<account-id>`，多个账号互不覆盖。刷新响应省略 `refresh_token` 或 `expires_in` 时分别保留旧 refresh token 和旧过期时间。

---

## 2. 文件列表（dropbox.go）

| 子功能 | 状态 | Go 证据 | 差距 |
|--------|:----:|---------|------|
| List / ListPaged | ✅ | `dropbox.go:108-146` /files/list_folder + /continue | 无 |
| cursor 防环 | ✅ | `dropbox.go:138-142` seen | 无 |
| deleted 过滤 | ✅ | `dropbox.go:120-127` filterDeleted | 无 |
| limit=2000 | ✅ | `ListPage` 请求参数 | 按 has_more/cursor 继续分页 |

2026-09-10：确认并修复前端根目录标记 `root` 未转换的问题。同一真实账号下，`path:"root"` 返回 HTTP 500，`path:""` 返回 HTTP 200；原测试仅使用 `dropbox_root`，未覆盖前端入口。现在列表、分页、上传及移动/复制目标统一转换 `root`、`dropbox_root`、`/` 和空路径，保留 `/root` 等真实目录。修复后的正式驱动使用真实账号通过 `root` 入口读取根目录及分页列表；测试账号根目录没有子目录，本次未进行真实子目录遍历或写入操作。

---

## 3. 下载（onedrive.go）

| 子功能 | 状态 | Go 证据 | 差距 |
|--------|:----:|---------|------|
| GetDownloadURL | ✅ | `onedrive.go:103-117` TemporaryLink /files/get_temporary_link | 无 |
| 过期时间 | ✅ | 4h | Go 版更完善 |
| Authorization | ✅ | `/files/get_temporary_link` 返回的签名 URL 不携带 Bearer | 防止把访问令牌转发到预签名地址 |

---

## 4. 视频预览（onedrive.go）

✅ `onedrive.go` 复用 GetDownloadURL origin，并统一经过本地 Range 播放会话代理交给网页播放器；Dropbox/OneDrive 预签名地址不携带 Bearer。

---

## 5. 上传（dropbox.go）

| 子功能 | 状态 | Go 证据 | 差距 |
|--------|:----:|---------|------|
| 小文件 ≤150MB | ✅ | UploadSmall `/files/upload`，校验返回 metadata 的 `id` 并写入上传任务 | 🟡 旧版阈值 8MB（Go 版更接近 API 上限） |
| Session upload | ✅ | `dropbox.go:375-410` start + append_v2 + finish | 无 |
| chunk 8MB | ✅ | `dropbox.go:17` | 无 |
| 完成结果校验 | ✅ | session finish 同样必须返回 metadata `id` | 空响应不会被标记为成功 |
| 断点隔离 | ✅ | 会话键包含用户/账号、路径、大小、内容 SHA1 或 mtime、冲突策略 | 同名同大小替换文件不会串用旧会话 |
| **429 重试** | ✅ | `dropbox.go:530-560` 按 Retry-After/退避重试 | 无 |
| **暂停支持** | ✅ | `dropbox.go:429-432` 大文件分片前检查 IsStop | 小文件由任务 context 取消 |
| 冲突策略 | ✅ | `dropbox.go:258-270` refuse/rename/skip/overwrite | 无 |
| **父目录解析** | ✅ | Driver 统一使用 Dropbox path/id | 无 |

---

## 6. 文件操作（dropbox.go / onedrive.go）

| 操作 | 状态 | Go 证据 | 差距 |
|------|:----:|---------|------|
| Mkdir | ✅ | `dropbox.go:187-206` /files/create_folder_v2 autorename:true | 🟡 旧版 autorename:false |
| Rename | ✅ | `dropbox.go:209-219` move_v2 | 🟡 多一次 Detail 调用 |
| Move | ✅ | `dropbox.go` move_v2 + `allow_shared_folder` | 无 |
| Copy | ✅ | `dropbox.go` copy_v2 + `allow_shared_folder` | 无 |
| 路径解析 | ⚠️ | `dropbox.go:438-450` resolveCommandPath 简单处理 | 🟡 旧版从 description 提取 dropbox_path: |

---

## 7. 回收站

| 操作 | 状态 | 说明 |
|------|:----:|------|
| Trash | ✅ | `dropbox.go:216` /files/delete_v2 |
| Restore | ❌ | NotSupported |
| TrashView | ❌ | 未设 |

---

## 8. 分享（dropbox.go / onedrive.go）

| 子功能 | 状态 | Go 证据 | 差距 |
|--------|:----:|---------|------|
| CreateShare | ✅ | `dropbox.go:255-289` /sharing/create_shared_link_with_settings | 无 |
| 过期/密码 | ✅ | settings.expires / link_password | 无 |
| 已存在链接处理 | ✅ | list_shared_links 返回对象按 `links` 解码，并跟随 cursor 分页后 modify | 无 |
| **visibility 逻辑** | ✅ | `dropbox.go:306-312` 有密码时 `requested_visibility`="password" | 无 |
| **list 分页** | ✅ | 跟随 has_more + cursor 循环 | 无 |
| **settings_error 处理** | ❌ | 不区分 | 🟡 旧版友好提示 |

---

## 9. 搜索（dropbox.go）

| 子功能 | 状态 | Go 证据 | 差距 |
|--------|:----:|---------|------|
| Search | ✅ | `dropbox.go:149-171` /files/search_v2 max_results:1000 | 无 |
| **搜索分页** | ✅ | `dropbox.go:162-194` 跟随 has_more + cursor 调 search/continue_v2 | 无 |
| **过滤器** | ❌ | 无 | 🟡 旧版 filterDropboxSearchResults |
| file_status | ❌ | 无 | 🟡 旧版 active |

---

## 10. RefreshAccount

✅ `dropbox.go:637-670` 调 refreshDropboxToken 刷新 token，再获取 get_current_account / get_space_usage。对齐。

---

## 11. ProvideHashes

✅ `dropbox.go:43` SetHashes(["dropbox"], nil)。`dropbox.go:489-491` mapItem 提取 content_hash → ContentHash。

---

## 12. 缺失功能

| 功能 | 状态 | 说明 |
|------|:----:|------|
| 版本历史 revisions | ❌ | 旧版有 list_revisions / restore |
| 缩略图 thumbnail | ❌ | 旧版有 get_thumbnail_v2，新版 mapItem Thumbnail 始终空 |

---

## P0 差距清单

1. ✅ **Token 刷新已实现**（`dropbox.go:637-670` RefreshAccount）
2. ✅ **账号信息/配额已实现**（`dropbox.go:650-670`）
3. ✅ **分享 visibility 逻辑已修复**（`dropbox.go:306-312` 有密码时为 password）
4. ✅ **ProvideHashes 已声明**（`dropbox.go:43` SetHashes）
5. ✅ **搜索分页已实现**（`dropbox.go:162-194` 跟随 cursor）
6. ✅ 上传具备 429 重试、Retry-After 和大文件暂停
7. ✅ 上传冲突策略已按统一 ConflictPolicy 处理
8. 🟡 搜索无过滤器、file_status
9. 🟡 分享 list settings_error 无友好提示
10. ✅ move/copy 已支持 shared folder 参数
11. 🟡 版本历史、缩略图仍未迁移（按当前范围保留为已知边界）
