package badge

import (
	"math"
	"regexp"
	"strconv"
	"strings"
)

// The colour names badge-maker has, and its aliases for them.
var (
	namedColors = map[string]string{
		"brightgreen": "#4c1",
		"green":       "#97ca00",
		"yellow":      "#dfb317",
		"yellowgreen": "#a4a61d",
		"orange":      "#fe7d37",
		"red":         "#e05d44",
		"blue":        "#007ec6",
		"grey":        "#555",
		"lightgrey":   "#9f9f9f",
	}
	colorAliases = map[string]string{
		"gray":          "grey",
		"lightgray":     "lightgrey",
		"critical":      "red",
		"important":     "orange",
		"success":       "brightgreen",
		"informational": "blue",
		"inactive":      "lightgrey",
	}
)

var (
	bareHex     = regexp.MustCompile(`(?i)^([\da-f]{3}){1,2}$`)
	cssHex      = regexp.MustCompile(`^#([0-9a-fA-F]{2})([0-9a-fA-F]{2})([0-9a-fA-F]{2})([0-9a-fA-F]{2})?$`)
	cssShortHex = regexp.MustCompile(`^#([0-9a-fA-F])([0-9a-fA-F])([0-9a-fA-F])([0-9a-fA-F])?$`)
	cssRGB      = regexp.MustCompile(`^rgba?\(\s*(\d+)\s*,\s*(\d+)\s*,\s*(\d+)(?:\s*,\s*(0|1|0?\.\d+|\d+%))?\s*\)$`)
	cssRGBFn    = regexp.MustCompile(`^rgba?\(\s*(\d+)\s+(\d+)\s+(\d+)(?:\s*/\s*(0|1|0?\.\d+|\d+%))?\s*\)$`)
	cssRGBPerc  = regexp.MustCompile(`^rgba?\(\s*(\d+%)\s*,\s*(\d+%)\s*,\s*(\d+%)(?:\s*,\s*(0|1|0?\.\d+|\d+%))?\s*\)$`)
	cssRGBPercF = regexp.MustCompile(`^rgba?\(\s*(\d+%)\s+(\d+%)\s+(\d+%)(?:\s*/\s*(0|1|0?\.\d+|\d+%))?\s*\)$`)
	cssHSL      = regexp.MustCompile(`^hsla?\(\s*(\d+)(deg|rad|grad|turn)?\s*,\s*(\d+)%\s*,\s*(\d+)%(?:\s*,\s*(0|1|0?\.\d+|\d+%))?\s*\)$`)
)

// toSvgColor is badge-maker's toSvgColor. It reports false where badge-maker has undefined,
// which means the default colour.
func toSvgColor(color string) (string, bool) {
	if color == "" {
		return "", false
	}
	if value, ok := namedColors[color]; ok {
		return value, true
	}
	if original, ok := colorAliases[color]; ok {
		return namedColors[original], true
	}
	if bareHex.MatchString(color) {
		return "#" + strings.ToLower(color), true
	}
	if _, ok := parseCSSColor(jsTrim(color)); ok {
		return strings.ToLower(color), true
	}
	return "", false
}

// parseCSSColor is css-color-converter's fromString, as far as brightness needs it.
func parseCSSColor(s string) ([3]int, bool) {
	if rgb, ok := cssColors[s]; ok {
		return [3]int{int(rgb[0]), int(rgb[1]), int(rgb[2])}, true
	}
	if m := cssHex.FindStringSubmatch(s); m != nil {
		return [3]int{hexByte(m[1]), hexByte(m[2]), hexByte(m[3])}, true
	}
	if m := cssShortHex.FindStringSubmatch(s); m != nil {
		return [3]int{hexByte(m[1] + m[1]), hexByte(m[2] + m[2]), hexByte(m[3] + m[3])}, true
	}
	for _, re := range []*regexp.Regexp{cssRGB, cssRGBFn, cssRGBPerc, cssRGBPercF} {
		if m := re.FindStringSubmatch(s); m != nil {
			var out [3]int
			for i := range 3 {
				part := m[i+1]
				if pct, found := strings.CutSuffix(part, "%"); found {
					v, _ := strconv.ParseFloat(pct, 64)
					out[i] = clampByte(int(math.Round(v / 100 * 255)))
				} else {
					v, _ := strconv.Atoi(part)
					out[i] = clampByte(v)
				}
			}
			return out, true
		}
	}
	if m := cssHSL.FindStringSubmatch(s); m != nil {
		h, _ := strconv.ParseFloat(m[1], 64)
		switch m[2] {
		case "rad":
			h = h * 180 / math.Pi
		case "grad":
			h = h * 0.9
		case "turn":
			h = h * 360
		}
		sat, _ := strconv.ParseFloat(m[3], 64)
		light, _ := strconv.ParseFloat(m[4], 64)
		r, g, b := hslToRGB(h, sat, light)
		return [3]int{clampByte(int(r)), clampByte(int(g)), clampByte(int(b))}, true
	}
	return [3]int{}, false
}

func hexByte(s string) int {
	v, _ := strconv.ParseUint(s, 16, 8)
	return int(v)
}

func clampByte(v int) int { return max(0, min(v, 255)) }

// hslToRGB is css-color-converter's conversion, which leaves the values unrounded.
func hslToRGB(h, s, l float64) (float64, float64, float64) {
	hp, sp, lp := h/60, s/100, l/100
	c := (1 - math.Abs(2*lp-1)) * sp
	x := c * (1 - math.Abs(math.Mod(hp, 2)-1))
	m := lp - c/2
	var r, g, b float64
	switch {
	case hp < 1:
		r, g, b = c, x, 0
	case hp < 2:
		r, g, b = x, c, 0
	case hp < 3:
		r, g, b = 0, c, x
	case hp < 4:
		r, g, b = 0, x, c
	case hp < 5:
		r, g, b = x, 0, c
	default:
		r, g, b = c, 0, x
	}
	return (r + m) * 255, (g + m) * 255, (b + m) * 255
}

// brightness is badge-maker's brightness: 0 for a colour it can't read.
func brightness(color string) float64 {
	rgb, ok := parseCSSColor(color)
	if !ok {
		return 0
	}
	v := float64(rgb[0]*299+rgb[1]*587+rgb[2]*114) / 255000
	rounded, _ := strconv.ParseFloat(strconv.FormatFloat(v, 'f', 2, 64), 64)
	return rounded
}

// PercentageToColor is the Node.js backend's colour for an uptime from 0 to 1: red to green,
// as chroma-js makes it from hsl(hue, 90%, 40%).
func PercentageToColor(percentage float64) string {
	hue := percentage*(90-10) + 10
	r, g, b := chromaHSL(hue, 0.9, 0.4)
	return "#" + hex2(r) + hex2(g) + hex2(b)
}

func hex2(v float64) string {
	n := clampByte(int(math.Round(v)))
	s := strconv.FormatInt(int64(n), 16)
	if len(s) == 1 {
		s = "0" + s
	}
	return s
}

// chromaHSL is chroma-js's hsl2rgb.
func chromaHSL(h, s, l float64) (float64, float64, float64) {
	if s == 0 {
		return l * 255, l * 255, l * 255
	}
	t3 := [3]float64{}
	var t2 float64
	if l < 0.5 {
		t2 = l * (1 + s)
	} else {
		t2 = l + s - l*s
	}
	t1 := 2*l - t2
	hn := h / 360
	t3[0] = hn + 1.0/3
	t3[1] = hn
	t3[2] = hn - 1.0/3
	var c [3]float64
	for i := range 3 {
		if t3[i] < 0 {
			t3[i]++
		}
		if t3[i] > 1 {
			t3[i]--
		}
		switch {
		case 6*t3[i] < 1:
			c[i] = t1 + (t2-t1)*6*t3[i]
		case 2*t3[i] < 1:
			c[i] = t2
		case 3*t3[i] < 2:
			c[i] = t1 + (t2-t1)*((2.0/3)-t3[i])*6
		default:
			c[i] = t1
		}
	}
	return c[0] * 255, c[1] * 255, c[2] * 255
}
