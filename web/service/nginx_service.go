package service

import (
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/coinman-dev/3ax-ui/v2/database"
	"github.com/coinman-dev/3ax-ui/v2/database/model"
	"github.com/coinman-dev/3ax-ui/v2/logger"
	"github.com/coinman-dev/3ax-ui/v2/nginx"
)

// PublicPort is the port everything is consolidated onto. It is 443 and not a
// setting: the whole point of the feature is that the server looks like an
// ordinary website, and a website lives on 443.
const PublicPort = 443

// PublicSubBase reports the scheme and host a subscription link has to be built
// from, and whether the front-end is the one answering for them.
//
// Once nginx publishes the subscription server under the site's domain, that
// server's own port stops being the address clients use: nginx terminates TLS
// for the domain on the public port and passes the subscription paths through.
// A link built from subDomain:subPort would then point at a second way in that
// the operator did not mean to advertise, and in "only 443" mode at a port the
// firewall has closed. The port is left off the returned host on purpose — the
// public port is 443, and a URL says that by saying nothing.
//
// It reads the stored settings rather than the ones on screen, and that is the
// point: Apply saves them last, after nginx is actually serving the config, so
// what is stored is what is running. A link never moves ahead of the server.
func PublicSubBase() (scheme string, host string, ok bool) {
	var svc NginxService
	set := svc.GetSettings()
	if nginx.Mode(set.Mode) == nginx.ModeOff || !set.SubsBehind443 || set.Domain == "" {
		return "", "", false
	}
	return "https", set.Domain, true
}

// NginxSettings is the stored front-end configuration.
type NginxSettings struct {
	Mode           string `json:"mode"`
	Domain         string `json:"domain"`
	StubSiteId     int    `json:"stubSiteId"`
	SubsBehind443  bool   `json:"subsBehind443"`
	PanelBehind443 bool   `json:"panelBehind443"`
	ManageFirewall bool   `json:"manageFirewall"`
	// FirewallExtra is the operator's own list of ports to keep open, in the
	// same free-form syntax the port-forwarding field uses ("53, 8000-8100").
	FirewallExtra string `json:"firewallExtra"`
	RealityPort   int    `json:"realityPort"`
	// HTTPPort is the loopback port nginx terminates TLS for our own domain on.
	// Zero means the panel has not picked one yet.
	HTTPPort int `json:"httpPort"`
}

// NginxRoute is one inbound as seen from the front-end.
type NginxRoute struct {
	InboundId int      `json:"inboundId"`
	Remark    string   `json:"remark"`
	Protocol  string   `json:"protocol"`
	SNIs      []string `json:"snis"`
	Port      int      `json:"port"`     // where the inbound listens once moved
	Listen    string   `json:"listen"`   // and on which address
	Fallback  bool     `json:"fallback"` // takes the unmatched connections
	// Dual means the inbound keeps its own port and is reachable both there
	// and through the public port. Only an inbound that was sitting on the
	// public port itself has to give it up.
	Dual bool `json:"dual"`
}

// NginxStatus is what the settings page shows.
type NginxStatus struct {
	Installed  bool   `json:"installed"`
	Version    string `json:"version"`
	HasStream  bool   `json:"hasStream"`
	Running    bool   `json:"running"`
	Mode       string `json:"mode"`
	Domain     string `json:"domain"`
	CertFile   string `json:"certFile"`
	CertOk     bool   `json:"certOk"`
	CertExpiry int64  `json:"certExpiry"` // unix ms, 0 when unknown
	PublicPort int    `json:"publicPort"`
	PanelPort  int    `json:"panelPort"`
	// FirewallOn reports whether our chain is in the INPUT path right now —
	// what the machine is actually doing, not what the settings ask for.
	FirewallOn bool `json:"firewallOn"`
	// ConfirmDeadline is when the panel gives up waiting to be told it is
	// still reachable and opens the ports again. Unix ms, zero when nothing
	// is pending.
	ConfirmDeadline int64          `json:"confirmDeadline"`
	Routes          []NginxRoute   `json:"routes"`
	Warnings        []NginxWarning `json:"warnings"`
}

// NginxWarning is something worth telling the operator, in a form the panel can
// say in their own language.
//
// The sentence cannot be built here: this runs with no request and no locale,
// so anything assembled in Go would reach the page in English whatever the
// panel is set to. The code and its parameters travel instead, and the wording
// lives with the rest of the translations.
type NginxWarning struct {
	Code   string   `json:"code"`
	Params []string `json:"params,omitempty"`
	// Text carries a message with no wording of its own — an error from below,
	// which the panel shows as it came.
	Text string `json:"text,omitempty"`
}

func warn(code string, params ...string) NginxWarning {
	return NginxWarning{Code: code, Params: params}
}

// certWarning says which of the two certificate problems this is. «There is no
// certificate for that domain» is a sentence the panel can say in the
// operator's own language; a certificate that exists and is broken carries a
// message from below that only makes sense as it came.
func certWarning(domain string, err error) NginxWarning {
	if errors.Is(err, errNoCertificate) {
		return warn("certMissingFor", domain)
	}
	return NginxWarning{Code: "certProblem", Params: []string{domain}, Text: err.Error()}
}

// NginxChange is one line of the "what is about to happen" list the panel shows
// before applying. Nothing here is reversible for the clients — a link that
// changes has to be handed out again — so it is spelled out first.
type NginxChange struct {
	Kind    string `json:"kind"` // move | links | serve | warning
	Subject string `json:"subject"`
	From    string `json:"from"`
	To      string `json:"to"`
}

// NginxPlan is the change list plus the reasons it cannot be applied yet.
type NginxPlan struct {
	Mode     string         `json:"mode"`
	Changes  []NginxChange  `json:"changes"`
	Blockers []NginxWarning `json:"blockers"`
}

// NginxService owns the front-end: the settings, the generated config, and the
// inbound moves that go with it.
type NginxService struct {
	settingService SettingService
	inboundService InboundService
	xrayService    XrayService
	stubService    StubService

	// Backoff state for the reconcile job. A server where the front-end cannot
	// come up — no certificate, a port taken, nginx refusing the config — must
	// not be retried, and have Xray restarted, on every tick forever.
	failures  int
	skipTicks int
}

// GetSettings reads the stored configuration, falling back to the defaults.
func (s *NginxService) GetSettings() NginxSettings {
	get := func(key string) string {
		v, err := s.settingService.getString(key)
		if err != nil {
			logger.Warningf("nginx: read setting %s: %v", key, err)
		}
		return v
	}
	domain := strings.TrimSpace(get("nginxDomain"))
	if domain == "" {
		domain = s.defaultDomain()
	}
	realityPort, _ := strconv.Atoi(get("nginxRealityPort"))
	if realityPort <= 0 {
		realityPort = 8443
	}
	stubId, _ := strconv.Atoi(get("nginxStubSiteId"))
	httpPort, _ := strconv.Atoi(get("nginxHttpPort"))
	mode := get("nginxMode")
	subsBehind443 := get("nginxSubsBehind443") == "true"
	panelBehind443 := get("nginxPanelBehind443") == "true"
	manageFirewall := get("nginxManageFirewall") == "true"
	if mode == string(nginx.ModeOff) || mode == "" {
		subsBehind443 = false
		panelBehind443 = false
		manageFirewall = false
	} else if mode == string(nginx.ModeShared) {
		panelBehind443 = false
		manageFirewall = false
	}
	return NginxSettings{
		Mode:           mode,
		Domain:         domain,
		StubSiteId:     stubId,
		SubsBehind443:  subsBehind443,
		PanelBehind443: panelBehind443,
		ManageFirewall: manageFirewall,
		FirewallExtra:  get("nginxFirewallExtra"),
		RealityPort:    realityPort,
		HTTPPort:       httpPort,
	}
}

// SaveSettings stores the configuration without applying it.
func (s *NginxService) SaveSettings(in NginxSettings) error {
	if !nginx.Mode(in.Mode).Valid() {
		return fmt.Errorf("unknown mode %q", in.Mode)
	}
	if in.RealityPort <= 0 || in.RealityPort > 65535 {
		return fmt.Errorf("internal port %d is out of range", in.RealityPort)
	}
	if in.RealityPort == PublicPort {
		return fmt.Errorf("the internal port cannot be %d — that is the port nginx takes over", PublicPort)
	}
	if in.Mode == string(nginx.ModeOff) {
		in.SubsBehind443 = false
		in.PanelBehind443 = false
		in.ManageFirewall = false
	} else if in.Mode == string(nginx.ModeShared) {
		in.PanelBehind443 = false
		in.ManageFirewall = false
	}
	pairs := map[string]string{
		"nginxMode":           in.Mode,
		"nginxDomain":         strings.TrimSpace(in.Domain),
		"nginxStubSiteId":     strconv.Itoa(in.StubSiteId),
		"nginxSubsBehind443":  strconv.FormatBool(in.SubsBehind443),
		"nginxPanelBehind443": strconv.FormatBool(in.PanelBehind443),
		"nginxManageFirewall": strconv.FormatBool(in.ManageFirewall),
		"nginxFirewallExtra":  strings.TrimSpace(in.FirewallExtra),
		"nginxRealityPort":    strconv.Itoa(in.RealityPort),
	}
	if in.HTTPPort > 0 {
		pairs["nginxHttpPort"] = strconv.Itoa(in.HTTPPort)
	}
	for key, value := range pairs {
		if err := s.settingService.setString(key, value); err != nil {
			return err
		}
	}
	return nil
}

// GetStatus reports what the server looks like right now.
func (s *NginxService) GetStatus() NginxStatus {
	set := s.GetSettings()
	panelPort, _ := s.settingService.GetPort()
	st := NginxStatus{
		Installed:  nginx.IsInstalled(),
		Mode:       set.Mode,
		Domain:     set.Domain,
		PublicPort: PublicPort,
		PanelPort:  panelPort,
		FirewallOn: nginx.FirewallActive(),

		ConfirmDeadline: s.PendingConfirmation(),
	}
	if st.Installed {
		st.Version = nginx.Version()
		st.HasStream = nginx.HasStream()
		st.Running = nginx.IsRunning()
		if !st.HasStream {
			st.Warnings = append(st.Warnings, warn("noStreamModule"))
		}
	}
	// Two settings that are easy to leave empty and produce a result nobody
	// would guess: with no domain the front-end is pure passthrough, so a
	// browser opening the panel's own domain falls through to the Reality
	// inbound and is answered with somebody else's certificate. It looks like
	// a broken certificate rather than a missing setting.
	if set.Mode != string(nginx.ModeOff) {
		if set.Domain == "" {
			st.Warnings = append(st.Warnings, warn("noDomain"))
		} else if active := s.stubService.ActiveSite(); active != nil {
			// The panel installs its built-in page rather than leave the
			// domain empty, but serving it unchanged is a fingerprint: the
			// same bytes on every 3AX-UI server anywhere.
			for _, tpl := range s.stubService.Templates() {
				if strings.TrimSpace(active.Html) == strings.TrimSpace(tpl.Html) {
					st.Warnings = append(st.Warnings, warn("stockCoverPage"))
					break
				}
			}
		}
	}

	if set.Domain != "" {
		if cert, _, expiry, err := findCertificate(set.Domain); err == nil {
			st.CertFile, st.CertOk = cert, true
			st.CertExpiry = expiry.UnixMilli()
			if time.Until(expiry) < 14*24*time.Hour {
				st.Warnings = append(st.Warnings, warn("certExpiring", set.Domain, expiry.Format("2006-01-02")))
			}
		} else {
			st.Warnings = append(st.Warnings, certWarning(set.Domain, err))
		}
	}

	routes, warnings := s.collectRoutes(set)
	st.Routes = routes
	st.Warnings = append(st.Warnings, warnings...)
	return st
}

// collectRoutes turns the inbounds into the SNI routes the front-end needs.
//
// Only the two protocols that can share a TLS port are taken: VLESS Reality and
// MTProto with FakeTLS. Both present a server name in the clear at the start of
// the connection, which is the only thing nginx can route on without decrypting
// anything. UDP protocols — AmneziaWG, native WireGuard — cannot share a TCP
// port at all and are left alone.
func (s *NginxService) collectRoutes(set NginxSettings) ([]NginxRoute, []NginxWarning) {
	inbounds, err := s.inboundService.GetAllInbounds()
	if err != nil {
		return nil, []NginxWarning{{Code: "inboundsUnreadable", Text: err.Error()}}
	}

	var routes []NginxRoute
	var warnings []NginxWarning
	seen := map[string]string{}

	snapshots := s.loadSnapshots()
	origPorts := map[int]int{}
	for _, snap := range snapshots {
		origPorts[snap.Id] = snap.Port
	}

	for _, ib := range inbounds {
		if !ib.Enable {
			continue
		}
		var snis []string
		var fallback bool
		port, listen := ib.Port, ib.Listen

		switch {
		case ib.Protocol == model.VLESS && realitySNIs(ib.StreamSettings) != nil:
			snis = realitySNIs(ib.StreamSettings)
			// Reality answers a name it does not know by proxying to the real
			// site it borrows its identity from, which is the best possible
			// answer to a prober. So it, and not our own certificate, takes
			// everything unmatched.
			fallback = true
		case ib.Protocol == model.MTProto:
			if domain := model.MtprotoFakeTLSDomain(ib.Settings); domain != "" {
				snis = []string{domain}
			}
		}
		if len(snis) == 0 {
			continue
		}

		// An inbound sitting on the public port has to give it up — nginx
		// wants it, and there is nowhere else for both to be. Everything else
		// stays exactly where it is and simply gains a second way in, so the
		// links already handed out keep working.
		//
		// PublicPort is the second half of the test and not decoration: once
		// that inbound has moved it no longer sits on the public port, and
		// judging by the port alone would reclassify it as an ordinary one on
		// the very next pass. It would then be handed a relay that strips the
		// PROXY header it is configured to require, and every client would be
		// refused.
		var gaveUpPublicPort bool
		if orig, ok := origPorts[ib.Id]; ok {
			gaveUpPublicPort = (orig == PublicPort)
		} else {
			gaveUpPublicPort = (ib.Port == PublicPort) || (ib.Port == set.RealityPort && ib.PublicPort == PublicPort)
		}
		dual := !gaveUpPublicPort
		if nginx.Mode(set.Mode) == nginx.ModeOnly443 {
			// Nothing is dual once the other ports are closed: an inbound left
			// on its own port would keep advertising a link to a port the
			// firewall no longer lets through. It moves to the loopback like
			// the rest and is published on the public port instead.
			dual = false
		}
		switch {
		case gaveUpPublicPort:
			port, listen = set.RealityPort, "127.0.0.1"
		case !dual:
			// It keeps its port number — nothing else wants it — but only on
			// the loopback, where nginx is the only thing that can reach it.
			listen = "127.0.0.1"
		}

		for _, sni := range snis {
			key := strings.ToLower(sni)
			if owner, dup := seen[key]; dup {
				warnings = append(warnings, warn("sniConflict", owner, ib.Remark, sni))
			}
			seen[key] = ib.Remark
		}
		if strings.EqualFold(set.Domain, strings.Join(snis, "")) || contains(snis, set.Domain) {
			warnings = append(warnings, warn("sniIsOwnDomain", ib.Remark, set.Domain))
		}

		routes = append(routes, NginxRoute{
			InboundId: ib.Id, Remark: ib.Remark, Protocol: string(ib.Protocol),
			SNIs: snis, Port: port, Listen: listen, Fallback: fallback, Dual: dual,
		})
	}

	// Only one route can be the fallback; if there are several Reality
	// inbounds, the first keeps it.
	first := true
	for i := range routes {
		if routes[i].Fallback {
			if !first {
				routes[i].Fallback = false
			}
			first = false
		}
	}
	return routes, warnings
}

// buildConfig assembles what nginx should be running for the given settings.
func (s *NginxService) buildConfig(set NginxSettings) (nginx.Config, error) {
	cfg := nginx.Config{Mode: nginx.Mode(set.Mode), Port: PublicPort}
	if cfg.Mode == nginx.ModeOff {
		return cfg, nil
	}

	routes, _ := s.collectRoutes(set)
	for _, r := range routes {
		host := r.Listen
		if host == "" || host == "0.0.0.0" || host == "::" {
			host = "127.0.0.1"
		}
		route := nginx.Route{
			Name:     fmt.Sprintf("%s (%s)", r.Remark, r.Protocol),
			SNIs:     r.SNIs,
			Upstream: net.JoinHostPort(host, strconv.Itoa(r.Port)),
			Fallback: r.Fallback,
		}
		// An inbound that kept its own port serves direct clients too, and
		// they send no PROXY header — so it must not be given one either.
		//
		// Inbounds using REALITY (e.g. VLESS REALITY with xhttp, tcp, grpc)
		// must NEVER receive the PROXY protocol header: XTLS REALITY decodes
		// raw TLS ClientHello, and injecting PROXY protocol triggers handshake
		// hangs/timeouts (XTLS/Xray-core Issue #2779), breaking clients like Hiddify.
		// Strip the PROXY header via relay for all REALITY inbounds as well.
		if r.Dual || r.Protocol == string(model.VLESS) {
			relay, err := s.relayPort(r.InboundId)
			if err != nil {
				return cfg, err
			}
			route.Relay = net.JoinHostPort("127.0.0.1", strconv.Itoa(relay))
		}
		cfg.Routes = append(cfg.Routes, route)
	}

	if set.Domain != "" {
		cert, key, _, err := findCertificate(set.Domain)
		if err != nil {
			return cfg, err
		}
		port, err := s.httpBackendPort(set)
		if err != nil {
			return cfg, err
		}
		cfg.Site = &nginx.Site{
			Domain:   set.Domain,
			CertFile: cert,
			KeyFile:  key,
			Listen:   net.JoinHostPort("127.0.0.1", strconv.Itoa(port)),
			Root:     nginx.WebRoot,
		}
		if set.PanelBehind443 && cfg.Mode == nginx.ModeOnly443 {
			panel, err := s.panelProxy()
			if err != nil {
				return cfg, err
			}
			cfg.Site.Panel = panel
		}
		if set.SubsBehind443 {
			sub, err := s.subProxy()
			if err != nil {
				return cfg, err
			}
			cfg.Site.Sub = sub
		}
	}
	return cfg, nil
}

// defaultDomain is what the domain field starts out as when nothing has been
// chosen yet.
//
// The panel already knows which domain this server answers to — it is serving
// its own interface over TLS for that name — so asking the operator to type it
// again is asking them to repeat themselves, and to get it wrong. The
// certificate is the authority here rather than the setting: an operator may
// leave webDomain empty and still have a perfectly good certificate, which is
// exactly the case on a panel set up by the install script.
func (s *NginxService) defaultDomain() string {
	if domain, err := s.settingService.GetWebDomain(); err == nil {
		if domain = strings.TrimSpace(domain); domain != "" {
			return domain
		}
	}
	certFile, err := s.settingService.GetCertFile()
	if err != nil || strings.TrimSpace(certFile) == "" {
		return ""
	}
	for _, name := range certificateDomains(certFile) {
		// A wildcard is a certificate for a family of names, not a name to
		// put in a config.
		if !strings.HasPrefix(name, "*") {
			return name
		}
	}
	return ""
}

// certificateDomains lists the names a certificate is valid for.
func certificateDomains(path string) []string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	for len(raw) > 0 {
		var block *pem.Block
		block, raw = pem.Decode(raw)
		if block == nil {
			break
		}
		if block.Type != "CERTIFICATE" {
			continue
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			continue
		}
		// The leaf comes first in a chain, and it is the one that carries the
		// names this server answers to.
		if len(cert.DNSNames) > 0 {
			return cert.DNSNames
		}
		if cert.Subject.CommonName != "" {
			return []string{cert.Subject.CommonName}
		}
	}
	return nil
}

// CheckCertificate reports whether a certificate can be found for a domain the
// operator has typed but not yet applied. Without it the certificate row would
// answer for the domain that is saved, not the one on screen — and read as a
// verdict on what was just typed.
func (s *NginxService) CheckCertificate(domain string) NginxStatus {
	st := NginxStatus{Domain: strings.TrimSpace(domain), PublicPort: PublicPort}
	if st.Domain == "" {
		return st
	}
	cert, _, expiry, err := findCertificate(st.Domain)
	if err != nil {
		st.Warnings = append(st.Warnings, certWarning(st.Domain, err))
		return st
	}
	st.CertFile, st.CertOk = cert, true
	st.CertExpiry = expiry.UnixMilli()
	return st
}

// httpBackendPort returns the loopback port nginx terminates TLS on for our own
// domain, choosing one the first time and remembering it afterwards.
//
// It is deliberately not re-chosen on every call: once the front-end is up, our
// own nginx holds the port, so "is it free" would answer no and the panel would
// wander to a new port on every reconcile.
func (s *NginxService) httpBackendPort(set NginxSettings) (int, error) {
	if set.HTTPPort > 0 {
		return set.HTTPPort, nil
	}
	port, err := nginx.FreeLoopbackPort(0, s.takenLoopbackPorts())
	if err != nil {
		return 0, err
	}
	if err := s.settingService.setString("nginxHttpPort", strconv.Itoa(port)); err != nil {
		return 0, err
	}
	logger.Info("nginx: serving the site's domain on loopback port", port)
	return port, nil
}

// takenLoopbackPorts is every loopback port the panel has already handed out.
//
// None of them is listening yet — they are only bound when nginx reloads — so
// asking the operating system «is this port free» answers yes for a port that
// is already spoken for. Without this, a server with a dual inbound and a
// domain gets the same number twice: once as the relay that strips the PROXY
// header, once as the address the site's TLS terminates on. The config passes
// `nginx -t`, which binds nothing, and then nginx will not start.
func (s *NginxService) takenLoopbackPorts() map[int]bool {
	taken := map[int]bool{}
	if raw, err := s.settingService.getString("nginxHttpPort"); err == nil {
		if port, _ := strconv.Atoi(raw); port > 0 {
			taken[port] = true
		}
	}
	for _, port := range s.savedRelayPorts() {
		taken[port] = true
	}
	return taken
}

// savedRelayPorts is the inbound-id → port map recorded by earlier applies.
func (s *NginxService) savedRelayPorts() map[string]int {
	ports := map[string]int{}
	raw, err := s.settingService.getString("nginxRelayPorts")
	if err != nil || strings.TrimSpace(raw) == "" {
		return ports
	}
	if err := json.Unmarshal([]byte(raw), &ports); err != nil {
		logger.Warning("nginx: the saved relay ports are unreadable, choosing again:", err)
		return map[string]int{}
	}
	return ports
}

// relayPort returns the loopback port nginx uses to strip the PROXY header for
// one inbound, choosing it once and remembering it.
//
// Remembering matters: the reconcile job compares the rendered config with what
// is on disk, and a port that wandered would look like a change and reload
// nginx every half minute.
func (s *NginxService) relayPort(inboundId int) (int, error) {
	ports := s.savedRelayPorts()
	key := strconv.Itoa(inboundId)
	if port, ok := ports[key]; ok && port > 0 {
		return port, nil
	}

	port, err := nginx.FreeLoopbackPort(0, s.takenLoopbackPorts())
	if err != nil {
		return 0, err
	}

	ports[key] = port
	raw, err := json.Marshal(ports)
	if err != nil {
		return 0, err
	}
	if err := s.settingService.setString("nginxRelayPorts", string(raw)); err != nil {
		return 0, err
	}
	return port, nil
}

// panelProxy describes the panel itself as an HTTP service behind the domain.
func (s *NginxService) panelProxy() (*nginx.Proxy, error) {
	port, err := s.settingService.GetPort()
	if err != nil {
		return nil, err
	}
	basePath, err := s.settingService.GetBasePath()
	if err != nil {
		return nil, err
	}
	if basePath == "" || basePath == "/" {
		// Proxying "/" would swallow the stub site.
		return nil, fmt.Errorf("the panel has no base path, so it cannot be published under the site's domain — set one in Settings first")
	}
	certFile, _ := s.settingService.GetCertFile()
	return &nginx.Proxy{
		Name:   "panel",
		Paths:  []string{basePath},
		Target: net.JoinHostPort("127.0.0.1", strconv.Itoa(port)),
		TLS:    certFile != "",
	}, nil
}

// subProxy describes the subscription server the same way.
func (s *NginxService) subProxy() (*nginx.Proxy, error) {
	enable, err := s.settingService.GetSubEnable()
	if err != nil {
		return nil, err
	}
	if !enable {
		return nil, fmt.Errorf("the subscription server is switched off")
	}
	port, err := s.settingService.GetSubPort()
	if err != nil {
		return nil, err
	}
	paths := []string{}
	if p, _ := s.settingService.GetSubPath(); p != "" && p != "/" {
		paths = append(paths, p)
	}
	if on, _ := s.settingService.GetSubJsonEnable(); on {
		if p, _ := s.settingService.GetSubJsonPath(); p != "" && p != "/" {
			paths = append(paths, p)
		}
	}
	if on, _ := s.settingService.GetSubClashEnable(); on {
		if p, _ := s.settingService.GetSubClashPath(); p != "" && p != "/" {
			paths = append(paths, p)
		}
	}
	if len(paths) == 0 {
		return nil, fmt.Errorf("the subscription server has no path to publish")
	}
	certFile, _ := s.settingService.GetSubCertFile()
	return &nginx.Proxy{
		Name:   "subscriptions",
		Paths:  paths,
		Target: net.JoinHostPort("127.0.0.1", strconv.Itoa(port)),
		TLS:    certFile != "",
	}, nil
}

func contains(list []string, want string) bool {
	if want == "" {
		return false
	}
	for _, v := range list {
		if strings.EqualFold(v, want) {
			return true
		}
	}
	return false
}

// realitySNIs returns the cover domains of a Reality inbound, or nil when the
// inbound is not Reality.
func realitySNIs(streamSettings string) []string {
	var parsed struct {
		Security        string `json:"security"`
		RealitySettings struct {
			ServerNames []string `json:"serverNames"`
		} `json:"realitySettings"`
	}
	if json.Unmarshal([]byte(streamSettings), &parsed) != nil || parsed.Security != "reality" {
		return nil
	}
	out := make([]string, 0, len(parsed.RealitySettings.ServerNames))
	for _, name := range parsed.RealitySettings.ServerNames {
		if name = strings.TrimSpace(name); name != "" {
			out = append(out, name)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// certDirs are the layouts the panel knows how to find a certificate in:
// acme.sh as the install script sets it up, and certbot.
var certDirs = []string{"/root/cert", "/etc/letsencrypt/live", "/root/cert.crt"}

// findCertificate locates a certificate for the domain and checks it is the
// right one and still valid. Handing nginx a certificate for another domain
// produces a site every browser refuses, which is worse than not enabling the
// front-end at all.
func findCertificate(domain string) (certFile, keyFile string, expiry time.Time, err error) {
	type candidate struct{ cert, key string }
	var candidates []candidate
	for _, dir := range certDirs {
		base := filepath.Join(dir, domain)
		candidates = append(candidates,
			candidate{filepath.Join(base, "fullchain.pem"), filepath.Join(base, "privkey.pem")},
			candidate{filepath.Join(base, "fullchain.cer"), filepath.Join(base, domain+".key")},
			candidate{filepath.Join(base, domain+".cer"), filepath.Join(base, domain+".key")},
		)
	}
	for _, c := range candidates {
		if _, err := os.Stat(c.cert); err != nil {
			continue
		}
		if _, err := os.Stat(c.key); err != nil {
			continue
		}
		exp, err := certificateExpiry(c.cert, domain)
		if err != nil {
			return "", "", time.Time{}, err
		}
		return c.cert, c.key, exp, nil
	}
	return "", "", time.Time{}, fmt.Errorf("%s: %w", domain, errNoCertificate)
}

// errNoCertificate is the ordinary answer — nobody has issued one yet — as
// opposed to a certificate that exists and is wrong. The two read very
// differently to an operator, so they are not the same warning.
var errNoCertificate = errors.New("no certificate")

// certificateExpiry parses the leaf certificate and verifies it covers domain.
func certificateExpiry(path, domain string) (time.Time, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return time.Time{}, fmt.Errorf("read %s: %w", path, err)
	}
	for len(raw) > 0 {
		var block *pem.Block
		block, raw = pem.Decode(raw)
		if block == nil {
			break
		}
		if block.Type != "CERTIFICATE" {
			continue
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return time.Time{}, fmt.Errorf("parse %s: %w", path, err)
		}
		if cert.VerifyHostname(domain) != nil {
			// Not the leaf, or a certificate for another name: keep looking
			// through the chain before giving up.
			continue
		}
		if time.Now().After(cert.NotAfter) {
			return cert.NotAfter, fmt.Errorf("the certificate for %s expired on %s",
				domain, cert.NotAfter.Format("2006-01-02"))
		}
		return cert.NotAfter, nil
	}
	return time.Time{}, fmt.Errorf("%s does not contain a certificate for %s", path, domain)
}

// db is a small helper so the relocation code reads clearly.
func inboundByID(id int) (*model.Inbound, error) {
	ib := &model.Inbound{}
	if err := database.GetDB().Model(model.Inbound{}).Where("id = ?", id).First(ib).Error; err != nil {
		return nil, err
	}
	return ib, nil
}
