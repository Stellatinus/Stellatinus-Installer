package theme

import (
	"errors"
	"math"
	"testing"
)

func TestContrastBlackWhite(t *testing.T) {
	got, err := Contrast("#000000", "#ffffff")
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(got-21) > 0.01 {
		t.Fatalf("black on white = %.2f, want 21", got)
	}
}

func TestParseHexRejectsBadInput(t *testing.T) {
	for _, in := range []string{"", "0f9d9a", "#0f9d9", "#0f9d9ag", "#xyzxyz", "red", "#0f9d9a00"} {
		if _, err := Derive(in, false); err == nil {
			t.Errorf("Derive(%q) accepted a bad colour", in)
		}
	}
}

func TestTealLightMatchesTheDraft(t *testing.T) {
	// The interactive draft (docs/design/wizard-mockup.html) computes the same
	// palette for its default colour.
	p, err := Derive("#0f9d9a", false)
	if err != nil {
		t.Fatal(err)
	}
	if p.Fill != "#0c817f" || p.Ink != "#ffffff" {
		t.Fatalf("teal on the light theme: fill %s ink %s, want #0c817f on #ffffff", p.Fill, p.Ink)
	}
}

func TestEverySampleAccentIsLegibleInBothThemes(t *testing.T) {
	for _, accent := range []string{"#0f9d9a", "#f2c200", "#2f6df6", "#ee7a14", "#2e9e5b", "#ffffff", "#000000", "#808080", "#ff00ff", "#00ff00", "#0000ff"} {
		for _, dark := range []bool{false, true} {
			p, err := Derive(accent, dark)
			if err != nil {
				t.Errorf("%s dark=%v: %v", accent, dark, err)
				continue
			}
			if p.InkContrast < MinContrast || p.TextContrast < MinContrast {
				t.Errorf("%s dark=%v: ink %.2f text %.2f", accent, dark, p.InkContrast, p.TextContrast)
			}
		}
	}
}

func TestYellowGetsBlackInkInBothThemes(t *testing.T) {
	for _, dark := range []bool{false, true} {
		p, err := Derive("#f2c200", dark)
		if err != nil {
			t.Fatal(err)
		}
		if p.Ink != "#000000" {
			t.Errorf("yellow dark=%v: ink %s, want black", dark, p.Ink)
		}
	}
}

func TestErrIllegibleIsExported(t *testing.T) {
	if !errors.Is(ErrIllegible, ErrIllegible) {
		t.Fatal("sentinel broken")
	}
}
