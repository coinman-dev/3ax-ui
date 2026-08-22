package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"runtime"
	"sync"

	"github.com/coinman-dev/3ax-ui/v2/database"
	"github.com/coinman-dev/3ax-ui/v2/database/model"
	"github.com/coinman-dev/3ax-ui/v2/logger"
	"github.com/coinman-dev/3ax-ui/v2/tunnel"
	"github.com/coinman-dev/3ax-ui/v2/util/json_util"
	"github.com/coinman-dev/3ax-ui/v2/xray"

	"go.uber.org/atomic"
)

var (
	// procPtr holds the live Xray process. It is an atomic pointer rather than
	// a plain variable because readers (cron jobs @every 1s / @every 10s, HTTP
	// handlers) race with RestartXray/StopXray swapping it. It must stay
	// lock-free: RestartXray builds the config while holding `lock`, and that
	// path reaches back into currentProcess() via InboundService.AddTraffic.
	procPtr           atomic.Pointer[xray.Process]
	lock              sync.Mutex
	isNeedXrayRestart atomic.Bool // Indicates that restart was requested for Xray
	isManuallyStopped atomic.Bool // Indicates that Xray was stopped manually from the panel
	result            string
)

// XrayService provides business logic for Xray process management.
// It handles starting, stopping, restarting Xray, and managing its configuration.
type XrayService struct {
	inboundService InboundService
	settingService SettingService
	xrayAPI        xray.XrayAPI
}

// currentProcess returns the live Xray process handle under `lock`.
// `p` is swapped by RestartXray/StopXray from HTTP handlers while cron jobs
// (@every 1s and @every 10s) read it, so every read outside those two
// already-locked functions must go through here.
func currentProcess() *xray.Process {
	return procPtr.Load()
}

// xrayAPIPort returns the gRPC API port of the running Xray process, or 0 when
// Xray is not running — xrayApi.Init then fails with an error instead of the
// nil dereference these call sites used to risk.
func xrayAPIPort() int {
	if proc := currentProcess(); proc != nil {
		return proc.GetAPIPort()
	}
	return 0
}

// xrayProcRunning reports whether the Xray process exists and is running.
func xrayProcRunning() bool {
	proc := currentProcess()
	return proc != nil && proc.IsRunning()
}

// xrayOnlineClients returns the emails Xray reported as transferring in the
// last collection window (empty when Xray is not running).
func xrayOnlineClients() []string {
	if proc := currentProcess(); proc != nil {
		return proc.GetOnlineClients()
	}
	return nil
}

// IsXrayRunning checks if the Xray process is currently running.
func (s *XrayService) IsXrayRunning() bool {
	proc := currentProcess()
	return proc != nil && proc.IsRunning()
}

// GetXrayErr returns the error from the Xray process, if any.
func (s *XrayService) GetXrayErr() error {
	proc := currentProcess()
	if proc == nil {
		return nil
	}

	err := proc.GetErr()
	if err == nil {
		return nil
	}

	if runtime.GOOS == "windows" && err.Error() == "exit status 1" {
		// exit status 1 on Windows means that Xray process was killed
		// as we kill process to stop in on Windows, this is not an error
		return nil
	}

	return err
}

// GetXrayResult returns the result string from the Xray process.
func (s *XrayService) GetXrayResult() string {
	// `result` is package state shared with RestartXray, so it stays under lock.
	lock.Lock()
	defer lock.Unlock()
	if result != "" {
		return result
	}
	proc := procPtr.Load()
	if proc == nil || proc.IsRunning() {
		return ""
	}

	result = proc.GetResult()

	if runtime.GOOS == "windows" && result == "exit status 1" {
		// exit status 1 on Windows means that Xray process was killed
		// as we kill process to stop in on Windows, this is not an error
		return ""
	}

	return result
}

// GetXrayVersion returns the version of the running Xray process.
func (s *XrayService) GetXrayVersion() string {
	proc := currentProcess()
	if proc == nil {
		return "Unknown"
	}
	return proc.GetVersion()
}

// GetXrayConfig retrieves and builds the Xray configuration from settings and inbounds.
func (s *XrayService) GetXrayConfig() (*xray.Config, error) {
	templateConfig, err := s.settingService.GetXrayConfigTemplate()
	if err != nil {
		return nil, err
	}

	xrayConfig := &xray.Config{}
	err = json.Unmarshal([]byte(templateConfig), xrayConfig)
	if err != nil {
		return nil, err
	}

	// Side effect kept from upstream: this flushes pending auto-renew /
	// expiry bookkeeping so the config below is built from current state.
	if _, _, err := s.inboundService.AddTraffic(nil, nil); err != nil {
		logger.Warning("pre-config traffic maintenance failed:", err)
	}

	inbounds, err := s.inboundService.GetAllInbounds()
	if err != nil {
		return nil, err
	}
	for _, inbound := range inbounds {
		if !inbound.Enable {
			continue
		}
		// Skip AmneziaWG, NativeWG and MTProto — they are not Xray protocols
		// (MTProto runs as a standalone mtg sidecar; see the mtproto package).
		if inbound.Protocol == model.AmneziaWG || inbound.Protocol == model.NativeWG || inbound.Protocol == model.MTProto {
			continue
		}
		// get settings clients
		settings := map[string]any{}
		json.Unmarshal([]byte(inbound.Settings), &settings)
		clients, ok := settings["clients"].([]any)
		if ok {
			// Fast O(N) lookup map for client traffic enablement
			clientStats := inbound.ClientStats
			enableMap := make(map[string]bool, len(clientStats))
			for _, clientTraffic := range clientStats {
				enableMap[clientTraffic.Email] = clientTraffic.Enable
			}

			if inbound.Protocol == model.Mixed || inbound.Protocol == model.HTTP {
				// MIXED (SOCKS5) and HTTP inbounds use xray's settings.accounts[]={user,pass}.
				// Translate panel's clients[] to that shape, dropping panel-only fields and
				// disabled users — the basic-auth username acts as the per-user identity that
				// xray reports back in its stats keys (user>>>EMAIL>>>traffic>>>...).
				accounts := make([]any, 0, len(clients))
				for _, client := range clients {
					c, ok := client.(map[string]any)
					if !ok {
						continue
					}
					if enable, ok := c["enable"].(bool); ok && !enable {
						continue
					}
					user, _ := c["email"].(string)
					if user == "" {
						continue
					}
					pass, _ := c["password"].(string)
					accounts = append(accounts, map[string]any{
						"user": user,
						"pass": pass,
					})
				}
				delete(settings, "clients")
				settings["accounts"] = accounts
				if inbound.Protocol == model.Mixed {
					if _, ok := settings["auth"]; !ok && len(accounts) > 0 {
						settings["auth"] = "password"
					}
				}
			} else {
				// filter and clean clients
				var final_clients []any
				for _, client := range clients {
					c, ok := client.(map[string]any)
					if !ok {
						continue
					}

					email, _ := c["email"].(string)

					// check users active or not via stats
					if enable, exists := enableMap[email]; exists && !enable {
						logger.Infof("Remove Inbound User %s due to expiration or traffic limit", email)
						continue
					}

					// check manual disabled flag
					if manualEnable, ok := c["enable"].(bool); ok && !manualEnable {
						continue
					}

					// clear client config for additional parameters
					for key := range c {
						if key != "email" && key != "id" && key != "password" && key != "flow" && key != "method" && key != "auth" && key != "reverse" {
							delete(c, key)
						}
						if flow, ok := c["flow"].(string); ok && flow == "xtls-rprx-vision-udp443" {
							c["flow"] = "xtls-rprx-vision"
						}
					}
					final_clients = append(final_clients, any(c))
				}
				settings["clients"] = final_clients
			}

			modifiedSettings, err := json.MarshalIndent(settings, "", "  ")
			if err != nil {
				return nil, err
			}
			inbound.Settings = string(modifiedSettings)
		}

		if len(inbound.StreamSettings) > 0 {
			// Unmarshal stream JSON
			var stream map[string]any
			json.Unmarshal([]byte(inbound.StreamSettings), &stream)

			// Remove the "settings" field under "tlsSettings" and "realitySettings"
			tlsSettings, ok1 := stream["tlsSettings"].(map[string]any)
			realitySettings, ok2 := stream["realitySettings"].(map[string]any)
			if ok1 || ok2 {
				if ok1 {
					delete(tlsSettings, "settings")
				} else if ok2 {
					delete(realitySettings, "settings")
				}
			}

			delete(stream, "externalProxy")

			newStream, err := json.MarshalIndent(stream, "", "  ")
			if err != nil {
				return nil, err
			}
			inbound.StreamSettings = string(newStream)
		}

		inboundConfig := inbound.GenXrayInboundConfig()
		xrayConfig.InboundConfigs = append(xrayConfig.InboundConfigs, *inboundConfig)
	}

	// Append synthetic dokodemo-door TPROXY inbounds for each enabled
	// AWG/WG server that opted into RouteViaXray. These are not stored as
	// regular inbound records (AWG/WG live in their own tables and never
	// pass through Xray under direct mode), but when the user wires WG/AWG
	// traffic into Xray we need a sink for the TPROXY-redirected packets.
	xrayConfig.InboundConfigs = append(xrayConfig.InboundConfigs, tunnelTproxyInbounds()...)

	// Route opted-in mtproto inbounds through Xray's router. Each one gets a
	// loopback SOCKS bridge — tagged with the inbound's own tag — that its mtg
	// sidecar dials Telegram through, plus an optional routing rule sending that
	// tag to a selected outbound (e.g. a chain to a server where Telegram is
	// reachable). mtproto inbounds are skipped from the main loop above (not Xray
	// protocols), so this is the only place their egress bridge is built.
	for _, inbound := range inbounds {
		if inbound.Protocol == model.MTProto && inbound.Enable {
			injectMtprotoEgress(xrayConfig, inbound)
		}
	}

	return xrayConfig, nil
}

// mtprotoEgressSocksSettings is the loopback SOCKS server a routed mtproto
// inbound exposes for its mtg sidecar to dial Telegram through. mtg makes plain
// TCP connections, so UDP is left off.
const mtprotoEgressSocksSettings = `{"auth":"noauth","udp":false}`

// routingTagIsBalancer reports whether tag names a balancer in the parsed
// routing section. A field rule targets a balancer via balancerTag and a
// concrete outbound via outboundTag, so the caller picks the right key.
func routingTagIsBalancer(routing map[string]any, tag string) bool {
	if tag == "" {
		return false
	}
	balancers, ok := routing["balancers"].([]any)
	if !ok {
		return false
	}
	for _, b := range balancers {
		bm, ok := b.(map[string]any)
		if !ok {
			continue
		}
		if t, ok := bm["tag"].(string); ok && t == tag {
			return true
		}
	}
	return false
}

// injectMtprotoEgress wires one routed mtproto inbound into the generated
// config: it appends a loopback SOCKS inbound (tagged with the inbound's own
// tag, on the egress port persisted in settings) and, when an outbound is
// selected, prepends a routing rule sending that tag to it. Both live only in
// the generated config — the stored template is untouched.
func injectMtprotoEgress(cfg *xray.Config, inbound *model.Inbound) {
	var parsed struct {
		RouteThroughXray bool   `json:"routeThroughXray"`
		RouteXrayPort    int    `json:"routeXrayPort"`
		OutboundTag      string `json:"outboundTag"`
	}
	if err := json.Unmarshal([]byte(inbound.Settings), &parsed); err != nil {
		return
	}
	if !parsed.RouteThroughXray || parsed.RouteXrayPort <= 0 || inbound.Tag == "" {
		return
	}
	tag := inbound.Tag
	for i := range cfg.InboundConfigs {
		if cfg.InboundConfigs[i].Tag == tag {
			logger.Warning("mtproto egress: inbound tag [", tag, "] already present in generated config, skipping bridge")
			return
		}
	}

	if parsed.OutboundTag != "" {
		routing := map[string]any{}
		parseOK := true
		if len(cfg.RouterConfig) > 0 {
			if err := json.Unmarshal(cfg.RouterConfig, &routing); err != nil {
				logger.Warning("mtproto egress: routing section is unparsable, skipping rule:", err)
				parseOK = false
			}
		}
		if parseOK {
			rules, _ := routing["rules"].([]any)
			rule := map[string]any{
				"type":       "field",
				"inboundTag": []any{tag},
			}
			if routingTagIsBalancer(routing, parsed.OutboundTag) {
				rule["balancerTag"] = parsed.OutboundTag
			} else {
				rule["outboundTag"] = parsed.OutboundTag
			}
			routing["rules"] = append([]any{rule}, rules...)
			if newRouting, err := json.Marshal(routing); err == nil {
				cfg.RouterConfig = json_util.RawMessage(newRouting)
			} else {
				logger.Warning("mtproto egress: failed to rebuild routing section, skipping rule:", err)
			}
		}
	}

	cfg.InboundConfigs = append(cfg.InboundConfigs, xray.InboundConfig{
		Listen:   json_util.RawMessage(`"127.0.0.1"`),
		Port:     parsed.RouteXrayPort,
		Protocol: "socks",
		Settings: json_util.RawMessage(mtprotoEgressSocksSettings),
		Tag:      tag,
	})
}

// tunnelTproxyInbounds builds dokodemo-door inbounds for every enabled
// WG/AWG server that has RouteViaXray turned on. The returned inbounds
// listen on 127.0.0.1 (IPv4 only — see PostUp helpers in awg/wg config.go),
// accept TCP+UDP, and read the original destination from the TPROXY mark.
func tunnelTproxyInbounds() []xray.InboundConfig {
	db := database.GetDB()
	if db == nil {
		return nil
	}
	var (
		out      []xray.InboundConfig
		servers  []model.TunnelServer
		seenTag  = map[string]struct{}{}
		seenPort = map[int]struct{}{}
	)
	if err := db.Where("enable = ? AND route_via_xray = ?", true, true).
		Order("kind").Find(&servers).Error; err != nil {
		logger.Warning("tunnelTproxyInbounds: scan tunnel servers failed:", err)
	}

	add := func(tag string, port int, defaultTag string, defaultPort int) {
		if tag == "" {
			tag = defaultTag
		}
		if port <= 0 {
			port = defaultPort
		}
		if _, dup := seenTag[tag]; dup {
			logger.Warningf("tunnelTproxyInbounds: skip duplicate tag %q", tag)
			return
		}
		if _, dup := seenPort[port]; dup {
			logger.Warningf("tunnelTproxyInbounds: skip duplicate port %d for tag %q", port, tag)
			return
		}
		seenTag[tag] = struct{}{}
		seenPort[port] = struct{}{}
		out = append(out, buildTproxyInbound(tag, port))
	}

	for _, srv := range servers {
		k := tunnel.AWG
		if srv.Kind == model.TunnelKindWg {
			k = tunnel.WG
		}
		add(srv.XrayInboundTag, srv.XrayTproxyPort, k.Name+"-tproxy-in", k.TproxyPort)
	}
	return out
}

// buildTproxyInbound assembles a dokodemo-door inbound with the TPROXY
// socket option set, sniffing enabled so routing rules can match by domain.
// Listens on :: so a single socket catches both IPv4 (via v4-mapped
// addresses on Linux when net.ipv6.bindv6only=0, the default) and native
// IPv6 TPROXY redirects — no need for a second inbound per family.
func buildTproxyInbound(tag string, port int) xray.InboundConfig {
	settings := `{"network":"tcp,udp","followRedirect":true}`
	stream := `{"sockopt":{"tproxy":"tproxy"}}`
	sniffing := `{"enabled":true,"destOverride":["http","tls","quic"],"routeOnly":true}`
	return xray.InboundConfig{
		Listen:         json_util.RawMessage(fmt.Sprintf("%q", "::")),
		Port:           port,
		Protocol:       "dokodemo-door",
		Settings:       json_util.RawMessage(settings),
		StreamSettings: json_util.RawMessage(stream),
		Tag:            tag,
		Sniffing:       json_util.RawMessage(sniffing),
	}
}

// GetXrayTraffic fetches the current traffic statistics from the running Xray process.
func (s *XrayService) GetXrayTraffic() ([]*xray.Traffic, []*xray.ClientTraffic, error) {
	proc := currentProcess()
	if proc == nil || !proc.IsRunning() {
		err := errors.New("xray is not running")
		logger.Debug("Attempted to fetch Xray traffic, but Xray is not running:", err)
		return nil, nil, err
	}
	apiPort := proc.GetAPIPort()
	if err := s.xrayAPI.Init(apiPort); err != nil {
		logger.Debug("Failed to initialize Xray API:", err)
		return nil, nil, err
	}
	defer s.xrayAPI.Close()

	traffic, clientTraffic, err := s.xrayAPI.GetTraffic(true)
	if err != nil {
		logger.Debug("Failed to fetch Xray traffic:", err)
		return nil, nil, err
	}
	return traffic, clientTraffic, nil
}

// RestartXray restarts the Xray process, optionally forcing a restart even if config unchanged.
func (s *XrayService) RestartXray(isForce bool) error {
	lock.Lock()
	defer lock.Unlock()
	logger.Debug("restart Xray, force:", isForce)
	isManuallyStopped.Store(false)

	xrayConfig, err := s.GetXrayConfig()
	if err != nil {
		return err
	}

	if proc := procPtr.Load(); proc != nil && proc.IsRunning() {
		if !isForce && proc.GetConfig().Equals(xrayConfig) && !isNeedXrayRestart.Load() {
			logger.Debug("It does not need to restart Xray")
			return nil
		}
		proc.Stop()
	}

	newProc := xray.NewProcess(xrayConfig)
	procPtr.Store(newProc)
	result = ""
	if err := newProc.Start(); err != nil {
		return err
	}

	return nil
}

// StopXray stops the running Xray process.
func (s *XrayService) StopXray() error {
	lock.Lock()
	defer lock.Unlock()
	isManuallyStopped.Store(true)
	logger.Debug("Attempting to stop Xray...")
	if proc := procPtr.Load(); proc != nil && proc.IsRunning() {
		return proc.Stop()
	}
	return errors.New("xray is not running")
}

// SetToNeedRestart marks that Xray needs to be restarted.
func (s *XrayService) SetToNeedRestart() {
	isNeedXrayRestart.Store(true)
}

// IsNeedRestartAndSetFalse checks if restart is needed and resets the flag to false.
func (s *XrayService) IsNeedRestartAndSetFalse() bool {
	return isNeedXrayRestart.CompareAndSwap(true, false)
}

// DidXrayCrash checks if Xray crashed by verifying it's not running and wasn't manually stopped.
func (s *XrayService) DidXrayCrash() bool {
	return !s.IsXrayRunning() && !isManuallyStopped.Load()
}
