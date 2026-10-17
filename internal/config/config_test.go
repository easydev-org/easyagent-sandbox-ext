package config

import "testing"

func TestParse(t *testing.T) {
	raw := `[
		{"name":"a","url":"http://a:80","token":"ta"},
		{"name":"","url":"http://x"},
		{"name":"c","url":""},
		{"name":"d","url":"http://d:80"}
	]`
	got := Parse(raw)
	if len(got) != 2 || got[0].Name != "a" || got[1].Name != "d" {
		t.Fatalf("got %+v", got)
	}
	if Parse("") != nil || Parse("nope") != nil || Parse("{}") != nil {
		t.Error("empty/malformed should be nil")
	}
}

func TestSelect(t *testing.T) {
	list := []Sandbox{{Name: "one", URL: "u1"}, {Name: "two", URL: "u2"}}
	if s, err := Select(list, ""); err != nil || s.Name != "one" {
		t.Errorf("default = %+v %v", s, err)
	}
	if s, err := Select(list, "two"); err != nil || s.Name != "two" {
		t.Errorf("by name = %+v %v", s, err)
	}
	if _, err := Select(list, "nope"); err == nil {
		t.Error("unknown should error")
	}
	if _, err := Select(nil, ""); err == nil {
		t.Error("empty list should error")
	}
}
