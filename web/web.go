// Package web provides the main web server implementation for the 3AX-UI panel,
// including HTTP/HTTPS serving, routing, templates, and background job scheduling.
package web

import (
	"context"
	"crypto/tls"
	"embed"
	"html/template"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/coinman-dev/3ax-ui/v2/config"
	"github.com/coinman-dev/3ax-ui/v2/logger"
	"github.com/coinman-dev/3ax-ui/v2/mtproto"
	"github.com/coinman-dev/3ax-ui/v2/util/common"
	"github.com/coinman-dev/3ax-ui/v2/web/controller"
	"github.com/coinman-dev/3ax-ui/v2/web/job"
	"github.com/coinman-dev/3ax-ui/v2/web/locale"
	"github.com/coinman-dev/3ax-ui/v2/web/middleware"
	"github.com/coinman-dev/3ax-ui/v2/web/network"
	"github.com/coinman-dev/3ax-ui/v2/web/service"
	"github.com/coinman-dev/3ax-ui/v2/web/websocket"

	"github.com/gin-contrib/gzip"
	"github.com/gin-contrib/sessions"
	"github.com/gin-contrib/sessions/cookie"
	"github.com/gin-gonic/gin"
	"github.com/robfig/cron/v3"
)

//go:embed assets
var assetsFS embed.FS

//go:embed html/*
var htmlFS embed.FS

//go:embed translation/*
var i18nFS embed.FS

var startTime = time.Now()

type wrapAssetsFS struct {
	embed.FS
}

func (f *wrapAssetsFS) Open(name string) (fs.File, error) {
	file, err := f.FS.Open("assets/" + name)
	if err != nil {
		return nil, err
	}
	return &wrapAssetsFile{
		File: file,
	}, nil
}

type wrapAssetsFile struct {
	fs.File
}

func (f *wrapAssetsFile) Stat() (fs.FileInfo, error) {
	info, err := f.File.Stat()
	if err != nil {
		return nil, err
	}
	return &wrapAssetsFileInfo{
		FileInfo: info,
	}, nil
}

type wrapAssetsFileInfo struct {
	fs.FileInfo
}

func (f *wrapAssetsFileInfo) ModTime() time.Time {
	return startTime
}

// EmbeddedHTML returns the embedded HTML templates filesystem for reuse by other servers.
func EmbeddedHTML() embed.FS {
	return htmlFS
}

// EmbeddedAssets returns the embedded assets filesystem for reuse by other servers.
func EmbeddedAssets() embed.FS {
	return assetsFS
}

// Server represents the main web server for the 3AX-UI panel with controllers, services, and scheduled jobs.
type Server struct {
	httpServer *http.Server
	listener   net.Listener

	index *controller.IndexController
	panel *controller.XUIController
	api   *controller.APIController
	ws    *controller.WebSocketController

	xrayService      service.XrayService
	settingService   service.SettingService
	tgbotService     service.Tgbot
	customGeoService *service.CustomGeoService

	wsHub *websocket.Hub

	cron *cron.Cron

	ctx    context.Context
	cancel context.CancelFunc
}

// NewServer creates a new web server instance with a cancellable context.
func NewServer() *Server {
	ctx, cancel := context.WithCancel(context.Background())
	return &Server{
		ctx:    ctx,
		cancel: cancel,
	}
}

// getHtmlFiles walks the local `web/html` directory and returns a list of
// template file paths. Used only in debug/development mode.
func (s *Server) getHtmlFiles() ([]string, error) {
	files := make([]string, 0)
	dir, _ := os.Getwd()
	err := fs.WalkDir(os.DirFS(dir), "web/html", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		files = append(files, path)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return files, nil
}

// getHtmlTemplate parses embedded HTML templates from the bundled `htmlFS`
// using the provided template function map and returns the resulting
// template set for production usage.
func (s *Server) getHtmlTemplate(funcMap template.FuncMap) (*template.Template, error) {
	t := template.New("").Funcs(funcMap)
	err := fs.WalkDir(htmlFS, "html", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if d.IsDir() {
			newT, err := t.ParseFS(htmlFS, path+"/*.html")
			if err != nil {
				// ignore
				return nil
			}
			t = newT
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return t, nil
}

// initRouter initializes Gin, registers middleware, templates, static
// assets, controllers and returns the configured engine.
func (s *Server) initRouter() (*gin.Engine, error) {
	if config.IsDebug() {
		gin.SetMode(gin.DebugMode)
	} else {
		gin.DefaultWriter = io.Discard
		gin.DefaultErrorWriter = io.Discard
		gin.SetMode(gin.ReleaseMode)
	}

	engine := gin.Default()

	webDomain, err := s.settingService.GetWebDomain()
	if err != nil {
		return nil, err
	}

	if webDomain != "" {
		engine.Use(middleware.DomainValidatorMiddleware(webDomain))
	}

	secret, err := s.settingService.GetSecret()
	if err != nil {
		return nil, err
	}

	basePath, err := s.settingService.GetBasePath()
	if err != nil {
		return nil, err
	}
	engine.Use(gzip.Gzip(gzip.DefaultCompression))
	assetsBasePath := basePath + "assets/"

	store := cookie.NewStore(secret)
	// Configure default session cookie options, including expiration (MaxAge)
	sessionOptions := sessions.Options{
		Path:     basePath,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		// Mirrors the TLS decision in Start(): when the panel serves HTTPS the
		// session cookie must never be sent over a plaintext request.
		Secure: s.panelServesTLS(),
	}
	if sessionMaxAge, err := s.settingService.GetSessionMaxAge(); err == nil && sessionMaxAge > 0 {
		sessionOptions.MaxAge = sessionMaxAge * 60 // minutes -> seconds
	}
	store.Options(sessionOptions)
	engine.Use(sessions.Sessions("3ax-ui", store))
	engine.Use(func(c *gin.Context) {
		c.Set("base_path", basePath)
	})
	engine.Use(func(c *gin.Context) {
		uri := c.Request.RequestURI
		if strings.HasPrefix(uri, assetsBasePath) {
			c.Header("Cache-Control", "max-age=31536000")
		}
	})

	// init i18n
	err = locale.InitLocalizer(i18nFS, &s.settingService)
	if err != nil {
		return nil, err
	}

	// Apply locale middleware for i18n
	i18nWebFunc := func(key string, params ...string) string {
		return locale.I18n(locale.Web, key, params...)
	}
	// Register template functions before loading templates
	funcMap := template.FuncMap{
		"i18n": i18nWebFunc,
	}
	engine.SetFuncMap(funcMap)
	engine.Use(locale.LocalizerMiddleware())

	// set static files and template
	if config.IsDebug() {
		// for development
		files, err := s.getHtmlFiles()
		if err != nil {
			return nil, err
		}
		// Use the registered func map with the loaded templates
		engine.LoadHTMLFiles(files...)
		engine.StaticFS(basePath+"assets", http.FS(os.DirFS("web/assets")))
	} else {
		// for production
		template, err := s.getHtmlTemplate(funcMap)
		if err != nil {
			return nil, err
		}
		engine.SetHTMLTemplate(template)
		engine.StaticFS(basePath+"assets", http.FS(&wrapAssetsFS{FS: assetsFS}))
	}

	// Apply the redirect middleware (`/xui` to `/panel`)
	engine.Use(middleware.RedirectMiddleware(basePath))

	g := engine.Group(basePath)

	s.index = controller.NewIndexController(g)
	s.panel = controller.NewXUIController(g)
	s.api = controller.NewAPIController(g, s.customGeoService)

	// Initialize WebSocket hub
	s.wsHub = websocket.NewHub()
	go s.wsHub.Run()

	// Initialize WebSocket controller
	s.ws = controller.NewWebSocketController(s.wsHub)
	// Register WebSocket route with basePath (g already has basePath prefix)
	g.GET("/ws", s.ws.HandleWebSocket)

	// Chrome DevTools endpoint for debugging web apps
	engine.GET("/.well-known/appspecific/com.chrome.devtools.json", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{})
	})

	// Add a catch-all route to handle undefined paths and return 404
	engine.NoRoute(func(c *gin.Context) {
		c.AbortWithStatus(http.StatusNotFound)
	})

	return engine, nil
}

// panelServesTLS reports whether Start() will actually wrap the listener in
// TLS. It mirrors that decision exactly — including the key-pair load — because
// marking the session cookie Secure while the panel is in fact serving plain
// HTTP (a mistyped or missing certificate path) would make login impossible:
// the browser would refuse to store the cookie.
func (s *Server) panelServesTLS() bool {
	certFile, err := s.settingService.GetCertFile()
	if err != nil {
		return false
	}
	keyFile, err := s.settingService.GetKeyFile()
	if err != nil {
		return false
	}
	if certFile == "" || keyFile == "" {
		return false
	}
	if _, err := tls.LoadX509KeyPair(certFile, keyFile); err != nil {
		logger.Warning("session cookie stays non-Secure, certificate cannot be loaded:", err)
		return false
	}
	return true
}

// addJob registers a cron job and logs a failure instead of dropping it: a bad
// schedule spec used to disable the job silently, with no trace in the log.
func (s *Server) addJob(spec string, j cron.Job) {
	if _, err := s.cron.AddJob(spec, j); err != nil {
		logger.Warningf("failed to schedule job %T at %q: %v", j, spec, err)
	}
}

// addFunc is addJob for plain functions.
func (s *Server) addFunc(spec string, fn func()) {
	if _, err := s.cron.AddFunc(spec, fn); err != nil {
		logger.Warningf("failed to schedule func at %q: %v", spec, err)
	}
}

// startTask schedules background jobs (Xray checks, traffic jobs, cron
// jobs) which the panel relies on for periodic maintenance and monitoring.
func (s *Server) startTask() {
	s.customGeoService.EnsureOnStartup()
	err := s.xrayService.RestartXray(true)
	if err != nil {
		logger.Warning("start xray failed:", err)
	}
	// Check whether xray is running every second
	s.addJob("@every 1s", job.NewCheckXrayRunningJob())

	// Check if xray needs to be restarted every 30 seconds
	s.addFunc("@every 30s", func() {
		if s.xrayService.IsNeedRestartAndSetFalse() {
			err := s.xrayService.RestartXray(false)
			if err != nil {
				logger.Error("restart xray failed:", err)
			}
		}
	})

	go func() {
		time.Sleep(time.Second * 5)
		// Statistics every 10 seconds, start the delay for 5 seconds for the first time, and staggered with the time to restart xray
		s.addJob("@every 10s", job.NewXrayTrafficJob())
		// AmneziaWG traffic stats every 10 seconds
		s.addJob("@every 10s", job.NewAwgTrafficJob())
		// WireGuard Native traffic stats every 10 seconds
		s.addJob("@every 10s", job.NewWgTrafficJob())
		// Reconcile mtproto (mtg) sidecars + scrape their traffic every 10 seconds
		s.addJob("@every 10s", job.NewMtprotoJob())
	}()

	// check client ips from log file every 10 sec
	s.addJob("@every 10s", job.NewCheckClientIpJob())

	// check client ips from log file every day
	s.addJob("@daily", job.NewClearLogsJob())

	// Inbound traffic reset jobs
	// Run every hour
	s.addJob("@hourly", job.NewPeriodicTrafficResetJob("hourly"))
	// Run once a day, midnight
	s.addJob("@daily", job.NewPeriodicTrafficResetJob("daily"))
	// Run once a week, midnight between Sat/Sun
	s.addJob("@weekly", job.NewPeriodicTrafficResetJob("weekly"))
	// Run once a month, midnight, first of month
	s.addJob("@monthly", job.NewPeriodicTrafficResetJob("monthly"))

	// LDAP sync scheduling
	if ldapEnabled, _ := s.settingService.GetLdapEnable(); ldapEnabled {
		runtime, err := s.settingService.GetLdapSyncCron()
		if err != nil || runtime == "" {
			runtime = "@every 1m"
		}
		j := job.NewLdapSyncJob()
		// job has zero-value services with method receivers that read settings on demand
		s.addJob(runtime, j)
	}

	// Make a traffic condition every day, 8:30
	var entry cron.EntryID
	isTgbotenabled, err := s.settingService.GetTgbotEnabled()
	if (err == nil) && (isTgbotenabled) {
		runtime, err := s.settingService.GetTgbotRuntime()
		if err != nil {
			logger.Warningf("Add NewStatsNotifyJob: failed to load runtime: %v; using default @daily", err)
			runtime = "@daily"
		} else if strings.TrimSpace(runtime) == "" {
			logger.Warning("Add NewStatsNotifyJob runtime is empty, using default @daily")
			runtime = "@daily"
		}
		logger.Infof("Tg notify enabled,run at %s", runtime)
		_, err = s.cron.AddJob(runtime, job.NewStatsNotifyJob())
		if err != nil {
			logger.Warningf("Add NewStatsNotifyJob: failed to schedule runtime %q: %v", runtime, err)
			return
		}

		// check for Telegram bot callback query hash storage reset
		s.addJob("@every 2m", job.NewCheckHashStorageJob())

		// Check CPU load and alarm to TgBot if threshold passes
		cpuThreshold, err := s.settingService.GetTgCpu()
		if (err == nil) && (cpuThreshold > 0) {
			s.addJob("@every 10s", job.NewCheckCpuJob())
		}
	} else {
		s.cron.Remove(entry)
	}
}

// Start initializes and starts the web server with configured settings, routes, and background jobs.
func (s *Server) Start() (err error) {
	// This is an anonymous function, no function name
	defer func() {
		if err != nil {
			s.Stop()
		}
	}()

	loc, err := s.settingService.GetTimeLocation()
	if err != nil {
		return err
	}
	s.cron = cron.New(
		cron.WithLocation(loc),
		cron.WithSeconds(),
		// Recover must come first: robfig/cron runs each job in a bare
		// goroutine with no recover of its own, so an unhandled panic in any
		// job (traffic collection, ip-limit, mtproto reconcile, ...) would
		// take down the whole panel process.
		cron.WithChain(
			cron.Recover(cron.DefaultLogger),
			cron.SkipIfStillRunning(cron.DiscardLogger),
		),
	)
	s.cron.Start()

	s.customGeoService = service.NewCustomGeoService()

	engine, err := s.initRouter()
	if err != nil {
		return err
	}

	certFile, err := s.settingService.GetCertFile()
	if err != nil {
		return err
	}
	keyFile, err := s.settingService.GetKeyFile()
	if err != nil {
		return err
	}
	listen, err := s.settingService.GetListen()
	if err != nil {
		return err
	}
	port, err := s.settingService.GetPort()
	if err != nil {
		return err
	}
	listenAddr := net.JoinHostPort(listen, strconv.Itoa(port))
	listener, err := net.Listen("tcp", listenAddr)
	if err != nil {
		return err
	}
	if certFile != "" || keyFile != "" {
		cert, err := tls.LoadX509KeyPair(certFile, keyFile)
		if err == nil {
			c := &tls.Config{
				Certificates: []tls.Certificate{cert},
			}
			listener = network.NewAutoHttpsListener(listener)
			listener = tls.NewListener(listener, c)
			logger.Info("Web server running HTTPS on", listener.Addr())
		} else {
			logger.Error("Error loading certificates:", err)
			logger.Info("Web server running HTTP on", listener.Addr())
		}
	} else {
		logger.Info("Web server running HTTP on", listener.Addr())
	}
	s.listener = listener

	s.httpServer = &http.Server{
		Handler: engine,
	}

	go func() {
		s.httpServer.Serve(listener)
	}()

	s.startTask()

	// Restore AmneziaWG interface if it was enabled before shutdown/reboot
	go func() {
		var awgService service.AwgService
		awgService.StartIfEnabled()
	}()

	// Restore WireGuard Native interface if it was enabled before shutdown/reboot
	go func() {
		var wgService service.WgService
		wgService.StartIfEnabled()
	}()

	isTgbotenabled, err := s.settingService.GetTgbotEnabled()
	if (err == nil) && (isTgbotenabled) {
		tgBot := s.tgbotService.NewTgbot()
		tgBot.Start(i18nFS)
	}

	return nil
}

// Stop gracefully shuts down the web server, stops Xray, cron jobs, and Telegram bot.
func (s *Server) Stop() error {
	s.cancel()
	s.xrayService.StopXray()
	// Stop every mtg MTProto sidecar we launched (they run outside Xray).
	mtproto.GetManager().StopAll()
	if s.cron != nil {
		s.cron.Stop()
	}
	if s.tgbotService.IsRunning() {
		s.tgbotService.Stop()
	}
	// Gracefully stop WebSocket hub
	if s.wsHub != nil {
		s.wsHub.Stop()
	}
	var err1 error
	var err2 error
	if s.httpServer != nil {
		err1 = s.httpServer.Shutdown(s.ctx)
	}
	if s.listener != nil {
		err2 = s.listener.Close()
	}
	return common.Combine(err1, err2)
}

// GetCtx returns the server's context for cancellation and deadline management.
func (s *Server) GetCtx() context.Context {
	return s.ctx
}

// GetCron returns the server's cron scheduler instance.
func (s *Server) GetCron() *cron.Cron {
	return s.cron
}

// GetWSHub returns the WebSocket hub instance.
func (s *Server) GetWSHub() any {
	return s.wsHub
}

func (s *Server) RestartXray() error {
	return s.xrayService.RestartXray(true)
}
