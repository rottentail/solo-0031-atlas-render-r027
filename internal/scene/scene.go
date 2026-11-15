package scene

import (
	"atlas-render/internal/model"
	"atlas-render/internal/paint"
	"math"
	"regexp"
)

var identifier = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,79}$`)

func ValidID(id string) bool { return identifier.MatchString(id) }
func finite(v float64) bool  { return !math.IsNaN(v) && !math.IsInf(v, 0) && math.Abs(v) <= 100000 }
func Validate(d model.Document) error {
	if d.Width < 1 || d.Height < 1 || d.Width > 1536 || d.Height > 1536 {
		return model.Invalid("canvas dimensions must be 1..1536")
	}
	if _, e := paint.Parse(d.Background); e != nil {
		return model.Invalid("background: %v", e)
	}
	seen := map[string]bool{}
	count := 0
	var walk func([]model.Layer, int) error
	walk = func(layers []model.Layer, depth int) error {
		if depth > 8 {
			return model.Invalid("scene nesting exceeds eight")
		}
		for _, l := range layers {
			count++
			if count > 200 {
				return model.Invalid("scene exceeds 200 layers")
			}
			if !ValidID(l.ID) || seen[l.ID] {
				return model.Invalid("layer id invalid or repeated: %s", l.ID)
			}
			seen[l.ID] = true
			t := l.Transform
			for _, n := range []float64{l.Width, l.Height, l.Opacity, t.X, t.Y, t.ScaleX, t.ScaleY, t.Degrees} {
				if !finite(n) {
					return model.Invalid("layer %s: nonfinite or unbounded geometry", l.ID)
				}
			}
			if l.Opacity < 0 || l.Opacity > 1 || math.Abs(t.ScaleX) < .001 || math.Abs(t.ScaleY) < .001 || math.Abs(t.ScaleX) > 100 || math.Abs(t.ScaleY) > 100 {
				return model.Invalid("layer %s: invalid opacity or scale", l.ID)
			}
			if l.Clip != nil {
				r := l.Clip
				if !finite(r.X) || !finite(r.Y) || !finite(r.Width) || !finite(r.Height) || r.Width <= 0 || r.Height <= 0 {
					return model.Invalid("layer %s: invalid clip", l.ID)
				}
			}
			if l.Kind == "group" {
				if len(l.Children) == 0 {
					return model.Invalid("group %s is empty", l.ID)
				}
				if e := walk(l.Children, depth+1); e != nil {
					return e
				}
				continue
			}
			if len(l.Children) > 0 {
				return model.Invalid("only groups can contain layers")
			}
			if l.Width <= 0 || l.Height <= 0 || l.Width > 4096 || l.Height > 4096 {
				return model.Invalid("layer %s: invalid dimensions", l.ID)
			}
			switch l.Kind {
			case "rectangle", "ellipse":
			case "polygon":
				if len(l.Points) < 3 || len(l.Points) > 128 {
					return model.Invalid("polygon requires 3..128 points")
				}
				for _, p := range l.Points {
					if !finite(p.X) || !finite(p.Y) || p.X < 0 || p.X > l.Width || p.Y < 0 || p.Y > l.Height {
						return model.Invalid("polygon point outside local bounds")
					}
				}
			case "image":
				if !ValidID(l.AssetID) {
					return model.Invalid("image requires asset id")
				}
				continue
			default:
				return model.Invalid("layer %s: unknown kind", l.ID)
			}
			if l.Fill.Kind != "solid" && l.Fill.Kind != "linear" {
				return model.Invalid("unknown paint kind")
			}
			if l.Fill.Kind == "linear" {
				for _, n := range []float64{l.Fill.From.X, l.Fill.From.Y, l.Fill.To.X, l.Fill.To.Y} {
					if !finite(n) {
						return model.Invalid("layer %s: invalid gradient endpoint", l.ID)
					}
				}
				if l.Fill.From == l.Fill.To {
					return model.Invalid("layer %s: gradient endpoints coincide", l.ID)
				}
				if len(l.Fill.Stops) == 0 {
					// Legacy two-stop form: both endpoint colors are required.
					if _, e := paint.Parse(l.Fill.Color); e != nil {
						return model.Invalid("layer %s: invalid fill color: %v", l.ID, e)
					}
					if _, e := paint.Parse(l.Fill.EndColor); e != nil {
						return model.Invalid("layer %s: invalid gradient color: %v", l.ID, e)
					}
				} else {
					if l.Fill.Color != "" || l.Fill.EndColor != "" {
						return model.Invalid("layer %s: gradient stops replace color and endColor", l.ID)
					}
					if len(l.Fill.Stops) < 2 || len(l.Fill.Stops) > paint.MaxStops {
						return model.Invalid("layer %s: gradient requires 2..%d stops", l.ID, paint.MaxStops)
					}
					for i, st := range l.Fill.Stops {
						if !finite(st.Position) || st.Position < 0 || st.Position > 1 {
							return model.Invalid("layer %s: stop %d position must be 0..1", l.ID, i)
						}
						if !finite(st.Alpha) || st.Alpha < 0 || st.Alpha > 1 {
							return model.Invalid("layer %s: stop %d alpha must be 0..1", l.ID, i)
						}
						if _, e := paint.Parse(st.Color); e != nil {
							return model.Invalid("layer %s: stop %d color: %v", l.ID, i, e)
						}
					}
				}
				if l.Fill.Interpolation != "" && l.Fill.Interpolation != "rgb" && l.Fill.Interpolation != "linear-rgb" {
					return model.Invalid("layer %s: interpolation must be rgb or linear-rgb", l.ID)
				}
				if l.Fill.Interpolation != "" && len(l.Fill.Stops) == 0 {
					return model.Invalid("layer %s: interpolation requires explicit stops", l.ID)
				}
			} else {
				if _, e := paint.Parse(l.Fill.Color); e != nil {
					return model.Invalid("layer %s: invalid fill color: %v", l.ID, e)
				}
				if len(l.Fill.Stops) > 0 || l.Fill.Interpolation != "" {
					return model.Invalid("layer %s: only linear paint carries stops", l.ID)
				}
			}
		}
		return nil
	}
	return walk(d.Layers, 0)
}
func References(d model.Document) []string {
	seen := map[string]bool{}
	out := []string{}
	var walk func([]model.Layer)
	walk = func(ls []model.Layer) {
		for _, l := range ls {
			if l.Kind == "image" && !seen[l.AssetID] {
				seen[l.AssetID] = true
				out = append(out, l.AssetID)
			}
			walk(l.Children)
		}
	}
	walk(d.Layers)
	return out
}
