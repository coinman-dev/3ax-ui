package service

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coinman-dev/3ax-ui/v2/database"
	"github.com/coinman-dev/3ax-ui/v2/database/model"
	"github.com/coinman-dev/3ax-ui/v2/nginx"
)

const (
	realityStream = `{"network":"tcp","security":"reality","realitySettings":{"show":false,` +
		`"dest":"www.icloud.com:443","serverNames":["www.icloud.com","apple.com"],` +
		`"privateKey":"PRIV","shortIds":["ab"]},"tcpSettings":{"header":{"type":"none"}}}`
	mtprotoSettings = `{"fakeTlsDomain":"www.samsung.com","routeThroughXray":true,"routeXrayPort":39167}`
)

func newNginxTestServer(t *testing.T) *NginxService {
	t.Helper()
	if err := database.InitDB(filepath.Join(t.TempDir(), "x-ui.db")); err != nil {
		t.Fatalf("InitDB: %v", err)
	}
	return &NginxService{}
}

func seedInbounds(t *testing.T) (reality, mt *model.Inbound) {
	t.Helper()
	db := database.GetDB()
	reality = &model.Inbound{
		UserId: 1, Remark: "ru-vpn", Enable: true, Listen: "", Port: 443,
		Protocol: model.VLESS, Tag: "inbound-443",
		Settings: `{"clients":[]}`, StreamSettings: realityStream,
	}
	mt = &model.Inbound{
		UserId: 1, Remark: "Telegram-via-nl", Enable: true, Listen: "", Port: 4343,
		Protocol: model.MTProto, Tag: "inbound-4343", Settings: mtprotoSettings,
	}
	// An inbound the front-end must not touch: UDP cannot share a TCP port.
	wg := &model.Inbound{
		UserId: 1, Remark: "awg", Enable: true, Port: 55200,
		Protocol: model.AmneziaWG, Tag: "inbound-55200", Settings: `{}`,
	}
	for _, ib := range []*model.Inbound{reality, mt, wg} {
		if err := db.Create(ib).Error; err != nil {
			t.Fatalf("create %s: %v", ib.Remark, err)
		}
	}
	return reality, mt
}

func TestCollectRoutes(t *testing.T) {
	s := newNginxTestServer(t)
	seedInbounds(t)

	routes, warnings := s.collectRoutes(NginxSettings{Mode: "shared", RealityPort: 8443})
	if len(warnings) != 0 {
		t.Errorf("unexpected warnings: %v", warnings)
	}
	if len(routes) != 2 {
		t.Fatalf("got %d routes, want 2 (Reality and MTProto; the UDP tunnel must be skipped)", len(routes))
	}

	reality, mt := routes[0], routes[1]
	if reality.Protocol != string(model.VLESS) {
		reality, mt = mt, reality
	}

	if got := strings.Join(reality.SNIs, ","); got != "www.icloud.com,apple.com" {
		t.Errorf("Reality cover domains = %q", got)
	}
	if !reality.Fallback {
		t.Error("Reality must take the unmatched connections — it hides better than our own certificate")
	}
	if reality.Port != 8443 || reality.Listen != "127.0.0.1" {
		t.Errorf("Reality moves to %s:%d, want 127.0.0.1:8443", reality.Listen, reality.Port)
	}

	if got := strings.Join(mt.SNIs, ","); got != "www.samsung.com" {
		t.Errorf("MTProto cover domain = %q", got)
	}
	if mt.Fallback {
		t.Error("MTProto must not be the fallback: an unknown name would get a FakeTLS answer")
	}
	if mt.Port != 4343 {
		t.Errorf("MTProto port = %d, want it left alone at 4343", mt.Port)
	}
}

// TestCollectRoutesReportsSharedCoverDomain guards the one mistake nginx cannot
// catch by itself: two protocols behind one server name means the map keeps one
// and the other goes quietly dark.
func TestCollectRoutesReportsSharedCoverDomain(t *testing.T) {
	s := newNginxTestServer(t)
	_, mt := seedInbounds(t)

	if err := database.GetDB().Model(model.Inbound{}).Where("id = ?", mt.Id).
		Update("settings", `{"fakeTlsDomain":"APPLE.com"}`).Error; err != nil {
		t.Fatal(err)
	}

	_, warnings := s.collectRoutes(NginxSettings{Mode: "shared", RealityPort: 8443})
	if len(warnings) == 0 {
		t.Fatal("two inbounds share a cover domain and nothing was reported")
	}
	if warnings[0].Code != "sniConflict" {
		t.Errorf("unexpected warning %q", warnings[0].Code)
	}
	if !strings.Contains(strings.ToLower(strings.Join(warnings[0].Params, " ")), "apple.com") {
		t.Errorf("the warning does not name the domain: %v", warnings[0].Params)
	}
}

func TestDisabledInboundsAreNotRouted(t *testing.T) {
	s := newNginxTestServer(t)
	reality, _ := seedInbounds(t)
	if err := database.GetDB().Model(model.Inbound{}).Where("id = ?", reality.Id).
		Update("enable", false).Error; err != nil {
		t.Fatal(err)
	}
	routes, _ := s.collectRoutes(NginxSettings{Mode: "shared", RealityPort: 8443})
	for _, r := range routes {
		if r.InboundId == reality.Id {
			t.Error("a disabled inbound was routed; nginx would send clients to a closed port")
		}
	}
}

// TestRelocateAndRestoreRoundTrip is the promise the mode switch makes: turning
// the front-end off has to put every inbound back exactly where it was,
// including a listen address the operator set by hand.
func TestRelocateAndRestoreRoundTrip(t *testing.T) {
	s := newNginxTestServer(t)
	reality, mt := seedInbounds(t)
	if err := database.GetDB().Model(model.Inbound{}).Where("id = ?", mt.Id).
		Update("listen", "10.0.0.5").Error; err != nil {
		t.Fatal(err)
	}

	set := NginxSettings{Mode: "shared", RealityPort: 8443}
	if moved, err := s.relocateInbounds(set, false); err != nil {
		t.Fatalf("relocate: %v", err)
	} else if !moved {
		t.Fatal("relocate reported nothing moved, but the inbounds were on their own ports")
	}

	moved, err := inboundByID(reality.Id)
	if err != nil {
		t.Fatal(err)
	}
	if moved.Port != 8443 || moved.Listen != "127.0.0.1" {
		t.Errorf("Reality is on %s:%d, want 127.0.0.1:8443", moved.Listen, moved.Port)
	}
	if moved.PublicPort != PublicPort {
		t.Errorf("PublicPort = %d, want %d so the links keep working", moved.PublicPort, PublicPort)
	}
	if moved.Tag != "inbound-443" {
		t.Errorf("the tag changed to %q; routing rules point at it by name", moved.Tag)
	}
	if jsonFlag(t, moved.StreamSettings, "sockopt", "acceptProxyProtocol") {
		t.Error("acceptProxyProtocol is on for Reality: XTLS REALITY breaks when PROXY protocol header is injected")
	}
	if !strings.Contains(moved.StreamSettings, "realitySettings") {
		t.Error("the Reality settings were lost while setting one flag")
	}

	// The MTProto inbound was not on the public port, so it keeps its own and
	// is left completely alone: it goes on serving the clients it already has,
	// and gains a second way in through 443. Moving it, or telling it to expect
	// a PROXY header, would break every direct client — which is exactly the
	// failure that looks like "green in the panel, does not connect".
	untouched, err := inboundByID(mt.Id)
	if err != nil {
		t.Fatal(err)
	}
	if untouched.Listen != "10.0.0.5" || untouched.Port != 4343 {
		t.Errorf("the MTProto inbound moved to %s:%d; it should have stayed put",
			untouched.Listen, untouched.Port)
	}
	if untouched.PublicPort != 0 {
		t.Errorf("PublicPort = %d; its links must keep pointing at its own port", untouched.PublicPort)
	}
	if jsonFlag(t, untouched.Settings, "proxyProtocolListener") {
		t.Error("mtg was told to expect a PROXY header, which direct clients do not send")
	}

	// Applying twice must not overwrite the remembered original state.
	if moved, err := s.relocateInbounds(set, false); err != nil {
		t.Fatalf("second relocate: %v", err)
	} else if moved {
		t.Error("a second relocate reported a change; the reconcile job would restart xray every tick")
	}

	if err := s.restoreInbounds(); err != nil {
		t.Fatalf("restore: %v", err)
	}
	back, err := inboundByID(reality.Id)
	if err != nil {
		t.Fatal(err)
	}
	if back.Port != 443 || back.Listen != "" {
		t.Errorf("Reality came back as %q:%d, want the original :443", back.Listen, back.Port)
	}
	if back.PublicPort != 0 {
		t.Errorf("PublicPort = %d, want 0 so links go back to the real port", back.PublicPort)
	}
	if jsonFlag(t, back.StreamSettings, "sockopt", "acceptProxyProtocol") {
		t.Error("acceptProxyProtocol stayed on with no nginx in front — every client would be refused")
	}
	backMt, err := inboundByID(mt.Id)
	if err != nil {
		t.Fatal(err)
	}
	if backMt.Listen != "10.0.0.5" || backMt.Port != 4343 {
		t.Errorf("the untouched inbound changed anyway: %s:%d", backMt.Listen, backMt.Port)
	}
	if len(s.loadSnapshots()) != 0 {
		t.Error("the saved state was not cleared after restoring")
	}
}

func TestSetJSONFlagKeepsEverythingElse(t *testing.T) {
	out, err := setJSONFlag(`{"network":"tcp","sockopt":{"tproxy":"off"},"extra":[1,2]}`,
		true, "sockopt", "acceptProxyProtocol")
	if err != nil {
		t.Fatalf("setJSONFlag: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("the result is not valid JSON: %v", err)
	}
	sockopt := doc["sockopt"].(map[string]any)
	if sockopt["acceptProxyProtocol"] != true {
		t.Error("the flag was not set")
	}
	if sockopt["tproxy"] != "off" {
		t.Error("a sibling key inside sockopt was dropped")
	}
	if doc["network"] != "tcp" || doc["extra"] == nil {
		t.Error("unrelated keys were dropped")
	}

	// An empty document is normal for an inbound that never had stream settings.
	if _, err := setJSONFlag("", true, "sockopt", "acceptProxyProtocol"); err != nil {
		t.Errorf("empty settings should be fine: %v", err)
	}
	if _, err := setJSONFlag("not json", true, "sockopt"); err == nil {
		t.Error("unreadable settings must be reported, not silently replaced")
	}
}

func TestSaveSettingsRejectsTheirOwnPort(t *testing.T) {
	s := newNginxTestServer(t)
	err := s.SaveSettings(NginxSettings{Mode: "shared", RealityPort: PublicPort})
	if err == nil {
		t.Fatal("the internal port was allowed to be the public one, which is a port conflict with nginx itself")
	}
	if err := s.SaveSettings(NginxSettings{Mode: "nope", RealityPort: 8443}); err == nil {
		t.Error("an unknown mode was accepted")
	}
	if err := s.SaveSettings(NginxSettings{Mode: "shared", Domain: " example.net ", RealityPort: 8443}); err != nil {
		t.Fatalf("a valid save failed: %v", err)
	}
	if got := s.GetSettings(); got.Domain != "example.net" {
		t.Errorf("domain = %q, want it trimmed", got.Domain)
	}
}

func jsonFlag(t *testing.T, raw string, path ...string) bool {
	t.Helper()
	doc := map[string]any{}
	if strings.TrimSpace(raw) == "" {
		return false
	}
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		t.Fatalf("unreadable JSON: %v", err)
	}
	var node any = doc
	for _, key := range path {
		m, ok := node.(map[string]any)
		if !ok {
			return false
		}
		node = m[key]
	}
	flag, _ := node.(bool)
	return flag
}

// TestPublicPortIsReservedWhileTheFrontEndIsOn: once nginx holds 443 no inbound
// is in the table claiming it, so without this guard the port form would happily
// let someone create one — and Xray would fail to bind, taking down everything
// behind 443 rather than just the new inbound.
func TestPublicPortIsReservedWhileTheFrontEndIsOn(t *testing.T) {
	s := newNginxTestServer(t)
	seedInbounds(t)
	inbounds := &InboundService{}

	if err := s.SaveSettings(NginxSettings{Mode: "off", RealityPort: 8443}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.relocateInbounds(NginxSettings{Mode: "shared", RealityPort: 8443}, false); err != nil {
		t.Fatalf("relocate: %v", err)
	}

	// Front-end off: 443 is free now that the Reality inbound moved away.
	taken, err := inbounds.checkPortExist("", PublicPort, 0)
	if err != nil {
		t.Fatal(err)
	}
	if taken {
		t.Error("443 is reported taken while the front-end is off and no inbound holds it")
	}

	if err := s.SaveSettings(NginxSettings{Mode: "shared", RealityPort: 8443}); err != nil {
		t.Fatal(err)
	}
	taken, err = inbounds.checkPortExist("", PublicPort, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !taken {
		t.Error("443 must be reserved while nginx is in front of it")
	}
}

// TestReconcileDryRunDoesNotMoveAnything: the reconcile job asks "would
// anything move?" before deciding to act. If that question moved the inbounds
// as a side effect, a server where the front-end cannot come up would be left
// with its inbounds on the loopback and nothing listening in front of them.
func TestReconcileDryRunDoesNotMoveAnything(t *testing.T) {
	s := newNginxTestServer(t)
	reality, _ := seedInbounds(t)
	set := NginxSettings{Mode: "shared", RealityPort: 8443}

	needsMove, err := s.relocateInbounds(set, true)
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if !needsMove {
		t.Error("the inbounds are on their own ports, so a move is needed")
	}

	untouched, err := inboundByID(reality.Id)
	if err != nil {
		t.Fatal(err)
	}
	if untouched.Port != 443 || untouched.Listen != "" || untouched.PublicPort != 0 {
		t.Errorf("the dry run moved the inbound anyway: %s:%d public=%d",
			untouched.Listen, untouched.Port, untouched.PublicPort)
	}
	if len(s.loadSnapshots()) != 0 {
		t.Error("the dry run recorded a snapshot; a later restore would act on a move that never happened")
	}

	// After a real move it must answer "nothing to do".
	if _, err := s.relocateInbounds(set, false); err != nil {
		t.Fatalf("relocate: %v", err)
	}
	needsMove, err = s.relocateInbounds(set, true)
	if err != nil {
		t.Fatalf("second dry run: %v", err)
	}
	if needsMove {
		t.Error("everything is already in place, but a move is still reported — the job would apply on every tick")
	}
}

// TestResetSettingsKeepsTheFrontEndState: "reset all settings" must not forget
// where the inbounds physically are. With nginx still serving 443 and the
// inbounds on the loopback, losing nginxRelocated leaves nothing that remembers
// the ports they came from — and no way back short of editing the database.
func TestResetSettingsKeepsTheFrontEndState(t *testing.T) {
	s := newNginxTestServer(t)
	seedInbounds(t)

	if err := s.SaveSettings(NginxSettings{Mode: "shared", Domain: "example.net", RealityPort: 8443}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.relocateInbounds(NginxSettings{Mode: "shared", RealityPort: 8443}, false); err != nil {
		t.Fatal(err)
	}
	var setting SettingService
	if err := setting.setString("subTitle", "should not survive"); err != nil {
		t.Fatal(err)
	}

	if err := setting.ResetSettings(); err != nil {
		t.Fatalf("ResetSettings: %v", err)
	}

	if got := s.GetSettings(); got.Mode != "shared" {
		t.Errorf("mode = %q after a reset, want it kept — nginx is still in front of 443", got.Mode)
	}
	if len(s.loadSnapshots()) == 0 {
		t.Error("the record of where the inbounds came from was deleted; they could not be put back")
	}

	// Ordinary preferences still go.
	if v, err := setting.getString("subTitle"); err == nil && v == "should not survive" {
		t.Error("the reset kept an ordinary setting")
	}

	// And the way back still works.
	if err := s.restoreInbounds(); err != nil {
		t.Fatalf("restore after a reset: %v", err)
	}
	back, err := inboundByID(1)
	if err != nil {
		t.Fatal(err)
	}
	if back.Port != 443 || back.PublicPort != 0 {
		t.Errorf("the inbound came back as :%d public=%d, want :443 public=0", back.Port, back.PublicPort)
	}
}

// TestDualRoutesKeepTheirOwnPort is the promise the mode's name makes: only the
// inbound that was sitting on the public port has to give it up. Everything
// else answers on both its own port and 443, so the links already handed out go
// on working and nothing has to be reissued.
func TestDualRoutesKeepTheirOwnPort(t *testing.T) {
	s := newNginxTestServer(t)
	reality, mt := seedInbounds(t)
	set := NginxSettings{Mode: "shared", RealityPort: 8443}

	routes, warnings := s.collectRoutes(set)
	if len(warnings) != 0 {
		t.Errorf("unexpected warnings: %v", warnings)
	}
	byId := map[int]NginxRoute{}
	for _, r := range routes {
		byId[r.InboundId] = r
	}

	if r := byId[reality.Id]; r.Dual {
		t.Error("the Reality inbound was on 443; it cannot keep a port nginx needs")
	} else if r.Port != 8443 || r.Listen != "127.0.0.1" {
		t.Errorf("Reality moves to %s:%d, want 127.0.0.1:8443", r.Listen, r.Port)
	}

	if r := byId[mt.Id]; !r.Dual {
		t.Error("the MTProto inbound was on its own port and should have kept it")
	} else if r.Port != 4343 || r.Listen != "" {
		t.Errorf("MTProto ended up on %q:%d; it should not have been touched", r.Listen, r.Port)
	}

	// The generated config has to reflect that: a relay for the inbound that
	// serves direct clients, none for the one that only nginx can reach.
	cfg, err := s.buildConfig(set)
	if err != nil {
		t.Fatalf("buildConfig: %v", err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("the generated config is invalid: %v", err)
	}
	for _, r := range cfg.Routes {
		if r.Relay == "" {
			t.Errorf("%s was not given a relay to strip the PROXY protocol header", r.Name)
		}
	}

	// The relay port is chosen once and remembered, or the reconcile job would
	// see a changed config and reload nginx every half minute.
	again, err := s.buildConfig(set)
	if err != nil {
		t.Fatal(err)
	}
	first, _ := cfg.StreamConf()
	second, _ := again.StreamConf()
	if first != second {
		t.Error("two builds produced different configs; the relay port is wandering")
	}
}

// TestClassificationSurvivesAnApply is the trap the port alone sets: once the
// inbound that gave up the public port has moved, it sits on an ordinary port
// like any other. Judging by the port alone would call it dual on the next
// pass, hand it a relay that strips the PROXY header it requires, and refuse
// every client — with nothing in the logs to say why.
func TestClassificationSurvivesAnApply(t *testing.T) {
	s := newNginxTestServer(t)
	reality, mt := seedInbounds(t)
	set := NginxSettings{Mode: "shared", RealityPort: 8443}

	classify := func(stage string) (realityDual, mtDual bool) {
		t.Helper()
		routes, _ := s.collectRoutes(set)
		for _, r := range routes {
			switch r.InboundId {
			case reality.Id:
				realityDual = r.Dual
			case mt.Id:
				mtDual = r.Dual
			}
		}
		return
	}

	if rDual, mDual := classify("before"); rDual || !mDual {
		t.Fatalf("before the move: Reality dual=%v (want false), MTProto dual=%v (want true)", rDual, mDual)
	}

	if _, err := s.relocateInbounds(set, false); err != nil {
		t.Fatalf("relocate: %v", err)
	}

	rDual, mDual := classify("after")
	if rDual {
		t.Error("after the move Reality was reclassified as dual; the next pass would strip the PROXY header it requires")
	}
	if !mDual {
		t.Error("MTProto stopped being dual")
	}

	// Both routes (MTProto because it keeps its port, and Reality to prevent XTLS Issue #2779)
	// get a relay to strip the PROXY protocol header.
	cfg, err := s.buildConfig(set)
	if err != nil {
		t.Fatal(err)
	}
	relays := 0
	for _, r := range cfg.Routes {
		if r.Relay != "" {
			relays++
		}
	}
	if relays != 2 {
		t.Errorf("%d routes got a relay after the move, want exactly 2", relays)
	}
}

// TestStatusWarnsWhenTheDomainHasNothingBehindIt covers the trap that looks
// like a broken certificate: with the front-end on but no domain configured,
// 443 is pure passthrough, so a browser opening the server's own domain falls
// through to the Reality inbound and is shown the cover site's certificate.
// Nothing is wrong — but nothing says so either.
func TestStatusWarnsWhenTheDomainHasNothingBehindIt(t *testing.T) {
	s := newNginxTestServer(t)
	seedInbounds(t)

	// Warnings travel as codes now, so the panel can say them in the operator's
	// own language rather than in whatever English the service happened to
	// assemble.
	mentions := func(warnings []NginxWarning, code string) bool {
		for _, w := range warnings {
			if w.Code == code {
				return true
			}
		}
		return false
	}

	if err := s.SaveSettings(NginxSettings{Mode: "shared", RealityPort: 8443}); err != nil {
		t.Fatal(err)
	}
	if got := s.GetStatus().Warnings; !mentions(got, "noDomain") {
		t.Errorf("an empty domain was not reported: %v", got)
	}

	// Switched off, none of this is worth saying — nothing is in front of
	// anything.
	if err := s.SaveSettings(NginxSettings{Mode: "off", RealityPort: 8443}); err != nil {
		t.Fatal(err)
	}
	if got := s.GetStatus().Warnings; mentions(got, "noDomain") {
		t.Errorf("the front-end is off, yet it complained about the domain: %v", got)
	}

	// With a domain set, the built-in page is installed rather than leaving
	// the domain empty — but serving it unchanged is a fingerprint of its own,
	// so the panel says so.
	if err := s.SaveSettings(NginxSettings{Mode: "shared", Domain: "example.net", RealityPort: 8443}); err != nil {
		t.Fatal(err)
	}
	nginx.WebRoot = t.TempDir()
	t.Cleanup(func() { nginx.WebRoot = "/usr/local/x-ui/www" })
	stubs := &StubService{}
	if err := stubs.SyncToDisk(); err != nil {
		t.Fatal(err)
	}
	if got := s.GetStatus().Warnings; !mentions(got, "stockCoverPage") {
		t.Errorf("an unedited built-in page was not reported: %v", got)
	}

	// Picking one of the other built-in pages is a decision somebody made, not
	// the default nobody chose, and the panel does not argue with it.
	if err := stubs.ActivateTemplate("snake"); err != nil {
		t.Fatal(err)
	}
	if got := s.GetStatus().Warnings; mentions(got, "stockCoverPage") {
		t.Errorf("a page picked out of the gallery was reported as stock: %v", got)
	}

	// Once the operator has written their own, the nagging stops.
	own := &model.StubSite{Name: "mine", Html: "<!doctype html><title>mine</title><p>ours"}
	if _, err := stubs.SaveSite(own); err != nil {
		t.Fatal(err)
	}
	if err := stubs.ActivateSite(own.Id); err != nil {
		t.Fatal(err)
	}
	if got := s.GetStatus().Warnings; mentions(got, "stockCoverPage") {
		t.Errorf("a page the operator wrote was still called stock: %v", got)
	}
}

// TestDefaultDomainComesFromTheCertificate: the panel already serves its own
// interface over TLS for a particular name, so asking the operator to type that
// name again is asking them to repeat themselves — and to get it wrong. The
// certificate is the authority, not the webDomain setting, because the install
// script leaves that empty while issuing a perfectly good certificate.
func TestDefaultDomainComesFromTheCertificate(t *testing.T) {
	s := newNginxTestServer(t)
	dir := t.TempDir()
	certFile, _ := writeTestCert(t, dir, "net-ru.modulator.net")

	var setting SettingService
	if err := setting.setString("webCertFile", certFile); err != nil {
		t.Fatal(err)
	}
	if got := s.GetSettings().Domain; got != "net-ru.modulator.net" {
		t.Errorf("domain = %q, want it taken from the panel's own certificate", got)
	}

	// An explicitly configured panel domain wins: it is the operator's choice.
	if err := setting.setString("webDomain", "chosen.example.net"); err != nil {
		t.Fatal(err)
	}
	if got := s.GetSettings().Domain; got != "chosen.example.net" {
		t.Errorf("domain = %q, want the configured panel domain", got)
	}

	// And once the front-end has a domain of its own, nothing overrides it.
	if err := s.SaveSettings(NginxSettings{Mode: "off", Domain: "cover.example.net", RealityPort: 8443}); err != nil {
		t.Fatal(err)
	}
	if got := s.GetSettings().Domain; got != "cover.example.net" {
		t.Errorf("domain = %q, want the one saved for the front-end", got)
	}
}

// TestCheckCertificateAnswersForTheTypedDomain: the row under the domain field
// used to report on whatever was saved, so typing a domain produced "no
// certificate" — a verdict on the empty saved value that reads as a verdict on
// what was just typed.
func TestCheckCertificateAnswersForTheTypedDomain(t *testing.T) {
	s := newNginxTestServer(t)
	dir := t.TempDir()
	certFile, _ := writeTestCert(t, dir, "typed.example.net")

	certDirs = []string{dir + "-missing"}
	t.Cleanup(func() { certDirs = []string{"/root/cert", "/etc/letsencrypt/live", "/root/cert.crt"} })

	if got := s.CheckCertificate(""); got.CertOk {
		t.Error("an empty domain cannot have a certificate")
	}

	// Not where the panel looks: reported, naming the domain, and as the
	// ordinary «nobody has issued one» rather than «this one is broken» — the
	// two read very differently to an operator.
	got := s.CheckCertificate("typed.example.net")
	if got.CertOk || len(got.Warnings) == 0 {
		t.Fatalf("a missing certificate was not reported: %+v", got)
	}
	if w := got.Warnings[0]; w.Code != "certMissingFor" || len(w.Params) != 1 || w.Params[0] != "typed.example.net" {
		t.Errorf("warning = %+v, want certMissingFor for the typed domain", w)
	}

	// Now put it where the panel does look.
	certDirs = []string{filepath.Dir(filepath.Dir(certFile))}
	got = s.CheckCertificate("typed.example.net")
	if !got.CertOk {
		t.Errorf("the certificate was not found: %v", got.Warnings)
	}
	if got.CertExpiry == 0 {
		t.Error("no expiry was reported")
	}
}

// writeTestCert writes a self-signed certificate for domain into
// <dir>/<domain>/ the way the install script lays them out.
func writeTestCert(t *testing.T, dir, domain string) (certFile, keyFile string) {
	t.Helper()
	base := filepath.Join(dir, domain)
	if err := os.MkdirAll(base, 0755); err != nil {
		t.Fatal(err)
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: domain},
		DNSNames:     []string{domain},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(30 * 24 * time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	certFile = filepath.Join(base, "fullchain.pem")
	keyFile = filepath.Join(base, "privkey.pem")
	if err := os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0644); err != nil {
		t.Fatal(err)
	}
	der2, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der2}), 0600); err != nil {
		t.Fatal(err)
	}
	return certFile, keyFile
}

func TestOnly443RelocatesWithoutCollidingOnRealityPort(t *testing.T) {
	s := newNginxTestServer(t)
	reality, mt := seedInbounds(t)
	set := NginxSettings{Mode: "only443", RealityPort: 8443}

	if _, err := s.relocateInbounds(set, false); err != nil {
		t.Fatalf("first relocate: %v", err)
	}

	rIb, _ := inboundByID(reality.Id)
	mIb, _ := inboundByID(mt.Id)

	if rIb.Listen != "127.0.0.1" || rIb.Port != 8443 || rIb.PublicPort != 443 {
		t.Errorf("Reality = %s:%d (public %d), want 127.0.0.1:8443 (public 443)", rIb.Listen, rIb.Port, rIb.PublicPort)
	}
	if mIb.Listen != "127.0.0.1" || mIb.Port != 4343 || mIb.PublicPort != 443 {
		t.Errorf("MTProto = %s:%d (public %d), want 127.0.0.1:4343 (public 443)", mIb.Listen, mIb.Port, mIb.PublicPort)
	}

	if _, err := s.relocateInbounds(set, false); err != nil {
		t.Fatalf("second relocate failed (likely port collision): %v", err)
	}
}

func TestSwitchFromOnly443ToSharedRestoresDualInbounds(t *testing.T) {
	s := newNginxTestServer(t)
	reality, mt := seedInbounds(t)

	setOnly443 := NginxSettings{Mode: "only443", RealityPort: 8443}
	if _, err := s.relocateInbounds(setOnly443, false); err != nil {
		t.Fatalf("relocate only443: %v", err)
	}

	mIb, _ := inboundByID(mt.Id)
	if mIb.Listen != "127.0.0.1" || mIb.Port != 4343 || mIb.PublicPort != 443 {
		t.Fatalf("MTProto not relocated in only443: %+v", mIb)
	}

	// Switch to shared mode where MTProto is dual
	setShared := NginxSettings{Mode: "shared", RealityPort: 8443}
	if _, err := s.relocateInbounds(setShared, false); err != nil {
		t.Fatalf("relocate shared: %v", err)
	}

	mIbRestored, _ := inboundByID(mt.Id)
	if mIbRestored.Listen != "" || mIbRestored.Port != 4343 || mIbRestored.PublicPort != 0 {
		t.Errorf("MTProto not restored when switching to shared: listen=%q, port=%d, public_port=%d",
			mIbRestored.Listen, mIbRestored.Port, mIbRestored.PublicPort)
	}

	rIb, _ := inboundByID(reality.Id)
	if rIb.Listen != "127.0.0.1" || rIb.Port != 8443 || rIb.PublicPort != 443 {
		t.Errorf("Reality should stay on 127.0.0.1:8443 (public 443): %+v", rIb)
	}
}

func TestModeSwitchingSanitizesSettings(t *testing.T) {
	s := newNginxTestServer(t)

	err := s.SaveSettings(NginxSettings{
		Mode:           "off",
		SubsBehind443:  true,
		PanelBehind443: true,
		ManageFirewall: true,
		RealityPort:    8443,
	})
	if err != nil {
		t.Fatalf("save off: %v", err)
	}
	got := s.GetSettings()
	if got.SubsBehind443 || got.PanelBehind443 || got.ManageFirewall {
		t.Errorf("expected all false for mode off, got subs=%v panel=%v fw=%v",
			got.SubsBehind443, got.PanelBehind443, got.ManageFirewall)
	}

	err = s.SaveSettings(NginxSettings{
		Mode:           "shared",
		SubsBehind443:  true,
		PanelBehind443: true,
		ManageFirewall: true,
		RealityPort:    8443,
	})
	if err != nil {
		t.Fatalf("save shared: %v", err)
	}
	got = s.GetSettings()
	if !got.SubsBehind443 || got.PanelBehind443 || got.ManageFirewall {
		t.Errorf("expected subs=true, panel=false, fw=false for mode shared, got subs=%v panel=%v fw=%v",
			got.SubsBehind443, got.PanelBehind443, got.ManageFirewall)
	}
}

func TestPlanDetectsPortConflict(t *testing.T) {
	s := newNginxTestServer(t)
	seedInbounds(t)

	db := database.GetDB()
	conflictIb := &model.Inbound{
		UserId: 1, Remark: "Existing-8443", Enable: true, Listen: "", Port: 8443,
		Protocol: model.VLESS, Tag: "inbound-8443", Settings: `{"clients":[]}`,
	}
	if err := db.Create(conflictIb).Error; err != nil {
		t.Fatal(err)
	}

	plan := s.Plan(NginxSettings{Mode: "only443", RealityPort: 8443})
	hasConflict := false
	for _, b := range plan.Blockers {
		if b.Code == "portConflict" && len(b.Params) >= 1 && b.Params[0] == "8443" {
			hasConflict = true
			break
		}
	}
	if !hasConflict {
		t.Errorf("expected portConflict blocker in plan for port 8443, got blockers: %+v", plan.Blockers)
	}
}
