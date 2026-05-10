package doctor

import "testing"

func TestFormatInt(t *testing.T) {
	cases := map[int]string{
		0:    "0",
		7:    "7",
		42:   "42",
		1234: "1234",
	}
	for in, want := range cases {
		if got := formatInt(in); got != want {
			t.Errorf("formatInt(%d): got %q want %q", in, got, want)
		}
	}
}

func TestCountWithLabel(t *testing.T) {
	if got := countWithLabel(1); got != "1 container labelled agentctl.session" {
		t.Errorf("singular form wrong: %q", got)
	}
	if got := countWithLabel(0); got != "0 containers labelled agentctl.session" {
		t.Errorf("zero form wrong: %q", got)
	}
}
