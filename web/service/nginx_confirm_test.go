package service

import (
	"strconv"
	"testing"
	"time"

	"github.com/coinman-dev/3ax-ui/v2/nginx"
)

func off() NginxSettings { return NginxSettings{Mode: string(nginx.ModeOff)} }

// TestArmsConfirmationOnlyWhenSomethingCloses. A confirmation prompt that
// appears when nothing is at stake teaches the operator to click it without
// reading, which is exactly what must not happen the one time it matters.
func TestArmsConfirmationOnlyWhenSomethingCloses(t *testing.T) {
	shared := NginxSettings{Mode: string(nginx.ModeShared)}
	closed := NginxSettings{Mode: string(nginx.ModeOnly443), ManageFirewall: true}
	closedPanel := NginxSettings{Mode: string(nginx.ModeOnly443), ManageFirewall: true, PanelBehind443: true}
	only443Open := NginxSettings{Mode: string(nginx.ModeOnly443), PanelBehind443: true}

	cases := []struct {
		name     string
		from, to NginxSettings
		want     bool
	}{
		{"nothing to nothing", off(), shared, false},
		{"the panel published while every port stays open", shared, only443Open, false},
		{"the firewall comes on", shared, closed, true},
		{"the firewall was already on", closed, closed, false},
		{"the panel's own port goes away", closed, closedPanel, true},
		{"the panel comes back out from behind 443", closedPanel, closed, false},
		{"the firewall goes off again", closed, shared, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := armsConfirmation(tc.from, tc.to); got != tc.want {
				t.Errorf("armsConfirmation = %v, want %v", got, tc.want)
			}
		})
	}
}

// arm writes a pending confirmation with the given deadline.
func arm(t *testing.T, svc *NginxService, fallback NginxSettings, deadline time.Time) {
	t.Helper()
	if err := svc.armConfirmation(fallback); err != nil {
		t.Fatalf("armConfirmation: %v", err)
	}
	set := SettingService{}
	if err := set.setString("nginxConfirmDeadline", strconv.FormatInt(deadline.UnixMilli(), 10)); err != nil {
		t.Fatalf("set deadline: %v", err)
	}
}

// catchRollback stands in for the real thing and records what it was asked to
// put back.
func catchRollback(t *testing.T) *[]NginxSettings {
	t.Helper()
	var got []NginxSettings
	prev := rollBack
	rollBack = func(_ *NginxService, to NginxSettings) error {
		got = append(got, to)
		return nil
	}
	t.Cleanup(func() { rollBack = prev })
	return &got
}

// TestRollsBackWhenNobodyConfirms is the whole point: an operator who has just
// closed the port they were reading the panel through gets the server back
// without having to reach it at all.
func TestRollsBackWhenNobodyConfirms(t *testing.T) {
	svc := newNginxTestServer(t)
	rolled := catchRollback(t)
	fallback := NginxSettings{Mode: string(nginx.ModeShared), Domain: "vpn.example.com", RealityPort: 8443}

	// Still inside the window: nothing happens yet.
	arm(t, svc, fallback, time.Now().Add(time.Minute))
	svc.CheckConfirmation()
	if len(*rolled) != 0 {
		t.Fatalf("rolled back while there was still time: %+v", *rolled)
	}
	if svc.PendingConfirmation() == 0 {
		t.Fatal("the pending confirmation was forgotten while it was still pending")
	}

	// Past it: back the way it was, and nothing left to retry.
	arm(t, svc, fallback, time.Now().Add(-time.Second))
	svc.CheckConfirmation()
	if len(*rolled) != 1 {
		t.Fatalf("expected exactly one rollback, got %d", len(*rolled))
	}
	if (*rolled)[0].Mode != fallback.Mode || (*rolled)[0].Domain != fallback.Domain {
		t.Errorf("rolled back to %+v, want %+v", (*rolled)[0], fallback)
	}
	if svc.PendingConfirmation() != 0 {
		t.Error("the deadline is still set, so the rollback would run again on the next tick")
	}

	// And a second tick does nothing at all.
	svc.CheckConfirmation()
	if len(*rolled) != 1 {
		t.Errorf("rolled back again on the next tick: %d times", len(*rolled))
	}
}

func TestConfirmKeepsTheNewSettings(t *testing.T) {
	svc := newNginxTestServer(t)
	rolled := catchRollback(t)

	arm(t, svc, off(), time.Now().Add(-time.Second))
	if err := svc.Confirm(); err != nil {
		t.Fatalf("Confirm: %v", err)
	}
	if svc.PendingConfirmation() != 0 {
		t.Error("confirming left the deadline in place")
	}
	svc.CheckConfirmation()
	if len(*rolled) != 0 {
		t.Errorf("rolled back although it was confirmed: %+v", *rolled)
	}
}
