package badge

import (
	"bytes"
	"compress/gzip"
	"embed"
	"encoding/binary"
	"io"
	"sort"
)

// The width tables come from anafanafo (MIT License, Copyright (c) 2018 Metabolize LLC), made
// into data/ by generate/generate.py.
//
//go:embed data/*.bin.gz
var data embed.FS

type charRange struct {
	lower, upper rune
	width        float64
}

// font measures text as anafanafo does: the sum of each code point's width, with control
// characters as zero, and characters it has no width for as wide as "m".
type font struct {
	ranges  []charRange
	emWidth float64
}

var (
	verdana10       = loadFont("verdana-10px-normal")
	verdana10Bold   = loadFont("verdana-10px-bold")
	verdana11       = loadFont("verdana-11px-normal")
	helvetica11Bold = loadFont("helvetica-11px-bold")
)

func loadFont(name string) *font {
	compressed, err := data.ReadFile("data/" + name + ".bin.gz")
	if err != nil {
		panic(err) // Embedded at build time.
	}
	reader, err := gzip.NewReader(bytes.NewReader(compressed))
	if err != nil {
		panic(err)
	}
	raw, err := io.ReadAll(reader)
	if err != nil {
		panic(err)
	}
	f := &font{}
	buf := bytes.NewReader(raw)
	previous := int64(-1)
	for buf.Len() > 0 {
		gap, err1 := binary.ReadUvarint(buf)
		length, err2 := binary.ReadUvarint(buf)
		hundredths, err3 := binary.ReadUvarint(buf)
		if err1 != nil || err2 != nil || err3 != nil {
			panic("badge: corrupt width table " + name)
		}
		lower := previous + 1 + int64(gap)
		upper := lower + int64(length)
		f.ranges = append(f.ranges, charRange{lower: rune(lower), upper: rune(upper), width: float64(hundredths) / 100})
		previous = upper
	}
	f.emWidth = f.widthOf("m")
	return f
}

func (f *font) charWidth(r rune) (float64, bool) {
	if r <= 31 || r == 127 {
		return 0, true
	}
	i := sort.Search(len(f.ranges), func(i int) bool { return f.ranges[i].upper >= r })
	if i < len(f.ranges) && f.ranges[i].lower <= r {
		return f.ranges[i].width, true
	}
	return 0, false
}

func (f *font) widthOf(text string) float64 {
	total := 0.0
	for _, r := range text {
		width, ok := f.charWidth(r)
		if !ok {
			width = f.emWidth
		}
		total += width
	}
	return total
}
