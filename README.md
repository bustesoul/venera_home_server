# Venera Home Server

[中文](./README.md) | [English](./README_EN.md)

`Venera Home Server` 是为 **[Venera](https://github.com/venera-app/venera)** 准备的本地漫画后端服务。它把本地磁盘、SMB、WebDAV 中的漫画统一暴露成轻量 HTTP API，并提供可直接导入 Venera 的 `venera_home.js`。

它当前已经覆盖两类核心场景：

- 日常阅读：扫描、索引、搜索、详情、章节阅读、收藏夹
- 本地元数据管理：本地 metadata store、外部 SQLite 补全、内置管理页、`.venera.json` sidecar 编辑

## 功能概览

### 存储与格式

- 书库后端：本地目录、SMB（当前仅 Windows）、WebDAV
- 图片目录：`jpg` / `jpeg` / `png` / `webp` / `gif` / `bmp` / `avif`
- 压缩包：`cbz` / `zip` / `cbr` / `rar` / `cb7` / `7z`
- 文档：`pdf`（当前仅 Windows 渲染）

### 阅读与 API

- 扫描、索引、首页、分类、搜索、详情、章节阅读
- 搜索支持标题 / 标签 / 作者，也支持路径相关检索
- 收藏夹与多文件夹收藏
- 带签名的封面 / 页面媒体 URL
- 归档与远程文件缓存
- PDF 首次访问按页渲染并缓存
- `venera_home.js` 详情页可显示本地路径与相对路径

### 元数据与管理页

- 读取 `ComicInfo.xml`
- 读取 `.venera.json` 手工覆盖元数据
- 扫描结果写入本地 `metadata.db`
- 自动发现 `data/externaldb/` 下的外部 SQLite 数据源
- 支持“填空式”补全：不会覆盖已有本地显式元数据
- 内置管理页（根路径 `/`），支持：
  - 手动批量补全
  - 单条补全、锁定、解锁、重置
  - 当前页多选批量操作
  - 浏览外部数据源内容
  - 清理 `missing` 记录（支持 dry run）
  - 编辑 / 删除 `.venera.json` sidecar（仅可写后端）
  - 手动触发 `Rescan`
  - Sidecar 保存 / 删除后自动触发重扫
- 提供 dry-run 工具 `exdb_dryrun`

## 快速开始

### 1. 准备配置

可以直接从 `server.example.toml` 开始，也可以先用一个最小配置：

```toml
[server]
listen = "0.0.0.0:34123"
token = "change-me"
data_dir = "./data"
cache_dir = "./cache"

[[libraries]]
id = "local-main"
name = "Local Manga"
kind = "local"
root = "D:/Comics"
scan_mode = "auto"
```

补充说明：

- `scan_mode = "auto"`：尝试把同一作品的多章节合并到一个漫画条目里
- `scan_mode = "flat"`：每个目录或压缩包都按独立漫画处理
- `server.example.toml` 里还有 `watch_local`、`rescan_interval_minutes`、`allow_remote_fetch` 等字段；当前主流程仍然以手动重扫 / 管理页触发为主

### 2. 如果使用 SMB / WebDAV，配置密码环境变量

```powershell
$env:SMB_PASS = "your-password"
$env:WEBDAV_PASS = "your-password"
```

### 3. 启动服务

```powershell
go run . -config ./server.example.toml
```

或使用已构建好的可执行文件：

```powershell
.\venera_home_server.exe -config .\server.example.toml
```

### 4. 在 Venera 中导入源脚本

导入 `venera_home.js`，然后填写：

- `Server URL`：例如 `http://127.0.0.1:34123`
- `Token`：与服务端配置一致
- `Default Library ID`：可留空
- `Default Sort`
- `Page Size`
- `Image Mode`

> 如果手机访问电脑上的服务，不要使用 `127.0.0.1`，要改成电脑的局域网 IP。

### 5. 打开管理页

服务启动后，浏览器访问根路径 `/` 即可打开内置管理页。

如果服务端启用了 `token`，在页面右上角填入 Bearer Token。

## 元数据工作流

### 本地元数据库

每次扫描都会把漫画条目写入本地元数据库，默认路径为：

- `data/metadata.db`

这里会保存：

- 书库定位信息
- 路径 / 文件夹信息
- 内容指纹
- 匹配 hint
- 已补全的标题、作者、标签、来源等字段

### 外部 SQLite 数据源

把外部 SQLite 文件直接放进：

- `data/externaldb/`

无需额外配置，管理页会自动发现它们。

### 管理页典型流程

- 先浏览外部数据源，确认内容正常
- 对 `state=empty` 执行一次批量补全
- 对误匹配条目使用锁定 / 重置
- 如有需要，直接编辑 sidecar 修正本地元数据
- 触发或等待自动 `Rescan`，让 Venera 立刻看到结果

`manual_locked` 字段的作用是：**阻止后续批量补全再次误伤同一条记录**。

## 元数据优先级

当前元数据合并顺序大致为：

1. `.venera.json`
2. `ComicInfo.xml`
3. 文件名 / 目录名推断
4. 本地 metadata store 中的补全字段（仅填空，不强制覆盖显式值）

示例 `.venera.json`：

```json
{
  "title": "Chapter 01",
  "series": "Dungeon Meshi",
  "subtitle": "Ryoko Kui",
  "description": "Hand-maintained metadata",
  "authors": ["Ryoko Kui"],
  "tags": ["Fantasy", "Adventure", "Food"],
  "language": "zh",
  "scan_mode": "flat"
}
```

附加规则：

- `hidden: true`：忽略当前目录或压缩包
- 目录 sidecar：目录内放 `.venera.json`
- 压缩包 sidecar：在压缩包旁放 `xxx.cbz.venera.json` / `xxx.zip.venera.json`

## 限制与现状

- `SMB` 当前仅 Windows 构建可用
- `PDF` 当前仅 Windows 可渲染，依赖系统 `Windows.Data.Pdf`
- 当前补全数据源主要是本地 SQLite；互联网元数据源还没有接入
- 当前没有完整的自动定时补全工作流，主流程仍是手动触发
- Sidecar 编辑依赖后端可写；只读后端只能查看，不能保存或删除

## 仓库结构

- `main.go`：启动入口
- `app/`：扫描、索引、元数据合并、补全任务
- `httpapi/`：HTTP API、媒体分发、管理页
- `metadata/`：本地 metadata store
- `backend/` / `archive/`：存储后端与归档读取
- `exdbdryrun/` + `cmd/exdb_dryrun/`：外部 SQLite 匹配 dry-run 工具
- `tests/`：测试与 `testkit/`
- `server.example.toml`：示例配置
- `openapi.yaml`：API 草案
- `venera_home.js`：Venera 源脚本

## 开发

运行测试：

```powershell
go test ./...
```

构建：

```powershell
go build ./...
```

当前测试覆盖重点包括：

- 配置加载
- 本地完整阅读流程
- WebDAV 扫描
- 元数据覆盖与补全流程
- `rar` / `7z` 归档读取
- 管理页相关元数据接口
