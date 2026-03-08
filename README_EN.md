# Venera Home Server

[中文](./README.md) | [English](./README_EN.md)

`Venera Home Server` is a local-comics backend for **[Venera](https://github.com/venera-app/venera)**. It exposes comics stored on local disks, SMB shares, and WebDAV through a lightweight HTTP API, and ships with a matching `venera_home.js` source script that can be imported into Venera directly.

It currently focuses on two main workflows:

- everyday reading: scan, index, search, details, chapter reading, favorites
- local metadata management: local metadata store, external SQLite enrichment, built-in admin UI, and `.venera.json` sidecar editing

## Feature Overview

### Storage and formats

- Library backends: local directories, SMB (currently Windows-only), WebDAV
- Image folders: `jpg` / `jpeg` / `png` / `webp` / `gif` / `bmp` / `avif`
- Archives: `cbz` / `zip` / `cbr` / `rar` / `cb7` / `7z`
- Documents: `pdf` (currently rendered on Windows only)

### Reading and API features

- Scan, index, home feed, categories, search, details, and chapter reading
- Search by title / tag / author, plus path-oriented matches
- Favorites with multiple folders
- Signed media URLs for covers and pages
- Archive and remote-file caching
- Cached PDF page rendering on first access
- `venera_home.js` details can expose local and relative paths

### Metadata and admin UI

- Reads `ComicInfo.xml`
- Reads `.venera.json` sidecar overrides
- Persists scan results into local `metadata.db`
- Auto-discovers external SQLite sources under `data/externaldb/`
- Uses fill-only enrichment so explicit local metadata is not overwritten
- Built-in admin page at `/`, with support for:
  - manual batch enrichment
  - single-record enrich / lock / unlock / reset
  - batch actions on selected records
  - browsing external sources
  - cleaning `missing` records with dry-run support
  - editing / deleting `.venera.json` sidecars on writable backends
  - manual `Rescan`
  - auto-rescan after sidecar save / delete
- Includes the `exdb_dryrun` dry-run matching tool

## Quick Start

### 1. Prepare the config

You can start from `server.example.toml`, or begin with this minimal example:

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

Notes:

- `scan_mode = "auto"` tries to group matching chapters into one comic entry
- `scan_mode = "flat"` treats each directory or archive as an independent comic
- `server.example.toml` also contains fields such as `watch_local`, `rescan_interval_minutes`, and `allow_remote_fetch`; the current primary workflow still relies on explicit rescans from the admin/API side

### 2. Set env vars for SMB / WebDAV if needed

```powershell
$env:SMB_PASS = "your-password"
$env:WEBDAV_PASS = "your-password"
```

### 3. Start the server

```powershell
go run . -config ./server.example.toml
```

Or use the built binary:

```powershell
.\venera_home_server.exe -config .\server.example.toml
```

### 4. Import the Venera source script

Import `venera_home.js`, then configure:

- `Server URL`: for example `http://127.0.0.1:34123`
- `Token`: must match the server config
- `Default Library ID`: optional
- `Default Sort`
- `Page Size`
- `Image Mode`

> If your phone connects to a server running on your PC, do not use `127.0.0.1`; use the PC's LAN IP instead.

### 5. Open the admin page

After startup, open `/` in a browser to access the built-in admin UI.

If the server uses a `token`, enter the Bearer token in the upper-right corner.

## Metadata Workflow

### Local metadata store

Every scan writes comic records into the local metadata database, which defaults to:

- `data/metadata.db`

It stores:

- stable library/comic locators
- path and folder information
- content fingerprints
- matching hints
- enriched titles, authors, tags, sources, and related metadata

### External SQLite sources

Drop external SQLite files directly into:

- `data/externaldb/`

No extra registration is required. The admin page discovers them automatically.

### Typical admin workflow

- browse an external source first to confirm the data looks correct
- run batch enrichment for `state=empty`
- use lock / reset for bad matches
- edit sidecars directly when you want local manual overrides
- trigger or wait for `Rescan` so Venera sees changes immediately

The purpose of `manual_locked` is simple: **prevent the same bad enrichment from being applied repeatedly**.

## Metadata Priority

Metadata is currently resolved in roughly this order:

1. `.venera.json`
2. `ComicInfo.xml`
3. filename / folder-name inference
4. enriched fields from the local metadata store (fill-only, not destructive overwrite)

Example `.venera.json`:

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

Additional rules:

- `hidden: true`: ignore the current directory or archive
- directory sidecar: place `.venera.json` inside the directory
- archive sidecar: place `xxx.cbz.venera.json` / `xxx.zip.venera.json` next to the archive

## Limitations and Current Scope

- `SMB` is currently only implemented for Windows builds
- `PDF` rendering is currently Windows-only and depends on `Windows.Data.Pdf`
- Enrichment providers are currently local SQLite databases; internet metadata sources are not integrated yet
- There is no full scheduled enrichment workflow yet; the main workflow is still manual/admin-triggered
- Sidecar editing depends on writable backends; read-only backends can be viewed but not modified

## Repository Layout

- `main.go`: startup entry
- `app/`: scan flow, indexing, metadata merge, enrichment jobs
- `httpapi/`: HTTP API, media serving, admin UI
- `metadata/`: local metadata store
- `backend/` / `archive/`: storage backends and archive access
- `exdbdryrun/` + `cmd/exdb_dryrun/`: dry-run tooling for external SQLite matching
- `tests/`: tests and `testkit/`
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

Current test coverage focuses on:

- config loading
- end-to-end local reading flow
- WebDAV scanning
- metadata override and enrichment flow
- `rar` / `7z` archive reading
- admin metadata endpoints
