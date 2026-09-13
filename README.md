# Rcast - Go DLNA MediaRenderer for macOS (IINA)

A lightweight DLNA/UPnP AV MediaRenderer (DMR) written in Go for macOS.  
It announces itself on your LAN, accepts external cast/control requests from DLNA control points, and plays media via IINA.  
Includes session ownership (single-controller at a time), an optional macOS menu bar interface, and optional system volume linkage.

## Features

- SSDP discovery as MediaRenderer
- UPnP services
  - AVTransport: SetAVTransportURI, Play, Pause, Stop, Seek, and status queries
  - RenderingControl: SetVolume/GetVolume, SetMute/GetMute
- IINA integration
  - Uses iina-cli if available, otherwise starts the IINA app binary
  - Controls playback through mpv JSON IPC
  - Normalizes the eight-step Douyin iOS volume control to IINA's full range
- Session ownership
  - Single active controller per session
  - Configurable preemption policy
- Optional macOS system volume linkage (via AppleScript, darwin only)
- Optional macOS menu bar GUI for status, playback controls, and settings
- Per-installation UUID persistence for stable, collision-free discovery identity

## Headless CLI

Running `rcast` without a subcommand keeps the original headless behavior:

```bash
# Run with default settings
rcast

# Enable debug logging
rcast --debug

# Open IINA in fullscreen mode
rcast --fullscreen
# or use the short form
rcast --fs

# Show help
rcast --help
```

## macOS Menu Bar GUI

Start the same renderer runtime with a native menu bar interface:

```bash
rcast gui

# Flags may follow the subcommand
rcast gui --debug --fullscreen
rcast gui --fs

# Root-level flag spelling is also supported
rcast --debug --fullscreen gui

# Build and run the GUI development target
make run-gui
```

The menu shows the renderer state, current media title, volume, and active controller. It also provides:

- Play, Pause/Resume, and Stop controls
- A Mute checkbox
- A **Settings** submenu for IINA fullscreen, macOS system-volume linkage, session preemption, and debug logging
- Version information and **Quit**

> **Screenshot placeholder:** macOS menu bar dropdown screenshot to be added.

The GUI is an optional front end over the same server runtime and player state used by headless mode. `rcast` with no arguments still runs headless; installing the GUI dependency does not change that default. Quitting from the menu or sending `SIGINT`/`SIGTERM` performs the same graceful server shutdown.

### Platform requirements

- The native menu bar is supported on macOS with cgo enabled.
- `make build-gui` explicitly builds with `CGO_ENABLED=1` and writes `output/bin/rcast-gui`.
- A working macOS cgo toolchain (normally the Xcode Command Line Tools) is required.
- Non-macOS or `CGO_ENABLED=0` builds retain full headless support. On those builds, `rcast gui` reports that the GUI is unsupported and exits non-zero.

### GUI setting behavior

GUI toggles are saved atomically to `~/.local/rcast/settings.json` by default; the server-related values are also used by later headless launches. The path can be changed with `DMR_SETTINGS_PATH`. In GUI mode:

- **Open IINA in Fullscreen** applies to the next playback session without restarting the server; an already-running IINA player is unchanged.
- **Link System Volume** and **Allow Session Preemption** are applied automatically by restarting the server while idle. If media is playing, the restart is deferred until playback stops.
- **Debug Logging** applies immediately.

Headless mode reads the shared server settings when the process starts; use `--debug` for headless debug logging. Command-line flags and environment variables can still override the loaded values.

## Configuration

Configuration precedence is:

1. Built-in defaults
2. `settings.json`
3. Environment variables
4. Explicit command-line flags, where available

A missing or damaged settings file is ignored. The GUI recreates it when a setting is changed.

Environment variables include:

- `DMR_HTTP_PORT`: HTTP listen port (default `8200`)
- `DMR_ADVERTISE_IP`: IPv4 address to advertise on multi-homed or VPN-connected Macs
- `DMR_ALLOW_PREEMPT`: allow a new controller to take the active session (default `true`)
- `DMR_LINK_SYSTEM_VOLUME`: mirror renderer volume to macOS system volume (default `false`)
- `DMR_UUID_PATH`: persistent device identity path (default `~/.local/rcast/dmr_uuid.txt`)
- `DMR_IINA_FULLSCREEN`: open IINA fullscreen (default `false`)
- `DMR_DEBUG_LOG`: initial Debug Logging setting for GUI mode (default `false`)
- `DMR_SETTINGS_PATH`: settings file path (default `~/.local/rcast/settings.json`)

The settings file contains the GUI-controlled booleans `iinaFullscreen`, `linkSystemVolume`, `allowPreempt`, and `debugLog`.

## Architecture

- `internal/app`: reusable HTTP, SSDP, player-state, and graceful-shutdown runtime shared by CLI modes
- `internal/config`: defaults, settings-file persistence, and environment overrides
- `internal/gui`: macOS menu bar controller and systray view, plus the unsupported-platform stub
- `internal/netutil`: network helpers (IPv4 selection)
- `internal/uuid`: device UUID persistence
- `internal/state`: thread-safe player/session state, immutable snapshots, and change subscriptions
- `internal/player`: IINA and macOS system volume control
- `internal/upnp`: SOAP helpers, service descriptions, AVTransport/RenderingControl handlers
- `internal/httpserver`: HTTP routes and handlers
- `internal/ssdp`: SSDP announce and M-SEARCH responder

## License

MIT
