package paint

import (
	"atlas-render/internal/model"
	"image/color"
	"math"
	"testing"
)

func legacySample(p model.Paint, at model.Point) color.NRGBA {
	a, _ := Parse(p.Color)
	b, _ := Parse(p.EndColor)
	dx, dy := p.To.X-p.From.X, p.To.Y-p.From.Y
	den := dx*dx + dy*dy
	t := 0.0
	if den > 0 {
		t = ((at.X-p.From.X)*dx + (at.Y-p.From.Y)*dy) / den
	}
	t = math.Max(0, math.Min(1, t))
	mix := func(x, y uint8) uint8 { return uint8(math.Round(float64(x)*(1-t) + float64(y)*t)) }
	return color.NRGBA{R: mix(a.R, b.R), G: mix(a.G, b.G), B: mix(a.B, b.B), A: 255}
}

func TestLegacyTwoStopIsByteIdentical(t *testing.T) {
	p := model.Paint{Kind: "linear", Color: "#145A70", EndColor: "#32A5A0", From: model.Point{X: 0, Y: 0}, To: model.Point{X: 220, Y: 150}}
	s, e := Compile(p)
	if e != nil {
		t.Fatal(e)
	}
	for i := 0; i <= 200; i++ {
		x := float64(i) * 3.1
		at := model.Point{X: x, Y: x * 0.7}
		got := s.SampleAt(at)
		want := legacySample(p, at)
		if got != want {
			t.Fatalf("at %v: legacy %v != new %v", at, want, got)
		}
	}
}

func TestExplicitStopsMatchLegacyWhenOpaque(t *testing.T) {
	legacy := model.Paint{Kind: "linear", Color: "#145A70", EndColor: "#32A5A0", From: model.Point{X: 10, Y: 0}, To: model.Point{X: 110, Y: 0}}
	explicit := model.Paint{Kind: "linear", From: legacy.From, To: legacy.To, Stops: []model.GradientStop{
		{Position: 0, Color: "#145A70", Alpha: 1},
		{Position: 1, Color: "#32A5A0", Alpha: 1},
	}}
	s, e := Compile(explicit)
	if e != nil {
		t.Fatal(e)
	}
	for i := 0; i <= 100; i++ {
		at := model.Point{X: 10 + float64(i), Y: 4}
		if got, want := s.SampleAt(at), legacySample(legacy, at); got != want {
			t.Fatalf("x=%d: explicit %v != legacy %v", i, got, want)
		}
	}
}

func TestTransparentStopLeavesNoDarkFringe(t *testing.T) {
	// The fully transparent endpoint keeps an arbitrary unseen color; premultiplied
	// interpolation must preserve the visible red instead of fading toward black.
	p := model.Paint{Kind: "linear", From: model.Point{X: 0, Y: 0}, To: model.Point{X: 100, Y: 0}, Stops: []model.GradientStop{
		{Position: 0, Color: "#FF0000", Alpha: 1},
		{Position: 1, Color: "#000000", Alpha: 0},
	}}
	s, e := Compile(p)
	if e != nil {
		t.Fatal(e)
	}
	for i := 1; i < 100; i++ {
		c := s.SampleAt(model.Point{X: float64(i), Y: 0})
		if c.R != 255 || c.G != 0 || c.B != 0 {
			t.Fatalf("x=%d: hue drifted toward unseen color: %v", i, c)
		}
		if c.A == 0 || c.A == 255 {
			t.Fatalf("x=%d: alpha should fade smoothly, got %d", i, c.A)
		}
	}
	if c := s.SampleAt(model.Point{X: 100, Y: 0}); c != (color.NRGBA{}) {
		t.Fatalf("transparent endpoint must be zero NRGBA, got %v", c)
	}
}

func TestProjectionClamp(t *testing.T) {
	p := model.Paint{Kind: "linear", From: model.Point{X: 0, Y: 0}, To: model.Point{X: 100, Y: 0}, Stops: []model.GradientStop{
		{Position: 0, Color: "#FF0000", Alpha: 1},
		{Position: 1, Color: "#00FF00", Alpha: 1},
	}}
	s, _ := Compile(p)
	first := s.SampleAt(model.Point{X: -50, Y: 9})
	if first != (color.NRGBA{R: 255, A: 255}) {
		t.Fatalf("before first stop should extend first color, got %v", first)
	}
	last := s.SampleAt(model.Point{X: 150, Y: 9})
	if last != (color.NRGBA{G: 255, A: 255}) {
		t.Fatalf("after last stop should extend last color, got %v", last)
	}
}

func TestCoincidentStopsFormHardBoundary(t *testing.T) {
	p := model.Paint{Kind: "linear", From: model.Point{X: 0, Y: 0}, To: model.Point{X: 100, Y: 0}, Stops: []model.GradientStop{
		{Position: 0, Color: "#FFFFFF", Alpha: 1},
		{Position: 0.5, Color: "#FF0000", Alpha: 1},
		{Position: 0.5, Color: "#0000FF", Alpha: 1},
		{Position: 1, Color: "#000000", Alpha: 1},
	}}
	s, e := Compile(p)
	if e != nil {
		t.Fatal(e)
	}
	below := s.SampleAt(model.Point{X: 49, Y: 0})
	if below.R != 255 || below.G != below.B || below.G >= 255 {
		t.Fatalf("just below shared position must stay in the white->red segment (red dominant), got %v", below)
	}
	at := s.SampleAt(model.Point{X: 50, Y: 0})
	if at != (color.NRGBA{B: 255, A: 255}) {
		t.Fatalf("shared position resolves to later (blue) stop, got %v", at)
	}
	above := s.SampleAt(model.Point{X: 51, Y: 0})
	if above.B == 0 || above.R != 0 {
		t.Fatalf("just above shared position must interpolate from blue, got %v", above)
	}
}

func TestCoincidentStopsLaterWinsEverywhere(t *testing.T) {
	// Three co-located anchors at the same interior position: exact match must
	// resolve to the last one, interpolation on each side brackets it.
	p := model.Paint{Kind: "linear", From: model.Point{X: 0, Y: 0}, To: model.Point{X: 100, Y: 0}, Stops: []model.GradientStop{
		{Position: 0, Color: "#FFFFFF", Alpha: 1},
		{Position: 0.4, Color: "#FF0000", Alpha: 1},
		{Position: 0.4, Color: "#00FF00", Alpha: 1},
		{Position: 0.4, Color: "#0000FF", Alpha: 1},
		{Position: 1, Color: "#000000", Alpha: 1},
	}}
	s, e := Compile(p)
	if e != nil {
		t.Fatal(e)
	}
	if got := s.SampleAt(model.Point{X: 40, Y: 0}); got != (color.NRGBA{B: 255, A: 255}) {
		t.Fatalf("exact shared position must be the last (blue) stop, got %v", got)
	}
	// Co-located anchors at the first position: extension and exact match take
	// the last anchor; interpolation starts from it as well.
	head := model.Paint{Kind: "linear", From: model.Point{X: 0, Y: 0}, To: model.Point{X: 100, Y: 0}, Stops: []model.GradientStop{
		{Position: 0, Color: "#FF0000", Alpha: 1},
		{Position: 0, Color: "#00FF00", Alpha: 1},
		{Position: 1, Color: "#0000FF", Alpha: 1},
	}}
	hs, e := Compile(head)
	if e != nil {
		t.Fatal(e)
	}
	wantHead := color.NRGBA{G: 255, A: 255}
	if got := hs.SampleAt(model.Point{X: 0, Y: 0}); got != wantHead {
		t.Fatalf("shared first position must be last (green) stop, got %v", got)
	}
	if got := hs.SampleAt(model.Point{X: -20, Y: 0}); got != wantHead {
		t.Fatalf("extension before first anchor must reuse last first-position stop, got %v", got)
	}
}

func TestStableSortMakesOrderIrrelevant(t *testing.T) {
	mk := func(stops []model.GradientStop) Sampler {
		s, e := Compile(model.Paint{Kind: "linear", From: model.Point{X: 0, Y: 0}, To: model.Point{X: 100, Y: 0}, Stops: stops})
		if e != nil {
			t.Fatal(e)
		}
		return s
	}
	ordered := []model.GradientStop{
		{Position: 0, Color: "#FF0000", Alpha: 1},
		{Position: .25, Color: "#00FF00", Alpha: .8},
		{Position: .75, Color: "#0000FF", Alpha: .5},
		{Position: 1, Color: "#FFFFFF", Alpha: 1},
	}
	shuffled := []model.GradientStop{ordered[3], ordered[1], ordered[2], ordered[0]}
	a, b := mk(ordered), mk(shuffled)
	for i := 0; i <= 100; i++ {
		at := model.Point{X: float64(i), Y: 0}
		if a.SampleAt(at) != b.SampleAt(at) {
			t.Fatalf("x=%d differs: %v vs %v", i, a.SampleAt(at), b.SampleAt(at))
		}
	}
}

func TestLinearRGBMode(t *testing.T) {
	stops := []model.GradientStop{
		{Position: 0, Color: "#000000", Alpha: 1},
		{Position: 1, Color: "#FFFFFF", Alpha: 1},
	}
	enc, _ := Compile(model.Paint{Kind: "linear", Interpolation: "rgb", From: model.Point{X: 0, Y: 0}, To: model.Point{X: 100, Y: 0}, Stops: stops})
	lin, _ := Compile(model.Paint{Kind: "linear", Interpolation: "linear-rgb", From: model.Point{X: 0, Y: 0}, To: model.Point{X: 100, Y: 0}, Stops: stops})
	midEnc := enc.SampleAt(model.Point{X: 50, Y: 0})
	midLin := lin.SampleAt(model.Point{X: 50, Y: 0})
	if midEnc.R != 128 {
		t.Fatalf("rgb midpoint expected 128, got %d", midEnc.R)
	}
	// Linear-light midpoint maps back to a brighter encoded value (~188).
	if midLin.R <= midEnc.R || midLin.R < 180 || midLin.R > 195 {
		t.Fatalf("linear-rgb midpoint outside expected band, got %d (rgb %d)", midLin.R, midEnc.R)
	}
	// Endpoints must agree exactly between modes.
	if lin.SampleAt(model.Point{X: 0, Y: 0}) != (color.NRGBA{A: 255}) {
		t.Fatal("linear-rgb black endpoint wrong")
	}
	if lin.SampleAt(model.Point{X: 100, Y: 0}) != (color.NRGBA{R: 255, G: 255, B: 255, A: 255}) {
		t.Fatal("linear-rgb white endpoint wrong")
	}
}

func TestCompileRejectsBadPaints(t *testing.T) {
	base := func() model.Paint {
		return model.Paint{Kind: "linear", From: model.Point{X: 0, Y: 0}, To: model.Point{X: 1, Y: 0}}
	}
	cases := []model.Paint{
		func() model.Paint {
			p := base()
			p.Stops = []model.GradientStop{{Position: 0, Color: "#FF0000", Alpha: 1}}
			return p
		}(),
		func() model.Paint {
			p := base()
			p.Stops = []model.GradientStop{{Position: -0.1, Color: "#FF0000", Alpha: 1}, {Position: 1, Color: "#00FF00", Alpha: 1}}
			return p
		}(),
		func() model.Paint {
			p := base()
			p.Stops = []model.GradientStop{{Position: 0, Color: "#FF0000", Alpha: 2}, {Position: 1, Color: "#00FF00", Alpha: 1}}
			return p
		}(),
		func() model.Paint {
			p := base()
			p.Stops = []model.GradientStop{{Position: 0, Color: "red", Alpha: 1}, {Position: 1, Color: "#00FF00", Alpha: 1}}
			return p
		}(),
		func() model.Paint {
			p := base()
			p.Interpolation = "hsv"
			p.Stops = []model.GradientStop{{Position: 0, Color: "#FF0000", Alpha: 1}, {Position: 1, Color: "#00FF00", Alpha: 1}}
			return p
		}(),
	}
	for i, p := range cases {
		if _, e := Compile(p); e == nil {
			t.Fatalf("case %d expected rejection", i)
		}
	}
}

func TestSolidSampler(t *testing.T) {
	s, e := Compile(model.Paint{Kind: "solid", Color: "#E87E4F"})
	if e != nil {
		t.Fatal(e)
	}
	want, _ := Parse("#E87E4F")
	if got := s.SampleAt(model.Point{X: 9, Y: 7}); got != want {
		t.Fatalf("solid sample %v != %v", got, want)
	}
}
