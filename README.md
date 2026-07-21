<div align="center">

# TeleCollection

### Unlimited, end‑to‑end encrypted cloud storage on Telegram

**Turn your Telegram account into a private, unlimited cloud drive** — with optional
zero‑knowledge encryption, a built‑in media library for your series and movies, and a
virtual drive on your desktop. Self‑hosted or fully local. Open source.

![CI](https://github.com/NeritonDias/telecollection/actions/workflows/ci.yml/badge.svg)
![Go](https://img.shields.io/badge/Go-1.26-00ADD8?logo=go&logoColor=white)
![Platforms](https://img.shields.io/badge/platforms-Windows%20%7C%20macOS%20%7C%20Linux-blue)
![License: MIT](https://img.shields.io/badge/license-MIT-green)
![Status](https://img.shields.io/badge/status-early%20development-orange)

</div>

---

## What is TeleCollection?

**TeleCollection** is an open‑source **Telegram cloud storage** app that treats your
Telegram account as unlimited storage — a free, self‑hosted alternative to Google Drive,
Dropbox and OneDrive. It runs as a **native desktop app** (Windows, macOS, Linux) *or*
as a **self‑hosted server** you deploy on your own VPS. One backend, two ways to run.

Unlike bot‑based tools that are capped at ~50 MB per file, TeleCollection connects through
the full Telegram client API (MTProto), so it handles large files, real streaming, and
transparent chunking of files **beyond the 2 GB limit**.

## Features

> 🚧 **Early development.** The Go foundation (data layer, HTTP API, CI) is in place and
> green; the features below are landing incrementally — see the [Roadmap](#roadmap).

- 🔐 **End‑to‑end encryption (opt‑in).** Files are encrypted client‑side with a **BIP39
  seed phrase** — zero‑knowledge storage that neither the server nor Telegram can read,
  recoverable from the seed alone.
- 📦 **No 2 GB limit.** Transparent chunking splits large files across messages and
  reassembles them on download.
- 📺 **Media library.** Organize series → seasons → episodes and **stream in‑app**, with
  *continue watching* and **M3U/M3U8 playlist export** for external IPTV players.
- 💾 **Virtual drive.** Mount your Telegram storage as a real system drive on
  **Windows, macOS and Linux** (FUSE / WinFsp).
- 🔄 **Folder sync.** Dropbox‑style automatic backup of a local folder.
- ⌨️ **CLI + rclone backend.** Scriptable, automatable, CI‑friendly.
- 🌐 **WebDAV & S3‑compatible API.** Use it from any client, mount it anywhere.
- 🧠 **AI semantic search.** Find files by meaning, not just filename.
- 🖥️ **Beautiful, responsive UI.** Mobile‑first, installable as a PWA.

## Why TeleCollection?

| | TeleCollection | Bot‑based tools | Browser‑only clones |
|---|:---:|:---:|:---:|
| Files > 2 GB | ✅ chunking | ❌ ~50 MB cap | ❌ 2 GB |
| End‑to‑end encryption | ✅ opt‑in, seed‑based | ❌ | ⚠️ rare |
| Native desktop + self‑hosted | ✅ both | ❌ | ❌ |
| Virtual drive (incl. Windows) | ✅ | ❌ | ❌ |
| Media library / streaming | ✅ HLS | ❌ | ⚠️ basic |
| Single Go binary | ✅ | varies | ❌ |

## Modes

| Mode | Runs on | Storage | Secrets |
|------|---------|---------|---------|
| **Desktop** | your machine (Wails) | SQLite | OS keychain |
| **Server** | your VPS (Docker) | PostgreSQL | envelope encryption |

## Tech stack

**Go** backend · **Wails** desktop shell · **React + TypeScript + Tailwind** frontend ·
**MTProto** (Telegram client API) · **SQLite / PostgreSQL** · single native binary,
cross‑platform, no cgo where avoidable.

## Getting started (development)

Requires **Go 1.26+**.

```bash
git clone https://github.com/NeritonDias/telecollection.git
cd telecollection

go test ./...                    # run the test suite
go run ./cmd/telecollectiond     # start the daemon on 127.0.0.1:8550
curl http://localhost:8550/health
```

## Roadmap

- [x] Foundation: data layer (SQLite + PostgreSQL), migrations, HTTP API, CI matrix
- [ ] Telegram auth (code / 2FA / QR) + encrypted session at rest
- [ ] File operations: folders, upload / download, transfer engine
- [ ] Chunking beyond 2 GB
- [ ] End‑to‑end encryption (BIP39) + serverless recovery
- [ ] Adaptive streaming (HLS/fMP4) + media library
- [ ] Wails desktop app + responsive UI
- [ ] Self‑hosted server + Docker Compose
- [ ] FUSE mount + WebDAV + S3 API
- [ ] Folder sync, CLI + rclone, AI search

## Contributing

Contributions are welcome. Open an issue to discuss substantial changes first.
The project follows TDD with a cross‑platform CI gate (`go vet`, lint, race tests, build).

## License

[MIT](LICENSE) © Neriton Dias. Open source; donations unlock multi‑account.

---

<sub>Not affiliated with Telegram FZ‑LLC. Use responsibly and in accordance with
Telegram's Terms of Service.</sub>
