//go:build ignore

// genicon regenerates the menu bar template icon (icon.png): a cast symbol
// (rounded screen outline with an opened corner plus two radiating waves)
// drawn as pure black with alpha over transparency.
//
// Template icons must be black-only pixels; macOS inverts them automatically
// to match light/dark menu bars. Run from anywhere:
//
//	go run internal/gui/assets/genicon.go [output.png]
//
// Without an argument it writes icon.png next to this file.
package main

import (
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
)

const (
	size       = 32
	stroke     = 2.0 // logical stroke width
	halfStroke = stroke / 2
)

func main() {
	out := "icon.png"
	if len(os.Args) > 1 {
		out = os.Args[1]
	} else if _, err := os.Stat("internal/gui/assets"); err == nil {
		// Invoked from the repository root: default to the checked-in asset.
		out = "internal/gui/assets/icon.png"
	}

	img := image.NewRGBA(image.Rect(0, 0, size, size))

	// Screen: rounded rectangle outline with the bottom-left corner opened
	// where the cast waves originate.
	scMinX, scMaxX := 4.0, 28.0
	scMinY, scMaxY := 5.0, 22.0
	r := 3.0 // corner radius

	// Waves: arcs radiating from the opened corner, up and to the right,
	// inside the screen silhouette (Material cast geometry). Screen
	// coordinates have y growing downward, so "up-right" is Atan2(-dy, dx)
	// in (0°, 90°).
	wx, wy := 7.0, 21.0
	waveRadii := []float64{4.5, 9.5}
	angMin := 1.0 * math.Pi / 180
	angMax := 92.0 * math.Pi / 180

	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			// 2x2 supersampling for smoother edges.
			acc := 0.0
			for sy := 0.5; sy < 2; sy++ {
				for sx := 0.5; sx < 2; sx++ {
					px := float64(x) + sx/2
					py := float64(y) + sy/2
					if onScreen(px, py, scMinX, scMaxX, scMinY, scMaxY, r) || onWaves(px, py, wx, wy, waveRadii, angMin, angMax) {
						acc += 0.25
					}
				}
			}
			if acc > 0 {
				img.Set(x, y, color.RGBA{0, 0, 0, byte(math.Round(acc * 255))})
			}
		}
	}

	f, err := os.Create(out)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Printf("wrote %s (%dx%d)\n", out, size, size)
}

// near reports whether v is within halfStroke of t.
func near(v, t float64) bool {
	return math.Abs(v-t) <= halfStroke
}

func onScreen(x, y, minX, maxX, minY, maxY, r float64) bool {
	// Distance to the rounded-rect border, restricted to the border band.
	if x < minX-halfStroke || x > maxX+halfStroke || y < minY-halfStroke || y > maxY+halfStroke {
		return false
	}
	cx := math.Max(minX+r, math.Min(maxX-r, x))
	cy := math.Max(minY+r, math.Min(maxY-r, y))
	dx, dy := x-cx, y-cy
	if !near(math.Hypot(dx, dy), r) {
		return false
	}
	// Open the bottom-left corner: skip where the waves take over.
	if x < minX+r && y > maxY-r {
		return false
	}
	return true
}

func onWaves(x, y, cx, cy float64, radii []float64, angMin, angMax float64) bool {
	dx, dy := x-cx, y-cy
	if dx < -halfStroke {
		return false // waves radiate to the right of the origin only
	}
	d := math.Hypot(dx, dy)
	for _, wr := range radii {
		if !near(d, wr) {
			continue
		}
		ang := math.Atan2(-dy, dx)
		if ang >= angMin && ang <= angMax {
			return true
		}
	}
	return false
}
