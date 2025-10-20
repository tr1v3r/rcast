# Rcast - Go DLNA MediaRenderer for macOS (IINA)

A lightweight DLNA/UPnP AV MediaRenderer (DMR) written in Go for macOS.  
It announces itself on your LAN, accepts external cast/control requests from DLNA control points, and plays media via IINA.  
Includes session ownership (single-controller at a time) and optional system volume linkage.

## Features

- SSDP discovery as MediaRenderer
- UPnP services
  - AVTransport: SetAVTransportURI, Play, Pause, Stop
  - RenderingControl: SetVolume/GetVolume, SetMute/GetMute
- IINA integration
  - Uses iina-cli if available, otherwise falls back to IINA app or AppleScript
- Session ownership
  - Single active controller per session
  - Configurable preemption policy
- Optional macOS system volume linkage (via AppleScript, darwin only)
- Static UUID persistence for stable discovery identity

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
