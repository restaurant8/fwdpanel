package main

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// 用 var 而不是 const：测试在线升级时可以用 -ldflags "-X main.panelVersion=..." 构建一个"旧版本"
var panelVersion = "0.9.3"

// nodeVersion 是内嵌的节点端安装包的版本，节点上报的版本与它不同时界面会提示升级。
const nodeVersion = "0.9.3"

// ---------- KV：站点设置、支付通道、套餐、兑换码、推送设置 ----------
//
// 这些设置目前只做保存和展示：支付通道、套餐、兑换码、邀请、Telegram 推送都没有接入实际业务。

var kvKeys = map[string]bool{"site": true, "pay": true, "plans": true, "redeem": true, "invite": true, "push": true, "dg_cats": true}

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

func saveKV(key string, v any) {
	raw, _ := json.Marshal(v)
	db.Save(&KV{Key: key, Value: string(raw)})
}

// siteSettings 是「站点设置 - 基本」和公告。只有 title、allow_register、notice 实际生效。
type siteSettings struct {
	Title          string `json:"title"`
	AllowRegister  *bool  `json:"allow_register"`
	Notice         string `json:"notice"`
	DiagnoseHideIP int    `json:"diagnose_hide_ip"`
	// 允许用户自带出口（单端隧道）
	AllowUserEgress bool `json:"allow_user_egress"`
	// 邀请注册策略：0 无限制，1 不允许邀请注册，2 仅开放邀请注册
	RegisterPolicy int `json:"register_policy"`
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
		"allow_user_egress": s.AllowUserEgress, "register_policy": s.RegisterPolicy,
		"tg_enabled": loadPush().TGEnabled, "version": panelVersion, "node_version": nodeVersion, "diagnose_hide_ip": s.DiagnoseHideIP})
}

// handleShop 返回商城页需要的数据：未隐藏的套餐、已启用的支付通道名称、最小充值金额。
func handleShop(c *gin.Context) {
	u := me(c)
	shown := []plan{}
	var mine *plan
	for _, p := range loadPlans() {
		if p.ID == u.PlanID {
			pp := p
			mine = &pp
		}
		if !p.Hidden {
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
	// my_plan 是用户当前的套餐（即使已被隐藏），用来显示名称和续费价格
	c.JSON(200, gin.H{"plans": shown, "channels": names, "min_recharge": pay.MinRecharge, "my_plan": mine})
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

// nodeRow 是探针页（以及探针开放接口）里的一个节点。
type nodeRow struct {
	ID       uint            `json:"id"`
	Name     string          `json:"name"`
	Note     string          `json:"note"` // 管理员给这台机器写的备注
	IP       string          `json:"ip"`
	IP6      string          `json:"ip6"`
	Version  string          `json:"version"`
	Conns    int             `json:"conns"`     // TCP 连接数 + UDP 会话数
	ConnsTCP int             `json:"conns_tcp"` // TCP 连接数
	ConnsUDP int             `json:"conns_udp"` // UDP 会话数
	Weight   int             `json:"weight"`    // 出口节点实际生效的权重
	Online   bool            `json:"online"`
	Terminal bool            `json:"terminal"`
	LastSeen time.Time       `json:"last_seen"`
	Status   json.RawMessage `json:"status"`
}

func statusRow(n *Node, admin bool) nodeRow {
	lv := getLive(n.ID)
	nr := nodeRow{ID: n.ID, Name: n.Name, Note: n.Note, IP: n.host(), IP6: n.IP6, Version: n.Version, Conns: n.Conns, ConnsTCP: n.Conns - n.ConnsUDP, ConnsUDP: n.ConnsUDP, Weight: n.weight(),
		Online: n.online(), LastSeen: n.LastSeen, Status: lv.Status,
		Terminal: admin && lv.Terminal && nodeConnected(n.ID)}
	if !nr.Online || len(nr.Status) == 0 {
		nr.Status = json.RawMessage("null")
	}
	if !admin {
		nr.IP, nr.IP6 = "", "" // 节点地址只给管理员看
	}
	return nr
}

// handleStatus 按设备组列出节点和探针数据。设备组可以设置对非管理员或对所有人隐藏。
func handleStatus(c *gin.Context) {
	u := me(c)
	var groups []DeviceGroup
	db.Order("id desc").Find(&groups)
	var nodes []Node
	db.Order("id").Find(&nodes)
	type groupRow struct {
		ID    uint      `json:"id"`
		Name  string    `json:"name"`
		Type  string    `json:"type"`
		Nodes []nodeRow `json:"nodes"`
	}
	out := []groupRow{}
	for _, g := range groups {
		if !statusVisible(&g, u) {
			continue
		}
		row := groupRow{ID: g.ID, Name: g.Name, Type: g.Type, Nodes: []nodeRow{}}
		for _, n := range nodes {
			if n.GroupID != g.ID {
				continue
			}
			row.Nodes = append(row.Nodes, statusRow(&n, u.Admin))
		}
		if len(row.Nodes) > 0 {
			out = append(out, row)
		}
	}
	c.JSON(200, out)
}

// ---------- 用户自带出口（单端隧道） ----------
//
// 站点开启后，用户可以建自己的出口设备组、把自己的机器对接上来。这种出口只有本人能用，倍率为 0。

const maxUserEgress = 5

func handleListMyEgress(c *gin.Context) {
	groups := []DeviceGroup{}
	db.Where("owner_id = ?", me(c).ID).Order("id").Find(&groups)
	on := onlineCounts()
	type row struct {
		ID     uint   `json:"id"`
		Name   string `json:"name"`
		Token  string `json:"token"`
		Online int    `json:"online"`
	}
	out := make([]row, 0, len(groups))
	for _, g := range groups {
		out = append(out, row{g.ID, g.Name, g.Token, on[g.ID]})
	}
	c.JSON(200, out)
}

func handleCreateMyEgress(c *gin.Context) {
	u := me(c)
	if !loadSite().AllowUserEgress && !u.Admin {
		fail(c, 403, "本站没有开放用户自带出口")
		return
	}
	var req struct {
		Name string `json:"name"`
	}
	if c.ShouldBindJSON(&req) != nil {
		fail(c, 400, "请求格式错误")
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" || len(req.Name) > 64 {
		fail(c, 400, "名称需为 1-64 个字符")
		return
	}
	var n int64
	if db.Model(&DeviceGroup{}).Where("owner_id = ?", u.ID).Count(&n); n >= maxUserEgress {
		fail(c, 400, fmt.Sprintf("每个用户最多 %d 个自带出口", maxUserEgress))
		return
	}
	g := DeviceGroup{Name: req.Name, Type: GroupOut, OwnerID: u.ID, TunnelProto: "tls_simple",
		Token: randHex(20), TunnelKey: randHex(24), TunnelPort: freeTunnelPort(), HideInStatus: 1}
	if dbFail(c, db.Create(&g).Error) {
		return
	}
	c.JSON(200, gin.H{"id": g.ID, "name": g.Name, "token": g.Token, "online": 0})
}

func handleDeleteMyEgress(c *gin.Context) {
	var g DeviceGroup
	if db.First(&g, "id = ? AND owner_id = ?", c.Param("id"), me(c).ID).Error != nil {
		fail(c, 404, "出口不存在")
		return
	}
	var n int64
	if db.Model(&Rule{}).Where("out_group_id = ?", g.ID).Count(&n); n > 0 {
		fail(c, 400, "还有规则在使用这个出口，请先修改或删除这些规则")
		return
	}
	db.Delete(&Node{}, "group_id = ?", g.ID)
	db.Delete(&g)
	c.JSON(200, gin.H{"ok": true})
}
