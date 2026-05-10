package cli

import (
	"reflect"
	"testing"
)

func TestReorderArgs(t *testing.T) {
	cases := []struct{ in, want []string }{
		{[]string{"github", "--yes"}, []string{"--yes", "github"}},
		{[]string{"--yes", "github"}, []string{"--yes", "github"}},
		{[]string{"team-x", "--url", "https://x", "--default-enabled"}, []string{"--url", "https://x", "--default-enabled", "team-x"}},
		{[]string{"sess_01", "--clear-queue"}, []string{"--clear-queue", "sess_01"}},
		{[]string{"--name=foo", "x"}, []string{"--name=foo", "x"}},
	}
	for _, tc := range cases {
		got := reorderArgs(tc.in)
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("reorderArgs(%v) = %v; want %v", tc.in, got, tc.want)
		}
	}
}
