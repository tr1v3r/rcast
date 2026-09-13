# Menu bar icon asset

`icon.png` is the macOS menu bar template icon shown by `rcast gui`
(32×32 RGBA, pure black pixels with alpha — no color, no shading). macOS
inverts template icons automatically to match light and dark menu bars.

It is embedded into the binary by `internal/gui/gui_darwin.go`
(`//go:embed assets/icon.png`) and applied with `systray.SetTemplateIcon`.

## Regenerating

The icon is drawn programmatically (rounded screen outline with an opened
corner plus two radiating waves — a cast symbol):

```sh
# from the repository root; overwrites the checked-in asset
go run internal/gui/assets/genicon.go

# or write elsewhere to preview
go run internal/gui/assets/genicon.go /tmp/icon.png
```

The generator is deterministic (fixed geometry, 2×2 supersampling); running
it without parameter changes reproduces the committed file byte-for-byte.
To restyle the glyph, adjust the geometry constants at the top of
`genicon.go` (screen bounds, corner radius, wave radii and angle span) and
keep the black-plus-alpha template constraint.
