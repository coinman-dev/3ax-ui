package amneziawgkern

import (
	"os"
	"strings"
	"testing"
	"time"
)

// TestInstallCommandNamesThePanelBinary: /usr/bin/x-ui is the management shell
// script, whose case list has no install-awg-kernel -- it just opens the menu.
func TestInstallCommandNamesThePanelBinary(t *testing.T) {
	resetReadinessCache()
	got := HostReadiness().InstallCommand

	exe, err := os.Executable()
	if err != nil {
		t.Skipf("os.Executable unavailable: %v", err)
	}
	if !strings.Contains(got, exe) {
		t.Errorf("InstallCommand = %q, want it to name the running binary %q", got, exe)
	}
	if !strings.HasSuffix(got, " install-awg-kernel") {
		t.Errorf("InstallCommand = %q, want it to end with the subcommand", got)
	}
}

// TestHostReadinessIsCached keeps the form's polling from re-running modprobe
// on every open.
func TestHostReadinessIsCached(t *testing.T) {
	resetReadinessCache()

	HostReadiness()
	readinessMu.Lock()
	first := readinessAt
	readinessMu.Unlock()
	if first.IsZero() {
		t.Fatal("first call must record a probe time")
	}

	HostReadiness()
	readinessMu.Lock()
	second := readinessAt
	readinessMu.Unlock()
	if !second.Equal(first) {
		t.Error("a call inside the TTL must reuse the cached result, not re-probe")
	}

	// Age the entry past the TTL and confirm it re-probes.
	readinessMu.Lock()
	readinessAt = time.Now().Add(-2 * readinessTTL)
	readinessMu.Unlock()
	HostReadiness()
	readinessMu.Lock()
	third := readinessAt
	readinessMu.Unlock()
	if third.Equal(first) {
		t.Error("an expired entry must be re-probed")
	}
}

// TestPreflightAddsInstanceBlockersToHostOnes: the two kinds are collected in
// one pass so the admin sees everything to fix at once. 3AX-UI runs IPv6 and
// port forwarding in kernel mode, so the listen address is the instance one left.
func TestPreflightAddsInstanceBlockers(t *testing.T) {
	resetReadinessCache()

	pinned := testInstance()
	pinned.Listen = "203.0.113.10"
	if err := Preflight(pinned, false); err == nil || !strings.Contains(err.Error(), "cannot bind to the configured listen address") {
		t.Errorf("Preflight must refuse a pinned listen address the kernel backend cannot honor, got %v", err)
	}
}

// TestPreflightDoesNotInventInstanceBlockers: a plain instance must only ever
// be refused for host reasons, never for a feature it is not using.
func TestPreflightDoesNotInventInstanceBlockers(t *testing.T) {
	resetReadinessCache()

	if err := Preflight(testInstance(), false); err != nil {
		if strings.Contains(err.Error(), "IPv6") || strings.Contains(err.Error(), "port forwarding") {
			t.Errorf("plain instance must not trip feature gates, got %v", err)
		}
	}
}

// TestPreflightAcceptsKernelExtras: IPv6, port forwarding and routeThroughXray
// have a kernel datapath in 3AX-UI, so none of them may block kernel mode.
func TestPreflightAcceptsKernelExtras(t *testing.T) {
	resetReadinessCache()

	inst := testInstance()
	inst.IPv6Enabled = true
	inst.Address = append(inst.Address, "2001:db8::1/112")
	inst.Peers[0].AllowedIPs = append(inst.Peers[0].AllowedIPs, "2001:db8::2/128")
	inst.Peers[0].ForwardedPorts = "443"
	if err := ValidateInstanceFeatures(inst, true); err != nil {
		t.Errorf("kernel extras refused: %v", err)
	}
}
