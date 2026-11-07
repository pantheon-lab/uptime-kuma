package badge

import (
	"encoding/json"
	"os"
	"testing"
)

// testdata/golden.json is badge-maker 3.3.1's and chroma-js 2.4.2's output, made by a script
// that's described in generate/README.md.
type golden struct {
	Badges []struct {
		Input struct {
			Style      string  `json:"style"`
			Label      string  `json:"label"`
			Message    string  `json:"message"`
			Color      *string `json:"color"`
			LabelColor *string `json:"labelColor"`
		} `json:"input"`
		SVG string `json:"svg"`
	} `json:"badges"`
	PercentageColors []struct {
		P   float64 `json:"p"`
		Hex string  `json:"hex"`
	} `json:"percentageColors"`
}

func loadGolden(t *testing.T) golden {
	t.Helper()
	raw, err := os.ReadFile("testdata/golden.json")
	if err != nil {
		t.Fatal(err)
	}
	var g golden
	if err := json.Unmarshal(raw, &g); err != nil {
		t.Fatal(err)
	}
	return g
}

func TestRenderMatchesBadgeMaker(t *testing.T) {
	g := loadGolden(t)
	if len(g.Badges) < 100 {
		t.Fatalf("only %d cases", len(g.Badges))
	}
	for i, c := range g.Badges {
		b := Badge{Label: c.Input.Label, Message: c.Input.Message, Style: c.Input.Style}
		if c.Input.Color != nil {
			b.Color = *c.Input.Color
		}
		if c.Input.LabelColor != nil {
			b.LabelColor = *c.Input.LabelColor
		}
		got, err := Render(b)
		if err != nil {
			t.Fatalf("case %d: %v", i, err)
		}
		if got != c.SVG {
			t.Errorf("case %d %+v:\n got %s\nwant %s", i, b, got, c.SVG)
		}
	}
}

func TestPercentageToColorMatchesChroma(t *testing.T) {
	for _, c := range loadGolden(t).PercentageColors {
		if got := PercentageToColor(c.P); got != c.Hex {
			t.Errorf("%v: got %s, want %s", c.P, got, c.Hex)
		}
	}
}

func TestUnknownStyle(t *testing.T) {
	if _, err := Render(Badge{Message: "x", Style: "round"}); err == nil {
		t.Fatal("accepted an unknown style")
	}
}
