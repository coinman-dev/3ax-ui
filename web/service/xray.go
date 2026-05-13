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
	"github.com/coinman-dev/3ax-ui/v2/util/json_util"
	"github.com/coinman-dev/3ax-ui/v2/xray"

	"go.uber.org/atomic"
)

var (
	p                 *xray.Process
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

// IsXrayRunning checks if the Xray process is currently running.
func (s *XrayService) IsXrayRunning() bool {
	return p != nil && p.IsRunning()
}

// GetXrayErr returns the error from the Xray process, if any.
func (s *XrayService) GetXrayErr() error {
	if p == nil {
		return nil
	}

	err := p.GetErr()
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
	if result != "" {
		return result
	}
	if s.IsXrayRunning() {
		return ""
	}
	if p == nil {
		return ""
	}

	result = p.GetResult()

	if runtime.GOOS == "windows" && result == "exit status 1" {
		// exit status 1 on Windows means that Xray process was killed
		// as we kill process to stop in on Windows, this is not an error
		return ""
	}

	return result
}

// GetXrayVersion returns the version of the running Xray process.
func (s *XrayService) GetXrayVersion() string {
	if p == nil {
		return "Unknown"
	}
	return p.GetVersion()
}

// RemoveIndex removes an element at the specified index from a slice.
// Returns a new slice with the element removed.
func RemoveIndex(s []any, index int) []any {
	return append(s[:index], s[index+1:]...)
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

	s.inboundService.AddTraffic(nil, nil)

	inbounds, err := s.inboundService.GetAllInbounds()
	if err != nil {
		return nil, err
	}
	for _, inbound := range inbounds {
		if !inbound.Enable {
			continue
		}
		// Skip AmneziaWG and NativeWG — they are not Xray protocols
		if inbound.Protocol == model.AmneziaWG || inbound.Protocol == model.NativeWG {
			continue
		}
		// get settings clients
		settings := map[string]any{}
		json.Unmarshal([]byte(inbound.Settings), &settings)
		clients, ok := settings["clients"].([]any)
		if ok {
			// check users active or not
			clientStats := inbound.ClientStats
			for _, clientTraffic := range clientStats {
				indexDecrease := 0
				for index, client := range clients {
					c := client.(map[string]any)
					if c["email"] == clientTraffic.Email {
						if !clientTraffic.Enable {
							clients = RemoveIndex(clients, index-indexDecrease)
							indexDecrease++
							logger.Infof("Remove Inbound User %s due to expiration or traffic limit", c["email"])
						}
					}
				}
			}

			// MIXED (SOCKS5) and HTTP inbounds use xray's settings.accounts[]={user,pass}.
			// Translate panel's clients[] to that shape, dropping panel-only fields and
			// disabled users — the basic-auth username acts as the per-user identity that
			// xray reports back in its stats keys (user>>>EMAIL>>>traffic>>>...).
			if inbound.Protocol == model.Mixed || inbound.Protocol == model.HTTP {
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
				modifiedSettings, err := json.MarshalIndent(settings, "", "  ")
				if err != nil {
					return nil, err
				}
				inbound.Settings = string(modifiedSettings)
			} else {
				// clear client config for additional parameters
				var final_clients []any
				for _, client := range clients {
					c := client.(map[string]any)
					if c["enable"] != nil {
						if enable, ok := c["enable"].(bool); ok && !enable {
							continue
						}
					}
					for key := range c {
						if key != "email" && key != "id" && key != "password" && key != "flow" && key != "method" {
							delete(c, key)
						}
						if c["flow"] == "xtls-rprx-vision-udp443" {
							c["flow"] = "xtls-rprx-vision"
						}
					}
					final_clients = append(final_clients, any(c))
				}

				settings["clients"] = final_clients
				modifiedSettings, err := json.MarshalIndent(settings, "", "  ")
				if err != nil {
					return nil, err
				}

				inbound.Settings = string(modifiedSettings)
			}
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

	return xrayConfig, nil
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
		awgs     []model.AwgServer
		wgs      []model.WgServer
		seenTag  = map[string]struct{}{}
		seenPort = map[int]struct{}{}
	)
	if err := db.Where("enable = ? AND route_via_xray = ?", true, true).Find(&awgs).Error; err != nil {
		logger.Warning("tunnelTproxyInbounds: scan awg servers failed:", err)
	}
	if err := db.Where("enable = ? AND route_via_xray = ?", true, true).Find(&wgs).Error; err != nil {
		logger.Warning("tunnelTproxyInbounds: scan wg servers failed:", err)
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

	for _, s := range awgs {
		add(s.XrayInboundTag, s.XrayTproxyPort, "awg-tproxy-in", 12345)
	}
	for _, s := range wgs {
		add(s.XrayInboundTag, s.XrayTproxyPort, "wg-tproxy-in", 12346)
	}
	return out
}

// buildTproxyInbound assembles a dokodemo-door inbound with the TPROXY
// socket option set, sniffing enabled so routing rules can match by domain.
func buildTproxyInbound(tag string, port int) xray.InboundConfig {
	settings := `{"network":"tcp,udp","followRedirect":true}`
	stream := `{"sockopt":{"tproxy":"tproxy"}}`
	sniffing := `{"enabled":true,"destOverride":["http","tls","quic"],"routeOnly":true}`
	return xray.InboundConfig{
		Listen:         json_util.RawMessage(fmt.Sprintf("%q", "127.0.0.1")),
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
	if !s.IsXrayRunning() {
		err := errors.New("xray is not running")
		logger.Debug("Attempted to fetch Xray traffic, but Xray is not running:", err)
		return nil, nil, err
	}
	apiPort := p.GetAPIPort()
	s.xrayAPI.Init(apiPort)
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

	if s.IsXrayRunning() {
		if !isForce && p.GetConfig().Equals(xrayConfig) && !isNeedXrayRestart.Load() {
			logger.Debug("It does not need to restart Xray")
			return nil
		}
		p.Stop()
	}

	p = xray.NewProcess(xrayConfig)
	result = ""
	err = p.Start()
	if err != nil {
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
	if s.IsXrayRunning() {
		return p.Stop()
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
