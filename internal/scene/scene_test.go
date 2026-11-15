package scene

import (
	"atlas-render/internal/model"
	"atlas-render/internal/paint"
	"encoding/json"
	"testing"
)

func TestLegacyPaintJSONRoundTrip(t *testing.T) {
	// Exact pre-change Paint JSON: only kind/color/endColor/from/to.
	const legacyPaint = `{
	  "kind":"linear","color":"#145A70","endColor":"#32A5A0",
	  "from":{"x":0,"y":0},"to":{"x":220,"y":150}
	}`
	var p model.Paint
	if e := json.Unmarshal([]byte(legacyPaint), &p); e != nil {
		t.Fatal(e)
	}
	if len(p.Stops) != 0 || p.Interpolation != "" {
		t.Fatal("legacy fields leaked into new attributes")
	}
	doc := model.Document{Width: 10, Height: 10, Background: "#FFFFFF", Layers: []model.Layer{{
		ID: "plate", Kind: "rectangle", Width: 8, Height: 8,
		Transform: model.Transform{ScaleX: 1, ScaleY: 1}, Opacity: 1, Fill: p,
	}}}
	if e := Validate(doc); e != nil {
		t.Fatalf("legacy document rejected: %v", e)
	}
	s, e := paint.Compile(p)
	if e != nil {
		t.Fatal(e)
	}
	if c := s.SampleAt(model.Point{X: 110, Y: 75}); c.A != 255 {
		t.Fatalf("legacy gradient must stay opaque, got %v", c)
	}
	out, e := json.Marshal(p)
	if e != nil {
		t.Fatal(e)
	}
	var back model.Paint
	if e := json.Unmarshal(out, &back); e != nil || back.EndColor != "#32A5A0" || len(back.Stops) != 0 {
		t.Fatalf("legacy JSON shape not preserved: %s %v", out, e)
	}
}

func gradientDoc(mutate func(*model.Paint)) model.Document {
	fill := model.Paint{Kind: "linear", From: model.Point{X: 0, Y: 0}, To: model.Point{X: 100, Y: 0},
		Interpolation: "linear-rgb",
		Stops: []model.GradientStop{
			{Position: 0, Color: "#FF0000", Alpha: 1},
			{Position: 0.5, Color: "#00FF00", Alpha: 0.4},
			{Position: 1, Color: "#0000FF", Alpha: 0},
		}}
	mutate(&fill)
	return model.Document{Width: 120, Height: 80, Background: "#FFFFFF", Layers: []model.Layer{{
		ID: "fade", Kind: "rectangle", Width: 100, Height: 40,
		Transform: model.Transform{ScaleX: 1, ScaleY: 1}, Opacity: 1, Fill: fill,
	}}}
}

func TestValidMultiStopDocument(t *testing.T) {
	if e := Validate(gradientDoc(func(*model.Paint) {})); e != nil {
		t.Fatalf("expected valid: %v", e)
	}
	// Opaque rgb form and co-located stops are valid.
	doc := gradientDoc(func(p *model.Paint) {
		p.Interpolation = "rgb"
		p.Stops = append(p.Stops, model.GradientStop{Position: 0.5, Color: "#112233", Alpha: 1})
	})
	if e := Validate(doc); e != nil {
		t.Fatalf("co-located stops should validate: %v", e)
	}
}

func TestGradientStopValidation(t *testing.T) {
	cases := map[string]func(*model.Paint){
		"single stop":          func(p *model.Paint) { p.Stops = p.Stops[:1] },
		"position out of band": func(p *model.Paint) { p.Stops[0].Position = 1.2 },
		"alpha out of band":    func(p *model.Paint) { p.Stops[1].Alpha = -0.2 },
		"bad color":            func(p *model.Paint) { p.Stops[1].Color = "green" },
		"bad interpolation":    func(p *model.Paint) { p.Interpolation = "cmyk" },
		"endColor with stops":  func(p *model.Paint) { p.EndColor = "#000000" },
		"color with stops":     func(p *model.Paint) { p.Color = "#000000" },
	}
	for name, mutate := range cases {
		if e := Validate(gradientDoc(mutate)); e == nil {
			t.Fatalf("case %q expected rejection", name)
		}
	}
	// Stops on a solid paint are rejected.
	solid := model.Document{Width: 10, Height: 10, Background: "#FFFFFF", Layers: []model.Layer{{
		ID: "s", Kind: "rectangle", Width: 5, Height: 5,
		Transform: model.Transform{ScaleX: 1, ScaleY: 1}, Opacity: 1,
		Fill: model.Paint{Kind: "solid", Color: "#FF0000", Interpolation: "rgb"},
	}}}
	if e := Validate(solid); e == nil {
		t.Fatal("solid paint must reject interpolation/stops")
	}
	// Interpolation on a legacy two-stop form is rejected (ambiguous).
	legacy := gradientDoc(func(p *model.Paint) {
		p.Stops = nil
		p.Color = "#FF0000"
		p.EndColor = "#0000FF"
	})
	if e := Validate(legacy); e == nil {
		t.Fatal("interpolation without explicit stops must be rejected")
	}
	// Legacy two-stop form without interpolation remains valid.
	legacy = gradientDoc(func(p *model.Paint) {
		p.Stops = nil
		p.Interpolation = ""
		p.Color = "#FF0000"
		p.EndColor = "#0000FF"
	})
	if e := Validate(legacy); e != nil {
		t.Fatalf("legacy form must stay valid: %v", e)
	}
}

func TestMaxStopsBound(t *testing.T) {
	doc := gradientDoc(func(p *model.Paint) {
		p.Stops = make([]model.GradientStop, 33)
		for i := range p.Stops {
			p.Stops[i] = model.GradientStop{Position: float64(i) / 32, Color: "#123456", Alpha: 1}
		}
	})
	if e := Validate(doc); e == nil {
		t.Fatal("more than MaxStops must be rejected")
	}
}
