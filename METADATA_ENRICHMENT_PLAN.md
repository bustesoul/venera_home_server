# Metadata Enrichment Plan

> 目标：在 **不拖慢启动扫描**、**不覆盖本地元数据**、**允许第三方/后台慢慢填充** 的前提下，为 `venera_home_server` 增加可持久化、可追踪、可重绑的扩展元数据系统。

## 1. 已确认设计原则

- [x] 不用 `folder_path` 作为主键，但必须保存它，方便人工定位和第三方补录。
- [x] 普通扫描只读 **本地文件元数据 + 本地 SQLite 缓存 DB**，不在启动阶段做远程抓取。
- [x] DB metadata 必须在 **Comic 构建阶段合并**，不能只在 response 阶段临时拼装。
- [x] 合并策略必须是 **逐字段补空**，严禁覆盖本地 `.venera.json` / `ComicInfo.xml`。
- [x] 不依赖 `filepath.WalkDir`，统一基于现有 `Backend` 抽象实现，兼容 local / SMB / WebDAV。
- [x] 远程封面不直接热链给客户端；下载后持久化到 `data_dir`，并继续走现有媒体体系。
- [x] 首选 SQLite，不引入额外服务。
- [x] metadata refresh 先挂到现有 admin 接口，状态先用轮询，不急着 WebSocket。
- [x] 允许后台任务或第三方应用慢慢填 DB；填完后只需要 `rescan` 即可生效。
- [x] 必须处理“漫画被手工挪位置但未删除”的情况，尽量自动重绑旧 metadata。
- [x] 需要提供“悬空 metadata 清理”能力，避免垃圾数据长期干扰。

## 2. 当前代码的接入点

### App 层
- `app/app.go`
  - `NewApp()`：启动时会同步 `Rescan()`
  - `Rescan()`：负责重建内存中的 `Comic` / `Chapter` 索引
  - `loadMetadataForDir()` / `loadMetadataForArchive()`：当前本地 metadata 入口
  - `applyMetadata()`：当前把 metadata 写入 `Comic` 的统一入口

### HTTP 层
- `httpapi/server.go`
  - `handleMetadataRefresh()`：当前是占位接口，可升级为真正的后台任务入口

### 配置层
- `config/config.go`
  - `MetadataConfig.AllowRemoteFetch`：现成的远程能力总开关

## 3. MVP 范围（第一阶段）

### 3.1 必做
- [ ] 新增 SQLite metadata store
- [ ] 普通扫描时为每个漫画根对象写入/更新一条本地占位记录
- [ ] 为每条记录保存：定位键、`folder_path`、hint、`content_fingerprint`
- [ ] Comic 构建阶段从 DB 读取补充 metadata，并执行“逐字段补空”合并
- [ ] 实现 metadata refresh 后台 job（先轮询状态）
- [ ] 实现 missing / stale / error 状态标记
- [ ] 实现“按内容指纹重绑”来应对路径变化
- [ ] 实现悬空 metadata 列表与清理接口

### 3.2 第一阶段不做
- [ ] 不在启动扫描中联网抓取
- [ ] 不引入 PostgreSQL / GORM
- [ ] 不直接热链外站封面
- [ ] 不强依赖 EH/Pixiv provider 才能工作
- [ ] 不做 WebSocket job 推送

## 4. 数据模型

建议先用单表，够用后再拆：

```sql
CREATE TABLE IF NOT EXISTS manga_metadata (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    library_id TEXT NOT NULL,
    root_type TEXT NOT NULL,
    root_ref TEXT NOT NULL,
    folder_path TEXT NOT NULL,
    content_fingerprint TEXT,

    title TEXT,
    title_jpn TEXT,
    subtitle TEXT,
    description TEXT,
    artists_json TEXT,
    tags_json TEXT,
    language TEXT,
    rating REAL,
    category TEXT,

    source TEXT,
    source_id TEXT,
    source_token TEXT,
    source_url TEXT,

    match_kind TEXT,
    confidence REAL,
    manual_locked INTEGER NOT NULL DEFAULT 0,

    cover_source_url TEXT,
    cover_blob_relpath TEXT,

    hint_json TEXT,
    extra_json TEXT,
    last_error TEXT,

    fetched_at TIMESTAMP,
    stale_after TIMESTAMP,
    last_seen_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    missing_since TIMESTAMP
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_manga_metadata_locator
ON manga_metadata(library_id, root_type, root_ref);

CREATE INDEX IF NOT EXISTS idx_manga_metadata_folder_path
ON manga_metadata(folder_path);

CREATE INDEX IF NOT EXISTS idx_manga_metadata_content_fp
ON manga_metadata(content_fingerprint);

CREATE INDEX IF NOT EXISTS idx_manga_metadata_missing_since
ON manga_metadata(missing_since);
```

## 5. 主键与定位策略

### 5.1 真正定位键
- `library_id`
- `root_type` (`dir` / `archive` / `series`)
- `root_ref`

### 5.2 人类定位字段
- `folder_path`：仅用于人工处理、导出、第三方工具操作，不作为唯一键

### 5.3 路径变化自动重绑
扫描时按以下顺序找记录：
1. 先按 `(library_id, root_type, root_ref)` 精确匹配
2. 找不到时，再按 `content_fingerprint` 查找同库中 `missing_since IS NOT NULL` 的旧记录
3. 如果只命中 1 条，则更新旧记录的：
   - `root_ref`
   - `folder_path`
   - `last_seen_at`
   - `missing_since = NULL`
4. 仍然找不到时，创建新的占位记录

## 6. 内容指纹（content_fingerprint）

目标：尽量在“文件位置变化但内容未变”时复用旧 metadata。

### 6.1 第一版策略

#### dir 漫画
- 根目录相对路径仅用于展示，不参与指纹
- 指纹由以下信息组合 hash：
  - 图片总数
  - 前 3 张图片名 + size
  - 后 3 张图片名 + size

#### archive 漫画
- 指纹由以下信息组合 hash：
  - entry 总数
  - 前 3 个图片 entry 名 + size
  - 后 3 个图片 entry 名 + size

#### series 漫画
- 指纹由以下信息组合 hash：
  - chapter 总数
  - 每章标题（标准化）
  - 每章页数

### 6.2 后续可增强
- [ ] 加入第一页图片 hash（可选）
- [ ] 对 archive 先读取 entry 名单再算更稳定的结构指纹
- [ ] 提供手动“重新绑定 metadata”工具

## 7. metadata 合并优先级

严格遵循：**本地优先，DB 只补空，不主动覆盖。**

### 7.1 优先级
1. 本地 `.venera.json`
2. 本地 `ComicInfo.xml`
3. DB 中 `manual_locked = 1` 的字段
4. DB 中 `match_kind = exact_id` 的字段
5. DB 中 `match_kind = fuzzy` 的字段
6. fallback（目录名 / 压缩包名）

### 7.2 合并规则
- 标量字段：`title` / `subtitle` / `description` / `language` 仅在本地为空时补入
- 数组字段：`artists` / `tags` 可并集，但本地值优先保留顺序
- `hidden` 只接受本地文件定义，不从远程 metadata 推导
- `manual_locked = 1` 时，只允许手动编辑或显式强制 refresh 覆盖

## 8. 普通扫描流程（不联网）

每个漫画根对象扫描时执行：
1. 读取本地 metadata（现有逻辑）
2. 提取 hint（EH gid/token、Pixiv illust id、标题片段）
3. 计算 `content_fingerprint`
4. `upsert` 到 SQLite：
   - 如果已有记录，更新 `last_seen_at`
   - 如果 metadata 仍为空，也保留一条占位记录
5. 从 SQLite 读取补充 metadata
6. 执行“逐字段补空”合并
7. 构建 `Comic`

> 这样启动仍然快，后台或第三方把 DB 填满后，只要 `rescan` 一次即可对前台生效。

## 9. metadata refresh 后台任务

### 9.1 入口
复用现有接口：
- `POST /api/v1/admin/metadata/refresh`

建议请求体：
```json
{
  "library_id": "local-main",
  "path": "optional/sub/path",
  "force": false
}
```

### 9.2 job 状态
第一版可先内存保存最近 N 个 job：
- `queued`
- `running`
- `done`
- `failed`

### 9.3 查询接口
- [ ] `GET /api/v1/admin/metadata/jobs/{job_id}`
- [ ] `GET /api/v1/admin/metadata/records?state=empty|missing|stale|error`

### 9.4 任务行为
- 后台遍历目标范围内的漫画根对象
- 根据 hints / 第三方 provider / 手工数据库记录补全 metadata
- 成功后更新 `fetched_at`
- 完成后可提示用户执行 rescan，或提供“一键 rescan”开关

## 10. Provider 层（第二阶段开始接入）

### 10.1 抽象目标
- 服务器后台任务可写 DB
- 第三方应用可直接写 DB
- 后续可逐步添加 EH / Pixiv / 手工导入 / 离线库 provider

### 10.2 Provider 分类
- `HintProvider`：从路径名/文件名提取提示信息
- `ResolverProvider`：根据 hints 查本地离线库或远程源
- `CoverProvider`：拉取封面到持久目录

### 10.3 第一批候选
- [ ] EH 精确 gid/token provider
- [ ] Pixiv illust id provider
- [ ] 第三方手工导入/编辑 provider

## 11. 封面持久化

### 11.1 原则
- 不进 `cache_dir`
- 不直接热链外站
- 一旦抓到，长期保留，除非手动清理

### 11.2 存储位置
建议：
- `data/metadata-covers/<metadata_id>.jpg`

数据库保存：
- `cover_source_url`
- `cover_blob_relpath`

客户端仍通过现有媒体体系访问，不直接暴露外站 URL。

## 12. 悬空数据与清理

### 12.1 missing 语义
- 本次扫描没有再看到的记录：`missing_since = now`
- 后续扫描又看到了：`missing_since = NULL`

### 12.2 管理接口
- [ ] 列出 missing 记录
- [ ] `dry_run` 清理
- [ ] 按 `older_than_days` 清理
- [ ] 限定 `library_id` 范围清理

## 13. 实现拆分建议

### 13.1 新增目录
- [ ] `metadata/store.go`：SQLite CRUD
- [ ] `metadata/model.go`：metadata record / job record struct
- [ ] `metadata/fingerprint.go`：内容指纹计算
- [ ] `metadata/merge.go`：逐字段补空合并逻辑
- [ ] `metadata/service.go`：refresh job / provider 调度

### 13.2 需要修改的现有文件
- [ ] `config/config.go`
  - 增加 metadata DB 配置（可选，默认落到 `data_dir`）
- [ ] `app/app.go`
  - 普通扫描时 upsert 占位记录
  - `loadMetadataForDir()` / `loadMetadataForArchive()` 后叠加 DB metadata
- [ ] `httpapi/server.go`
  - 把 `handleMetadataRefresh()` 做实
  - 增加 job 查询 / record 查询 / cleanup 接口
- [ ] `main.go`
  - 初始化 metadata service/store

## 14. 里程碑

### Milestone 1：本地 SQLite 占位 + 合并
- [ ] SQLite store 可初始化
- [ ] 普通扫描写占位记录
- [ ] Comic 构建阶段读取 DB 并补空
- [ ] 基础单测覆盖 dir/archive/series

### Milestone 2：refresh job + missing 管理
- [ ] metadata refresh 后台任务可运行
- [ ] job 状态轮询可用
- [ ] missing / stale / error 可查询
- [ ] cleanup 可 dry-run

### Milestone 3：路径重绑 + provider 接入
- [ ] content_fingerprint 生效
- [ ] 路径变化可自动重绑旧 metadata
- [ ] 接入第一种 provider（建议先 exact-id）

### Milestone 4：封面持久化
- [ ] 封面下载到 `data_dir`
- [ ] 走现有媒体体系返回封面
- [ ] 可手动清理旧封面

## 15. 风险清单

- [ ] 指纹算法过弱会误绑，过强会难以命中，需要平衡
- [ ] series 漫画结构变动时可能导致指纹漂移
- [ ] 第三方/后台同时写 DB 需要控制并发与事务
- [ ] 模糊匹配若置信度过低，容易污染 metadata，需要 `confidence` 和 `match_kind`
- [ ] 封面持久化目录需要明确清理策略

## 16. 当前建议的下一步

按 MVP 顺序推进：
1. 先落 `metadata/store.go` 和 SQLite 表
2. 再把普通扫描的“占位写库 + DB 补空合并”接进去
3. 然后实现 metadata refresh job 和状态查询
4. 最后再接 provider / 封面持久化

---

## 进度记录

### 2026-03-07
- [x] 设计原则已确认
- [x] 明确必须支持路径变动后的自动重绑
- [x] 明确普通扫描不联网，只做占位 + 合并
- [ ] SQLite store 未开始实现
- [ ] metadata refresh job 未开始实现
- [ ] provider 未开始实现
