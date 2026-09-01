// mkicns draws winch's app icon and writes a .icns, with nothing but the
// standard library.
//
// The alternative was a build-time dependency on ImageMagick plus
// /usr/bin/iconutil — one heavy, the other impure (a system path a nix build
// is not entitled to assume). .icns is a trivial container: a magic word, a
// length, then typed chunks, and Apple has accepted PNG payloads since 10.7.
// So the whole thing is image/png plus eight headers.
//
//	go run ./cmd/mkicns out.icns
package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"os"
)

// The mark: winch is a sidebar for tmux, so the icon is that — a docked rail
// on the left, panes to the right of it. Rectangles only, which is what keeps
// this legible at 16px and drawable without a vector library.
var (
	ground = color.NRGBA{0x1e, 0x1e, 0x2e, 0xff} // catppuccin base
	rail   = color.NRGBA{0xb4, 0xbe, 0xfe, 0xff} // lavender: the sidebar
	pane   = color.NRGBA{0x58, 0x5b, 0x70, 0xff} // surface2: the rest
	accent = color.NRGBA{0xa6, 0xe3, 0xa1, 0xff} // green: the active pane
)

func rect(img *image.NRGBA, x, y, w, h int, c color.Color) {
	draw.Draw(img, image.Rect(x, y, x+w, y+h), &image.Uniform{c}, image.Point{}, draw.Src)
}

// roundedGround paints the squircle-ish ground macOS expects. Not a true
// squircle — at icon sizes a plain radius is indistinguishable, and a wrong
// curve is more noticeable than a simple one.
func roundedGround(img *image.NRGBA, s int) {
	r := s * 22 / 100
	for y := 0; y < s; y++ {
		for x := 0; x < s; x++ {
			// distance into the nearest corner box
			dx, dy := 0, 0
			if x < r {
				dx = r - x
			} else if x >= s-r {
				dx = x - (s - r - 1)
			}
			if y < r {
				dy = r - y
			} else if y >= s-r {
				dy = y - (s - r - 1)
			}
			if dx*dx+dy*dy > r*r {
				continue
			}
			img.SetNRGBA(x, y, ground)
		}
	}
}

func iconAt(s int) []byte {
	img := image.NewNRGBA(image.Rect(0, 0, s, s))
	roundedGround(img, s)

	// Inset the artwork so the mark never touches the ground's edge.
	pad := s * 20 / 100
	in := s - 2*pad
	if in < 4 {
		in = 4
	}
	gap := s * 4 / 100
	if gap < 1 {
		gap = 1
	}
	railW := in * 30 / 100
	if railW < 1 {
		railW = 1
	}

	// The sidebar: one full-height bar, the thing winch actually is.
	rect(img, pad, pad, railW, in, rail)

	// Panes: a stack to the right of it, the top one active.
	x := pad + railW + gap
	w := in - railW - gap
	rows := 3
	rh := (in - (rows-1)*gap) / rows
	if rh < 1 {
		rh = 1
	}
	for i := 0; i < rows; i++ {
		c := pane
		if i == 0 {
			c = accent
		}
		rect(img, x, pad+i*(rh+gap), w, rh, c)
	}

	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		panic(err)
	}
	return b.Bytes()
}

// The OSTypes Apple reads for PNG payloads, smallest first. ic04/ic05 are the
// 16pt and 32pt slots that make menu-bar and list renderings sharp; the rest
// are the doubling ladder up to 1024.
var slots = []struct {
	typ  string
	size int
}{
	{"icp4", 16}, {"icp5", 32}, {"ic11", 32}, {"ic12", 64},
	{"ic07", 128}, {"ic13", 256}, {"ic08", 256}, {"ic14", 512},
	{"ic09", 512}, {"ic10", 1024},
}

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: mkicns <out.icns>")
		os.Exit(2)
	}
	var body bytes.Buffer
	for _, s := range slots {
		p := iconAt(s.size)
		body.WriteString(s.typ)
		_ = binary.Write(&body, binary.BigEndian, uint32(len(p)+8))
		body.Write(p)
	}
	var out bytes.Buffer
	out.WriteString("icns")
	_ = binary.Write(&out, binary.BigEndian, uint32(body.Len()+8))
	out.Write(body.Bytes())

	if err := os.WriteFile(os.Args[1], out.Bytes(), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "mkicns:", err)
		os.Exit(1)
	}
}
