# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

Rcast is a lightweight DLNA/UPnP AV MediaRenderer (DMR) written in Go for macOS. It announces itself on the LAN, accepts external cast/control requests from DLNA control points, and plays media via IINA. The default command is headless; `rcast gui` adds an optional native menu bar front end over the same runtime and player state.

## Development Commands

### Building
```bash
go build -o rcast .                 # plain build
make build                          # canonical versioned binary in output/bin
make build-dev                      # unstripped dev binary with debug info
make build-gui                      # cgo-enabled GUI binary: output/bin/rcast-gui
```

`make build-gui` sets `CGO_ENABLED=1`. The systray dependency carries its own Cocoa framework linker directives, so no extra linker flags are required.

### Running
```bash
# Headless mode (the default)
./rcast

# Headless debug/fullscreen flags
./rcast --debug
./rcast --fullscreen               # alias: --fs

# Native macOS menu bar mode
./rcast gui
./rcast gui --debug --fullscreen   # alias: --fs
./rcast --debug --fullscreen gui   # root flags are also honored

# Via Make
make run
make run-dev                        # development binary with --debug
make run-gui                        # cgo GUI build, then `rcast-gui gui`
```

### Testing
```bash
# Run tests
go test ./...

# Run tests with verbose output
go test -v ./...

# Verify the headless/stub build path
CGO_ENABLED=0 go build ./...
```

### Code Quality (via Makefile)
```bash
make test-coverage   # HTML coverage report
make race-test       # tests with the race detector
make lint            # golangci-lint run
make vet             # go vet ./...
make fmt             # goimports-reviser -format ./...
make fmt-check       # verify formatting without modifying files
make dev             # full workflow: tidy fmt vet lint race-test build
make ci              # non-mutating CI checks + coverage + release build
```

## Architecture

### Core Components

- **main.go**: CLI command tree. No subcommand runs headless; `gui` selects the menu bar front end. Signal handling remains at this command layer.
- **internal/app**: Reusable server lifecycle shared by headless and GUI modes. `Start` returns a `Runtime` with `State`, `Stop`, and `Wait`; `Run` is the blocking convenience entry point. The runtime owns HTTP, SSDP, player state, and graceful shutdown, but installs no signal handlers itself.
- **internal/gui**: Portable GUI controller plus the platform view. It derives menu models from state snapshots, serializes local player controls with UPnP commands, persists settings, and safely restarts the server for settings that handlers capture at startup.
- **internal/config**: Configuration defaults, `settings.json` loading/atomic saving, and environment overrides.
- **internal/monitoring**: In-process metrics for HTTP, player, and UPnP activity (singleton `Metrics`).
- **internal/state**: Thread-safe player/session state plus immutable `Snapshot` values and change subscriptions.
- **internal/player**: IINA and macOS system volume control integration.
- **internal/upnp**: SOAP helpers, service descriptions, AVTransport/RenderingControl handlers.
- **internal/httpserver**: HTTP routes and handlers for UPnP services.
- **internal/ssdp**: SSDP announce and M-SEARCH responder for device discovery.
- **internal/uuid**: Device UUID persistence for stable discovery identity.
- **internal/netutil**: Network helpers for IPv4 selection.

### GUI Implementation

- The macOS view uses `fyne.io/systray` v1.12.2. It was selected for native submenus, checkable items, template icons, goroutine-safe updates, and active maintenance.
- `internal/gui/gui_darwin.go` is built only for `darwin && cgo`. It embeds `internal/gui/assets/icon.png` as a macOS template icon.
- `internal/gui/gui_stub.go` covers non-darwin and `CGO_ENABLED=0`. It returns `gui.ErrUnsupported`, which lets every build retain headless functionality without a native GUI toolchain.
- The systray event loop must occupy the main thread. Server work runs through `app.Runtime`; cleanup happens after `systray.Run` returns because fyne systray's programmatic `Quit` path does not invoke its `onExit` callback.
- The menu displays transport status, media title, volume, and session owner. Controls are Play/Pause/Resume, Stop, and Mute; the Settings submenu contains IINA fullscreen, system-volume linkage, session preemption, and debug logging.
- The icon generator is `internal/gui/assets/genicon.go` (`//go:build ignore`). Run `go run internal/gui/assets/genicon.go` from the repository root to reproduce the checked-in PNG.

### State Observation

`PlayerState.Snapshot()` returns a consistent immutable view containing transport state, DIDL-derived media title, URI, volume, mute state, and session owner.

`PlayerState.Subscribe(func(state.Snapshot))` asynchronously delivers an initial snapshot and then every change. Each subscriber has a serial worker, callbacks run without the player-state lock held, and the returned cancellation function is idempotent. GUI callbacks may therefore read or mutate state without deadlocking. Remember to cancel subscriptions before destroying their view objects.

### Configuration

Configuration precedence is:

1. Built-in defaults
2. `settings.json`
3. Environment variables
4. Explicit CLI flags where supported

The settings file defaults to `~/.local/rcast/settings.json`; `DMR_SETTINGS_PATH` changes it. Missing or malformed files are silently ignored. `config.Save` writes the four GUI booleans through a same-directory temporary file followed by `rename`, and creates the parent directory when needed.

Persisted JSON keys:

- `iinaFullscreen`
- `linkSystemVolume`
- `allowPreempt`
- `debugLog`

Environment variables:

- `DMR_UUID_PATH`: UUID persistence path (default `~/.local/rcast/dmr_uuid.txt`)
- `DMR_SETTINGS_PATH`: settings path (default `~/.local/rcast/settings.json`)
- `DMR_ALLOW_PREEMPT`: allow session preemption (default `true`)
- `DMR_LINK_SYSTEM_VOLUME`: link renderer volume to macOS system volume (default `false`)
- `DMR_HTTP_PORT`: HTTP server port (default `8200`)
- `DMR_ADVERTISE_IP`: IPv4 address to advertise when automatic selection is unsuitable
- `DMR_IINA_FULLSCREEN`: open IINA in fullscreen (default `false`; CLI: `--fullscreen`/`--fs`)
- `DMR_DEBUG_LOG`: initial GUI debug-logging setting (default `false`; CLI: `--debug`)

The server-related settings in the file are shared by GUI and headless launches. Headless mode reads them at process startup and still uses `--debug` for debug logging. GUI setting application is more dynamic:

- Fullscreen is read by the live player factory when the next player session is created; it does not interrupt an existing IINA instance.
- System-volume linkage and session preemption require handler reconstruction. The GUI restarts the server immediately while idle or defers that restart until the current playback stops.
- Debug logging changes immediately through `log.SetLevel`.

### Session Management

The application implements session ownership with single-controller-at-a-time semantics:
- Controllers are identified by IP address.
- Session preemption is configurable through settings or `DMR_ALLOW_PREEMPT`.
- Session state includes owner, creation time, and transport state.

### IINA Integration

The player component integrates with IINA through multiple methods:
- Prefers `iina-cli` if available (Homebrew or local installation).
- Falls back to direct IINA app execution.
- Controls playback through **mpv JSON IPC** over a Unix socket (`/tmp/rcast_iina-ipc-sock_*`); IINA is mpv-based, so it speaks the mpv IPC protocol. IPC types live in `internal/player/mpv.go`.
- AppleScript is used **only** for macOS system volume linkage (`internal/player/system_volume_darwin.go`), not playback control.

### UPnP Services

- **AVTransport**: SetAVTransportURI, Play, Pause, Stop, Seek, plus Get*Info queries (PositionInfo, TransportInfo, MediaInfo, DeviceCapabilities)
- **RenderingControl**: SetVolume/GetVolume, SetMute/GetMute
- **ConnectionManager**: GetProtocolInfo
- **Eventing (GENA)**: Not currently implemented; event endpoints return HTTP 501 instead of issuing unusable subscriptions
- **SSDP Discovery**: Automatic device announcement and search response

## Development Notes

- The module targets Go 1.27.
- Key dependencies: `fyne.io/systray` (menu bar), `github.com/tr1v3r/pkg/log`, `github.com/urfave/cli/v3`, and `github.com/google/uuid`.
- Thread-safe state management uses `sync.RWMutex`; mutating local GUI and remote UPnP commands also share `PlayerState.Serialize` ordering.
- macOS-specific GUI and system-volume features are isolated behind build-tagged files.
- UUID persistence ensures stable discovery identity across restarts.
- The HTTP server listens on configurable port 8200 by default.
- SSDP discovery uses multicast address 239.255.255.250:1900.

## Common Development Tasks

### Adding New UPnP Actions
1. Add the handler in `internal/upnp/avtransport.go` or `internal/upnp/renderingcontrol.go`.
2. Update SOAP action parsing in `internal/upnp/soap.go`.
3. Add corresponding state management in `internal/state/state.go`.
4. Notify snapshot subscribers when user-visible state changes.

### Modifying Player Integration
1. Update `internal/player/iina.go` for IINA-specific changes.
2. Modify `internal/player/player.go` for interface changes.
3. Update `internal/player/system_volume_darwin.go` for macOS volume integration.
4. Mirror user-facing control semantics in `internal/gui/gui.go` when appropriate.

### Modifying the GUI
1. Keep menu derivation pure and testable in `internal/gui/menu.go`.
2. Keep lifecycle, control, and settings logic in the portable `internal/gui/gui.go` controller.
3. Keep native systray calls in `gui_darwin.go` and preserve `gui_stub.go` build coverage.
4. Run both normal and `CGO_ENABLED=0` builds; native menu behavior requires a real macOS smoke test.

### Configuration Changes
1. Add fields/defaults and environment handling in `internal/config/config.go`.
2. Update the persisted schema and atomic writer in `internal/config/persist.go` for GUI-controlled values.
3. Preserve the defaults < settings file < environment priority.
4. Update both README configuration documentation and GUI toggle application semantics.
