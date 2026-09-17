package main

//this implements https://jsonapi.org/format/ as best as possible

import (
	"github.com/joho/godotenv"
	"goblog/admin"
	"goblog/auth"
	"goblog/blog"
	"goblog/mail"
	gplugin "goblog/plugin"
	"goblog/plugin/installer"
	"goblog/plugins/analytics"
	"goblog/plugins/directory"
	scholarplugin "goblog/plugins/scholar"
	"goblog/plugins/socialicons"
	"goblog/tools"
	"goblog/wizard"
	"gorm.io/gorm"
	"html/template"
	"log"
	"net/http"
	"os"
	"sync"
	"path/filepath"
	"strings"

	"github.com/gin-contrib/sessions"
	"github.com/gin-contrib/sessions/cookie"
	"github.com/gin-contrib/static"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// Version of the code generated from git describe
var Version = "development"

type goblog struct {
	_wizard            *wizard.Wizard
	_blog              *blog.Blog
	_auth              *auth.Auth
	_admin             *admin.Admin
	_registry          *gplugin.Registry
	sessionKey         string
	router             *gin.Engine
	routesOnce         sync.Once
}

func envFilePresent() bool {
	_, err := os.Stat(".env")
	if err != nil {
		return false
	}
	return true
}

func isAuthConfigured() bool {
	envFile, err := godotenv.Read(".env")
	if err != nil {
		log.Println("Couldn't read the .env file: " + err.Error())
		return false
	}
	if envFile["client_id"] == "" || envFile["client_secret"] == "" {
		return false
	}
	return true
}

func attemptConnectDb() *gorm.DB {
	envFile, err := godotenv.Read(".env")
	if err != nil {
		log.Println("Couldn't read the .env file: " + err.Error())
		return nil
	}
	cfg := dbConfigFromEnv(envFile)
	if !validDatabaseType(cfg.Type) {
		log.Println("Database type: " + cfg.Type + " is not valid. Expecting `sqlite`, `mysql` or `postgres`")
		return nil
	}
	db, err := openDatabase(cfg)
	if err != nil {
		log.Println("Error opening " + cfg.Type + " db: " + err.Error())
		return nil
	}
	log.Println("connected to " + cfg.Type + " db")
	return db
}

// depending on if the env file is present or not, we will show the wizard or the main site
func (g *goblog) rootHandler(c *gin.Context) {
	if !envFilePresent() {
		log.Println("Root handler: No .env file found")
		c.HTML(http.StatusOK, "wizard_db.html", gin.H{
			"version": Version,
			"title":   "GoBlog Install Wizard",
		})
		return
	} else {
		log.Println("Root handler:  Found .env file")
		envFile, err := godotenv.Read(".env")
		if err != nil {
			c.HTML(http.StatusOK, "wizard_db.html", gin.H{
				"version": Version,
				"title":   "GoBlog Install Wizard",
				"errors":  "Couldn't read the .env file: " + err.Error(),
			})
			return
		}
		// detect if the database hasn't be configured yet
		if !validDatabaseType(envFile["database"]) {
			log.Println("Root handler:  Database is not configured, redirecting to db wizard")
			c.HTML(http.StatusOK, "wizard_db.html", gin.H{
				"version": Version,
				"title":   "GoBlog Install Wizard",
			})
			return
		}
		log.Println("Root handler: Database is configured")
		if g._wizard.IsDbNil() {
			log.Println("Root handler: Database is nil - need to connect")
			db := attemptConnectDb()
			if db == nil {
				log.Println("Root handler: Couldn't connect to the database, showing db wizard")
				// show the wizard and get them to re-enter the db info with an error message
				c.HTML(http.StatusOK, "wizard_db.html", gin.H{
					"version": Version,
					"title":   "GoBlog Install Wizard",
					"errors":  "Couldn't connect to the database",
				})
				return
			}
			err := tools.Migrate(db)
			if err != nil {
				log.Println("Root handler: Couldn't migrate the database: " + err.Error())
				// show the wizard with an error message saying the db isn't compatible and let them file a gh ticket
				c.HTML(http.StatusOK, "wizard_db.html", gin.H{
					"version": Version,
					"title":   "GoBlog Install Wizard",
					"errors":  "Failed to Migrate the database: " + err.Error(),
				})
				return
			}
			log.Println("Root handler: Migrated the database, updating the db in the blog, auth, admin, and wizard")
			g._blog.UpdateDb(db)
			g._auth.UpdateDb(db)
			g._admin.UpdateDb(db)
			g._wizard.UpdateDb(db)
			g._registry.UpdateDb(db)
			if err := g._registry.Init(); err != nil {
				log.Printf("Plugin init errors: %v", err)
			}
			g._registry.StartScheduledJobs()

			if !isAuthConfigured() {
				g._wizard.Landing(c)
				return
			}
			g.addRoutes()
			g._blog.Home(c)
		} else {
			if !isAuthConfigured() {
				g._wizard.Landing(c)
				return
			}
			g._blog.Home(c)
			return
		}
	}
}

func (g *goblog) loginHandler(c *gin.Context) {
	if !envFilePresent() {
		log.Println("Root handler: No .env file found")
		c.HTML(http.StatusOK, "wizard_db.html", gin.H{
			"version": Version,
			"title":   "GoBlog Install Wizard",
		})
		return
	}
	if g._wizard.IsDbNil() {
		log.Println("Wizard db is nil in loginHandler")
		c.HTML(http.StatusOK, "wizard_db.html", gin.H{
			"version": Version,
			"title":   "GoBlog Install Wizard",
			"errors":  "Database is not configured",
		})
		return
	}
	if !isAuthConfigured() {
		err := g._wizard.LoginCode(c)
		if err != nil {
			c.HTML(http.StatusOK, "wizard_auth.html", gin.H{
				"version": Version,
				"title":   "GoBlog Install Wizard",
				"errors":  "Couldn't get the login code: " + err.Error(),
			})
			return
		} else {
			g.addRoutes()
			g._blog.Home(c)
		}
	} else {
		g._blog.Login(c)
	}
}

func main() {
	// Subcommands run and exit before any .env or database work.
	if len(os.Args) > 1 && os.Args[1] == "validate-plugin" {
		os.Exit(runValidatePlugin(os.Args[2:], os.Stdout, os.Stderr))
	}
	log.Println("Starting blog version: ", Version)
	var sessionKey string
	var db *gorm.DB = nil
	if !envFilePresent() {
		log.Println("No .env file found, creating one with a new session key")
		sessionKey = uuid.New().String()
		f, err := os.Create(".env")
		if err != nil {
			log.Println("Couldn't create the .env file: " + err.Error())
			return
		}
		_, err = f.WriteString("SESSION_KEY=" + sessionKey + "\n")
		err = f.Close()
		if err != nil {
			log.Println("Couldn't close the .env file: " + err.Error())
			return
		}
	} else {
		log.Println("Found .env file")
		envFile, err := godotenv.Read(".env")
		if err != nil {
			log.Println("Couldn't read the .env file: " + err.Error())
			return
		}
		sessionKey = envFile["SESSION_KEY"]
		if (sessionKey == "") || (len(sessionKey) != 36) {
			log.Println("No session key found or it's invalid, creating a new one")
			sessionKey = uuid.New().String()
			f, err := os.OpenFile(".env", os.O_APPEND|os.O_WRONLY, 0644)
			if err != nil {
				log.Println("Couldn't open the .env file: " + err.Error())
				return
			}
			_, err = f.WriteString("SESSION_KEY=" + sessionKey + "\n")
			err = f.Close()
			if err != nil {
				log.Println("Couldn't close the .env file: " + err.Error())
				return
			}
			log.Println("New Session key: ", sessionKey)
		} else {
			log.Println("Found Session key: ", sessionKey)
		}

		// database is configured, lets try to connect now
		if validDatabaseType(envFile["database"]) {
			log.Println("Database is configured, trying to connect")
			db = attemptConnectDb()
			if db == nil {
				log.Println("Couldn't connect to the database")
				return
			}
			err := tools.Migrate(db)
			if err != nil {
				log.Println("Couldn't migrate the database: " + err.Error())
				return
			}
		} else {
			log.Println("Database is not configured, should get routed to wizard")
		}
	}

	// The wizard and admin pin read settings lazily via os.Getenv after a
	// godotenv.Load; SMTP is needed at startup, so load once here too.
	if err := godotenv.Load(".env"); err != nil {
		log.Println("Couldn't load .env into the environment: " + err.Error())
	}

	_auth := auth.New(db, Version)
	if sender, ok := mail.NewSMTPSenderFromEnv(); ok {
		log.Println("SMTP configured; email login enabled")
		_auth.Mailer = sender
	}
	_blog := blog.New(db, &_auth, Version)
	_admin := admin.New(db, &_auth, &_blog, Version)
	_wizard := wizard.New(db, Version)

	// setup the minimal router at the start to support both the wizard and the main server once the wizard is done
	router := gin.Default()
	// Trust only loopback proxies by default so c.ClientIP() works correctly
	// behind a reverse proxy. Override with TRUSTED_PROXIES env var (comma-separated).
	trustedProxies := []string{"127.0.0.1", "::1"}
	if envProxies := os.Getenv("TRUSTED_PROXIES"); envProxies != "" {
		trustedProxies = strings.Split(envProxies, ",")
		for i := range trustedProxies {
			trustedProxies[i] = strings.TrimSpace(trustedProxies[i])
		}
	}
	router.SetTrustedProxies(trustedProxies)

	// Initialize plugin system
	registry := gplugin.NewRegistry(db)
	registry.Register(analytics.New())
	registry.Register(socialicons.New())
	registry.Register(scholarplugin.New())
	dir := directory.New()
	dir.SetUserAgent("goblog-directory/" + Version)
	registry.Register(dir)
	dynamicEnabled := os.Getenv("ENABLE_DYNAMIC_PLUGINS") == "true"
	if dynamicEnabled {
		gplugin.LoadDynamicPlugins(registry, "plugins/dynamic")
	}
	pluginInstaller := &installer.Installer{
		Dir:       "plugins/dynamic",
		Registry:  registry,
		Directory: directory.NewFetcher(nil),
		Version:   Version,
		Enabled:   dynamicEnabled,
		IndexURL:  func() string { return _blog.SettingValue("plugin_directory_url", installer.DefaultIndexURL) },
	}
	pluginInstaller.Directory.SetUserAgent("goblog-installer/" + Version)
	_admin.Installer = pluginInstaller
	if db != nil {
		if err := registry.Init(); err != nil {
			log.Printf("Plugin init errors: %v", err)
		}
		registry.StartScheduledJobs()
	}

	goblog := &goblog{
		_wizard:    &_wizard,
		_blog:      &_blog,
		_auth:      &_auth,
		_admin:     &_admin,
		_registry:  registry,
		sessionKey: sessionKey,
		router:     router,
	}

	// Filter nav pages: hide pages owned by disabled plugins
	_blog.PageFilter = func(page blog.Page) bool {
		if !registry.HasPageType(page.PageType) {
			return true // not a plugin page, always show
		}
		return registry.IsPageTypeEnabled(page.PageType)
	}

	router.Use(CORS())
	router.Use(gplugin.Middleware(registry))
	store := cookie.NewStore([]byte(sessionKey))
	hostname, err := os.Hostname()
	router.Use(sessions.Sessions(hostname, store))
	log.Println("Session key: ", sessionKey)
	log.Println("Hostname: ", hostname)
	// Load templates from the active theme directory, falling back to "default"
	activeTheme := "default"
	if !_blog.IsDbNil() {
		settings := _blog.GetSettings()
		if t, ok := settings["theme"]; ok && t.Value != "" {
			activeTheme = t.Value
		}
	}

	funcMap := template.FuncMap{
		"rawHTML": func(s string) template.HTML { return template.HTML(s) },
	}
	router.SetFuncMap(funcMap)

	// isValidTheme checks that a theme name is safe and exists on disk.
	isValidTheme := func(theme string) bool {
		// Only allow alphanumeric, hyphens, and underscores
		for _, c := range theme {
			if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '_') {
				return false
			}
		}
		info, err := os.Stat(filepath.Join("themes", theme, "templates"))
		return err == nil && info.IsDir()
	}

	loadTheme := func(theme string) {
		if !isValidTheme(theme) {
			log.Printf("Warning: theme %q is invalid or missing, falling back to default", theme)
			theme = "default"
		}
		themePath := filepath.Join("themes", theme) + "/"
		log.Println("Loading theme: " + theme)
		// Load shared templates first, then theme templates
		tmpl, err := template.New("").Funcs(funcMap).ParseGlob("templates/shared/*.html")
		if err != nil {
			log.Fatalf("Failed to load shared templates: %v", err)
		}
		tmpl, err = tmpl.ParseGlob(themePath + "templates/*.html")
		if err != nil {
			log.Printf("Warning: failed to load theme %q: %v — falling back to default", theme, err)
			theme = "default"
			themePath = "themes/default/"
			tmpl = template.Must(template.Must(template.New("").Funcs(funcMap).ParseGlob("templates/shared/*.html")).ParseGlob(themePath + "templates/*.html"))
		}
		router.SetHTMLTemplate(tmpl)
		activeTheme = theme
	}
	loadTheme(activeTheme)

	// Wire up hot-reload callback so theme changes take effect without restart
	_admin.OnThemeChange = func(theme string) {
		loadTheme(theme)
	}

	// Serve theme static files dynamically based on active theme
	router.GET("/theme/*filepath", func(c *gin.Context) {
		fp := filepath.Clean(c.Param("filepath"))
		c.File(filepath.Join("themes", activeTheme, "static", fp))
	})

	getAndHead(router, "/", goblog.rootHandler)
	getAndHead(router, "/login", goblog.loginHandler)
	router.GET("/wizard", goblog._wizard.SaveToken)
	router.POST("/wizard_db", updateDB)
	router.POST("/test_db", testDB)
	router.POST("/api/v1/upload", goblog._admin.UploadFile)
	router.PATCH("/api/v1/settings", goblog._admin.UpdateSettings)
	router.PATCH("/api/v1/plugin-settings", goblog._admin.UpdatePluginSettings)
	router.GET("/api/v1/plugins/status", goblog._admin.PluginStatus)
	router.GET("/api/v1/plugins/directory", goblog._admin.PluginDirectory)
	router.POST("/api/v1/plugins/install", goblog._admin.InstallPlugin)
	router.POST("/api/v1/plugins/update", goblog._admin.UpdatePlugin)
	router.DELETE("/api/v1/plugins/:name", goblog._admin.UninstallPlugin)
	router.POST("/api/v1/plugins/refresh", goblog._admin.RefreshPluginDirectory)
	//if we use true here - it will override the home route and just show files
	router.Use(static.Serve("/", static.LocalFile("www", false)))
	if err != nil {
		log.Println("Couldn't get the hostname")
		return
	}

	if db != nil {
		goblog.addRoutes()
	}

	err = router.Run(":7000")
	if err != nil {
		log.Println("Error running goblog server: " + err.Error())
	}
}

func (g *goblog) addRoutes() {
	g.routesOnce.Do(func() {
		g.addRoutesInner()
	})
}

func (g *goblog) addRoutesInner() {
	log.Println("Adding main blog routes")
	//all of this is the json api
	g.router.MaxMultipartMemory = 50 << 20
	g.router.POST("/api/login", g._auth.LoginPostHandler)
	g.router.POST("/api/login/email", g._auth.SendLoginCodeHandler)
	g.router.POST("/api/login/email/verify", g._auth.VerifyLoginCodeHandler)
	g.router.POST("/api/v1/posts", g._admin.CreatePost)
	g.router.PATCH("/api/v1/posts", g._admin.UpdatePost)
	g.router.PATCH("/api/v1/publish/:id", g._admin.PublishPost)
	g.router.PATCH("/api/v1/draft/:id", g._admin.DraftPost)
	g.router.DELETE("/api/v1/posts", g._admin.DeletePost)
	g.router.GET("/api/v1/revisions/:id", g._admin.ListRevisions)
	g.router.POST("/api/v1/revisions/:id/rollback/:revisionId", g._admin.RollbackRevision)
	g.router.POST("/comments", g._blog.SubmitComment)
	g.router.DELETE("/api/v1/comments", g._admin.DeleteComment)
	g.router.POST("/api/v1/admins", g._admin.PromoteAdmin)
	g.router.DELETE("/api/v1/admins", g._admin.DemoteAdmin)
	g.router.GET("/api/v1/posts/:yyyy/:mm/:dd/:slug", g._blog.GetPost)
	g.router.GET("/api/v1/posts", g._blog.ListPosts)
	g.router.GET("/api/v1/setting/:slug", g._admin.GetSetting)
	g.router.GET("/api/v1/settings", g._admin.GetSettings)
	g.router.POST("/api/v1/setting", g._admin.AddSetting)

	// Page CRUD API
	g.router.GET("/api/v1/pages", g._admin.ListPages)
	g.router.POST("/api/v1/pages", g._admin.CreatePage)
	g.router.PATCH("/api/v1/pages", g._admin.UpdatePage)
	g.router.DELETE("/api/v1/pages", g._admin.DeletePage)

	// Post Type CRUD API
	g.router.GET("/api/v1/post-types", g._admin.ListPostTypes)
	g.router.POST("/api/v1/post-types", g._admin.CreatePostType)
	g.router.PATCH("/api/v1/post-types", g._admin.UpdatePostType)
	g.router.DELETE("/api/v1/post-types", g._admin.DeletePostType)

	//all of this serves html full pages, but re-uses much of the logic of
	//the json API. The json API is tested more easily. Also javascript can
	//served in the html can be used to create and update posts by directly
	//working with the json API.
	getAndHead(g.router, "/index.php", g._blog.Home)
	getAndHead(g.router, "/posts/:yyyy/:mm/:dd/:slug", g._blog.Post)
	// lets posts work with our without the word posts in front
	getAndHead(g.router, "/:yyyy/:mm/:dd/:slug", g._blog.Post)
	g.router.GET("/admin/posts/:yyyy/:mm/:dd/:slug", g._admin.Post)
	getAndHead(g.router, "/tag/*name", g._blog.Tag)
	g.router.GET("/logout", g._blog.Logout)

	getAndHead(g.router, "/search", g._blog.Search)
	getAndHead(g.router, "/sitemap.xml", g._blog.Sitemap)
	// lets old WordPress stuff stored at wp-content/uploads work
	g.router.Use(static.Serve("/wp-content", static.LocalFile("www", false)))

	g.router.GET("/admin", g._admin.Admin)
	g.router.GET("/admin/dashboard", g._admin.AdminDashboard)
	g.router.GET("/admin/posts", g._admin.AdminPosts)
	g.router.GET("/admin/newpost", g._admin.AdminNewPost)
	g.router.GET("/admin/settings", g._admin.AdminSettings)
	g.router.GET("/admin/pages", g._admin.AdminPages)
	g.router.GET("/admin/comments", g._admin.AdminComments)
	g.router.GET("/admin/users", g._admin.AdminUsers)
	g.router.GET("/admin/plugins", g._admin.AdminPlugins)
	g.router.GET("/admin/pages/:id", g._admin.AdminEditPage)
	g.router.GET("/admin/post-types", g._admin.AdminPostTypes)
	g.router.GET("/admin/post-types/:id", g._admin.AdminEditPostType)

	g.router.NoRoute(g._blog.NoRoute)
}

// getAndHead registers a handler for both GET and HEAD on the given path.
// HEAD is required by the HTTP spec for any resource that supports GET.
func getAndHead(router gin.IRoutes, path string, handler gin.HandlerFunc) {
	router.GET(path, handler)
	router.HEAD(path, handler)
}

func CORS() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Writer.Header().Set("Access-Control-Allow-Origin", "*")
		c.Writer.Header().Set("Access-Control-Allow-Credentials", "true")
		c.Writer.Header().Set("Access-Control-Allow-Headers", "*")
		c.Writer.Header().Set("Access-Control-Allow-Methods", "GET, HEAD, POST, PUT, DELETE, OPTIONS, PATCH")
		c.Next()
	}
}

// parse the form which should have passed the db info
// and then create the env file with it
func updateDB(c *gin.Context) {
	fail := func(msg string) {
		c.HTML(http.StatusOK, "wizard_db.html", gin.H{
			"version": Version,
			"title":   "GoBlog Install Wizard",
			"errors":  msg,
		})
	}
	if err := c.Request.ParseForm(); err != nil {
		log.Println("Couldn't parse the form: " + err.Error())
		return
	}
	cfg := dbConfigFromForm(c)
	if !validDatabaseType(cfg.Type) {
		fail("Invalid database type")
		return
	}
	if err := os.WriteFile(".env", []byte(cfg.envFile()), 0600); err != nil {
		fail("Couldn't write the .env file: " + err.Error())
		return
	}

	// if we make it this far, success, redirect to /
	c.Redirect(http.StatusSeeOther, "/")
}

func testDB(c *gin.Context) {
	if err := c.Request.ParseForm(); err != nil {
		log.Println("Couldn't parse the form: " + err.Error())
		return
	}
	// Don't log the form: it carries the database password.
	cfg := dbConfigFromForm(c)
	if !validDatabaseType(cfg.Type) {
		log.Println("Invalid database type: " + cfg.Type)
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid database type"})
		return
	}
	if _, err := openDatabase(cfg); err != nil {
		log.Println("Couldn't connect to the database: " + err.Error())
		c.JSON(http.StatusBadRequest, gin.H{"error": "Couldn't connect to the database: " + err.Error()})
		return
	}
	log.Println("Connected to the database")
	c.JSON(http.StatusOK, gin.H{"success": "Connected to the database"})
}
