package main

import (
	"github.com/gin-gonic/gin"
)

// 探针开放接口：给监控脚本、机器人等外部程序用，不需要登录。
// 每个用户有自己的 API 密钥（在探针页生成，可以随时重置或停用），密钥代表这个用户：
// 能看到的内容和他登录后在探针页看到的一样；设置权重、升级节点只有管理员的密钥可以。
// 密钥只能访问 /api/v1/open/ 下的接口，不能当登录凭证用。
//
//	GET  /api/v1/open/status              自己能看到的设备组和节点的探针数据
//	GET  /api/v1/open/nodes/:id           单个节点
//	PUT  /api/v1/open/nodes/:id/weight    （管理员）设置出口权重 {"weight": 5}，0 恢复默认
//	PUT  /api/v1/open/nodes/:id/note      （管理员）设置节点备注 {"note": "..."}
//	POST /api/v1/open/nodes/:id/upgrade   （管理员）让节点升级到面板内置的版本
//
// 鉴权：请求头 X-API-Key: <密钥>，或 Authorization: Bearer <密钥>。
func openAuth(c *gin.Context) {
	key := c.GetHeader("X-API-Key")
	if key == "" {
		key = bearer(c)
	}
	var u User
	if key == "" || db.First(&u, "api_key = ?", key).Error != nil {
		// 密钥错误也算进登录频率限制，防止被拿来猜密钥
		if !loginLimiter.allow(c.ClientIP()) {
			fail(c, 429, "操作太频繁，请稍后再试")
			return
		}
		fail(c, 401, "API 密钥无效")
		return
	}
	if !u.Enabled {
		fail(c, 401, "账号不可用")
		return
	}
	c.Set("user", &u)
	c.Next()
}

func registerOpenRoutes(api *gin.RouterGroup) {
	o := api.Group("/open", openAuth, limitByUser)
	o.GET("/status", handleStatus)
	o.GET("/nodes/:id", handleOpenNode)
	o.PUT("/nodes/:id/weight", adminRequired, handleAdminNodeWeight)
	o.PUT("/nodes/:id/note", adminRequired, handleAdminNodeNote)
	o.POST("/nodes/:id/upgrade", adminRequired, handleAdminUpgradeNode)
}

// statusVisible 判断用户能不能在探针里看到这个设备组，规则和探针页一致。
func statusVisible(g *DeviceGroup, u *User) bool {
	if g.HideInStatus == 2 || (g.HideInStatus == 1 && !u.Admin) || g.Type == GroupChain {
		return false
	}
	return u.Admin || groupAllowsUser(g, u)
}

func handleOpenNode(c *gin.Context) {
	u := me(c)
	var n Node
	if db.First(&n, "id = ?", c.Param("id")).Error != nil {
		fail(c, 404, "节点不存在")
		return
	}
	g := loadGroup(n.GroupID)
	if g == nil || !statusVisible(g, u) {
		fail(c, 404, "节点不存在") // 看不到的节点和不存在的节点不作区分
		return
	}
	c.JSON(200, gin.H{"node": statusRow(&n, u.Admin), "group_id": g.ID, "group_name": g.Name, "group_type": g.Type})
}

// ---------- 自己的密钥 ----------

func handleGetProbeAPI(c *gin.Context) {
	c.JSON(200, gin.H{"key": me(c).APIKey})
}

func setProbeAPIKey(c *gin.Context, key string) {
	if dbFail(c, db.Model(me(c)).Update("api_key", key).Error) {
		return
	}
	c.JSON(200, gin.H{"key": key})
}

// handleResetProbeAPI 生成一个新密钥，旧密钥立即失效。
func handleResetProbeAPI(c *gin.Context) { setProbeAPIKey(c, randHex(20)) }

func handleDisableProbeAPI(c *gin.Context) { setProbeAPIKey(c, "") }
