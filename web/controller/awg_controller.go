package controller

import (
	"errors"
	"strconv"

	"github.com/coinman-dev/3ax-ui/v2/database/model"
	"github.com/coinman-dev/3ax-ui/v2/web/service"

	"github.com/gin-gonic/gin"
)

type AwgController struct {
	awgService service.AwgService
}

func NewAwgController(g *gin.RouterGroup) *AwgController {
	a := &AwgController{}
	a.initRouter(g)
	return a
}

func (a *AwgController) initRouter(g *gin.RouterGroup) {
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

func (a *AwgController) getServer(c *gin.Context) {
	server, err := a.awgService.GetServer()
	if err != nil {
		jsonMsg(c, "get AWG server", err)
		return
	}
	jsonObj(c, server, nil)
}

func (a *AwgController) saveServer(c *gin.Context) {
	var server model.AwgServer
	if err := c.ShouldBindJSON(&server); err != nil {
		jsonMsg(c, "invalid request", err)
		return
	}
	err := a.awgService.SaveServer(&server)
	jsonMsg(c, "AWG server settings saved", err)
}

func (a *AwgController) toggleServer(c *gin.Context) {
	var body struct {
		Enable bool `json:"enable"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		jsonMsg(c, "invalid request", err)
		return
	}
	err := a.awgService.ToggleServer(body.Enable)
	if body.Enable {
		jsonMsg(c, I18nWeb(c, "pages.awg.restartSuccess"), err)
		return
	}
	jsonMsg(c, I18nWeb(c, "pages.awg.stopSuccess"), err)
}

func (a *AwgController) resetServer(c *gin.Context) {
	server, err := a.awgService.ResetToDefaults()
	if err != nil {
		jsonMsg(c, "reset AWG server", err)
		return
	}
	jsonObj(c, server, nil)
}

func (a *AwgController) getServerStatus(c *gin.Context) {
	status := a.awgService.GetServerStatus()
	jsonObj(c, status, nil)
}

func (a *AwgController) getInterfaces(c *gin.Context) {
	ifaces := a.awgService.GetNetworkInterfaces()
	jsonObj(c, ifaces, nil)
}

func (a *AwgController) getClients(c *gin.Context) {
	clients, err := a.awgService.GetClients()
	if err != nil {
		jsonMsg(c, "get AWG clients", err)
		return
	}
	jsonObj(c, clients, nil)
}

func (a *AwgController) addClient(c *gin.Context) {
	var client model.AwgClient
	if err := c.ShouldBindJSON(&client); err != nil {
		jsonMsg(c, "invalid request", err)
		return
	}
	err := a.awgService.AddClient(&client)
	if err != nil {
		jsonMsg(c, "add AWG client", err)
		return
	}
	jsonObj(c, client, nil)
}

func (a *AwgController) updateClient(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		jsonMsg(c, "invalid id", err)
		return
	}
	var client model.AwgClient
	if err := c.ShouldBindJSON(&client); err != nil {
		jsonMsg(c, "invalid request", err)
		return
	}
	client.Id = id
	err = a.awgService.UpdateClient(&client)
	jsonMsg(c, "AWG client updated", err)
}

func (a *AwgController) updateClientByUUID(c *gin.Context) {
	clientUUID := c.Param("uuid")
	if clientUUID == "" {
		jsonMsg(c, "invalid uuid", errors.New("missing uuid"))
		return
	}
	var client model.AwgClient
	if err := c.ShouldBindJSON(&client); err != nil {
		jsonMsg(c, "invalid request", err)
		return
	}
	err := a.awgService.UpdateClientByUUID(clientUUID, &client)
	jsonMsg(c, "AWG client updated", err)
}

func (a *AwgController) deleteClient(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		jsonMsg(c, "invalid id", err)
		return
	}
	err = a.awgService.DeleteClient(id)
	jsonMsg(c, "AWG client deleted", err)
}

func (a *AwgController) deleteClientByUUID(c *gin.Context) {
	clientUUID := c.Param("uuid")
	if clientUUID == "" {
		jsonMsg(c, "invalid uuid", errors.New("missing uuid"))
		return
	}
	err := a.awgService.DeleteClientByUUID(clientUUID)
	jsonMsg(c, "AWG client deleted", err)
}

func (a *AwgController) toggleClient(c *gin.Context) {
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
	err = a.awgService.ToggleClient(id, body.Enable)
	jsonMsg(c, "AWG client toggled", err)
}

func (a *AwgController) toggleClientByUUID(c *gin.Context) {
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
	err := a.awgService.ToggleClientByUUID(clientUUID, body.Enable)
	jsonMsg(c, "AWG client toggled", err)
}

func (a *AwgController) getClientConfig(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		jsonMsg(c, "invalid id", err)
		return
	}
	config, err := a.awgService.GetClientConfig(id)
	if err != nil {
		jsonMsg(c, "get client config", err)
		return
	}
	jsonObj(c, config, nil)
}

func (a *AwgController) getClientConfigByUUID(c *gin.Context) {
	clientUUID := c.Param("uuid")
	if clientUUID == "" {
		jsonMsg(c, "invalid uuid", errors.New("missing uuid"))
		return
	}
	config, err := a.awgService.GetClientConfigByUUID(clientUUID)
	if err != nil {
		jsonMsg(c, "get client config", err)
		return
	}
	jsonObj(c, config, nil)
}

func (a *AwgController) resetClientTraffic(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		jsonMsg(c, "invalid id", err)
		return
	}
	err = a.awgService.ResetClientTraffic(id)
	jsonMsg(c, "AWG client traffic reset", err)
}

func (a *AwgController) resetClientTrafficByUUID(c *gin.Context) {
	clientUUID := c.Param("uuid")
	if clientUUID == "" {
		jsonMsg(c, "invalid uuid", errors.New("missing uuid"))
		return
	}
	err := a.awgService.ResetClientTrafficByUUID(clientUUID)
	jsonMsg(c, "AWG client traffic reset", err)
}
