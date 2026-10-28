package tools

import "testing"

/**
 * A result's `view` must open the panel ON WHAT IT WAS ABOUT.
 *
 * The machine NAME is resolved by the tool — a call may name one or fall back to the only configured
 * one — so it exists in no argument and no manifest template could express it. The job id likewise.
 */
func TestSandboxView(t *testing.T) {
	v := sandboxView("windows")
	if v == nil {
		t.Fatal("no view")
	}
	if v["ext"] != "sandbox" {
		t.Errorf("ext = %v, want sandbox", v["ext"])
	}
	if v["route"] != "/sb/windows" {
		t.Errorf("route = %v, want /sb/windows", v["route"])
	}
}

// A job is only unique within a machine, so BOTH values are needed or the panel would open on the
// wrong output — or on nothing.
func TestJobViewNeedsBothValues(t *testing.T) {
	v := jobView("windows", "abc123")
	if v == nil {
		t.Fatal("no view")
	}
	if v["route"] != "/sb/windows/job/abc123" {
		t.Errorf("route = %v, want /sb/windows/job/abc123", v["route"])
	}
	if jobView("", "abc123") != nil {
		t.Error("a job with no machine produced a view")
	}
	if jobView("windows", "") != nil {
		t.Error("a view with no job id produced a view")
	}
}

// No machine means no button: a link to a panel with nothing to open is worse than none.
func TestSandboxViewNeedsAName(t *testing.T) {
	if v := sandboxView("  "); v != nil {
		t.Errorf("an empty name produced a view: %#v", v)
	}
}

// The name is escaped, so a machine whose name contains a "/" cannot reshape the route into a path
// that means something else.
func TestSandboxViewEscapesTheName(t *testing.T) {
	v := sandboxView("a/b")
	if v == nil {
		t.Fatal("no view")
	}
	if v["route"] != "/sb/a%2Fb" {
		t.Errorf("route = %v, want the name escaped as one segment", v["route"])
	}
}
