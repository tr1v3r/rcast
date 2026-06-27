# Rcast - Go DLNA MediaRenderer for macOS (IINA)

A lightweight DLNA/UPnP AV MediaRenderer (DMR) written in Go for macOS.  
It announces itself on your LAN, accepts external cast/control requests from DLNA control points, and plays media via IINA.  
Includes session ownership (single-controller at a time) and optional system volume linkage.

## Features

- SSDP discovery as MediaRenderer
- UPnP services
- AVTransport: SetAVTransportURI, Play, Pause, Stop, Seek, and status queries
  - RenderingControl: SetVolume/GetVolume, SetMute/GetMute
- IINA integration
  - Uses iina-cli if available, otherwise starts the IINA app binary
  - Controls playback through mpv JSON IPC
- Session ownership
  - Single active controller per session
  - Configurable preemption policy
- Optional macOS system volume linkage (via AppleScript, darwin only)
- Per-installation UUID persistence for stable, collision-free discovery identity

## Usage

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

## Configuration

Environment variables include:

- `DMR_HTTP_PORT`: HTTP listen port (default `8200`)
- `DMR_ADVERTISE_IP`: IPv4 address to advertise on multi-homed or VPN-connected Macs
- `DMR_ALLOW_PREEMPT`: allow a new controller to take the active session
- `DMR_LINK_SYSTEM_VOLUME`: mirror renderer volume to macOS system volume
- `DMR_UUID_PATH`: persistent device identity path
- `DMR_IINA_FULLSCREEN`: open IINA fullscreen

## Architecture

- internal/config: configuration and env overrides
- internal/netutil: network helpers (IPv4 selection)
- internal/uuid: device UUID persistence
- internal/state: player and session state (thread-safe)
- internal/player: IINA and macOS system volume control
- internal/upnp: SOAP helpers, service descriptions, AVTransport/RenderingControl handlers
- internal/httpserver: HTTP routes and handlers
- internal/ssdp: SSDP announce and M-SEARCH responder

## License

MIT
