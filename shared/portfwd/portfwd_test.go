package portfwd

import (
	"reflect"
	"testing"
)

func TestParse(t *testing.T) {
	cases := []struct {
		in   string
		want []PortSpec
	}{
		{"", nil},
		{"  ", nil},
		{"80", []PortSpec{{80, 80}}},
		{"80, 443", []PortSpec{{80, 80}, {443, 443}}},
		{"80; 443", []PortSpec{{80, 80}, {443, 443}}},
		{"80,443;22", []PortSpec{{80, 80}, {443, 443}, {22, 22}}},
		{" 8000-8100 ", []PortSpec{{8000, 8100}}},
		{"80, 80, 443", []PortSpec{{80, 80}, {443, 443}}}, // dedup
		{"abc, 80", []PortSpec{{80, 80}}},                 // garbage dropped
		{"0, 65536, 70000", nil},                          // out of range
		{"100-50", nil},                                   // reversed
		{"27000-27050; 27015-27030", []PortSpec{{27000, 27050}, {27015, 27030}}},
	}
	for _, tc := range cases {
		got := Parse(tc.in)
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("Parse(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestNormalize(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"", ""},
		{"80, 80, 443", "80,443"},
		{" 8000-8100 ; 80 ", "8000-8100,80"},
		{"abc", ""},
	}
	for _, tc := range cases {
		got := Normalize(tc.in)
		if got != tc.want {
			t.Errorf("Normalize(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestRulesShape(t *testing.T) {
	specs := Parse("80, 8000-8100")
	rs := Rules("eth0", "wg0", "10.66.66.2/32", "uuid-1", specs)
	// 2 specs * 2 protocols = 4 nat rules and 4 forward rules.
	if got := len(rs.Nat); got != 4 {
		t.Fatalf("Nat rules: got %d, want 4", got)
	}
	if got := len(rs.Forward); got != 4 {
		t.Fatalf("Forward rules: got %d, want 4", got)
	}
	// PostUp / PostDown lines should mirror count and start with iptables -A / -D.
	up := PostUpLines(rs)
	down := PostDownLines(rs)
	if len(up) != 8 || len(down) != 8 {
		t.Fatalf("Post lines: up=%d down=%d, want 8/8", len(up), len(down))
	}
	for _, line := range up {
		if line[:11] != "iptables -t" && line[:11] != "iptables -A" {
			t.Errorf("up line missing -A/-t prefix: %s", line)
		}
	}
}

func TestRulesEmptyAndInvalid(t *testing.T) {
	if rs := Rules("eth0", "wg0", "10.66.66.2", "u", nil); len(rs.Nat) != 0 {
		t.Error("nil specs should produce no rules")
	}
	if rs := Rules("eth0", "wg0", "", "u", []PortSpec{{80, 80}}); len(rs.Nat) != 0 {
		t.Error("empty client IP should produce no rules")
	}
}
