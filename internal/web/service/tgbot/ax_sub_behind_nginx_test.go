package tgbot

import (
	"strings"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

// 3AX-UI: with nginx publishing the subscriptions on the site's domain, the bot
// must not hand out subDomain:subPort, a port "only 443" mode closes.
func TestBotSubURLsFollowNginx(t *testing.T) {
	tb, _ := newLinksCallbackTgbot(t, ownerMail)
	for k, v := range map[string]string{
		"subDomain": "sub.cdn.com", "subPort": "2096",
		"nginxMode": "shared", "nginxDomain": "site.example", "nginxSubsBehind443": "true",
	} {
		if err := database.GetDB().Create(&model.Setting{Key: k, Value: v}).Error; err != nil {
			t.Fatal(err)
		}
	}
	subURL, _, err := tb.buildSubscriptionURLs(ownerMail)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(subURL, "https://site.example/") || !strings.HasSuffix(subURL, "/sub-owned") {
		t.Fatalf("subURL = %q, want https://site.example/<subPath>/sub-owned", subURL)
	}
}
