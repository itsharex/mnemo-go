# 新增网盘插件指南（PROVIDER_GUIDE）

Mnemo-Go 的网盘是插件：**新增一个盘 = 新增一个 Go 包 + 一行 blank import**，
不需要改任何核心代码。本指南说明插件契约与移植流程。

## 1. 契约（internal/drive）

### 接口 `drive.Driver`

```go
type Driver interface {
    ID() string
    Meta() drive.Meta
    Capabilities() drive.Capabilities
    RootID() string
    List(ctx, c drive.Context, dirID string, opts *drive.ListOptions) ([]model.File, error)
    // 其余为可选，BaseDriver 提供默认 ErrNotImplemented
}
```

Provider 实现时**内嵌 `drive.BaseDriver`** 并覆盖支持的方法：

```go
type Driver struct{ drive.BaseDriver }

func (d *Driver) List(...) ([]model.File, error) { ... }   // 必实现
func (d *Driver) Search(...) ...                          // 可选
func (d *Driver) UploadOneFile(...) ...                   // 可选
```

### 注册（init()）

```go
func init() {
    drive.Register(drive.Registration{
        ID:   "mydrive",
        Meta: drive.GetMeta("mydrive"),   // 需在 drive/meta.go 的 builtinMetas 登记
        Caps: drive.NewCapabilities("mydrive", map[string]bool{
            "search": true, "createShare": true, ...,
        }, func(c *drive.Capabilities) {
            c.SetHashes([]string{"md5"}, []string{"md5"}) // 秒传指纹
            c.SetUploadMode(drive.UploadModeQueue)
        }),
        Factory: func() drive.Driver { return &Driver{} },
        Auth:    authLogin,                    // 可选：登录流（Registration.Auth）
        Login:   drive.LoginConfig{Fields: []drive.LoginField{
            {Key: "username", Type: "text", Label: "账号", Required: true},
            {Key: "password", Type: "password", Label: "密码", Required: true},
        }},
    })
}
```

### 能力位
`drive.Capabilities` 与旧版 TS `DriveProviderCapabilities` 一一对应。
UI 菜单/操作按能力位裁剪；能力位声明了但 Driver 未实现的方法会返回
`ErrNotImplemented`，属实现缺陷（应由单测守护）。

### 收藏（云端优先，本地回退）

所有网盘都有本地收藏入口。只有同时实现原生收藏列表与修改功能时，才声明 `favorite: true` 并实现 `drive.RemoteFavorites`：

```go
type RemoteFavorites interface {
    SupportsRemoteFavorites(ctx context.Context, c Context) (bool, error)
    ListFavorites(ctx context.Context, c Context) ([]model.File, error)
}
```

另外覆盖 `Driver.Favorite`，返回远端确认成功的文件 ID。`ListFavorites` 必须读取完整分页，保留文件所属空间和父目录，异常时返回错误而不是部分列表。账号类型差异由插件内部处理（如 OneDrive 个人账号返回不支持）；权限不足、网络故障等不能作为“不支持”的依据。

UI 使用 `App.AddFavorite` / `App.RemoveFavorite` / `App.ListFavorites`；本地回退、云端快照、备份与账号隔离由 app/store 层负责。不要在 UI 再调用一次 `FavoriteFiles`，也不要在中央代码按 provider 写分支。逐盘现状见 [收藏核查表](PROVIDER_STATUS.md#收藏逐盘核查2026-09-10)。

### 装配
`internal/drive/providers/all.go` 用 blank import 引入新包即完成注册：

```go
import (
    _ "mnemo-go/internal/drive/providers/mydrive"
)
```

## 2. 模型（internal/model）

- `model.File`：统一列表/详情模型（字段与旧版一致）。
- `model.TokenInfo`：登录凭据；`tokenfrom/user_id` 命名空间隔离。
  挂载存储类（webdav/s3）用 `TokenInfo.Conn`。
- `model.UploadingUI`：上传任务视图，进度写 `ui.Upload`。

## 3. 会话与上下文

每次驱动调用都会拿到 `drive.Context`（含 userId/driveId/token）。
从 `ctx.Token` 读取凭据；需要刷新时实现 `RefreshAccount`。

## 4. 登录流（Auth）

三种形态：

| 类型 | Auth 实现 | 示例 |
|---|---|---|
| OAuth PKCE | 本地 127.0.0.1 随机端口回调 | onedrive / dropbox |
| 账密/Token | 直接用表单字段调登录 API | pikpak / aliopen / pan139 |
| 挂载表单 | 不实现 Auth，表单填连接参数 | webdav / s3 |

`drive.AuthRequest.Config` 携带登录表单值；返回 `*model.TokenInfo`。

## 5. 文件 id 约定

- 简单盘：`file_id` 即服务端 id（pikpak/aliopen/onedrive/dropbox）。
- 路径盘：`file_id` 为路径（webdav/s3，根为 `/`）。
- 双盘盘：id 带作用域前缀（aliopen 的 `b:`/`r:`，根 `backup_root`/`resource_root`）。

## 6. 上传

`UploadOneFile(ctx, c, ui)` 接收完整上传任务：
- 小文件：单请求 PUT/POST。
- 大文件：分片/会话（onedrive upload session、dropbox session、aliopen part、
  pikpak GCID+OSS、pan139 chunk）。
- 进度写 `ui.Upload.DownSize/DownProcess`。

## 7. 移植自旧版（参考实现）

1. 读 `../Mnemo/src/drive/providers/<id>.ts`（驱动适配）+ `../Mnemo/src/<id>/*.ts`（API）。
2. 读同包内已移植 provider（`pikpak/`、`aliopen/` 是最完整的参考）。
3. 逐方法移植，字段/端点/加密算法必须与旧版一致。
4. 完成后：
   - `go build ./...`
   - `internal/drive/providers/all.go` 加 blank import
   - 为纯逻辑写单测（签名算法、分页、映射等）

## 8. 检查清单

- [ ] `internal/drive/meta.go` 的 `builtinMetas` 已登记（label/icon/rootKey）
- [ ] 能力位与旧版 `driveProviderCapabilities` 一致
- [ ] 登录流可用（Auth 或挂载表单）
- [ ] list / getDownloadUrl / 文件操作可用
- [ ] 上传/秒传按能力位实现
- [ ] `go build ./...` 通过，核心逻辑有单测
