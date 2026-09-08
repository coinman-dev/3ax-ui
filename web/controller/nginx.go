package controller

import (
	"net/http"
	"strconv"

	"github.com/coinman-dev/3ax-ui/v2/database/model"
	"github.com/coinman-dev/3ax-ui/v2/web/service"

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

// stubPreview serves one saved page for the gallery to draw a thumbnail of.
//
// It is not sent with the list: a page can be half a megabyte, and there is no
// reason to carry every one of them on every load of the settings page. The
// markup belongs to the operator, but it is still rendered inside a logged-in
// session, so it goes out with everything switched off. The iframe's sandbox
// takes away the origin; the header takes away scripts, forms, and every
// request to anywhere else.
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
	c.Header("Content-Security-Policy",
		"sandbox; default-src 'none'; style-src 'unsafe-inline'; img-src data:; font-src data:")
	c.Header("X-Content-Type-Options", "nosniff")
	c.Header("Cache-Control", "no-store")
	c.Data(http.StatusOK, "text/html; charset=utf-8", []byte(site.Html))
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

// confirm is the panel reporting that it was reached after the ports closed.
// Nothing is passed in and nothing needs to be: this request arriving at all is
// the proof, since it came through an authenticated session on the new address.
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
