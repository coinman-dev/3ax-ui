package nginx

import (
	"slices"
	"testing"
)

func TestParsePorts(t *testing.T) {
	got := ParsePorts(" 53, 8000-8100; bogus, 70000, 9000-8000, 53, 443 ")
	want := []PortRange{{From: 53, To: 53}, {From: 8000, To: 8100}, {From: 443, To: 443}}
	if !slices.Equal(got, want) {
		t.Errorf("ParsePorts = %v, want %v", got, want)
	}
}
