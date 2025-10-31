# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

Rcast is a lightweight DLNA/UPnP AV MediaRenderer (DMR) written in Go for macOS. It announces itself on your LAN, accepts external cast/control requests from DLNA control points, and plays media via IINA.

## Development Commands

### Building
```bash
go build -o rcast main.go
```

### Running
```bash
# Normal mode
./rcast

# Debug mode with verbose logging
./rcast --debug
```

### Testing
```bash
# Run tests (if any exist)
go test ./...

# Run tests with verbose output
go test -v ./...
```

## Architecture

### Core Components

- **main.go**: Application entry point - initializes configuration, network, state management, HTTP server, and SSDP discovery
- **internal/config**: Configuration management with environment variable overrides
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

### Session Management

The application implements session ownership with single-controller-at-a-time semantics:
- Controllers are identified by IP address
- Session preemption is configurable via `DMR_ALLOW_PREEMPT`
- Session state includes owner, creation time, and transport state

### IINA Integration

The player component integrates with IINA through multiple methods:
- Prefers `iina-cli` if available (Homebrew or local installation)
- Falls back to direct IINA app execution
- Uses IPC sockets for advanced control (pause, volume, etc.)
- Supports both command-line and AppleScript control methods

### UPnP Services

- **AVTransport**: SetAVTransportURI, Play, Pause, Stop
- **RenderingControl**: SetVolume/GetVolume, SetMute/GetMute
- **SSDP Discovery**: Automatic device announcement and search response

## Development Notes

- The codebase is written in Go 1.25.3
- Uses `github.com/tr1v3r/pkg/log` for structured logging
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