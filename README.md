# Venera Home Server

[中文](./README.md) | [English](./README_EN.md)

`Venera Home Server` 是为 **[Venera](https://github.com/venera-app/venera)** 准备的本地漫画后端服务。

它当前覆盖两条主线：

- 日常阅读：扫描、索引、搜索、详情、章节阅读、收藏夹
- 本地管理：元数据读取与补全、EH Bot 拉取导入、内置 Admin 面板、作业历史

## 当前功能总览

### 存储与格式支持

- 本地目录、SMB（当前主要用于 Windows 构建）、WebDAV 三类书库后端
- 图片目录：`jpg` / `jpeg` / `png` / `webp` / `gif` / `bmp` / `avif`
- 压缩包：`cbz` / `zip` / `cbr` / `rar` / `cb7` / `7z`
- 文档：`pdf`（当前主要是 Windows 渲染链路）

### 阅读与 API

- 扫描、首页、分类、搜索、详情、章节阅读
- 搜索支持标题 / 标签 / 作者 / 路径
- 收藏夹与多文件夹收藏
- 带签名的封面 / 页面媒体 URL
- 归档与远程文件缓存
- `venera_home.js` 可直接导入 Venera 使用

### 元数据能力

- 读取 `ComicInfo.xml`
- 读取 `.venera.json` sidecar
- 读取目录内或压缩包内的 `galleryinfo.txt`
- 将扫描结果与补全状态写入本地 `metadata.db`
- 自动发现 `data/externaldb/` 下的外部 SQLite 数据源
- 以“填空式”策略做本地 metadata enrichment，避免覆盖显式手工元数据

### EH Bot 集成

- 作为 `ehbot_server` 的 Pull API consumer
- 自动执行 `list -> claim -> download -> import -> complete/fail`
- 校验 artifact SHA256
- 导入指定 local library 的指定子目录
- 可选导入后自动 `rescan`
- Admin 面板可直接创建远程下载任务，不依赖 Telegram

### Admin 面板

当前内置 Admin 页面按功能拆成了独立区域：

- `EH Bot`：拉取状态、配置编辑、远程建单、手动 `Run Once`
- `Metadata`：补全、锁定、清理、sidecar 编辑等
- `Job History`：统一查看 EH Bot 与 metadata 相关作业历史

## 快速开始

### 1. 准备配置

可从 `server.example.toml` 开始。最小示例：

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

常用说明：

- `scan_mode = "auto"`：尽量把同作品章节归并到一个漫画条目
- `scan_mode = "flat"`：每个目录或压缩包单独视为一个漫画
- `data_dir` 下会保存本地 metadata store、缓存与相关状态数据

### 2. 如果使用 SMB / WebDAV，设置密码环境变量

```powershell
$env:SMB_PASS = "your-password"
$env:WEBDAV_PASS = "your-password"
```

### 3. 启动服务

```powershell
go run . -config ./server.example.toml
```

或使用可执行文件：

```powershell
.\venera_home_server.exe -config .\server.example.toml
```

### 4. 导入 Venera 源脚本

在 Venera 中导入 `venera_home.js`，填写：

- `Server URL`：例如 `http://127.0.0.1:34123`
- `Token`：与服务端一致
- `Default Library ID`：可选
- `Default Sort`
- `Page Size`
- `Image Mode`

> 如果手机访问的是电脑上的服务，不要用 `127.0.0.1`，要改成电脑的局域网 IP。

### 5. 打开 Admin 面板

服务启动后，浏览器访问根路径 `/` 即可进入内置管理页面。

如果启用了 `token`，在页面右上角填写 Bearer Token。

## 元数据工作流

### 本地 metadata store

每次扫描都会把漫画定位信息与元数据状态写入本地数据库，默认位于：

- `data/metadata.db`

当前会保存：

- 书库 / 路径定位信息
- 文件指纹与匹配 hint
- 已补全标题、作者、标签、来源等字段
- Job History

### 外部 SQLite 数据源

把外部 SQLite 文件放进：

- `data/externaldb/`

Admin 面板会自动发现并显示，无需额外注册。

### 当前元数据优先级

大致顺序为：

1. `.venera.json`
2. `ComicInfo.xml`
3. `galleryinfo.txt`
4. 文件名 / 目录名推断
5. 本地 metadata store 中的补全字段（填空式）

### `galleryinfo.txt` 当前会读取什么

无论 `galleryinfo.txt` 位于目录内还是压缩包内，当前都会读取：

- `Title:` 作为标题
- `Tags:` 作为标签
- `language:*` 标签推断语言
- `Uploader Comment:` / `Uploader's Comments:` 段落作为描述正文

这使得从 `ehbot_server` 或 H@H 产出的压缩包可以直接被 Home Server 消费。

## 关于 `source_url` 的重要说明

`galleryinfo.txt` 评论区里的“搬运来源”“原作者地址”“Pixiv 链接”等内容，**只是 uploader comment 正文的一部分**，不能被当作 canonical `source_url`。

当前项目的约定是：

- canonical `source_url` 应来自显式元数据字段
- 对于 E-H gallery，应由 `gid + token` 组合得到：

```text
https://e-hentai.org/g/<gid>/<token>/
```

因此：

- `galleryinfo.txt` 的评论正文只会进入 `description`
- Home Server 不会从评论正文里反推 `source_url`
- EH Bot 导入链路里如需 `source_url`，应以远端 job 的 gallery 信息为准

## EH Bot 集成

`venera_home_server` 已经内置 EH Bot consumer，不需要额外脚本。

`server.example.toml` 中的配置段如下：

```toml
[ehbot]
enabled = false
base_url = "https://ehbot.example.com"
pull_token = "change-me"
consumer_id = "home-main"
target_id = "home-main"
target_library_id = "local-main"
target_subdir = "EH Inbox"
poll_interval_seconds = 60
lease_seconds = 1800
download_timeout_seconds = 1800
auto_rescan = true
max_jobs_per_poll = 1
```

字段说明：

- `enabled`：是否自动轮询远端 EH Bot
- `base_url`：`ehbot_server` 的公网地址
- `pull_token`：远端 Pull API Bearer Token
- `consumer_id`：当前 Home Server 的 consumer 身份
- `target_id`：只消费匹配该 target 的远端任务
- `target_library_id`：导入到哪个本地书库
- `target_subdir`：导入子目录
- `poll_interval_seconds`：自动轮询间隔
- `lease_seconds`：claim 租约时长
- `download_timeout_seconds`：下载 artifact 超时
- `auto_rescan`：导入后是否自动重扫
- `max_jobs_per_poll`：每轮最多处理的 job 数

### 当前支持的 EH Bot 管理能力

Admin 面板里的 `EH Bot` tab 已支持：

- 查看运行状态
- 查看最近拉取任务
- 手动执行一次 `Run Once`
- 加载当前 EH Bot 配置
- 编辑并保存 EH Bot 配置到当前服务配置文件（保存会重写当前配置文件）
- 直接创建远程下载任务（调用 `ehbot_server` 的 `POST /api/v1/jobs`）

也就是说，Telegram 现在只是建单入口之一；你也可以完全绕过 Telegram，仅通过 Home Server Admin 面板测试整条下载链路。

## Job History

当前 Job History 会统一落库存，并在 Admin 面板单独展示。

已覆盖的作业类型包括：

- `ehbot.pull`
- `ehbot.create_remote`
- `metadata.refresh`
- `metadata.reset`
- `metadata.cleanup`

每条记录当前会保留：

- job id / kind / trigger / status
- library / target / remote job id
- 请求、开始、结束、更新时间
- payload / result JSON
- 错误信息

这使得你可以把“自动行为”和“手工操作”放在同一处追踪。

## Admin API 重点接口

除了阅读 API 外，当前与管理相关的关键接口有：

- `POST /api/v1/admin/rescan`
- `POST /api/v1/admin/metadata/refresh`
- `POST /api/v1/admin/metadata/enrich`
- `GET /api/v1/admin/metadata/jobs`
- `GET /api/v1/admin/jobs`
- `GET /api/v1/admin/ehbot/status`
- `GET /api/v1/admin/ehbot/config`
- `PUT /api/v1/admin/ehbot/config`
- `GET /api/v1/admin/ehbot/jobs`
- `POST /api/v1/admin/ehbot/jobs/create`
- `POST /api/v1/admin/ehbot/pull/run-once`

OpenAPI 草案位于 `openapi.yaml`。

## 典型 EH Bot 部署方式

推荐结构：

- `ehbot_server` 部署在公网机器上
- `venera_home_server` 部署在你家里或内网环境
- Home Server 不暴露公网
- Home Server 主动去拉 EH Bot 的 artifact

优点：

- 网络与 Cookie 问题集中在 EH Bot 侧解决
- Home Server 维持内网、职责单纯
- 最终漫画仍落到你的本地书库中，供 Venera 阅读

## 示例：从远端下载到本地导入

1. 在 `ehbot_server` 创建任务（Telegram 或 HTTP）
2. 远端 job 进入 `ready`
3. Home Server 自动轮询到该任务并 `claim`
4. 下载 artifact，校验 SHA256
5. 导入到 `target_library_id/target_subdir`
6. 回写远端 `complete`
7. 如果开启 `auto_rescan`，则自动刷新本地书库

## 仓库结构

- `main.go`：启动入口
- `app/`：扫描、索引、元数据、EH Bot consumer、作业历史
- `httpapi/`：HTTP API 与 Admin 页面
- `metadata/`：metadata store 与 job history 存储
- `backend/` / `archive/`：后端与归档读取
- `tests/`：测试与 `testkit`
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

