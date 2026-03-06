# Venera Home Server

`venera_home_server` is the backend project for Venera Home. It contains the Go server, example config, API contract, setup docs, and the canonical `venera_home.js` source script.

## Files

- `venera_home.js`: Venera source script maintained alongside the backend
- `server.example.toml`: example server configuration
- `openapi.yaml`: HTTP API contract
- `使用指南.md`: end-user setup guide
- `ARCHITECTURE.md`: architecture and API mapping notes

When this project is mirrored inside `venera-configs`, keep the repository-root copy `..\venera_home.js` in sync with `venera_home_server\venera_home.js`.

## Implemented MVP

- Local libraries
- SMB libraries on Windows through UNC access and `WNetAddConnection2W`
- WebDAV libraries via `PROPFIND` and `GET`
- Folder-image comics and `cbz`/`zip`, `cbr`/`rar`, `7z`/`cb7`, `pdf`
- Comic scanning, indexing, home feed, categories, search, details, chapter pages
- PDF rendering on Windows via built-in `Windows.Data.Pdf` with local page cache
- Favorites with multi-folder persistence
- Rescan endpoint
- Signed media URLs for covers and pages

## Run

```powershell
go run . -config .\server.example.toml
```

Then point the Venera source at `http://127.0.0.1:34123` and import either `C:\Users\buste\dev\venera-configs\venera_home.js` or `C:\Users\buste\dev\venera-configs\venera_home_server\venera_home.js` into Venera.