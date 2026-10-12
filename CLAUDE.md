# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Fork layout

This is a personal fork of [rommapp/grout](https://github.com/rommapp/grout), a Go/SDL2 RomM client for retro handhelds.

- `main` tracks upstream exactly. Never commit fork work to it; it only receives upstream updates.
- `argosy-save-sync` is the working branch. It carries the fork's features and is kept current by merging `main` into it (`git diff main...argosy-save-sync` shows the whole fork delta).

The fork's purpose is syncing saves both ways between Grout (on ROCKNIX) and the Argosy Android launcher (external RetroArch) through RomM, using the same on-server format Argosy uses.

### What the fork adds (argosy-save-sync only)

- **Separate install identity.** `version.AppName` is "Grout Save Sync". The ROCKNIX package is `Grout-Save-Sync.sh` + `Grout-Save-Sync/` (see `taskfiles/package.yml`, `scripts/ROCKNIX/`, `.github/workflows/release.yml`), so it installs beside upstream Grout. Self-update is hard-disabled (`selfUpdate = false` in `update/updater.go`) so an upstream release can't overwrite the fork.
- **Clock-file save units (.srm + .rtc).**
  - Scanning: `saves/companions.go` pairs `<stem>.rtc` with `<stem>.srm`. This only happens when `Firmware.PairsClockFiles()` is true, which is ROCKNIX only (`cfw/firmware.go`).
  - Upload: the pair goes up as a flat `<stem>.zip` (`saves/zip_save.go` `ZipSaveUnit`), matching Argosy.
  - Download: `saves/payload.go` detects zip payloads, classifies entries by role (`.srm`/`.sav`/`save.sram` vs `.rtc`), and unpacks them. It also refuses to write a save over a ROM or to write zip bytes into a raw save.
- **Clock-free identity hash.** `save_sync_state.identity_hash` (cache schema v15) hashes a save while ignoring clock files, so a clock-only change counts as "no change". `hashing/` reproduces RomM's hashing for loose files and in-memory zips, and the server's content hash is recorded after each upload.
- **Delete from device.** Each download records what it wrote:
  - `download/record.go` and `catalog/download_record.go` write to the `downloaded_files` table (schema v16).
  - `catalog/delete.go` removes exactly those files and keeps the saves.
  - `gamelist/remove.go` drops the gamelist entry.
  - The UI is `ui/delete_game.go`, reached from Game Options.
- **NDS save extension override.** `Config.SaveExtensionOverrides` (`settings/config.go`) sets the extension a downloaded save gets when the ROM has no local save yet.
- User-facing behaviour is documented in `docs/usage/save-sync.md`, `docs/usage/settings.md` and `docs/usage/guide.md`. Update these when behaviour changes.

**Merge hazard:** cache migrations are numbered sequentially (`schemaVersion` and `migrateIfNeeded` in `cache/schema.go`). Upstream was at v14 when the fork added v15/v16. If upstream adds its own v15, renumber the fork's migrations above it when merging `main`. Each migration must be idempotent (see `addColumnIfMissing`).

## Code navigation

Use the `LSP` tool, which is backed by gopls through the `gopls-lsp` plugin, to navigate Go code instead of grepping.

- Use `findReferences` and `incomingCalls` to see who uses a symbol before changing it.
- Use `goToDefinition` to follow a symbol to its definition.
- Use `goToImplementation` to find implementations of an interface.
- Use `workspaceSymbol` to find a symbol by name.

The save-sync, cache and CFW code is spread across many packages, so call sites are easy to miss with text search. Fall back to Grep only for non-Go files, string literals, or when gopls returns nothing.

## Commands

Local dev needs Go (version from `go.mod`), SDL2 dev libraries (`sdl2`, `sdl2_ttf`, `sdl2_gfx`, `sdl2_image`) and [Task](https://taskfile.dev) (on Arch the `go-task` package installs it as `go-task`, not `task`). Most packages link SDL through the gabagool toolkit via cgo, so even `go test` needs the SDL headers.

```sh
go build ./...
go test ./... -race -cover                     # what CI runs
go test ./saves -run TestName -v               # single test
task code:lint                                 # go fmt, go vet, staticcheck
task code:archcheck                            # package layering check (also run in CI)
task code:check-platform-docs                  # docs/platforms/*.md must match platforms.json
task code:gen-platform-docs                    # regenerate them
task i18n                                      # extract strings to resources/locales/active.en.toml and diff other locales
task code:hooks-setup                          # installs .githooks (strips local go.mod replace directives on commit)
```

CI sets `GOWORK=off` and fails on any file that isn't gofmt-formatted (`vendored/` excluded). Docs mention a `go.work` for local gabagool development. It is not in this checkout; `LOCAL=true` builds expect one alongside a sibling `../gabagool`.

**Running locally:** copy `.env-dev` to `.env` and set `ENVIRONMENT=DEV`, `CFW=<NEXTUI|MUOS|ROCKNIX|...>` and `BASE_PATH=<dir mimicking that CFW's SD card layout>`. Then run `app/grout.go` with that env.

**Device builds** cross-compile in Docker. `task all` builds and packages every CFW, and `task deploy:*` pushes over ADB, but this fork only ships ROCKNIX (below).

### Building the ROCKNIX package locally

ROCKNIX is the fork's only target. Deployment is manual: the package is copied to the device by hand, and there is no deploy task for it.

**Host requirements** (the dev host is x86_64 Arch/CachyOS; the device is arm64):

- Docker with **buildx**, i.e. the `docker-buildx` package. The build runs `docker buildx build --load`.
- **arm64 emulation**, because the image is built for `linux/arm64` on an x86_64 host. Use the `qemu-user-static` and `qemu-user-static-binfmt` packages, or run `docker run --privileged --rm tonistiigi/binfmt --install arm64`. Check that `/proc/sys/fs/binfmt_misc/qemu-aarch64` exists.
- Network access to pull the base image `ghcr.io/brandonkowalski/quasimodo:latest`. It provides the Go and arm64 SDL2 toolchain, so host SDL/Go aren't needed for this build.

**Build and package:** always use `./build_rocknix.sh`, not the Task targets.

```sh
./build_rocknix.sh                     # → dist/ROCKNIX/Grout-Save-Sync.sh + dist/ROCKNIX/Grout-Save-Sync/
./build_rocknix.sh --version 5.3.1.4   # override the version (default: pak.json .version)
./build_rocknix.sh --no-cache          # rebuild the Docker image from scratch
```

- The script checks the prerequisites above, cross-compiles in Docker, extracts `grout` and `libSDL2_gfx` into `build64/`, and lays out `dist/ROCKNIX/` like the `rocknix` task in `taskfiles/package.yml`. Task is not needed.
- It prints one line per Docker build step; the full log is in `build64.log`, and its last 40 lines are shown on failure.
- Rebuild after every code change before the user tests on the device. `dist/` is not rebuilt by `go build`/`go test`, so a stale package silently lacks the change.

**Manual deploy:**

1. Copy `Grout-Save-Sync.sh` and the `Grout-Save-Sync/` folder into the device's Ports directory. That is `/games-internal/roms/ports` with one SD card, or `/games-external/roms/ports` with two.
2. For an update, copy over the existing folder; don't delete it first. Grout keeps its state in its working directory, `Grout-Save-Sync/`: `config.json` (login and folder mappings), `.cache/grout.db` (cache plus save-sync state), `save_slots.json` and `logs/`. Wiping these means re-pairing the device and losing sync state.
3. On first install, refresh the frontend: Start → Game Settings → Update Gamelist. It then appears under Ports.
4. The launch script sets `CFW=ROCKNIX` and `LD_LIBRARY_PATH` to the bundled `lib/`, so it must stay beside the folder under the same name.

**Save sync dry run** (offline scenarios with synthetic inputs, defined in `saves/dryrun.go`):

```sh
go run -tags dryrun ./tools/save-sync-dry-run -scenario all   # or slot-switch, nextui-keep, nextui-retroarch, dreamcast-vmu, clock-tick
```

Without `-scenario` it does a live dry run against the host in `config.json`.

**E2E** (`test/e2e`, build tag `e2e`, Docker only): `scripts/e2e.sh [-run TestName]`. It runs the real binary against a containerised RomM under Xvfb.

## Architecture

Full layout notes are in `docs/contributing/development.md`. Screen navigation diagrams are in `docs/contributing/state-machine.md`.

- **Entry / FSM:** `app/` is the composition root (`package main`). `app/setup.go` handles login/pairing before the router starts. `app/router.go` builds a gabagool `router.Router`. Screen IDs are in `app/screens.go` and transitions between `ui/` screens are in `app/transitions.go`. Shared runtime state is `AppState` in `app/state.go`. New screens need an ID, a router registration, and transition wiring.
- **Layering is enforced** by `tools/archcheck`. From the bottom up the layers are pkg → domain (`library`, `settings`) → platform (`cfw`, `gamelist`) → infra (`romm`, `cache`) → service (`catalog`, `saves`, `download`) → ui → cmd (`app`). Only `ui`/`app` may import gabagool, and only `app` may hold package-level mutable state. Known violations live in `tools/archcheck/allow.txt`. The check fails on new violations *and* on entries that were fixed but not removed, so that list may only shrink.
- **CFW abstraction:** `cfw.CFW` is chosen by the `CFW` env var (set by each `scripts/<CFW>/` launch script). Each firmware has a subpackage plus a `Firmware` descriptor in `cfw/firmware.go` (save layout, ROM dirs, per-firmware flags such as `pairsClockFiles`). `cfw/<name>/data/platforms.json` maps RomM platform slugs to on-device folders and also generates `docs/platforms/`.
- **Cache:** `cache/` is a SQLite store (modernc, no cgo) of the RomM library, artwork, save-sync state and download records. Schema and migrations are in `cache/schema.go`.
- **RomM client:** `romm/` is hand-written, not OpenAPI-generated. Grout's version tracks RomM's: the first three components are the required RomM version and the fourth is Grout's patch.
- **Save sync:** `saves/flow.go` is the core.
  1. Scan local saves per platform and match them to cached ROMs (`saveLookupKeys`, `pspdb` for PSP).
  2. Negotiate with the server by comparing content hashes.
  3. Resolve conflicts (`conflicts.go`) and slots (`slots.go`).
  4. Upload or download, backing up local saves to `.backup/` with retention counted per file type (`history.go`).

  Sync state per `(device_id, rom_id, file_name)` is kept in `save_sync_state` so unchanged saves aren't re-uploaded.
- **i18n:** every user-visible string goes through go-i18n. New message IDs must be added to `resources/locales/active.en.toml` (`task i18n:extract`).
