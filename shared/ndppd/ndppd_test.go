package ndppd

import (
	"strings"
	"testing"
)

// TestSectionsCoexist is the property that matters most: AWG and native WG
// share /etc/ndppd.conf, and so may the operator's own rules. Writing one
// section must never disturb the other, and updating a section must replace it
// rather than append a second copy.
func TestSectionsCoexist(t *testing.T) {
	awg := NewSection("AWG")
	wg := NewSection("WG")

	manual := "route-ttl 30000\n\nproxy eth0 {\n    rule 2001:db8:manual::/64 {\n        iface eth0\n    }\n}\n"

	withAwg := awg.merge([]byte(manual), awg.Render("eth0", "awg0", "2001:db8:a::/64"))
	if !strings.Contains(withAwg, "2001:db8:manual::/64") {
		t.Fatal("hand-written rule lost when the AWG section was added")
	}

	withBoth := wg.merge([]byte(withAwg), wg.Render("eth0", "wg0", "2001:db8:b::/64"))
	for _, want := range []string{"2001:db8:manual::/64", "2001:db8:a::/64", "2001:db8:b::/64", "iface awg0", "iface wg0"} {
		if !strings.Contains(withBoth, want) {
			t.Errorf("missing %q after adding both sections", want)
		}
	}

	// Re-applying AWG replaces its block instead of duplicating it.
	updated := awg.merge([]byte(withBoth), awg.Render("eth0", "awg0", "2001:db8:c::/64"))
	if n := strings.Count(updated, "# --- BEGIN AWG ---"); n != 1 {
		t.Errorf("AWG section present %d times after an update, want 1", n)
	}
	if strings.Contains(updated, "2001:db8:a::/64") {
		t.Error("old AWG pool still present after the update")
	}
	for _, want := range []string{"2001:db8:manual::/64", "2001:db8:b::/64", "2001:db8:c::/64"} {
		if !strings.Contains(updated, want) {
			t.Errorf("update dropped %q", want)
		}
	}

	// Removing AWG leaves WG and the manual rule alone.
	left := awg.remove([]byte(updated))
	if strings.Contains(left, "BEGIN AWG") {
		t.Error("AWG section survived removal")
	}
	for _, want := range []string{"2001:db8:manual::/64", "2001:db8:b::/64"} {
		if !strings.Contains(left, want) {
			t.Errorf("removal of the AWG section dropped %q", want)
		}
	}
}

// TestEmptyConfigGetsHeader covers the fresh-file path, and TestRemoveToEmpty
// the "nothing left" state Stop() uses to decide whether to stop the daemon.
func TestEmptyConfigGetsHeader(t *testing.T) {
	awg := NewSection("AWG")
	out := awg.merge(nil, awg.Render("eth0", "awg0", "2001:db8::/64"))
	if !strings.HasPrefix(out, "route-ttl 30000") {
		t.Errorf("new config is missing the route-ttl header:\n%s", out)
	}
}

func TestRemoveToEmpty(t *testing.T) {
	awg := NewSection("AWG")
	only := awg.merge(nil, awg.Render("eth0", "awg0", "2001:db8::/64"))
	left := awg.remove([]byte(only))
	if left != "route-ttl 30000" {
		t.Errorf("expected only the header to remain, got %q", left)
	}
}
