package raster

import (
	"atlas-render/internal/codec"
	"atlas-render/internal/geometry"
	"atlas-render/internal/model"
	"atlas-render/internal/paint"
	"atlas-render/internal/scene"
	"image"
	"math"
)

type clip struct {
	inverse geometry.Matrix
	rect    model.Rect
}
type operation struct {
	layer   model.Layer
	inverse geometry.Matrix
	clips   []clip
	opacity float64
	bounds  image.Rectangle
	bitmap  image.Image
	paint   paint.Sampler
}
type Plan struct {
	Width, Height, Scale int
	Background           string
	ops                  []operation
	Cost                 int64
}

func Compile(s model.Snapshot, scale int) (Plan, error) {
	if e := scene.Validate(s.Document); e != nil {
		return Plan{}, e
	}
	if scale != 1 && scale != 2 {
		return Plan{}, model.Invalid("scale must be one or two")
	}
	p := Plan{Width: s.Document.Width * scale, Height: s.Document.Height * scale, Scale: scale, Background: s.Document.Background}
	decoded := map[string]image.Image{}
	var walk func([]model.Layer, geometry.Matrix, []clip, float64) error
	walk = func(ls []model.Layer, parent geometry.Matrix, clips []clip, alpha float64) error {
		for _, l := range ls {
			world := parent.Mul(geometry.From(l.Transform))
			inv, ok := world.Inverse()
			if !ok {
				return model.Invalid("layer %s has singular accumulated transform", l.ID)
			}
			localClips := append([]clip(nil), clips...)
			if l.Clip != nil {
				localClips = append(localClips, clip{inverse: inv, rect: *l.Clip})
			}
			a := alpha * l.Opacity
			if l.Kind == "group" {
				if e := walk(l.Children, world, localClips, a); e != nil {
					return e
				}
				continue
			}
			corners := []model.Point{{}, {X: l.Width}, {Y: l.Height}, {X: l.Width, Y: l.Height}}
			minX, minY, maxX, maxY := math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)
			for _, c := range corners {
				v := world.Apply(c)
				minX = math.Min(minX, v.X)
				minY = math.Min(minY, v.Y)
				maxX = math.Max(maxX, v.X)
				maxY = math.Max(maxY, v.Y)
			}
			minX = math.Max(0, math.Min(float64(s.Document.Width), minX))
			maxX = math.Max(0, math.Min(float64(s.Document.Width), maxX))
			minY = math.Max(0, math.Min(float64(s.Document.Height), minY))
			maxY = math.Max(0, math.Min(float64(s.Document.Height), maxY))
			b := image.Rect(int(math.Floor(minX*float64(scale))), int(math.Floor(minY*float64(scale))), int(math.Ceil(maxX*float64(scale))), int(math.Ceil(maxY*float64(scale))))
			op := operation{layer: l, inverse: inv, clips: localClips, opacity: a, bounds: b}
			if l.Kind == "image" {
				asset, ok := s.Assets[l.AssetID]
				if !ok {
					return model.Invalid("missing pinned asset %s", l.AssetID)
				}
				img := decoded[l.AssetID]
				if img == nil {
					var e error
					img, e = codec.Decode(asset.PNG)
					if e != nil {
						return e
					}
					decoded[l.AssetID] = img
				}
				op.bitmap = img
			} else {
				sampler, e := paint.Compile(l.Fill)
				if e != nil {
					return model.Invalid("layer %s: %v", l.ID, e)
				}
				op.paint = sampler
			}
			p.Cost += int64(b.Dx()) * int64(b.Dy())
			if p.Cost > 40_000_000 {
				return model.Busy("scene exceeds raster work budget")
			}
			p.ops = append(p.ops, op)
		}
		return nil
	}
	if e := walk(s.Document.Layers, geometry.Identity(), nil, 1); e != nil {
		return Plan{}, e
	}
	return p, nil
}
