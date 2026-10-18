package tools

import "testing"

// TestClampInt: an over-cap value is CLAMPED to the cap (not reset to the
// default), and a non-positive value uses the default.
func TestClampInt(t *testing.T) {
	cases := []struct {
		name string
		args map[string]any
		def  int
		max  int
		want int
	}{
		{"absent -> default", map[string]any{}, 5, 60, 5},
		{"zero -> default", map[string]any{"timeout": float64(0)}, 5, 60, 5},
		{"negative -> default", map[string]any{"timeout": float64(-3)}, 5, 60, 5},
		{"in range", map[string]any{"timeout": float64(30)}, 5, 60, 30},
		{"at cap", map[string]any{"timeout": float64(60)}, 5, 60, 60},
		{"over cap -> CLAMPED to cap (not default)", map[string]any{"timeout": float64(120)}, 5, 60, 60},
		{"job-wait over cap", map[string]any{"timeout": float64(300)}, 60, 90, 90},
	}
	for _, c := range cases {
		if got := clampInt(c.args, "timeout", c.def, c.max); got != c.want {
			t.Errorf("%s: clampInt = %d, want %d", c.name, got, c.want)
		}
	}
}
