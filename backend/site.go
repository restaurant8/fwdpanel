package main

import (
	"encoding/json"
	"io"
	"time"

	"github.com/gin-gonic/gin"
)

const panelVersion = "0.6.0"

// nodeVersion 是内嵌的节点端安装包的版本，节点上报的版本与它不同时界面会提示升级。
const nodeVersion = "0.6.0"

// ---------- KV：站点设置、支付通道、套餐、兑换码、推送设置 ----------
//
// 这些设置目前只做保存和展示：支付通道、套餐、兑换码、邀请、Telegram 推送都没有接入实际业务。

var kvKeys = map[string]bool{"site": true, "pay": true, "plans": true, "redeem": true, "invite": true, "push": true}

func getKV(key string, into any) bool {
	var kv KV
	if db.Where(&KV{Key: key}).First(&kv).Error != nil {
		return false
	}
	return json.Unmarshal([]byte(kv.Value), into) == nil
}

func handleAdminGetKV(c *gin.Context) {
	if !kvKeys[c.Param("key")] {
		fail(c, 404, "未知的设置项")
		return
	}
	var kv KV
	if db.Where(&KV{Key: c.Param("key")}).First(&kv).Error != nil {
		c.Data(200, "application/json", []byte("null"))
		return
	}
	c.Data(200, "application/json", []byte(kv.Value))
}

func handleAdminPutKV(c *gin.Context) {
	key := c.Param("key")
	if !kvKeys[key] {
		fail(c, 404, "未知的设置项")
		return
	}
	body, err := io.ReadAll(io.LimitReader(c.Request.Body, 1<<20))
	if err != nil || !json.Valid(body) {
		fail(c, 400, "JSON 格式错误")
		return
	}
	db.Save(&KV{Key: key, Value: string(body)})
	c.JSON(200, gin.H{"ok": true})
}

// siteSettings 是「站点设置 - 基本」和公告。只有 title、allow_register、notice 实际生效。
type siteSettings struct {
	Title          string `json:"title"`
	AllowRegister  *bool  `json:"allow_register"`
	Notice         string `json:"notice"`
	DiagnoseHideIP int    `json:"diagnose_hide_ip"`
}

func loadSite() siteSettings {
	var s siteSettings
	getKV("site", &s)
	if s.Title == "" {
		s.Title = "转发面板"
	}
	return s
}

// registerAllowed：启动参数 -allow-register=false 是总开关，其次看站点设置。
func registerAllowed() bool {
	if !allowRegister {
		return false
	}
	s := loadSite()
	return s.AllowRegister == nil || *s.AllowRegister
}

func handleSite(c *gin.Context) {
	s := loadSite()
	c.JSON(200, gin.H{"title": s.Title, "allow_register": registerAllowed(), "notice": s.Notice,
		"version": panelVersion, "node_version": nodeVersion, "diagnose_hide_ip": s.DiagnoseHideIP})
}

// handleShop 返回商城页需要的数据：未隐藏的套餐、已启用的支付通道名称、最小充值金额。
func handleShop(c *gin.Context) {
	var plans []map[string]any
	getKV("plans", &plans)
	shown := []map[string]any{}
	for _, p := range plans {
		if hidden, _ := p["hidden"].(bool); !hidden {
			shown = append(shown, p)
		}
	}
	var pay struct {
		MinRecharge float64 `json:"min_recharge"`
		Channels    []struct {
			Name    string `json:"name"`
			Enabled bool   `json:"enabled"`
		} `json:"channels"`
	}
	pay.MinRecharge = 10
	getKV("pay", &pay)
	names := []string{}
	for _, ch := range pay.Channels {
		if ch.Enabled {
			names = append(names, ch.Name)
		}
	}
	c.JSON(200, gin.H{"plans": shown, "channels": names, "min_recharge": pay.MinRecharge})
}

func handleAutoRenew(c *gin.Context) {
	var req struct {
		On bool `json:"on"`
	}
	if c.ShouldBindJSON(&req) != nil {
		fail(c, 400, "请求格式错误")
		return
	}
	db.Model(me(c)).Update("auto_renew", req.On)
	c.JSON(200, gin.H{"ok": true})
}

// ---------- 节点状态页 ----------

// handleStatus 按设备组列出节点和探针数据。设备组可以设置对非管理员或对所有人隐藏。
func handleStatus(c *gin.Context) {
	u := me(c)
	var groups []DeviceGroup
	db.Order("id desc").Find(&groups)
	var nodes []Node
	db.Order("id").Find(&nodes)
	type nodeRow struct {
		ID       uint            `json:"id"`
		Name     string          `json:"name"`
		IP       string          `json:"ip"`
		Version  string          `json:"version"`
		Conns    int             `json:"conns"`
		Online   bool            `json:"online"`
		Terminal bool            `json:"terminal"`
		LastSeen time.Time       `json:"last_seen"`
		Status   json.RawMessage `json:"status"`
	}
	type groupRow struct {
		ID    uint      `json:"id"`
		Name  string    `json:"name"`
		Type  string    `json:"type"`
		Nodes []nodeRow `json:"nodes"`
	}
	out := []groupRow{}
	for _, g := range groups {
		if g.HideInStatus == 2 || (g.HideInStatus == 1 && !u.Admin) || g.Type == GroupChain {
			continue
		}
		if !u.Admin && !groupAllowsUser(&g, u) {
			continue
		}
		row := groupRow{ID: g.ID, Name: g.Name, Type: g.Type, Nodes: []nodeRow{}}
		for _, n := range nodes {
			if n.GroupID != g.ID {
				continue
			}
			lv := getLive(n.ID)
			nr := nodeRow{ID: n.ID, Name: n.Name, IP: n.host(), Version: n.Version, Conns: n.Conns,
				Online: n.online(), LastSeen: n.LastSeen, Status: lv.Status,
				Terminal: u.Admin && lv.Terminal && nodeConnected(n.ID)}
			if !nr.Online || len(nr.Status) == 0 {
				nr.Status = json.RawMessage("null")
			}
			row.Nodes = append(row.Nodes, nr)
		}
		if len(row.Nodes) > 0 {
			out = append(out, row)
		}
	}
	c.JSON(200, out)
}
