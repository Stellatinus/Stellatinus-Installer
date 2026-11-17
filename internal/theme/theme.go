// Package theme derives the colours the installer window uses from the single
// accent colour an app sets in its manifest, for the light and the dark theme,
// and checks that the result is legible (WCAG AA, a contrast of at least 4.5:1).
//
// It does not draw anything and does not read the system theme: the window
// decides which theme is active and asks for that palette.
package theme

import (
	"errors"
	"fmt"
	"math"
	"strconv"
)

// MinContrast is the WCAG AA contrast ratio for normal text.
const MinContrast = 4.5

// Surface colours the derived text colour must be legible on. They are the
// window background in the light and in the dark theme.
const (
	LightSurface = "#fbfdfd"
	DarkSurface  = "#141d22"
)

// Palette is what the window needs for one theme.
type Palette struct {
	Fill string // buttons, progress bar, current step
	Ink  string // text drawn on Fill: black or white, whichever reads better
	Text string // the accent used as text (links, badges) on the window surface
	Soft string // a faint tint for highlighted boxes

	InkContrast  float64 // contrast of Ink on Fill
	TextContrast float64 // contrast of Text on the window surface
}

// ErrIllegible is returned when no legible palette can be derived.
var ErrIllegible = errors.New("accent colour cannot be made legible")

type rgb [3]float64

func parseHex(s string) (rgb, error) {
	if len(s) != 7 || s[0] != '#' {
		return rgb{}, fmt.Errorf("colour %q must look like #rrggbb", s)
	}
	var c rgb
	for i := 0; i < 3; i++ {
		v, err := strconv.ParseUint(s[1+2*i:3+2*i], 16, 8)
		if err != nil {
			return rgb{}, fmt.Errorf("colour %q must look like #rrggbb", s)
		}
		c[i] = float64(v)
	}
	return c, nil
}

func (c rgb) hex() string {
	b := func(v float64) int { return int(math.Max(0, math.Min(255, math.Round(v)))) }
	return fmt.Sprintf("#%02x%02x%02x", b(c[0]), b(c[1]), b(c[2]))
}

func luminance(c rgb) float64 {
	f := func(v float64) float64 {
		v /= 255
		if v <= 0.03928 {
			return v / 12.92
		}
		return math.Pow((v+0.055)/1.055, 2.4)
	}
	return 0.2126*f(c[0]) + 0.7152*f(c[1]) + 0.0722*f(c[2])
}

func contrast(a, b rgb) float64 {
	x, y := luminance(a), luminance(b)
	return (math.Max(x, y) + 0.05) / (math.Min(x, y) + 0.05)
}

// Contrast returns the WCAG contrast ratio between two #rrggbb colours.
func Contrast(a, b string) (float64, error) {
	ca, err := parseHex(a)
	if err != nil {
		return 0, err
	}
	cb, err := parseHex(b)
	if err != nil {
		return 0, err
	}
	return contrast(ca, cb), nil
}

func toHSL(c rgb) (h, s, l float64) {
	r, g, b := c[0]/255, c[1]/255, c[2]/255
	mx, mn := math.Max(r, math.Max(g, b)), math.Min(r, math.Min(g, b))
	l = (mx + mn) / 2
	if mx == mn {
		return 0, 0, l
	}
	d := mx - mn
	if l > 0.5 {
		s = d / (2 - mx - mn)
	} else {
		s = d / (mx + mn)
	}
	switch mx {
	case r:
		h = (g - b) / d
		if g < b {
			h += 6
		}
	case g:
		h = (b-r)/d + 2
	default:
		h = (r-g)/d + 4
	}
	return h / 6, s, l
}

func hueToRGB(p, q, t float64) float64 {
	if t < 0 {
		t++
	}
	if t > 1 {
		t--
	}
	switch {
	case t < 1.0/6:
		return p + (q-p)*6*t
	case t < 1.0/2:
		return q
	case t < 2.0/3:
		return p + (q-p)*(2.0/3-t)*6
	}
	return p
}

func fromHSL(h, s, l float64) rgb {
	l = math.Max(0, math.Min(1, l))
	if s == 0 {
		v := l * 255
		return rgb{v, v, v}
	}
	var q float64
	if l < 0.5 {
		q = l * (1 + s)
	} else {
		q = l + s - l*s
	}
	p := 2*l - q
	return rgb{hueToRGB(p, q, h+1.0/3) * 255, hueToRGB(p, q, h) * 255, hueToRGB(p, q, h-1.0/3) * 255}
}

func mix(a, b rgb, t float64) rgb {
	return rgb{a[0]*(1-t) + b[0]*t, a[1]*(1-t) + b[1]*t, a[2]*(1-t) + b[2]*t}
}

// Derive builds the palette for one theme from an accent colour (#rrggbb).
// The fill is moved toward the side where text on it can be read: darker in
// the light theme (white text), lighter in the dark theme (black text); when
// that cannot reach 4.5:1 the colour is kept and the better of black and white
// is used. The text colour is moved the same way until it is legible on the
// window surface. It fails with ErrIllegible if the result is still below
// MinContrast.
func Derive(accent string, dark bool) (Palette, error) {
	base, err := parseHex(accent)
	if err != nil {
		return Palette{}, err
	}
	surface := LightSurface
	if dark {
		surface = DarkSurface
	}
	win, _ := parseHex(surface)
	white, black := rgb{255, 255, 255}, rgb{0, 0, 0}
	h, s, l := toHSL(base)

	fill, ink, found := base, white, false
	ll := l
	for i := 0; i < 9; i++ {
		f := fromHSL(h, s, ll)
		target := white
		if dark {
			target = black
		}
		if contrast(f, target) >= MinContrast {
			fill, ink, found = f, target, true
			break
		}
		if dark {
			ll += 0.02
		} else {
			ll -= 0.02
		}
	}
	if !found {
		fill = base
		if contrast(base, black) >= MinContrast {
			ink = black
		} else {
			ink = white
		}
	}

	tl, text := l, base
	for i := 0; i < 30; i++ {
		text = fromHSL(h, s, math.Max(0.05, math.Min(0.95, tl)))
		if contrast(text, win) >= MinContrast {
			break
		}
		if dark {
			tl += 0.02
		} else {
			tl -= 0.02
		}
	}

	t := 0.14
	if dark {
		t = 0.22
	}
	p := Palette{
		Fill: fill.hex(), Ink: ink.hex(), Text: text.hex(), Soft: mix(win, base, t).hex(),
		InkContrast: contrast(fill, ink), TextContrast: contrast(text, win),
	}
	if p.InkContrast < MinContrast || p.TextContrast < MinContrast {
		return p, fmt.Errorf("%w: %s on %s theme gives %.1f:1 for text on the fill and %.1f:1 for text on the window (need %.1f:1)",
			ErrIllegible, accent, themeName(dark), p.InkContrast, p.TextContrast, MinContrast)
	}
	return p, nil
}

func themeName(dark bool) string {
	if dark {
		return "the dark"
	}
	return "the light"
}
