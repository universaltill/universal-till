// Command pngdiff is the diagnostic half of guard-docs-shots-determinism.sh
// (ut-docs#2929). When two docs-shots runs disagree on a PNG, a bare "differs
// between run A and run B" says nothing about WHERE on the page the
// nondeterminism lives, and the CI runner's artifacts are not always
// reachable from where the failure is triaged. This prints, straight into the
// job log, how many pixels differ and their bounding box, plus the rows of
// differing pixels grouped into bands, so the offending element can be found
// by mapping the box onto the page (1024x600 at deviceScaleFactor 1).
//
// Usage: pngdiff <a.png> <b.png>   (exit 0 always when both decode; the
// guard decides pass/fail by bytes, this only explains a failure)
package main

import (
	"fmt"
	"image"
	"image/png"
	"os"
)

// Diff summarises the differing pixels of two images.
type Diff struct {
	SizeA, SizeB image.Point
	Pixels       int
	Box          image.Rectangle // empty when Pixels == 0
	Bands        []image.Rectangle
	MaxDelta     uint32 // largest per-channel difference, 0..65535
}

// Compare reports every pixel whose RGBA differs. Bands merge differing
// pixels whose rows are at most bandGap apart, each with its own x-extent,
// so two unrelated changed regions (a chip in the status bar and a tile in
// the grid) show up separately instead of as one page-sized box.
func Compare(a, b image.Image, bandGap int) Diff {
	d := Diff{SizeA: a.Bounds().Size(), SizeB: b.Bounds().Size()}
	r := a.Bounds().Intersect(b.Bounds())
	var cur image.Rectangle
	open := false
	lastRow := 0
	for y := r.Min.Y; y < r.Max.Y; y++ {
		rowMin, rowMax := -1, -1
		for x := r.Min.X; x < r.Max.X; x++ {
			ar, ag, ab, aa := a.At(x, y).RGBA()
			br, bg, bb, ba := b.At(x, y).RGBA()
			if ar == br && ag == bg && ab == bb && aa == ba {
				continue
			}
			for _, p := range [][2]uint32{{ar, br}, {ag, bg}, {ab, bb}, {aa, ba}} {
				delta := p[0] - p[1]
				if p[1] > p[0] {
					delta = p[1] - p[0]
				}
				if delta > d.MaxDelta {
					d.MaxDelta = delta
				}
			}
			d.Pixels++
			if rowMin < 0 {
				rowMin = x
			}
			rowMax = x
		}
		if rowMin < 0 {
			continue
		}
		row := image.Rect(rowMin, y, rowMax+1, y+1)
		d.Box = d.Box.Union(row)
		if open && y-lastRow <= bandGap {
			cur = cur.Union(row)
		} else {
			if open {
				d.Bands = append(d.Bands, cur)
			}
			cur, open = row, true
		}
		lastRow = y
	}
	if open {
		d.Bands = append(d.Bands, cur)
	}
	return d
}

func decode(path string) (image.Image, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return png.Decode(f)
}

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: pngdiff <a.png> <b.png>")
		os.Exit(2)
	}
	a, err := decode(os.Args[1])
	if err != nil {
		fmt.Fprintf(os.Stderr, "pngdiff: %s: %v\n", os.Args[1], err)
		os.Exit(2)
	}
	b, err := decode(os.Args[2])
	if err != nil {
		fmt.Fprintf(os.Stderr, "pngdiff: %s: %v\n", os.Args[2], err)
		os.Exit(2)
	}
	d := Compare(a, b, 8)
	if d.SizeA != d.SizeB {
		fmt.Printf("    size differs: A %dx%d, B %dx%d (compared the overlap)\n", d.SizeA.X, d.SizeA.Y, d.SizeB.X, d.SizeB.Y)
	}
	if d.Pixels == 0 {
		if d.SizeA == d.SizeB {
			fmt.Println("    pixels identical (bytes differ only in PNG encoding/metadata)")
		} else {
			fmt.Println("    overlap identical")
		}
		return
	}
	fmt.Printf("    %d pixel(s) differ, max channel delta %d/255, box x=%d..%d y=%d..%d\n",
		d.Pixels, d.MaxDelta>>8, d.Box.Min.X, d.Box.Max.X-1, d.Box.Min.Y, d.Box.Max.Y-1)
	for i, band := range d.Bands {
		if i == 10 {
			fmt.Printf("    … %d more band(s)\n", len(d.Bands)-i)
			break
		}
		fmt.Printf("    band x=%d..%d y=%d..%d\n", band.Min.X, band.Max.X-1, band.Min.Y, band.Max.Y-1)
	}
}
