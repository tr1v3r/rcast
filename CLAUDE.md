# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

Rcast is a lightweight DLNA/UPnP AV MediaRenderer (DMR) written in Go for macOS. It announces itself on your LAN, accepts external cast/control requests from DLNA control points, and plays media via IINA.

## Development Commands

### Building
```bash
go build -o rcast main.go          # plain build
make build                          # canonical: versioned binary to output/bin (stripped, ldflags-injected)
make build-dev                      # unstripped dev binary with debug info
```

### Running
```bash
# Normal mode
./rcast

# Debug mode with verbose logging
./rcast --debug

# Open IINA fullscreen (also: --fs)
./rcast --fullscreen

# Via Make: build + run, or dev build + run with --debug
make run
make run-dev
```

### Testing
```bash
# Run tests
go test ./...

# Run tests with verbose output
go test -v ./...
```

### Code Quality (via Makefile)
```bash
make test-coverage   # race + HTML coverage report
make lint            # golangci-lint run
make vet             # go vet ./...
make fmt             # goimports-reviser -format ./...
make dev             # full workflow: tidy fmt vet lint test build
```

## Architecture

### Core Components

- **main.go**: Application entry point - initializes configuration, network, state management, HTTP server, and SSDP discovery
- **internal/config**: Configuration management with environment variable overrides
- **internal/monitoring**: In-process metrics for HTTP, player, and UPnP activity (singleton `Metrics`)
- **internal/state**: Thread-safe player and session state management
- **internal/player**: IINA and macOS system volume control integration
- **internal/upnp**: SOAP helpers, service descriptions, AVTransport/RenderingControl handlers
- **internal/httpserver**: HTTP routes and handlers for UPnP services
- **internal/ssdp**: SSDP announce and M-SEARCH responder for device discovery
- **internal/uuid**: Device UUID persistence for stable discovery identity
- **internal/netutil**: Network helpers for IPv4 selection

### Key Configuration

Environment variables:
- `DMR_UUID_PATH`: Path for UUID persistence (default: `~/.local/rcast/dmr_uuid.txt`)
- `DMR_ALLOW_PREEMPT`: Allow session preemption (default: `true`)
- `DMR_LINK_SYSTEM_VOLUME`: Link to macOS system volume (default: `false`)
- `DMR_HTTP_PORT`: HTTP server port (default: `8200`)
- `DMR_IINA_FULLSCREEN`: Open IINA in fullscreen (default: `false`; also settable via `--fullscreen`/`--fs`)

### Session Management

The application implements session ownership with single-controller-at-a-time semantics:
- Controllers are identified by IP address
- Session preemption is configurable via `DMR_ALLOW_PREEMPT`
- Session state includes owner, creation time, and transport state

### IINA Integration

The player component integrates with IINA through multiple methods:
- Prefers `iina-cli` if available (Homebrew or local installation)
- Falls back to direct IINA app execution
- Controls playback via **mpv JSON IPC** over a Unix socket (`/tmp/rcast_iina-ipc-sock_*`); IINA is mpv-based, so it speaks the mpv IPC protocol. IPC types live in `internal/player/mpv.go`.
- AppleScript is used **only** for macOS system volume linkage (`internal/player/system_volume_darwin.go`), not playback control.

### UPnP Services

- **AVTransport**: SetAVTransportURI, Play, Pause, Stop, Seek, plus Get*Info queries (PositionInfo, TransportInfo, MediaInfo, DeviceCapabilities)
- **RenderingControl**: SetVolume/GetVolume, SetMute/GetMute
- **ConnectionManager**: GetProtocolInfo
- **Eventing (GENA)**: SUBSCRIBE/UNSUBSCRIBE handlers on `/upnp/event/{avtransport,renderingcontrol,connectionmanager}`
- **SSDP Discovery**: Automatic device announcement and search response

## Development Notes

- The codebase is written in Go 1.25.3
- Key deps: `github.com/tr1v3r/pkg/log` (logging), `github.com/urfave/cli/v3` (CLI flags), `github.com/google/uuid`
- Thread-safe state management with sync.RWMutex
- macOS-specific features (system volume control) are isolated in Darwin-specific files
- UUID persistence ensures stable device identity across restarts
- HTTP server runs on configurable port (default: 8200)
- SSDP discovery uses multicast address 239.255.255.250:1900

## Common Development Tasks

### Adding New UPnP Actions
1. Add handler in `internal/upnp/avtransport.go` or `internal/upnp/renderingcontrol.go`
2. Update SOAP action parsing in `internal/upnp/soap.go`
3. Add corresponding state management in `internal/state/state.go`

### Modifying Player Integration
1. Update `internal/player/iina.go` for IINA-specific changes
2. Modify `internal/player/player.go` interface for general player changes
3. Update `internal/player/system_volume_darwin.go` for macOS volume integration

### Configuration Changes
1. Modify `internal/config/config.go` to add new configuration options
2. Update environment variable handling in the `envVar` function
3. Add default values and validation as needed