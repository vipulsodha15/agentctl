package tui

import "testing"

func TestPaletteVisibility(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  bool
	}{
		{"empty hides", "", false},
		{"plain text hides", "hello", false},
		{"slash alone shows", "/", true},
		{"slash prefix shows", "/in", true},
		{"slash with space hides", "/init now", false},
		{"slash with newline hides", "/init\nfoo", false},
		{"leading whitespace ignored", "   /sim", true},
	}
	p := palette{items: []Skill{{Name: "init"}, {Name: "simplify"}}}
	for _, tc := range cases {
		p.updateFromInput(tc.input)
		if p.visible != tc.want {
			t.Errorf("%s: visible=%v, want %v", tc.name, p.visible, tc.want)
		}
	}
}

func TestPaletteFilterAndSort(t *testing.T) {
	p := palette{items: []Skill{
		{Name: "simplify"},
		{Name: "init"},
		{Name: "review"},
		{Name: "security-review"},
	}}
	p.updateFromInput("/")
	if got := names(p.filtered); !equal(got, []string{"init", "review", "security-review", "simplify"}) {
		t.Fatalf("unfiltered (alpha): %v", got)
	}
	p.updateFromInput("/re")
	if got := names(p.filtered); !equal(got, []string{"review"}) {
		t.Fatalf("prefix re: %v", got)
	}
	p.updateFromInput("/SEC")
	if got := names(p.filtered); !equal(got, []string{"security-review"}) {
		t.Fatalf("case-insensitive: %v", got)
	}
	p.updateFromInput("/zzz")
	if len(p.filtered) != 0 {
		t.Fatalf("no match should empty: %v", names(p.filtered))
	}
}

func TestPaletteSelectionWraps(t *testing.T) {
	p := palette{items: []Skill{{Name: "a"}, {Name: "b"}, {Name: "c"}}}
	p.updateFromInput("/")
	if got, _ := p.selected(); got.Name != "a" {
		t.Fatalf("initial: %q", got.Name)
	}
	p.selectNext()
	p.selectNext()
	if got, _ := p.selected(); got.Name != "c" {
		t.Fatalf("after 2 next: %q", got.Name)
	}
	p.selectNext()
	if got, _ := p.selected(); got.Name != "a" {
		t.Fatalf("wrap forward: %q", got.Name)
	}
	p.selectPrev()
	if got, _ := p.selected(); got.Name != "c" {
		t.Fatalf("wrap back: %q", got.Name)
	}
}

func TestPaletteResetsIdxOnShrink(t *testing.T) {
	p := palette{items: []Skill{{Name: "alpha"}, {Name: "beta"}, {Name: "gamma"}}}
	p.updateFromInput("/")
	p.selectNext()
	p.selectNext()          // idx=2 -> gamma
	p.updateFromInput("/a") // narrows to [alpha], idx must clamp
	if p.idx != 0 {
		t.Fatalf("idx not clamped: %d", p.idx)
	}
	if got, _ := p.selected(); got.Name != "alpha" {
		t.Fatalf("selected after shrink: %q", got.Name)
	}
}

func names(s []Skill) []string {
	out := make([]string, len(s))
	for i, v := range s {
		out[i] = v.Name
	}
	return out
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
