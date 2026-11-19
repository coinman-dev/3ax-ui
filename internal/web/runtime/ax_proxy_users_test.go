package runtime

import (
	"context"
	"errors"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

// Xray has no user API for SOCKS/HTTP inbounds and its AddUser silently does
// nothing for them, so a client change must surface as "reload the inbound".
func TestLocalProxyUserChangesAskForAReload(t *testing.T) {
	local := NewLocal(LocalDeps{APIPort: func() int { return 63299 }})
	for _, p := range []model.Protocol{model.Mixed, model.HTTP} {
		ib := &model.Inbound{Protocol: p, Tag: "proxy-in"}
		if err := local.AddUser(context.Background(), ib, map[string]any{"email": "alice", "password": "pw"}); !errors.Is(err, ErrProxyAccountsNeedReload) {
			t.Errorf("%s AddUser = %v, want ErrProxyAccountsNeedReload", p, err)
		}
		if err := local.RemoveUser(context.Background(), ib, "alice"); !errors.Is(err, ErrProxyAccountsNeedReload) {
			t.Errorf("%s RemoveUser = %v, want ErrProxyAccountsNeedReload", p, err)
		}
	}
}
