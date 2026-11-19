package controller

import (
	"net/http"
	"strconv"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/web/service"

	"github.com/gin-gonic/gin"
)

// NginxController exposes the front-end settings: the mode, the domain, and the
// plan of what applying a mode would do before it does it.
type NginxController struct {
	BaseController
	nginxService service.NginxService
	stubService  service.StubService
}

func NewNginxController(g *gin.RouterGroup) *NginxController {
	a := &NginxController{}
	a.initRouter(g)
	return a
}

func (a *NginxController) initRouter(g *gin.RouterGroup) {
	g.GET("/status", a.status)
	g.GET("/settings", a.settings)
	g.GET("/certificate", a.certificate)
	g.POST("/plan", a.plan)
	g.POST("/apply", a.apply)
	g.POST("/confirm", a.confirm)

	// Cover pages
	g.GET("/stubs", a.stubs)
	g.GET("/stub/:id", a.stub)
	g.GET("/stub/:id/preview", a.stubPreview)
	g.GET("/stub-open", a.stubOpen)
	g.GET("/stub-templates", a.stubTemplates)
	g.POST("/stub/save", a.saveStub)
	g.POST("/stub/del/:id", a.deleteStub)
	g.POST("/stub/activate/:id", a.activateStub)
	g.POST("/stub/activate-template/:key", a.activateStubTemplate)
}

// stubs answers with the gallery: a tile for every built-in template and one
// for every page of the operator's own.
func (a *NginxController) stubs(c *gin.Context) {
	cards, err := a.stubService.Gallery()
	if err != nil {
		jsonMsg(c, I18nWeb(c, "pages.nginx.toasts.stubLoadFailed"), err)
		return
	}
	jsonObj(c, cards, nil)
}

func (a *NginxController) stub(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		jsonMsg(c, I18nWeb(c, "pages.nginx.toasts.invalidRequest"), err)
		return
	}
	site, err := a.stubService.GetSite(id)
	if err != nil {
		jsonMsg(c, I18nWeb(c, "pages.nginx.toasts.stubLoadFailed"), err)
		return
	}
	jsonObj(c, site, nil)
}

// stubPreview serves one page for a gallery thumbnail (pages can be 500KB, so not in the list).
// It renders in a logged-in session, so the CSP sandbox strips origin, scripts, forms and requests.
func (a *NginxController) stubPreview(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		jsonMsg(c, I18nWeb(c, "pages.nginx.toasts.invalidRequest"), err)
		return
	}
	site, err := a.stubService.GetSite(id)
	if err != nil {
		jsonMsg(c, I18nWeb(c, "pages.nginx.toasts.stubLoadFailed"), err)
		return
	}
	// The Nginx page frames this as a thumbnail; the panel-wide DENY would block it.
	c.Header("X-Frame-Options", "SAMEORIGIN")
	c.Header("Content-Security-Policy", "sandbox; default-src 'none'; style-src 'unsafe-inline'; "+
		"img-src data:; font-src data:; frame-ancestors 'self'")
	c.Header("X-Content-Type-Options", "nosniff")
	c.Header("Cache-Control", "no-store")
	c.Data(http.StatusOK, "text/html; charset=utf-8", []byte(site.Html))
}

// stubOpen serves a page with scripts on (the built-in games need them) under «sandbox
// allow-scripts»: an opaque origin keeps uploaded markup off the panel's cookies and API.
func (a *NginxController) stubOpen(c *gin.Context) {
	var html string
	if key := c.Query("key"); key != "" {
		for _, tpl := range a.stubService.Templates() {
			if tpl.Key == key {
				html = tpl.Html
				break
			}
		}
		if html == "" {
			c.String(http.StatusNotFound, "there is no built-in page called %q", key)
			return
		}
	} else {
		id, err := strconv.Atoi(c.Query("id"))
		if err != nil {
			c.String(http.StatusBadRequest, "no page was asked for")
			return
		}
		site, err := a.stubService.GetSite(id)
		if err != nil {
			c.String(http.StatusNotFound, "there is no page with that id")
			return
		}
		html = site.Html
	}
	c.Header("Content-Security-Policy", "sandbox allow-scripts")
	c.Header("X-Content-Type-Options", "nosniff")
	c.Header("Cache-Control", "no-store")
	c.Data(http.StatusOK, "text/html; charset=utf-8", []byte(html))
}

func (a *NginxController) stubTemplates(c *gin.Context) {
	jsonObj(c, a.stubService.Templates(), nil)
}

// saveStub answers with the saved page and whatever is worth warning about,
// which the panel shows without treating the save as failed.
func (a *NginxController) saveStub(c *gin.Context) {
	var site model.StubSite
	if err := c.ShouldBindJSON(&site); err != nil {
		jsonMsg(c, I18nWeb(c, "pages.nginx.toasts.invalidRequest"), err)
		return
	}
	warnings, err := a.stubService.SaveSite(&site)
	if err != nil {
		jsonMsg(c, I18nWeb(c, "pages.nginx.toasts.stubSaveFailed"), err)
		return
	}
	jsonObj(c, gin.H{"site": site, "warnings": warnings}, nil)
}

func (a *NginxController) deleteStub(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		jsonMsg(c, I18nWeb(c, "pages.nginx.toasts.invalidRequest"), err)
		return
	}
	jsonMsg(c, I18nWeb(c, "pages.nginx.toasts.stubDeleted"), a.stubService.DeleteSite(id))
}

func (a *NginxController) activateStub(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		jsonMsg(c, I18nWeb(c, "pages.nginx.toasts.invalidRequest"), err)
		return
	}
	jsonMsg(c, I18nWeb(c, "pages.nginx.toasts.stubActivated"), a.stubService.ActivateSite(id))
}

// activateStubTemplate picks a built-in page straight from the gallery, saving
// it on the way through so it can be edited afterwards like any other.
func (a *NginxController) activateStubTemplate(c *gin.Context) {
	err := a.stubService.ActivateTemplate(c.Param("key"))
	jsonMsg(c, I18nWeb(c, "pages.nginx.toasts.stubActivated"), err)
}

func (a *NginxController) status(c *gin.Context) {
	jsonObj(c, a.nginxService.GetStatus(), nil)
}

// certificate answers for the domain currently typed into the field, which is
// not necessarily the one saved.
func (a *NginxController) certificate(c *gin.Context) {
	jsonObj(c, a.nginxService.CheckCertificate(c.Query("domain")), nil)
}

func (a *NginxController) settings(c *gin.Context) {
	jsonObj(c, a.nginxService.GetSettings(), nil)
}

// plan answers "what would happen", so the panel can show it and ask before a
// single inbound moves.
func (a *NginxController) plan(c *gin.Context) {
	var in service.NginxSettings
	if err := c.ShouldBindJSON(&in); err != nil {
		jsonMsg(c, I18nWeb(c, "pages.nginx.toasts.invalidRequest"), err)
		return
	}
	jsonObj(c, a.nginxService.Plan(in), nil)
}

// confirm takes no input: the request arriving through an authenticated session after the
// ports closed is itself the proof that the panel is still reachable.
func (a *NginxController) confirm(c *gin.Context) {
	jsonMsg(c, I18nWeb(c, "pages.nginx.toasts.confirmed"), a.nginxService.Confirm())
}

func (a *NginxController) apply(c *gin.Context) {
	var in service.NginxSettings
	if err := c.ShouldBindJSON(&in); err != nil {
		jsonMsg(c, I18nWeb(c, "pages.nginx.toasts.invalidRequest"), err)
		return
	}
	err := a.nginxService.Apply(in)
	jsonMsg(c, I18nWeb(c, "pages.nginx.toasts.applied"), err)
}
