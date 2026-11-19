package service

// Mixed (SOCKS5) and HTTP inbounds take panel clients in 3AX-UI. The client's
// email is the proxy username, which Xray reports as the user in its stats, so
// traffic, quotas and expiry run through the same path as any other client.

import (
	"strings"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/util/common"
	"github.com/mhsanaei/3x-ui/v3/internal/util/random"
)

const proxyPasswordLength = 16

func isProxyClientProtocol(p model.Protocol) bool {
	return p == model.Mixed || p == model.HTTP
}

// fillProxyClientDefaults rejects a username HTTP Basic auth cannot carry
// (it splits user:pass on the first colon; SOCKS5 caps it at 255 bytes).
func fillProxyClientDefaults(c *model.Client) error {
	if strings.Contains(c.Email, ":") {
		return common.NewErrorf("client %q: a SOCKS5/HTTP username cannot contain ':'", c.Email)
	}
	if len(c.Email) > 255 {
		return common.NewErrorf("client %q: a SOCKS5/HTTP username is limited to 255 bytes", c.Email)
	}
	if c.Password == "" {
		c.Password = random.Seq(proxyPasswordLength)
	}
	return nil
}

func proxyAccount(c model.Client) map[string]any {
	return map[string]any{"user": c.Email, "pass": c.Password}
}

// applyProxyAccounts emits the enabled clients as Xray accounts after the
// inbound's own static ones. An inbound with any client always demands a
// password, and with none enabled gets an unusable account: an HTTP inbound
// without accounts lets anyone in.
func applyProxyAccounts(settings map[string]any, protocol model.Protocol, clientAccounts []any, hasClients bool) bool {
	_, hadClients := settings["clients"]
	delete(settings, "clients")
	if !hasClients {
		return hadClients
	}
	static, _ := settings["accounts"].([]any)
	accounts := append(append([]any{}, static...), clientAccounts...)
	if len(accounts) == 0 {
		accounts = []any{map[string]any{"user": random.Seq(24), "pass": random.Seq(32)}}
	}
	settings["accounts"] = accounts
	if protocol == model.Mixed {
		settings["auth"] = "password"
	}
	return true
}
