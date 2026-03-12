# Venera Home Server

[中文](./README.md) | [English](./README_EN.md)

`Venera Home Server` is a local-comics backend for **[Venera](https://github.com/venera-app/venera)**.

It currently covers two main workflows:

- everyday reading: scan, index, search, details, chapter reading, favorites
- local management: metadata ingestion/enrichment, EH Bot pull-import, built-in admin UI, and job history

## Current Feature Overview

### Storage backends and formats

- Library backends: local directories, SMB (currently mainly for Windows builds), WebDAV
- Image folders: `jpg` / `jpeg` / `png` / `webp` / `gif` / `bmp` / `avif`
- Archives: `cbz` / `zip` / `cbr` / `rar` / `cb7` / `7z`
- Documents: `pdf` (currently mainly on the Windows rendering path)

### Reading and API

- Scan, home feed, categories, search, details, and chapter reading
- Search by title, tag, author, and path
- Favorites with multiple folders
- Signed media URLs for covers and pages
- Archive and remote-file caching
- Automatic cleanup for expired disk cache under `cache_dir`
- `venera_home.js` can be imported directly into Venera

### Metadata features

- Reads `ComicInfo.xml`
- Reads `.venera.json` sidecars
- Reads `galleryinfo.txt` from directories and archives
- Persists scan results and enrichment state into local `metadata.db`
- Auto-discovers external SQLite sources under `data/externaldb/`
- Uses a fill-only enrichment strategy so explicit local metadata is not overwritten

### EH Bot integration

- Built-in Pull API consumer for `ehbot_server`
- Automatically runs `list -> claim -> download -> import -> complete/fail`
- Verifies artifact SHA256
- Imports into a selected local library and subdirectory
- Can trigger auto-rescan after import
- Admin UI can create remote download jobs directly, without Telegram

### Admin UI

The built-in admin page is now split by concern:

- `EH Bot`: runtime status, config editing, remote create-job, manual `Run Once`
- `Metadata`: enrichment, cleanup, lock/reset, sidecar editing
- `Job History`: unified history for EH Bot and metadata jobs

## Quick Start

### 1. Prepare the config

Start from `server.example.toml`. Minimal example:

```toml
[server]
listen = "0.0.0.0:34123"
token = "change-me"
data_dir = "./data"
cache_dir = "./cache"
cache_max_age_hours = 168
cache_cleanup_interval_minutes = 360

[[libraries]]
id = "local-main"
name = "Local Manga"
kind = "local"
root = "D:/Comics"
scan_mode = "auto"
```

Notes:

- `scan_mode = "auto"` tries to group matching chapters into one comic entry
- `scan_mode = "flat"` treats each directory or archive as a separate comic
- `data_dir` stores the local metadata database, cache, and related state
- `cache_max_age_hours` controls how long disk cache is retained before cleanup; set `0` to disable it
- `cache_cleanup_interval_minutes` controls how often the background janitor scans `cache_dir`

### 2. Set SMB / WebDAV passwords if needed

```powershell
$env:SMB_PASS = "your-password"
$env:WEBDAV_PASS = "your-password"
```

### 3. Start the server

```powershell
go run . -config ./server.example.toml
```

Or run the built binary:

```powershell
.\venera_home_server.exe -config .\server.example.toml
```

### 4. Import the Venera source script

Import `venera_home.js` into Venera and fill in:

- `Server URL`, for example `http://127.0.0.1:34123`
- `Token`, matching the server config
- `Default Library ID` (optional)
- `Default Sort`
- `Page Size`
- `Image Mode`

> If your phone is connecting to a server running on your PC, do not use `127.0.0.1`; use the PC's LAN IP instead.

### 5. Open the admin page

After startup, open `/` in a browser.

If a server token is configured, enter the Bearer token in the top-right corner.

## Metadata Workflow

### Local metadata store

Each scan writes comic locator data and metadata state into the local database, by default:

- `data/metadata.db`

It currently stores:

- library/path locator data
- file fingerprints and match hints
- enriched titles, authors, tags, source fields, and related metadata
- job history

### External SQLite sources

Drop external SQLite files into:

- `data/externaldb/`

The admin page discovers them automatically.

### Current metadata priority

Metadata is resolved roughly in this order:

1. `.venera.json`
2. `ComicInfo.xml`
3. `galleryinfo.txt`
4. filename / folder-name inference
5. enrichment fields from the local metadata store (fill-only)

### What `galleryinfo.txt` is used for

Whether `galleryinfo.txt` lives in a directory or inside an archive, the server currently reads:

- `Title:` as the title
- `Tags:` as tags
- `language:*` tags to infer language
- the `Uploader Comment:` / `Uploader's Comments:` block as the description body

This is why artifacts produced by `ehbot_server` or H@H packaging can be imported and scanned directly.

## Important `source_url` Rule

Strings such as “搬运来源”, original author links, Pixiv links, and similar text inside `galleryinfo.txt` comments are **just uploader-comment text**. They must not be treated as the canonical `source_url`.

The current project rule is:

- canonical `source_url` must come from explicit metadata fields
- for E-H galleries, it should be derived from `gid + token`

Canonical form:

```text
https://e-hentai.org/g/<gid>/<token>/
```

So in practice:

- `galleryinfo.txt` comments only flow into `description`
- Home Server does not infer `source_url` from uploader-comment text
- in the EH Bot import flow, `source_url` should come from the remote gallery payload

## EH Bot Integration

`venera_home_server` already includes the EH Bot consumer. No extra script is required.

The example config block is:

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

Field summary:

- `enabled`: enable automatic polling
- `base_url`: public `ehbot_server` base URL
- `pull_token`: remote Pull API Bearer token
- `consumer_id`: this Home Server's consumer identity
- `target_id`: only consume matching remote jobs
- `target_library_id`: destination local library
- `target_subdir`: import subdirectory
- `poll_interval_seconds`: automatic polling interval
- `lease_seconds`: claim lease duration
- `download_timeout_seconds`: artifact download timeout
- `auto_rescan`: rescan after import
- `max_jobs_per_poll`: max jobs handled per polling cycle

### Current EH Bot admin features

The `EH Bot` tab in the admin UI currently supports:

- viewing runtime status
- viewing recent pull/import jobs
- manually running one poll cycle with `Run Once`
- loading the current EH Bot config
- editing and saving EH Bot config back to the current server config file (saving rewrites that config file)
- creating a remote download job directly against `ehbot_server` (`POST /api/v1/jobs`)

Telegram is therefore optional; it is only one possible create-job entrypoint.

## Job History

Job History is now persisted and shown in a dedicated admin tab.

Current recorded job kinds include:

- `ehbot.pull`
- `ehbot.create_remote`
- `metadata.refresh`
- `metadata.reset`
- `metadata.cleanup`

Each entry currently stores:

- job id / kind / trigger / status
- library / target / remote job id
- requested, started, finished, and updated timestamps
- payload / result JSON
- error text

This gives you one place to inspect both automated and manual operations.

## Key Admin Endpoints

In addition to the reading APIs, the main admin endpoints are:

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

The OpenAPI draft lives in `openapi.yaml`.

## Recommended EH Bot Deployment Shape

Recommended layout:

- `ehbot_server` on a public machine
- `venera_home_server` on your home / private network
- Home Server stays off the public internet
- Home Server actively pulls artifacts from EH Bot

Benefits:

- networking and cookie handling stay on the EH Bot side
- Home Server remains simple and internal-only
- comics still end up in your local library for Venera

## Example End-to-End Flow

1. Create a job on `ehbot_server` via Telegram or HTTP
2. The remote job becomes `ready`
3. Home Server polls and `claim`s it
4. Home Server downloads the artifact and verifies SHA256
5. The file is imported into `target_library_id/target_subdir`
6. Home Server reports `complete` back to the remote service
7. If `auto_rescan` is enabled, the local library is refreshed automatically

## Repository Layout

- `main.go`: startup entrypoint
- `app/`: scan flow, indexing, metadata, EH Bot consumer, job history
- `httpapi/`: HTTP API and admin UI
- `metadata/`: metadata store and persisted job history
- `backend/` / `archive/`: backends and archive reading
- `tests/`: tests and `testkit`
- `server.example.toml`: sample config
- `openapi.yaml`: API draft
- `venera_home.js`: Venera source script

## Development

Run tests:

```powershell
go test ./...
```

Build:

```powershell
go build ./...
```

