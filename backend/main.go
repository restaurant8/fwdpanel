package main

import (
	"crypto/rand"
	"encoding/hex"
	"flag"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/acme/autocert"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

var (
	db            *gorm.DB
	allowRegister bool
)

func randHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

func hashPassword(p string) string {
	h, err := bcrypt.GenerateFromPassword([]byte(p), bcrypt.DefaultCost)
	if err != nil {
		panic(err)
	}
	return string(h)
}

func main() {
	listen := flag.String("listen", ":8080", "面板监听地址")
	dbPath := flag.String("db", "panel.db", "数据库：SQLite 文件路径，或 mysql://、postgres:// 连接串")
	web := flag.String("web", "", "前端目录（留空则使用内嵌的前端页面）")
	dist := flag.String("dist", "", "节点端安装包目录（留空则使用内嵌的安装包）")
	domain := flag.String("domain", "", "面板域名。填写后自动申请 HTTPS 证书并监听 443/80，同时表示同意 Let's Encrypt 的服务条款")
	flag.BoolVar(&allowRegister, "allow-register", true, "是否允许用户自行注册")
	offline := flag.Int("offline-node-time", 35, "节点多少秒没有同步判定为离线（最低 20）")
	retention := flag.Int("offline-node-retention", 86400, "离线节点的记录保留多少秒后自动删除（最低 600，0 表示不删除）")
	loginRate := flag.String("login-rate", "5/5", "登录/注册的频率限制，格式 次数/秒数，按来源 IP 计；留空不限制")
	userRate := flag.String("user-rate", "", "登录后接口的频率限制，格式 次数/秒数，按用户计；默认不限制")
	resetPw := flag.String("reset-password", "", "重置指定用户的密码并退出")
	clean := flag.Int("clean", 0, "清理数据并退出：1 失效规则，2 已过期且没有规则的普通用户")
	copyTo := flag.String("copy-database", "", "把全部数据复制到另一个数据库（连接串）并退出，目标库同名表会被覆盖")
	flag.Parse()

	if *offline < 20 {
		*offline = 20
	}
	nodeOnlineWindow = time.Duration(*offline) * time.Second
	loginLimiter, userLimiter = parseRate(*loginRate), parseRate(*userRate)

	var err error
	if db, err = openDB(*dbPath); err != nil {
		log.Fatalf("打开数据库失败: %v", err)
	}
	if err := db.AutoMigrate(allModels...); err != nil {
		log.Fatalf("迁移数据库失败: %v", err)
	}
	switch {
	case *resetPw != "":
		cliResetPassword(*resetPw)
		return
	case *clean != 0:
		cliClean(*clean)
		return
	case *copyTo != "":
		cliCopyDatabase(*copyTo)
		return
	}
	dataDir := "."
	if isSQLite(*dbPath) {
		dataDir = filepath.Dir(sqlitePath(*dbPath))
	}
	ensureAdmin(dataDir)
	go watchLoop()
	go func() {
		for range time.Tick(time.Hour) {
			db.Where("expire_at < ?", time.Now()).Delete(&Session{})
			autoRenew()
			if *retention > 0 {
				keep := max(*retention, 600)
				db.Where("last_seen < ?", time.Now().Add(-time.Duration(keep)*time.Second)).Delete(&Node{})
			}
		}
	}()

	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(gin.Recovery())
	// 不信任 X-Forwarded-For，节点 IP 以实际连接地址为准。
	// 如果面板部署在反向代理之后，请在此填入代理的地址。
	r.SetTrustedProxies(nil)
	registerRoutes(r.Group("/api/v1"))
	r.GET("/download/:file", handleDownload)
	distFS = pickFS(*dist, embeddedDist, "nodedist")
	serveWeb(r, pickFS(*web, embeddedWeb, "webdist"))

	if *domain != "" {
		// 自动 HTTPS：证书由 Let's Encrypt 签发并自动续期，缓存在数据库旁边的 certs 目录
		m := &autocert.Manager{
			Prompt:     autocert.AcceptTOS,
			HostPolicy: autocert.HostWhitelist(*domain),
			Cache:      autocert.DirCache(filepath.Join(dataDir, "certs")),
		}
		go func() { log.Println(http.ListenAndServe(":80", m.HTTPHandler(nil))) }()
		log.Printf("面板已启动: https://%s", *domain)
		srv := &http.Server{Addr: ":443", Handler: r, TLSConfig: m.TLSConfig()}
		log.Fatal(srv.ListenAndServeTLS("", ""))
	}
	log.Printf("面板已启动: %s", *listen)
	if err := r.Run(*listen); err != nil {
		log.Fatal(err)
	}
}

// ensureAdmin 在没有任何用户时创建初始管理员。
// 密码取自环境变量 PANEL_ADMIN_PASSWORD，未设置则随机生成并打印一次。
func ensureAdmin(dataDir string) {
	var n int64
	db.Model(&User{}).Count(&n)
	if n > 0 {
		return
	}
	pw := os.Getenv("PANEL_ADMIN_PASSWORD")
	generated := pw == ""
	if generated {
		pw = randHex(8)
	}
	db.Create(&User{Username: "admin", PasswordHash: hashPassword(pw), Admin: true, Enabled: true})
	if !generated {
		log.Printf("已创建初始管理员 admin（密码来自 PANEL_ADMIN_PASSWORD）")
		return
	}
	// 随机密码写到数据目录里的文件（仅所有者可读），安装脚本会把它显示给管理员
	f := filepath.Join(dataDir, "admin-password.txt")
	if err := os.WriteFile(f, []byte(pw+"\n"), 0o600); err != nil {
		log.Printf("已创建初始管理员 admin，密码: %s （请登录后立即修改）", pw)
		return
	}
	log.Printf("已创建初始管理员 admin，初始密码保存在 %s （请登录后立即修改并删除该文件）", f)
}

func registerRoutes(api *gin.RouterGroup) {
	api.GET("/site", handleSite)
	api.POST("/auth/login", limitByIP, handleLogin)
	api.POST("/auth/register", limitByIP, handleRegister)
	api.Any("/guest/pay/notify/:no", handlePayNotify)
	api.POST("/guest/telegram/webhook", handleTelegramWebhook)
	api.POST("/node/sync", handleNodeSync)
	api.GET("/node/ws", handleNodeWS)
	// 浏览器的 WebSocket 不能带 Authorization 头，终端接口在连接后的第一条消息里鉴权
	api.GET("/admin/terminal/:id", handleAdminTerminal)
	registerOpenRoutes(api)

	u := api.Group("", authRequired, limitByUser)
	u.POST("/auth/logout", handleLogout)
	u.GET("/user/me", handleMe)
	u.POST("/user/password", handleChangePassword)
	u.GET("/user/stats", handleUserStats)
	u.GET("/groups", handleListGroupsPublic)
	u.GET("/user/egress", handleListMyEgress)
	u.POST("/user/egress", handleCreateMyEgress)
	u.DELETE("/user/egress/:id", handleDeleteMyEgress)
	u.GET("/shop", handleShop)
	u.POST("/shop/buy", handleBuyPlan)
	u.POST("/shop/renew", handleRenew)
	u.POST("/shop/redeem", handleRedeem)
	u.POST("/shop/recharge", handleRecharge)
	u.GET("/orders", handleMyOrders)
	u.GET("/user/invite", handleMyInvite)
	u.POST("/user/telegram/bind", handleTelegramBind)
	u.POST("/user/telegram/unbind", handleTelegramUnbind)
	u.POST("/user/push", handleSetPush)
	u.GET("/status", handleStatus)
	u.GET("/user/probe_api", handleGetProbeAPI)
	u.POST("/user/probe_api/reset", handleResetProbeAPI)
	u.DELETE("/user/probe_api", handleDisableProbeAPI)
	u.POST("/user/auto_renew", handleAutoRenew)
	u.GET("/rules", handleListRules)
	u.POST("/rules", handleCreateRule)
	u.POST("/rules/batch", handleBatchRules)
	u.POST("/rules/batch_action", handleBatchAction)
	u.POST("/rules/:id/diagnose", handleDiagnose)
	u.GET("/rulegroups", handleListRuleGroups)
	u.POST("/rulegroups", handleSaveRuleGroup)
	u.PUT("/rulegroups/:id", handleSaveRuleGroup)
	u.DELETE("/rulegroups/:id", handleDeleteRuleGroup)
	u.PUT("/rules/:id", handleUpdateRule)
	u.DELETE("/rules/:id", handleDeleteRule)

	a := u.Group("/admin", adminRequired)
	a.GET("/stats", handleAdminStats)
	a.GET("/users", handleAdminListUsers)
	a.POST("/users", handleAdminCreateUser)
	a.PUT("/users/:id", handleAdminUpdateUser)
	a.DELETE("/users/:id", handleAdminDeleteUser)
	a.POST("/users/:id/reset", handleAdminResetTraffic)
	a.POST("/users/:id/password", handleAdminResetPassword)
	a.PUT("/groups/:id/config", handleAdminGroupConfig)
	a.POST("/groups/reset_traffic", handleAdminResetGroupTraffic)
	a.POST("/clean", handleAdminClean)
	a.GET("/orders", handleAdminOrders)
	a.POST("/orders/manual", handleAdminManualOrder)
	a.POST("/orders/delete", handleAdminDeleteOrders)
	a.POST("/users/:id/plan", handleAdminSetUserPlan)
	a.GET("/users/:id/invite", handleAdminUserInvite)
	a.PUT("/push", handleAdminSavePush)
	a.POST("/groups/set_cat", handleAdminSetGroupCat)
	a.GET("/kv/:key", handleAdminGetKV)
	a.PUT("/kv/:key", handleAdminPutKV)
	a.GET("/usergroups", handleAdminListUserGroups)
	a.POST("/usergroups", handleAdminSaveUserGroup)
	a.PUT("/usergroups/:id", handleAdminSaveUserGroup)
	a.DELETE("/usergroups/:id", handleAdminDeleteUserGroup)
	a.GET("/groups", handleAdminListGroups)
	a.POST("/groups", handleAdminCreateGroup)
	a.PUT("/groups/:id", handleAdminUpdateGroup)
	a.DELETE("/groups/:id", handleAdminDeleteGroup)
	a.POST("/groups/:id/token", handleAdminResetGroupToken)
	a.GET("/nodes", handleAdminListNodes)
	a.DELETE("/nodes/:id", handleAdminDeleteNode)
	a.POST("/nodes/:id/upgrade", handleAdminUpgradeNode)
	a.PUT("/nodes/:id/weight", handleAdminNodeWeight)
	a.PUT("/nodes/:id/note", handleAdminNodeNote)
}
