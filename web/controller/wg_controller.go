package controller

import (
	"errors"
	"strconv"

	"github.com/coinman-dev/3ax-ui/v2/database/model"
	"github.com/coinman-dev/3ax-ui/v2/web/service"

	"github.com/gin-gonic/gin"
)

type WgController struct {
	wgService service.WgService
}

func NewWgController(g *gin.RouterGroup) *WgController {
	a := &WgController{}
	a.initRouter(g)
	return a
}

func (a *WgController) initRouter(g *gin.RouterGroup) {
	g.GET("/server", a.getServer)
	g.POST("/server", a.saveServer)
	g.POST("/server/toggle", a.toggleServer)
	g.POST("/server/reset", a.resetServer)
	g.GET("/server/status", a.getServerStatus)
	g.GET("/interfaces", a.getInterfaces)

	g.GET("/clients", a.getClients)
	g.POST("/client/add", a.addClient)
	g.POST("/client/update/:id", a.updateClient)
	g.POST("/client/updateByUuid/:uuid", a.updateClientByUUID)
	g.POST("/client/del/:id", a.deleteClient)
	g.POST("/client/delByUuid/:uuid", a.deleteClientByUUID)
	g.POST("/client/toggle/:id", a.toggleClient)
	g.POST("/client/toggleByUuid/:uuid", a.toggleClientByUUID)

	g.GET("/client/:id/config", a.getClientConfig)
	g.GET("/client/uuid/:uuid/config", a.getClientConfigByUUID)
	g.POST("/client/resetTraffic/:id", a.resetClientTraffic)
	g.POST("/client/resetTrafficByUuid/:uuid", a.resetClientTrafficByUUID)
}

func (a *WgController) getServer(c *gin.Context) {
	server, err := a.wgService.GetServer()
	if err != nil {
		jsonMsg(c, "get WG server", err)
		return
	}
	jsonObj(c, server, nil)
}

func (a *WgController) saveServer(c *gin.Context) {
	var server model.WgServer
	if err := c.ShouldBindJSON(&server); err != nil {
		jsonMsg(c, "invalid request", err)
		return
	}
	err := a.wgService.SaveServer(&server)
	jsonMsg(c, "WG server settings saved", err)
}

func (a *WgController) toggleServer(c *gin.Context) {
	var body struct {
		Enable bool `json:"enable"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		jsonMsg(c, "invalid request", err)
		return
	}
	err := a.wgService.ToggleServer(body.Enable)
	if body.Enable {
		jsonMsg(c, I18nWeb(c, "pages.nativewg.restartSuccess"), err)
		return
	}
	jsonMsg(c, I18nWeb(c, "pages.nativewg.stopSuccess"), err)
}

func (a *WgController) resetServer(c *gin.Context) {
	server, err := a.wgService.ResetToDefaults()
	if err != nil {
		jsonMsg(c, "reset WG server", err)
		return
	}
	jsonObj(c, server, nil)
}

func (a *WgController) getServerStatus(c *gin.Context) {
	status := a.wgService.GetServerStatus()
	jsonObj(c, status, nil)
}

func (a *WgController) getInterfaces(c *gin.Context) {
	ifaces := a.wgService.GetNetworkInterfaces()
	jsonObj(c, ifaces, nil)
}

func (a *WgController) getClients(c *gin.Context) {
	clients, err := a.wgService.GetClients()
	if err != nil {
		jsonMsg(c, "get WG clients", err)
		return
	}
	jsonObj(c, clients, nil)
}

func (a *WgController) addClient(c *gin.Context) {
	var client model.WgClient
	if err := c.ShouldBindJSON(&client); err != nil {
		jsonMsg(c, "invalid request", err)
		return
	}
	err := a.wgService.AddClient(&client)
	if err != nil {
		jsonMsg(c, "add WG client", err)
		return
	}
	jsonObj(c, client, nil)
}

func (a *WgController) updateClient(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		jsonMsg(c, "invalid id", err)
		return
	}
	var client model.WgClient
	if err := c.ShouldBindJSON(&client); err != nil {
		jsonMsg(c, "invalid request", err)
		return
	}
	client.Id = id
	err = a.wgService.UpdateClient(&client)
	jsonMsg(c, "WG client updated", err)
}

func (a *WgController) updateClientByUUID(c *gin.Context) {
	clientUUID := c.Param("uuid")
	if clientUUID == "" {
		jsonMsg(c, "invalid uuid", errors.New("missing uuid"))
		return
	}
	var client model.WgClient
	if err := c.ShouldBindJSON(&client); err != nil {
		jsonMsg(c, "invalid request", err)
		return
	}
	err := a.wgService.UpdateClientByUUID(clientUUID, &client)
	jsonMsg(c, "WG client updated", err)
}

func (a *WgController) deleteClient(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		jsonMsg(c, "invalid id", err)
		return
	}
	err = a.wgService.DeleteClient(id)
	jsonMsg(c, "WG client deleted", err)
}

func (a *WgController) deleteClientByUUID(c *gin.Context) {
	clientUUID := c.Param("uuid")
	if clientUUID == "" {
		jsonMsg(c, "invalid uuid", errors.New("missing uuid"))
		return
	}
	err := a.wgService.DeleteClientByUUID(clientUUID)
	jsonMsg(c, "WG client deleted", err)
}

func (a *WgController) toggleClient(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		jsonMsg(c, "invalid id", err)
		return
	}
	var body struct {
		Enable bool `json:"enable"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		jsonMsg(c, "invalid request", err)
		return
	}
	err = a.wgService.ToggleClient(id, body.Enable)
	jsonMsg(c, "WG client toggled", err)
}

func (a *WgController) toggleClientByUUID(c *gin.Context) {
	clientUUID := c.Param("uuid")
	if clientUUID == "" {
		jsonMsg(c, "invalid uuid", errors.New("missing uuid"))
		return
	}
	var body struct {
		Enable bool `json:"enable"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		jsonMsg(c, "invalid request", err)
		return
	}
	err := a.wgService.ToggleClientByUUID(clientUUID, body.Enable)
	jsonMsg(c, "WG client toggled", err)
}

func (a *WgController) getClientConfig(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		jsonMsg(c, "invalid id", err)
		return
	}
	config, err := a.wgService.GetClientConfig(id)
	if err != nil {
		jsonMsg(c, "get client config", err)
		return
	}
	jsonObj(c, config, nil)
}

func (a *WgController) getClientConfigByUUID(c *gin.Context) {
	clientUUID := c.Param("uuid")
	if clientUUID == "" {
		jsonMsg(c, "invalid uuid", errors.New("missing uuid"))
		return
	}
	config, err := a.wgService.GetClientConfigByUUID(clientUUID)
	if err != nil {
		jsonMsg(c, "get client config", err)
		return
	}
	jsonObj(c, config, nil)
}

func (a *WgController) resetClientTraffic(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		jsonMsg(c, "invalid id", err)
		return
	}
	err = a.wgService.ResetClientTraffic(id)
	jsonMsg(c, "WG client traffic reset", err)
}

func (a *WgController) resetClientTrafficByUUID(c *gin.Context) {
	clientUUID := c.Param("uuid")
	if clientUUID == "" {
		jsonMsg(c, "invalid uuid", errors.New("missing uuid"))
		return
	}
	err := a.wgService.ResetClientTrafficByUUID(clientUUID)
	jsonMsg(c, "WG client traffic reset", err)
}
