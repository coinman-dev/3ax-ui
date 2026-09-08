package tunnel

import (
	"fmt"
	"strings"
	"testing"
)

// fakeKernel stands in for ip(8) and awg(8): it records what was run and
// decides whether the module accepts the 3.0 parameters.
type fakeKernel struct {
	ran       []string
	accepts   bool
	cannotAdd bool
}

func (f *fakeKernel) install(t *testing.T) {
	t.Helper()
	prev := runCmd
	runCmd = func(name string, args ...string) ([]byte, error) {
		line := name + " " + strings.Join(args, " ")
		f.ran = append(f.ran, line)
		switch {
		case strings.HasPrefix(line, "ip link add"):
			if f.cannotAdd {
				return []byte("operation not permitted"), fmt.Errorf("exit 2")
			}
		case strings.Contains(line, "setconf"):
			if !f.accepts {
				return []byte("Unable to modify interface: Invalid argument"), fmt.Errorf("exit 1")
			}
		}
		return nil, nil
	}
	// The answer is cached for the life of the process, which is right in
	// production and useless in a test.
	v3Mu.Lock()
	v3Cache = map[string]bool{}
	v3Mu.Unlock()
	t.Cleanup(func() {
		runCmd = prev
		v3Mu.Lock()
		v3Cache = map[string]bool{}
		v3Mu.Unlock()
	})
}

// TestSupportsV3AsksTheKernel is the fix for a live outage: the tools called
// themselves 3.1 while the module was 3.0, the panel believed the tools, and
// the module refused the 3.0 keys with a bare EINVAL. Version numbers cannot
// answer this question — only the module can.
func TestSupportsV3AsksTheKernel(t *testing.T) {
	t.Run("module takes them", func(t *testing.T) {
		f := &fakeKernel{accepts: true}
		f.install(t)
		if !SupportsV3(AWG) {
			t.Error("a kernel that accepts the 3.0 parameters was reported as not supporting them")
		}
	})

	t.Run("module refuses them", func(t *testing.T) {
		f := &fakeKernel{accepts: false}
		f.install(t)
		if SupportsV3(AWG) {
			t.Error("a kernel that refuses the 3.0 parameters was reported as supporting them")
		}
	})

	t.Run("no interface to probe with", func(t *testing.T) {
		f := &fakeKernel{accepts: true, cannotAdd: true}
		f.install(t)
		if SupportsV3(AWG) {
			t.Error("a host where the probe cannot even be built must fall back to 2.0")
		}
	})

	// WireGuard has no obfuscation at all, so there is nothing to ask about
	// and nothing to build.
	t.Run("native wireguard is never asked", func(t *testing.T) {
		f := &fakeKernel{accepts: true}
		f.install(t)
		if SupportsV3(WG) {
			t.Error("native WireGuard was reported as supporting AmneziaWG 3.0")
		}
		if len(f.ran) != 0 {
			t.Errorf("the probe ran for a flavour with no obfuscation: %v", f.ran)
		}
	})
}

// TestProbeCleansUpAfterItself: the scratch interface must not outlive the
// probe, and a leftover from a probe that was killed must not stop the next one.
func TestProbeCleansUpAfterItself(t *testing.T) {
	f := &fakeKernel{accepts: true}
	f.install(t)
	SupportsV3(AWG)

	joined := strings.Join(f.ran, "\n")
	if !strings.Contains(joined, "ip link add "+v3ProbeIface) {
		t.Fatalf("the probe never built an interface:\n%s", joined)
	}
	// Once before, in case an earlier probe died holding the name, and once
	// after, so nothing is left behind.
	if got := strings.Count(joined, "ip link del dev "+v3ProbeIface); got != 2 {
		t.Errorf("deleted the scratch interface %d times, want 2 (before and after):\n%s", got, joined)
	}
	if strings.Index(joined, "ip link add") > strings.Index(joined, "setconf") {
		t.Error("the interface was configured before it was created")
	}
}

// TestProbeIsCached: it runs commands as root and is asked for on every status
// poll, so it must happen once.
func TestProbeIsCached(t *testing.T) {
	f := &fakeKernel{accepts: true}
	f.install(t)

	for range 5 {
		SupportsV3(AWG)
	}
	if got := strings.Count(strings.Join(f.ran, "\n"), "ip link add"); got != 1 {
		t.Errorf("the probe ran %d times, want 1", got)
	}
}
