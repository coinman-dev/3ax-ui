package runtime

import "errors"

// ErrProxyAccountsNeedReload: Xray has no user API for SOCKS/HTTP inbounds, so
// a client change on mixed/http reaches the core only with a regenerated
// inbound (3AX-UI). Callers treat any user-op error as "needs restart".
var ErrProxyAccountsNeedReload = errors.New("mixed/http accounts change only with a reloaded inbound")
