package controller

import (
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

	// Cover pages
	g.GET("/stubs", a.stubs)
	g.GET("/stub/:id", a.stub)
	g.GET("/stub-templates", a.stubTemplates)
	g.POST("/stub/save", a.saveStub)
	g.POST("/stub/del/:id", a.deleteStub)
	g.POST("/stub/activate/:id", a.activateStub)
}

func (a *NginxController) stubs(c *gin.Context) {
	sites, err := a.stubService.GetSites()
	if err != nil {
		jsonMsg(c, I18nWeb(c, "pages.nginx.toasts.stubLoadFailed"), err)
		return
	}
	jsonObj(c, sites, nil)
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

func (a *NginxController) apply(c *gin.Context) {
	var in service.NginxSettings
	if err := c.ShouldBindJSON(&in); err != nil {
		jsonMsg(c, I18nWeb(c, "pages.nginx.toasts.invalidRequest"), err)
		return
	}
	err := a.nginxService.Apply(in)
	jsonMsg(c, I18nWeb(c, "pages.nginx.toasts.applied"), err)
}
