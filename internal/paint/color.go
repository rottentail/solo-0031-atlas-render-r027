package paint

import (
	"atlas-render/internal/model"
	"fmt"
	"image/color"
	"math"
	"sort"
	"strconv"
)

func Parse(s string) (color.NRGBA, error) {
	if len(s) != 7 || s[0] != '#' {
		return color.NRGBA{}, fmt.Errorf("color must be #RRGGBB")
	}
	n, e := strconv.ParseUint(s[1:], 16, 24)
	if e != nil {
		return color.NRGBA{}, e
	}
	return color.NRGBA{R: uint8(n >> 16), G: uint8(n >> 8), B: uint8(n), A: 255}, nil
}

// MaxStops bounds per-paint gradient complexity and validation cost.
const MaxStops = 32

// stop holds one fully validated gradient anchor in 0..1 position space.
// Channel values live on the 0..255 scale and are premultiplied by the stop's
// own alpha. For "linear-rgb" they are linearized first; for "rgb" they stay in
// encoded sRGB space. Premultiplication means fading toward alpha 0 never drags
// an unseen RGB value into a dark fringe, and the 255 scale keeps opaque
// interpolation byte-identical to the original two-stop gradient formula.
type stop struct {
	position          float64
	pr, pg, pb, alpha float64
}

// Sampler is a compiled paint. Compilation happens once per layer during plan
// compilation, so per-pixel rendering never parses color strings.
type Sampler struct {
	kind     string
	solid    color.NRGBA
	from, to model.Point
	linear   bool
	stops    []stop
}

// Compile turns a validated paint into a reusable sampler. It mirrors
// scene.Validate's paint checks so that a rejected document never reaches the
// raster stage; rendering itself relies on the scene validation performed in
// raster.Compile.
func Compile(p model.Paint) (Sampler, error) {
	s := Sampler{kind: p.Kind, from: p.From, to: p.To}
	if p.Kind != "linear" {
		c, e := Parse(p.Color)
		if e != nil {
			return s, e
		}
		s.solid = c
		return s, nil
	}
	switch p.Interpolation {
	case "", "rgb":
	case "linear-rgb":
		s.linear = true
	default:
		return s, fmt.Errorf("interpolation must be rgb or linear-rgb")
	}
	raw := p.Stops
	if len(raw) == 0 {
		if _, e := Parse(p.Color); e != nil {
			return s, e
		}
		if _, e := Parse(p.EndColor); e != nil {
			return s, e
		}
		raw = []model.GradientStop{
			{Position: 0, Color: p.Color, Alpha: 1},
			{Position: 1, Color: p.EndColor, Alpha: 1},
		}
	}
	if len(raw) < 2 || len(raw) > MaxStops {
		return s, fmt.Errorf("gradient requires 2..%d stops", MaxStops)
	}
	stops := make([]stop, 0, len(raw))
	for i, gs := range raw {
		if math.IsNaN(gs.Position) || math.IsInf(gs.Position, 0) || gs.Position < 0 || gs.Position > 1 {
			return s, fmt.Errorf("gradient stop %d position must be finite 0..1", i)
		}
		if math.IsNaN(gs.Alpha) || math.IsInf(gs.Alpha, 0) || gs.Alpha < 0 || gs.Alpha > 1 {
			return s, fmt.Errorf("gradient stop %d alpha must be 0..1", i)
		}
		c, e := Parse(gs.Color)
		if e != nil {
			return s, fmt.Errorf("gradient stop %d: %v", i, e)
		}
		st := stop{position: gs.Position, alpha: gs.Alpha}
		r, g, b := float64(c.R), float64(c.G), float64(c.B)
		if s.linear {
			r = srgbToLinear(r/255) * 255
			g = srgbToLinear(g/255) * 255
			b = srgbToLinear(b/255) * 255
		}
		st.pr, st.pg, st.pb = r*gs.Alpha, g*gs.Alpha, b*gs.Alpha
		stops = append(stops, st)
	}
	// Stable sort keeps document order as the documented tie-break: at a shared
	// position the later stop in the document wins the boundary and any earlier
	// stop at the same position never participates in a segment.
	sort.SliceStable(stops, func(i, j int) bool { return stops[i].position < stops[j].position })
	s.stops = stops
	return s, nil
}

func srgbToLinear(c float64) float64 {
	if c <= 0.04045 {
		return c / 12.92
	}
	return math.Pow((c+0.055)/1.055, 2.4)
}
func linearToSRGB(c float64) float64 {
	if c <= 0.0031308 {
		return c * 12.92
	}
	return 1.055*math.Pow(c, 1/2.4) - 0.055
}

// SampleAt evaluates the paint at a layer-local point. The projection of the
// point onto the gradient axis is clamped to 0..1, matching the documented
// gradient semantics.
func (s Sampler) SampleAt(at model.Point) color.NRGBA {
	if s.kind != "linear" {
		return s.solid
	}
	dx, dy := s.to.X-s.from.X, s.to.Y-s.from.Y
	den := dx*dx + dy*dy
	t := 0.0
	if den > 0 {
		t = ((at.X-s.from.X)*dx + (at.Y-s.from.Y)*dy) / den
	}
	if t < 0 {
		t = 0
	} else if t > 1 {
		t = 1
	}
	stops := s.stops
	n := len(stops)
	if t < stops[0].position {
		// Extension before the first anchor. The boundary color is the last
		// stop (document order) sharing the first position.
		k := 0
		for k+1 < n && stops[k+1].position == stops[0].position {
			k++
		}
		return s.colorAt(stops[k])
	}
	if t >= stops[n-1].position {
		// Stable sort makes the final slice element the latest document-order
		// stop among any sharing the last position.
		return s.colorAt(stops[n-1])
	}
	// First stop whose position is strictly greater than t.
	hi := sort.Search(n, func(i int) bool { return stops[i].position > t })
	if stops[hi-1].position == t {
		// t lands exactly on an anchor (possibly co-located): the last stop at
		// that position wins, giving co-located stops one deterministic,
		// explainable hard boundary.
		return s.colorAt(stops[hi-1])
	}
	// Otherwise bracket t between two distinct positions; co-located anchors
	// never form a zero-length interpolated segment.
	a, b := stops[hi-1], stops[hi]
	f := (t - a.position) / (b.position - a.position)
	alpha := a.alpha*(1-f) + b.alpha*f
	pr := a.pr*(1-f) + b.pr*f
	pg := a.pg*(1-f) + b.pg*f
	pb := a.pb*(1-f) + b.pb*f
	out := color.NRGBA{A: clamp8(alpha * 255)}
	if alpha <= 0 {
		// Fully transparent: RGB carries no visible meaning; emit zero channels
		// instead of resurrecting an unseen color as a dark edge.
		return out
	}
	cr, cg, cb := pr/alpha, pg/alpha, pb/alpha
	if s.linear {
		cr = linearToSRGB(cr/255) * 255
		cg = linearToSRGB(cg/255) * 255
		cb = linearToSRGB(cb/255) * 255
	}
	out.R = clamp8(cr)
	out.G = clamp8(cg)
	out.B = clamp8(cb)
	return out
}

func (s Sampler) colorAt(st stop) color.NRGBA {
	out := color.NRGBA{A: clamp8(st.alpha * 255)}
	if st.alpha <= 0 {
		return out
	}
	r, g, b := st.pr/st.alpha, st.pg/st.alpha, st.pb/st.alpha
	if s.linear {
		r = linearToSRGB(r/255) * 255
		g = linearToSRGB(g/255) * 255
		b = linearToSRGB(b/255) * 255
	}
	out.R = clamp8(r)
	out.G = clamp8(g)
	out.B = clamp8(b)
	return out
}

func clamp8(v float64) uint8 {
	v = math.Round(v)
	if v < 0 {
		return 0
	}
	if v > 255 {
		return 255
	}
	return uint8(v)
}
