package controller

import (
	"errors"
	"fmt"
	"strconv"

	"github.com/coinman-dev/3ax-ui/v2/database/model"
	"github.com/coinman-dev/3ax-ui/v2/tunnel"
	"github.com/coinman-dev/3ax-ui/v2/web/service"

	"github.com/gin-gonic/gin"
)

// tunnelService is the slice of the tunnel service the HTTP layer needs. Both
// AwgService and WgService satisfy it — they are the same generic
// implementation, told apart by their type parameter.
type tunnelService interface {
	GetServer() (*model.TunnelServer, error)
	SaveServer(server *model.TunnelServer) error
	ToggleServer(enable bool) error
	ResetToDefaults() (*model.TunnelServer, error)
	GetServerStatus() *service.TunnelStatus
	GetNetworkInterfaces() []service.NetworkInterface

	GetClients() ([]model.TunnelClient, error)
	AddClient(client *model.TunnelClient) error
	UpdateClient(client *model.TunnelClient) error
	UpdateClientByUUID(clientUUID string, client *model.TunnelClient) error
	DeleteClient(id int) error
	DeleteClientByUUID(clientUUID string) error
	ToggleClient(id int, enable bool) error
	ToggleClientByUUID(clientUUID string, enable bool) error

	GetClientConfig(id int) (string, error)
	GetClientConfigByUUID(clientUUID string) (string, error)
	ResetClientTraffic(id int) error
	ResetClientTrafficByUUID(clientUUID string) error

	GenerateObfuscation(preset string) tunnel.Obfuscation20
}

// TunnelController serves one tunnel flavour. The route set is identical for
// both; what differs is the service behind it, the i18n namespace of a couple
// of messages, and the two AmneziaWG-only endpoints.
type TunnelController struct {
	BaseController
	svc      tunnelService
	kind     tunnel.Kind
	i18nBase string
	tgbot    service.Tgbot
}

// NewAwgController mounts the AmneziaWG API under g.
func NewAwgController(g *gin.RouterGroup) *TunnelController {
	return newTunnelController(g, &service.AwgService{}, tunnel.AWG, "pages.awg")
}

// NewWgController mounts the native WireGuard API under g.
func NewWgController(g *gin.RouterGroup) *TunnelController {
	return newTunnelController(g, &service.WgService{}, tunnel.WG, "pages.nativewg")
}

func newTunnelController(g *gin.RouterGroup, svc tunnelService, kind tunnel.Kind, i18nBase string) *TunnelController {
	a := &TunnelController{svc: svc, kind: kind, i18nBase: i18nBase}
	a.initRouter(g)
	return a
}

func (a *TunnelController) initRouter(g *gin.RouterGroup) {
	g.GET("/server", a.getServer)
	g.POST("/server", a.saveServer)
	g.POST("/server/toggle", a.toggleServer)
	g.POST("/server/reset", a.resetServer)
	g.GET("/server/status", a.getServerStatus)
	g.GET("/interfaces", a.getInterfaces)

	// Obfuscation and the "push configs to clients" helper exist only for the
	// flavour that has obfuscation.
	if a.kind.Obfuscation {
		g.POST("/server/generate", a.generateObfuscation)
		g.POST("/server/notify", a.notifyClients)
	}

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

func (a *TunnelController) getServer(c *gin.Context) {
	server, err := a.svc.GetServer()
	if err != nil {
		jsonMsg(c, fmt.Sprintf("get %s server", a.kind.Title), err)
		return
	}
	jsonObj(c, server, nil)
}

func (a *TunnelController) saveServer(c *gin.Context) {
	var server model.TunnelServer
	if err := c.ShouldBindJSON(&server); err != nil {
		jsonMsg(c, "invalid request", err)
		return
	}
	err := a.svc.SaveServer(&server)
	jsonMsg(c, fmt.Sprintf("%s server settings saved", a.kind.Title), err)
}

func (a *TunnelController) toggleServer(c *gin.Context) {
	var body struct {
		Enable bool `json:"enable"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		jsonMsg(c, "invalid request", err)
		return
	}
	err := a.svc.ToggleServer(body.Enable)
	if body.Enable {
		jsonMsg(c, I18nWeb(c, a.i18nBase+".restartSuccess"), err)
		return
	}
	jsonMsg(c, I18nWeb(c, a.i18nBase+".stopSuccess"), err)
}

func (a *TunnelController) resetServer(c *gin.Context) {
	server, err := a.svc.ResetToDefaults()
	if err != nil {
		jsonMsg(c, fmt.Sprintf("reset %s server", a.kind.Title), err)
		return
	}
	jsonObj(c, server, nil)
}

// generateObfuscation returns a randomized AmneziaWG 2.0 parameter set for the
// requested preset ("default" or "mobile"). It does not save anything — the UI
// fills the form with the result and the admin saves to apply.
func (a *TunnelController) generateObfuscation(c *gin.Context) {
	preset := c.Query("preset")
	jsonObj(c, a.svc.GenerateObfuscation(preset), nil)
}

// notifyClients sends every Telegram-linked AWG client their current config
// via the bot — used after switching the server to 2.0 so clients can re-import.
func (a *TunnelController) notifyClients(c *gin.Context) {
	count, err := a.tgbot.SendAwgConfigsToClients()
	if err != nil {
		jsonMsg(c, fmt.Sprintf("notify %s clients", a.kind.Title), err)
		return
	}
	jsonObj(c, count, nil)
}

func (a *TunnelController) getServerStatus(c *gin.Context) {
	// The service reports a flavour-neutral status; the payload keeps the
	// awgInstalled/awgVersion keys the frontend has always read.
	status := a.svc.GetServerStatus()
	if a.kind.Obfuscation {
		jsonObj(c, service.AwgStatus{
			Running:      status.Running,
			AwgInstalled: status.Installed,
			AwgVersion:   status.Version,
		}, nil)
		return
	}
	jsonObj(c, service.WgStatus{
		Running:     status.Running,
		WgInstalled: status.Installed,
		WgVersion:   status.Version,
	}, nil)
}

func (a *TunnelController) getInterfaces(c *gin.Context) {
	ifaces := a.svc.GetNetworkInterfaces()
	jsonObj(c, ifaces, nil)
}

func (a *TunnelController) getClients(c *gin.Context) {
	clients, err := a.svc.GetClients()
	if err != nil {
		jsonMsg(c, fmt.Sprintf("get %s clients", a.kind.Title), err)
		return
	}
	jsonObj(c, clients, nil)
}

func (a *TunnelController) addClient(c *gin.Context) {
	var client model.TunnelClient
	if err := c.ShouldBindJSON(&client); err != nil {
		jsonMsg(c, "invalid request", err)
		return
	}
	err := a.svc.AddClient(&client)
	if err != nil {
		jsonMsg(c, fmt.Sprintf("add %s client", a.kind.Title), err)
		return
	}
	jsonObj(c, client, nil)
}

func (a *TunnelController) updateClient(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		jsonMsg(c, "invalid id", err)
		return
	}
	var client model.TunnelClient
	if err := c.ShouldBindJSON(&client); err != nil {
		jsonMsg(c, "invalid request", err)
		return
	}
	client.Id = id
	err = a.svc.UpdateClient(&client)
	jsonMsg(c, fmt.Sprintf("%s client updated", a.kind.Title), err)
}

func (a *TunnelController) updateClientByUUID(c *gin.Context) {
	clientUUID := c.Param("uuid")
	if clientUUID == "" {
		jsonMsg(c, "invalid uuid", errors.New("missing uuid"))
		return
	}
	var client model.TunnelClient
	if err := c.ShouldBindJSON(&client); err != nil {
		jsonMsg(c, "invalid request", err)
		return
	}
	err := a.svc.UpdateClientByUUID(clientUUID, &client)
	jsonMsg(c, fmt.Sprintf("%s client updated", a.kind.Title), err)
}

func (a *TunnelController) deleteClient(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		jsonMsg(c, "invalid id", err)
		return
	}
	err = a.svc.DeleteClient(id)
	jsonMsg(c, fmt.Sprintf("%s client deleted", a.kind.Title), err)
}

func (a *TunnelController) deleteClientByUUID(c *gin.Context) {
	clientUUID := c.Param("uuid")
	if clientUUID == "" {
		jsonMsg(c, "invalid uuid", errors.New("missing uuid"))
		return
	}
	err := a.svc.DeleteClientByUUID(clientUUID)
	jsonMsg(c, fmt.Sprintf("%s client deleted", a.kind.Title), err)
}

func (a *TunnelController) toggleClient(c *gin.Context) {
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
	err = a.svc.ToggleClient(id, body.Enable)
	jsonMsg(c, fmt.Sprintf("%s client toggled", a.kind.Title), err)
}

func (a *TunnelController) toggleClientByUUID(c *gin.Context) {
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
	err := a.svc.ToggleClientByUUID(clientUUID, body.Enable)
	jsonMsg(c, fmt.Sprintf("%s client toggled", a.kind.Title), err)
}

func (a *TunnelController) getClientConfig(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		jsonMsg(c, "invalid id", err)
		return
	}
	config, err := a.svc.GetClientConfig(id)
	if err != nil {
		jsonMsg(c, "get client config", err)
		return
	}
	jsonObj(c, config, nil)
}

func (a *TunnelController) getClientConfigByUUID(c *gin.Context) {
	clientUUID := c.Param("uuid")
	if clientUUID == "" {
		jsonMsg(c, "invalid uuid", errors.New("missing uuid"))
		return
	}
	config, err := a.svc.GetClientConfigByUUID(clientUUID)
	if err != nil {
		jsonMsg(c, "get client config", err)
		return
	}
	jsonObj(c, config, nil)
}

func (a *TunnelController) resetClientTraffic(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		jsonMsg(c, "invalid id", err)
		return
	}
	err = a.svc.ResetClientTraffic(id)
	jsonMsg(c, fmt.Sprintf("%s client traffic reset", a.kind.Title), err)
}

func (a *TunnelController) resetClientTrafficByUUID(c *gin.Context) {
	clientUUID := c.Param("uuid")
	if clientUUID == "" {
		jsonMsg(c, "invalid uuid", errors.New("missing uuid"))
		return
	}
	err := a.svc.ResetClientTrafficByUUID(clientUUID)
	jsonMsg(c, fmt.Sprintf("%s client traffic reset", a.kind.Title), err)
}
