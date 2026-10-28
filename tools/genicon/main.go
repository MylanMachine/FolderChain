// genicon draws FolderChain's icons (two linked chain rings on a gradient
// tile) and writes them as .ico files into assets/. Run: go run ./tools/genicon
package main

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
	"path/filepath"
)

type rgb struct{ r, g, b float64 }

type style struct {
	top, bottom rgb
	dot         *rgb
}

var (
	brand = style{top: rgb{0.31, 0.38, 0.98}, bottom: rgb{0.02, 0.71, 0.83}}
	gray  = style{top: rgb{0.55, 0.58, 0.64}, bottom: rgb{0.42, 0.45, 0.50}}
	warn  = style{top: brand.top, bottom: brand.bottom, dot: &rgb{0.98, 0.62, 0.10}}
	sync  = style{top: brand.top, bottom: brand.bottom, dot: &rgb{0.20, 0.85, 0.45}}
)

func sdRoundBox(px, py, hx, hy, r float64) float64 {
	qx := math.Abs(px) - hx + r
	qy := math.Abs(py) - hy + r
	out := math.Hypot(math.Max(qx, 0), math.Max(qy, 0))
	return out + math.Min(math.Max(qx, qy), 0) - r
}

func sdSegment(px, py, ax, ay, bx, by float64) float64 {
	pax, pay := px-ax, py-ay
	bax, bay := bx-ax, by-ay
	h := math.Max(0, math.Min(1, (pax*bax+pay*bay)/(bax*bax+bay*bay)))
	return math.Hypot(pax-bax*h, pay-bay*h)
}

// sample returns premultiplied RGBA for a point in [0,1]².
func sample(x, y float64, st style, small bool) (r, g, b, a float64) {
	margin := 0.03
	if small {
		margin = 0
	}
	if sdRoundBox(x-0.5, y-0.5, 0.5-margin, 0.5-margin, 0.23) > 0 {
		return 0, 0, 0, 0
	}
	t := (x + y) / 2
	r = st.top.r*(1-t) + st.bottom.r*t
	g = st.top.g*(1-t) + st.bottom.g*t
	b = st.top.b*(1-t) + st.bottom.b*t
	a = 1

	// Two interlocking chain links along the diagonal: each link passes over
	// the other at one crossing, with a small gap around the upper one.
	rad, thick, gap := 0.125, 0.07, 0.035
	if small {
		rad, thick, gap = 0.14, 0.095, 0.04
	}
	d1 := math.Abs(sdSegment(x, y, 0.25, 0.25, 0.44, 0.44)-rad) - thick/2
	d2 := math.Abs(sdSegment(x, y, 0.56, 0.56, 0.75, 0.75)-rad) - thick/2
	top, bot := d1, d2
	if x-y < 0 {
		top, bot = d2, d1
	}
	if top < 0 || (bot < 0 && top > gap) {
		r, g, b = 1, 1, 1
	}
	if st.dot != nil {
		dd := math.Hypot(x-0.80, y-0.80)
		if dd < 0.20 {
			r, g, b = 1, 1, 1 // ring
		}
		if dd < 0.15 {
			r, g, b = st.dot.r, st.dot.g, st.dot.b
		}
	}
	return
}

func render(size int, st style) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	const ss = 6
	small := size <= 24
	for py := 0; py < size; py++ {
		for px := 0; px < size; px++ {
			var r, g, b, a float64
			for sy := 0; sy < ss; sy++ {
				for sx := 0; sx < ss; sx++ {
					x := (float64(px) + (float64(sx)+0.5)/ss) / float64(size)
					y := (float64(py) + (float64(sy)+0.5)/ss) / float64(size)
					cr, cg, cb, ca := sample(x, y, st, small)
					r += cr * ca
					g += cg * ca
					b += cb * ca
					a += ca
				}
			}
			n := float64(ss * ss)
			if a > 0 {
				r, g, b = r/a, g/a, b/a
			}
			img.SetNRGBA(px, py, color.NRGBA{uint8(r*255 + .5), uint8(g*255 + .5), uint8(b*255 + .5), uint8(a/n*255 + .5)})
		}
	}
	return img
}

func writeICO(path string, sizes []int, st style) {
	var imgs [][]byte
	for _, s := range sizes {
		var buf bytes.Buffer
		png.Encode(&buf, render(s, st))
		imgs = append(imgs, buf.Bytes())
	}
	var out bytes.Buffer
	binary.Write(&out, binary.LittleEndian, [3]uint16{0, 1, uint16(len(sizes))})
	offset := 6 + 16*len(sizes)
	for i, s := range sizes {
		dim := uint8(s)
		if s >= 256 {
			dim = 0
		}
		out.Write([]byte{dim, dim, 0, 0})
		binary.Write(&out, binary.LittleEndian, [2]uint16{1, 32})
		binary.Write(&out, binary.LittleEndian, [2]uint32{uint32(len(imgs[i])), uint32(offset)})
		offset += len(imgs[i])
	}
	for _, b := range imgs {
		out.Write(b)
	}
	if err := os.WriteFile(path, out.Bytes(), 0o644); err != nil {
		panic(err)
	}
}

func main() {
	dir := "assets"
	os.MkdirAll(dir, 0o755)
	writeICO(filepath.Join(dir, "app.ico"), []int{16, 20, 24, 32, 40, 48, 64, 128, 256}, brand)
	tray := []int{16, 20, 24, 32, 40, 48, 64}
	writeICO(filepath.Join(dir, "tray.ico"), tray, brand)
	writeICO(filepath.Join(dir, "tray-off.ico"), tray, gray)
	writeICO(filepath.Join(dir, "tray-warn.ico"), tray, warn)
	writeICO(filepath.Join(dir, "tray-sync.ico"), tray, sync)
	f, _ := os.Create(filepath.Join(dir, "preview.png"))
	png.Encode(f, render(256, brand))
	f.Close()
}
