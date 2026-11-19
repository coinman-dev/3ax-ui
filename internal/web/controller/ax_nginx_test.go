package controller

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/dbtest"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/web/middleware"
)

// The Nginx page shows saved cover pages in an iframe; the panel-wide DENY made
// browsers refuse every thumbnail.
func TestStubPreviewMayBeFramedByThePanel(t *testing.T) {
	dbtest.InitDB(t, filepath.Join(t.TempDir(), "x-ui.db"))
	site := &model.StubSite{Name: "cover", Html: "<p>hi</p>"}
	if err := database.GetDB().Create(site).Error; err != nil {
		t.Fatal(err)
	}
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(middleware.SecurityHeadersMiddleware(false))
	NewNginxController(r.Group("/panel/api/nginx"))

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/panel/api/nginx/stub/"+strconv.Itoa(site.Id)+"/preview", nil))
	if w.Code != http.StatusOK || w.Body.String() != "<p>hi</p>" {
		t.Fatalf("preview = %d %q", w.Code, w.Body.String())
	}
	if got := w.Header().Get("X-Frame-Options"); got != "SAMEORIGIN" {
		t.Errorf("X-Frame-Options = %q, want SAMEORIGIN", got)
	}
	csp := w.Header().Get("Content-Security-Policy")
	if !strings.Contains(csp, "frame-ancestors 'self'") || !strings.Contains(csp, "sandbox") {
		t.Errorf("CSP = %q, want the sandbox kept and frame-ancestors 'self'", csp)
	}
}
