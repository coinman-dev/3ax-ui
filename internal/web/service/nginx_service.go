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

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/logger"
	"github.com/mhsanaei/3x-ui/v3/internal/nginx"
)

// PublicPort is fixed, not a setting: the feature exists to make the server look like an
// ordinary website, and websites live on 443.
const PublicPort = 443

// PublicSubBase returns the host subscription links must use once nginx publishes them on 443
// (port omitted). Stored settings are read: Apply saves them only after nginx serves the config.
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
	// Dual: the inbound keeps its own port and is also reachable via the public port.
	// Only an inbound that sat on the public port itself has to give it up.
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
	// ConfirmDeadline is when the panel stops waiting for a reachability confirmation
	// and reopens the ports. Unix ms, zero when nothing is pending.
	ConfirmDeadline int64          `json:"confirmDeadline"`
	Routes          []NginxRoute   `json:"routes"`
	Warnings        []NginxWarning `json:"warnings"`
}

// NginxWarning carries a code and params rather than a sentence: it is built with no request
// locale, so the wording lives with the panel's translations.
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

// certWarning tells a missing certificate (translatable) from a broken one, whose error from
// below only makes sense verbatim.
func certWarning(domain string, err error) NginxWarning {
	if errors.Is(err, errNoCertificate) {
		return warn("certMissingFor", domain)
	}
	return NginxWarning{Code: "certProblem", Params: []string{domain}, Text: err.Error()}
}

// NginxChange is one line of the preview shown before applying: a changed client link has to
// be handed out again, so it is spelled out first.
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

	// Reconcile-job backoff: a front-end that cannot come up (no cert, port taken, config
	// refused) must not be retried, restarting Xray, on every tick forever.
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
	// With no domain the front-end is pure passthrough: the panel's own domain falls through to
	// Reality and gets someone else's certificate, which looks broken rather than unset.
	if set.Mode != string(nginx.ModeOff) {
		if set.Domain == "" {
			st.Warnings = append(st.Warnings, warn("noDomain"))
		} else if active := s.stubService.ActiveSite(); active != nil {
			// The unchanged built-in page is a fingerprint shared by every 3AX-UI server; a
			// page picked from the gallery or hand-written is a decision, so is not flagged.
			if tpl, ok := s.stubService.DefaultTemplate(); ok && strings.TrimSpace(active.Html) == strings.TrimSpace(tpl.Html) {
				st.Warnings = append(st.Warnings, warn("stockCoverPage", tpl.Name))
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

// collectRoutes takes only VLESS Reality and FakeTLS MTProto: both send a cleartext SNI, the
// only thing nginx can route on without decrypting. UDP protocols cannot share a TCP port.
func (s *NginxService) collectRoutes(set NginxSettings) ([]NginxRoute, []NginxWarning) {
	inbounds, err := s.localInbounds()
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
			// Reality proxies unknown names to the site it borrows its identity from, the
			// best answer to a prober, so it rather than our own certificate takes the rest.
			fallback = true
		case ib.Protocol == model.MTProto:
			if domain := mtprotoFakeTLSDomain(ib.Settings); domain != "" {
				snis = []string{domain}
			}
		}
		if len(snis) == 0 {
			continue
		}

		// Only an inbound on the public port must give it up; the rest stay put and gain a
		// second way in, so links already handed out keep working.
		var gaveUpPublicPort bool
		if orig, ok := origPorts[ib.Id]; ok {
			gaveUpPublicPort = (orig == PublicPort)
		} else {
			// ib.PublicPort catches one already moved: by port alone it would turn dual and get
			// a relay stripping the PROXY header it requires, refusing every client.
			gaveUpPublicPort = (ib.Port == PublicPort) || (ib.Port == set.RealityPort && ib.PublicPort == PublicPort)
		}
		dual := !gaveUpPublicPort
		if nginx.Mode(set.Mode) == nginx.ModeOnly443 {
			// Nothing is dual once other ports are closed: its own-port link would point at
			// a port the firewall blocks, so it moves to loopback behind the public port.
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
		// A dual inbound's direct clients send no PROXY header, and REALITY hangs on one
		// (XTLS/Xray-core #2779), so both get a relay that strips it.
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

// defaultDomain prefills the domain from webDomain, else from the panel's own certificate:
// install-script panels often leave webDomain empty yet have a valid certificate.
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

// CheckCertificate checks a typed-but-unapplied domain, so the certificate row answers for
// what is on screen rather than for the saved domain.
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

// httpBackendPort picks the loopback TLS port for our domain once and remembers it: once nginx
// holds it, a free-port check fails and the port would wander on every reconcile.
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

// takenLoopbackPorts lists ports handed out but not bound until nginx reloads, which an OS check
// misses; a duplicate passes `nginx -t` (binds nothing) and then nginx will not start.
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

// relayPort picks an inbound's PROXY-stripping loopback port once and remembers it: a wandering
// port would differ from the on-disk config and reload nginx on every reconcile.
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

// findCertificate locates a valid certificate covering domain: one for another name yields a
// site every browser refuses, worse than no front-end at all.
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

// errNoCertificate means none was issued yet, as opposed to one that exists and is wrong; the
// two get different warnings.
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

// inboundByID reads one inbound fresh from the database.
func inboundByID(id int) (*model.Inbound, error) {
	ib := &model.Inbound{}
	if err := database.GetDB().Model(model.Inbound{}).Where("id = ?", id).First(ib).Error; err != nil {
		return nil, err
	}
	return ib, nil
}
