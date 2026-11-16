package main

import (
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// ---------- 统计 ----------

type dayStat struct {
	Date  string `json:"date"`
	Bytes int64  `json:"bytes"`
}

// lastDays 返回最近 n 天（含今天）的流量，userID 为 0 时统计全站。
func lastDays(n int, userID uint) []dayStat {
	out := make([]dayStat, n)
	idx := map[string]int{}
	now := time.Now()
	for i := 0; i < n; i++ {
		d := now.AddDate(0, 0, i-n+1).Format("2006-01-02")
		out[i] = dayStat{Date: d}
		idx[d] = i
	}
	q := db.Model(&TrafficDaily{}).Select("date, SUM(bytes) AS bytes").
		Where("date >= ?", out[0].Date).Group("date")
	if userID != 0 {
		q = q.Where("user_id = ?", userID)
	}
	var rows []dayStat
	q.Scan(&rows)
	for _, r := range rows {
		if i, ok := idx[r.Date]; ok {
			out[i].Bytes = r.Bytes
		}
	}
	return out
}

func handleUserStats(c *gin.Context) {
	u := me(c)
	var rules int64
	db.Model(&Rule{}).Where("user_id = ?", u.ID).Count(&rules)
	c.JSON(200, gin.H{"rules": rules, "days": lastDays(7, u.ID)})
}

type rankRow struct {
	Name  string `json:"name"`
	Bytes int64  `json:"bytes"`
}

// handleAdminStats 仪表盘：今日/昨日流量、用户数、用户与入口设备组的流量排行。
func handleAdminStats(c *gin.Context) {
	now := time.Now()
	today, yesterday := now.Format("2006-01-02"), now.AddDate(0, 0, -1).Format("2006-01-02")
	sum := func(date string) (n int64) {
		db.Model(&TrafficDaily{}).Where("date = ?", date).Select("COALESCE(SUM(bytes),0)").Scan(&n)
		return
	}
	userRank := func(date string) []rankRow {
		var ts []TrafficDaily
		db.Where("date = ? AND bytes > 0", date).Order("bytes desc").Limit(10).Find(&ts)
		rows := make([]rankRow, 0, len(ts))
		for _, t := range ts {
			var u User
			db.Select("username").First(&u, t.UserID)
			rows = append(rows, rankRow{fmt.Sprintf("%s (#%d)", u.Username, t.UserID), t.Bytes})
		}
		return rows
	}
	groupRank := func(date string) []rankRow {
		var ts []GroupTrafficDaily
		db.Where("date = ? AND bytes > 0", date).Order("bytes desc").Limit(10).Find(&ts)
		rows := make([]rankRow, 0, len(ts))
		for _, t := range ts {
			var g DeviceGroup
			db.Select("name").First(&g, t.GroupID)
			rows = append(rows, rankRow{fmt.Sprintf("%s (#%d)", g.Name, t.GroupID), t.Bytes})
		}
		return rows
	}
	var users int64
	db.Model(&User{}).Count(&users)
	// 充值统计：已完成的充值订单，按到账时间
	recharge := func(from, to time.Time) (v float64) {
		db.Model(&Order{}).Where("type = ? AND status = ? AND paid_at >= ? AND paid_at < ?", "充值", "已完成", from, to).
			Select("COALESCE(SUM(amount),0)").Scan(&v)
		return
	}
	day := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	month := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())
	c.JSON(200, gin.H{"users": users, "today_bytes": sum(today), "yesterday_bytes": sum(yesterday),
		"recharge_today": recharge(day, day.AddDate(0, 0, 1)), "recharge_yesterday": recharge(day.AddDate(0, 0, -1), day),
		"recharge_month": recharge(month, month.AddDate(0, 1, 0)), "recharge_last_month": recharge(month.AddDate(0, -1, 0), month),
		"user_rank_today": userRank(today), "user_rank_yesterday": userRank(yesterday),
		"node_rank_today": groupRank(today), "node_rank_yesterday": groupRank(yesterday)})
}

// ---------- 用户 ----------

type userReq struct {
	Username     string     `json:"username"`
	Password     string     `json:"password"`
	Admin        bool       `json:"admin"`
	Enabled      bool       `json:"enabled"`
	TrafficLimit int64      `json:"traffic_limit"`
	MaxRules     int        `json:"max_rules"`
	GroupID      uint       `json:"group_id"`
	SpeedLimit   int64      `json:"speed_limit"`
	IPLimit      int        `json:"ip_limit"`
	ConnLimit    int        `json:"conn_limit"`
	Balance      float64    `json:"balance"`
	Note         string     `json:"note"`
	TrafficUsed  *int64     `json:"traffic_used"`
	ExpireAt     *time.Time `json:"expire_at"`
}

func (r *userReq) validate() string {
	if r.TrafficLimit < 0 || r.MaxRules < 0 || r.SpeedLimit < 0 || r.IPLimit < 0 || r.ConnLimit < 0 {
		return "限制不能为负数"
	}
	return ""
}

func handleAdminListUsers(c *gin.Context) {
	var users []User
	db.Order("id").Find(&users)
	c.JSON(200, users)
}

// handleAdminCreateUser 新建用户。界面上只填用户名：密码随机生成并在响应里返回一次，
// 其余属性用默认值，之后再通过「编辑」修改。
func handleAdminCreateUser(c *gin.Context) {
	var req userReq
	if c.ShouldBindJSON(&req) != nil {
		fail(c, 400, "请求格式错误")
		return
	}
	generated := req.Password == ""
	if generated {
		req.Password = randHex(8)
		req.Enabled = true
	}
	if msg := validateCred(req.Username, req.Password); msg != "" {
		fail(c, 400, msg)
		return
	}
	if msg := req.validate(); msg != "" {
		fail(c, 400, msg)
		return
	}
	u := User{Username: req.Username, PasswordHash: hashPassword(req.Password), Admin: req.Admin,
		Enabled: req.Enabled, TrafficLimit: req.TrafficLimit, MaxRules: req.MaxRules, ExpireAt: req.ExpireAt,
		GroupID: req.GroupID, SpeedLimit: req.SpeedLimit, IPLimit: req.IPLimit, ConnLimit: req.ConnLimit,
		Balance: req.Balance, Note: req.Note}
	if db.Create(&u).Error != nil {
		fail(c, 409, "用户名已存在")
		return
	}
	resp := gin.H{"user": u}
	if generated {
		resp["password"] = req.Password
	}
	c.JSON(200, resp)
}

// handleAdminResetPassword 给用户换一个随机密码并返回一次，同时踢掉该用户的所有登录。
func handleAdminResetPassword(c *gin.Context) {
	var u User
	if db.First(&u, c.Param("id")).Error != nil {
		fail(c, 404, "用户不存在")
		return
	}
	pw := randHex(8)
	db.Model(&u).Update("password_hash", hashPassword(pw))
	db.Delete(&Session{}, "user_id = ?", u.ID)
	c.JSON(200, gin.H{"password": pw})
}

func handleAdminUpdateUser(c *gin.Context) {
	var u User
	if db.First(&u, c.Param("id")).Error != nil {
		fail(c, 404, "用户不存在")
		return
	}
	var req userReq
	if c.ShouldBindJSON(&req) != nil {
		fail(c, 400, "请求格式错误")
		return
	}
	if msg := req.validate(); msg != "" {
		fail(c, 400, msg)
		return
	}
	if u.ID == me(c).ID && (!req.Admin || !req.Enabled) {
		fail(c, 400, "不能取消自己的管理员权限或禁用自己")
		return
	}
	if req.Password != "" {
		if len(req.Password) < 6 || len(req.Password) > 72 {
			fail(c, 400, "密码长度需为 6-72 位")
			return
		}
		u.PasswordHash = hashPassword(req.Password)
		db.Delete(&Session{}, "user_id = ?", u.ID)
	}
	if !req.Enabled {
		db.Delete(&Session{}, "user_id = ?", u.ID)
	}
	u.Admin, u.Enabled = req.Admin, req.Enabled
	u.TrafficLimit, u.MaxRules, u.ExpireAt = req.TrafficLimit, req.MaxRules, req.ExpireAt
	u.GroupID, u.SpeedLimit, u.IPLimit = req.GroupID, req.SpeedLimit, req.IPLimit
	u.ConnLimit, u.Balance, u.Note = req.ConnLimit, req.Balance, req.Note
	if req.TrafficUsed != nil && *req.TrafficUsed >= 0 {
		u.TrafficUsed = *req.TrafficUsed
	}
	if dbFail(c, db.Save(&u).Error) {
		return
	}
	nudgeAll()
	c.JSON(200, u)
}

func handleAdminDeleteUser(c *gin.Context) {
	var u User
	if db.First(&u, c.Param("id")).Error != nil {
		fail(c, 404, "用户不存在")
		return
	}
	if u.ID == me(c).ID {
		fail(c, 400, "不能删除自己")
		return
	}
	db.Delete(&Rule{}, "user_id = ?", u.ID)
	db.Delete(&Session{}, "user_id = ?", u.ID)
	db.Delete(&TrafficDaily{}, "user_id = ?", u.ID)
	db.Delete(&u)
	nudgeAll()
	c.JSON(200, gin.H{"ok": true})
}

func handleAdminResetTraffic(c *gin.Context) {
	if db.Model(&User{}).Where("id = ?", c.Param("id")).Update("traffic_used", 0).RowsAffected == 0 {
		fail(c, 404, "用户不存在")
		return
	}
	nudgeAll()
	c.JSON(200, gin.H{"ok": true})
}

// ---------- 用户组 ----------

func handleAdminListUserGroups(c *gin.Context) {
	groups := []UserGroup{}
	db.Order("id desc").Find(&groups)
	type row struct {
		UserGroup
		Users int64 `json:"users"`
	}
	out := make([]row, 0, len(groups))
	for _, g := range groups {
		var n int64
		db.Model(&User{}).Where("group_id = ?", g.ID).Count(&n)
		out = append(out, row{g, n})
	}
	c.JSON(200, out)
}

// handleAdminSaveUserGroup 同时处理新建 (POST) 和改名 (PUT)。新建时可以指定 ID，留空自动生成。
func handleAdminSaveUserGroup(c *gin.Context) {
	var req struct {
		ID   uint   `json:"id"`
		Name string `json:"name"`
	}
	if c.ShouldBindJSON(&req) != nil {
		fail(c, 400, "请求格式错误")
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if len(req.Name) > 64 {
		fail(c, 400, "名称最多 64 个字符")
		return
	}
	var g UserGroup
	if id := c.Param("id"); id != "" {
		if db.First(&g, id).Error != nil {
			fail(c, 404, "用户组不存在")
			return
		}
	} else if req.ID != 0 {
		if db.First(&UserGroup{}, req.ID).Error == nil {
			fail(c, 409, "这个用户组 ID 已存在")
			return
		}
		g.ID = req.ID
	}
	g.Name = req.Name
	if dbFail(c, db.Save(&g).Error) {
		return
	}
	c.JSON(200, g)
}

func handleAdminDeleteUserGroup(c *gin.Context) {
	var g UserGroup
	if db.First(&g, c.Param("id")).Error != nil {
		fail(c, 404, "用户组不存在")
		return
	}
	// 组内用户变为未分组
	db.Model(&User{}).Where("group_id = ?", g.ID).Update("group_id", 0)
	db.Delete(&g)
	nudgeAll()
	c.JSON(200, gin.H{"ok": true})
}

// ---------- 设备组 ----------

type groupReq struct {
	Name        string  `json:"name"`
	Type        string  `json:"type"`
	Ratio       float64 `json:"ratio"`
	Note        string  `json:"note"`
	AllowGroups []uint  `json:"allow_groups"`
	// 入口
	OutLimit     string `json:"out_limit"`
	ConnectAddr  string `json:"connect_addr"`
	PortRange    string `json:"port_range"`
	DirectPolicy int    `json:"direct_policy"`
	UDPSmartBind bool   `json:"udp_smart_bind"`
	// 出口 / 链式出口
	InLimit         string `json:"in_limit"`
	TunnelProto     string `json:"tunnel_proto"`
	OfflineDrain    int    `json:"offline_drain"`
	FailoverGroupID uint   `json:"failover_group_id"`
	Hops            []uint `json:"hops"`

	HideInStatus int `json:"hide_in_status"`
}

// apply 把通过校验的请求写入设备组（不保存）。
func (r *groupReq) apply(g *DeviceGroup) {
	g.Name, g.Ratio, g.Note, g.HideInStatus = r.Name, r.Ratio, r.Note, r.HideInStatus
	g.AllowGroups, g.AllowGroupIDs = joinIDs(r.AllowGroups), r.AllowGroups
	if g.AllowGroupIDs == nil {
		g.AllowGroupIDs = []uint{}
	}
	g.HopIDs = []uint{}
	switch g.Type {
	case GroupIn:
		g.OutLimit, g.ConnectAddr, g.PortRange = r.OutLimit, r.ConnectAddr, r.PortRange
		g.DirectPolicy, g.UDPSmartBind = r.DirectPolicy, r.UDPSmartBind
	case GroupOut:
		g.InLimit, g.TunnelProto, g.OfflineDrain, g.FailoverGroupID = r.InLimit, r.TunnelProto, r.OfflineDrain, r.FailoverGroupID
		if g.TunnelPort == 0 {
			// 界面上不填隧道端口，新建时自动分配一个；需要固定端口可在「高级编辑」里改
			g.TunnelPort = freeTunnelPort()
		}
	case GroupChain:
		g.InLimit, g.Hops, g.HopIDs = r.InLimit, joinIDs(r.Hops), r.Hops
	}
}

func (r *groupReq) validate(selfID uint) string {
	r.Name = strings.TrimSpace(r.Name)
	if r.Name == "" || len(r.Name) > 64 {
		return "名称需为 1-64 个字符"
	}
	if r.Ratio < 0 || r.Ratio > 1000 {
		return "倍率需在 0-1000 之间"
	}
	if r.HideInStatus < 0 || r.HideInStatus > 2 {
		return "「在探针中隐藏」取值错误"
	}
	idList := func(s, what string) string {
		for _, p := range strings.Split(strings.ReplaceAll(s, "，", ","), ",") {
			if p = strings.TrimSpace(p); p == "" {
				continue
			}
			if n, err := strconv.ParseUint(p, 10, 32); err != nil || n == 0 {
				return what + "应为设备组 ID，多个用逗号分隔"
			}
		}
		return ""
	}
	switch r.Type {
	case GroupMonitor:
	case GroupIn:
		r.OutLimit = strings.TrimSpace(r.OutLimit)
		if _, err := parseOutLimit(r.OutLimit); err != nil {
			return err.Error()
		}
		r.PortRange = strings.TrimSpace(r.PortRange)
		if _, err := parsePortRange(r.PortRange); err != nil {
			return err.Error()
		}
		if r.DirectPolicy < 0 || r.DirectPolicy > 2 {
			return "直接转发策略取值错误"
		}
	case GroupOut:
		if msg := idList(r.InLimit, "限制入口"); msg != "" {
			return msg
		}
		switch r.TunnelProto {
		case "":
			return "请选择协议"
		case "ws", "http", "tls_simple":
		default:
			return "协议需为 ws、http 或 tls_simple"
		}
		if r.OfflineDrain < 0 {
			return "负载下线不能为负数"
		}
		if r.FailoverGroupID != 0 {
			if r.FailoverGroupID == selfID {
				return "故障转移组不能是自己"
			}
			if db.First(&DeviceGroup{}, "id = ? AND type = ?", r.FailoverGroupID, GroupOut).Error != nil {
				return "故障转移组必须是一个存在的出口组"
			}
		}
	case GroupChain:
		if msg := idList(r.InLimit, "限制入口"); msg != "" {
			return msg
		}
		if len(r.Hops) == 0 || len(r.Hops) > maxChainHops {
			return fmt.Sprintf("链式出口需要配置 1~%d 跳", maxChainHops)
		}
		seen := map[uint]bool{}
		for i, id := range r.Hops {
			if seen[id] {
				return "同一个出口不能在链路里出现两次"
			}
			seen[id] = true
			if db.First(&DeviceGroup{}, "id = ? AND type = ?", id, GroupOut).Error != nil {
				return fmt.Sprintf("第 %d 跳必须选择一个出口组", i+1)
			}
		}
	default:
		return "类型需为 monitor、in、out 或 chain"
	}
	return ""
}

// groupConfig 是设备组「高级编辑」里的 JSON，都是界面表单上没有的设置。
type groupConfig struct {
	// ---- 入口 ----
	DisableUDP *bool `json:"disable_udp,omitempty"`
	// 入站屏蔽（只对 TCP 生效，后缀匹配）
	BlockedProtocol []string `json:"blocked_protocol,omitempty"` // socks / http / tls
	BlockedHost     []string `json:"blocked_host,omitempty"`
	AllowedHost     []string `json:"allowed_host,omitempty"`
	BlockedPath     []string `json:"blocked_path,omitempty"`
	// 出口故障判定：连续失败 max_fail 次后，fail_timeout_sec 秒内不再分配新连接
	MaxFail        int `json:"max_fail,omitempty"`
	FailTimeoutSec int `json:"fail_timeout_sec,omitempty"`
	// 连接这些出口组时优先用 IPv6。填出口组 ID；[0] 表示全部优先 IPv6，
	// [0, 1, 2] 表示全部优先 IPv6、但 1 和 2 仍用 IPv4。出口没有 IPv6 地址时回落到 IPv4
	IPv6Group []uint `json:"ipv6_group,omitempty"`
	// 反向隧道：这些出口组由出口主动连入口（出口在内网时用）。填出口组 ID，[0] 表示所有出口组。
	// 只对启动时带了 -rev-port 的入口节点生效
	ReverseGroup []uint `json:"reverse_group,omitempty"`
	// TLS 入站策略：0 不限制；1 只允许 TLS 入站规则；2 只允许 TLS 入站规则，且只有管理员能监听端口（用户只能建子规则）
	TLSInboundPolicy int `json:"tls_inbound_policy,omitempty"`
	// 拒绝不带 SNI 的 TLS 连接（防扫描）
	TLSRejectEmptySNI bool `json:"tls_reject_empty_sni,omitempty"`

	// ---- 出口 ----
	TunnelPort *int `json:"tunnel_port,omitempty"`
	// connect_type：static 时入口固定连接 connect_address:connect_port（经 CDN、NAT 或端口映射时用）；
	// dyn_ip4 强制用节点的 IPv4；dyn_ip6 强制用节点的 IPv6（没有 IPv6 的节点不会被使用，不回落）
	ConnectType    string `json:"connect_type,omitempty"`
	ConnectAddress string `json:"connect_address,omitempty"`
	ConnectPort    int    `json:"connect_port,omitempty"`
	WS             *struct {
		Host string `json:"host,omitempty"`
		Path string `json:"path,omitempty"`
	} `json:"ws,omitempty"`
	Mux *bool `json:"mux,omitempty"` // 连接复用，默认开启
}

func (g *DeviceGroup) cfg() groupConfig {
	var c groupConfig
	if g.Config != "" {
		json.Unmarshal([]byte(g.Config), &c)
	}
	return c
}

func (c *groupConfig) validate() string {
	for _, p := range c.BlockedProtocol {
		if p != "socks" && p != "http" && p != "tls" {
			return "blocked_protocol 只能包含 socks、http、tls"
		}
	}
	if len(c.AllowedHost) > 0 && len(c.BlockedHost)+len(c.BlockedProtocol)+len(c.BlockedPath) > 0 {
		return "allowed_host（白名单）不能和其他屏蔽选项同时使用"
	}
	if c.TLSInboundPolicy < 0 || c.TLSInboundPolicy > 2 {
		return "tls_inbound_policy 取值为 0、1 或 2"
	}
	if c.MaxFail < 0 || c.FailTimeoutSec < 0 {
		return "max_fail 和 fail_timeout_sec 不能为负数"
	}
	if c.TunnelPort != nil && (*c.TunnelPort < 1 || *c.TunnelPort > 65535) {
		return "tunnel_port 需在 1-65535 之间"
	}
	switch c.ConnectType {
	case "":
	case "dyn_ip4", "dyn_ip6":
	case "static":
		if c.ConnectAddress == "" {
			return "connect_type 为 static 时需要填写 connect_address"
		}
		if c.ConnectPort < 0 || c.ConnectPort > 65535 {
			return "connect_port 需在 1-65535 之间"
		}
	default:
		return "connect_type 可取 static、dyn_ip4、dyn_ip6（留空表示默认）"
	}
	return ""
}

// handleAdminGroupConfig 保存「高级编辑」的 JSON。
func handleAdminGroupConfig(c *gin.Context) {
	var g DeviceGroup
	if db.First(&g, c.Param("id")).Error != nil {
		fail(c, 404, "设备组不存在")
		return
	}
	var cfg groupConfig
	dec := json.NewDecoder(c.Request.Body)
	dec.DisallowUnknownFields() // 写错的键名直接报错，免得以为生效了
	if err := dec.Decode(&cfg); err != nil {
		fail(c, 400, "JSON 有误: "+err.Error())
		return
	}
	if msg := cfg.validate(); msg != "" {
		fail(c, 400, msg)
		return
	}
	if g.Type == GroupIn {
		g.BlockUDP = cfg.DisableUDP != nil && *cfg.DisableUDP
	}
	if g.Type == GroupOut && cfg.TunnelPort != nil {
		g.TunnelPort = *cfg.TunnelPort
	}
	raw, _ := json.Marshal(cfg)
	g.Config = string(raw)
	if dbFail(c, db.Save(&g).Error) {
		return
	}
	nudgeAll()
	c.JSON(200, g)
}

type groupPublic struct {
	ID     uint    `json:"id"`
	Name   string  `json:"name"`
	Type   string  `json:"type"`
	Ratio  float64 `json:"ratio"`
	Online int     `json:"online"`
	// 仅入口组
	DirectPolicy int    `json:"direct_policy"`
	AllowedOut   []uint `json:"allowed_out"` // 空表示不限
	BlockUDP     bool   `json:"block_udp"`
	ConnectAddr  string `json:"connect_addr"`
	PortRange    string `json:"port_range"`
	// 仅出口 / 链式出口
	AllowedIn []uint `json:"allowed_in"` // 空表示不限
	Hops      int    `json:"hops"`       // 链式出口的跳数
}

func onlineCounts() map[uint]int {
	var nodes []Node
	db.Find(&nodes)
	m := map[uint]int{}
	for i := range nodes {
		if nodes[i].online() {
			m[nodes[i].GroupID]++
		}
	}
	return m
}

// handleListGroupsPublic 给普通用户选择设备组用，不含对接密钥。
func handleListGroupsPublic(c *gin.Context) {
	var groups []DeviceGroup
	db.Order("id").Find(&groups)
	on := onlineCounts()
	out := make([]groupPublic, 0, len(groups))
	u := me(c)
	for _, g := range groups {
		if g.Type == GroupMonitor || !groupAllowsUser(&g, u) {
			continue
		}
		lim, _ := parseOutLimit(g.OutLimit)
		if lim.IDs == nil {
			lim.IDs = []uint{}
		}
		dp := g.DirectPolicy
		if lim.NoDirect {
			dp = 0
		}
		online := on[g.ID]
		if g.Type == GroupChain {
			// 链式出口没有自己的节点，在线数取第一跳的
			if hops := parseIDs(g.Hops); len(hops) > 0 {
				online = on[hops[0]]
			}
		}
		out = append(out, groupPublic{ID: g.ID, Name: g.Name, Type: g.Type, Ratio: g.Ratio, Online: online,
			DirectPolicy: dp, AllowedOut: lim.IDs, BlockUDP: g.BlockUDP, ConnectAddr: g.ConnectAddr,
			PortRange: g.PortRange, AllowedIn: parseIDs(g.InLimit), Hops: len(parseIDs(g.Hops))})
	}
	c.JSON(200, out)
}

func handleAdminListGroups(c *gin.Context) {
	var groups []DeviceGroup
	db.Order("id").Find(&groups)
	on := onlineCounts()
	type row struct {
		DeviceGroup
		Online int `json:"online"`
	}
	out := make([]row, 0, len(groups))
	for _, g := range groups {
		out = append(out, row{g, on[g.ID]})
	}
	c.JSON(200, out)
}

func handleAdminCreateGroup(c *gin.Context) {
	var req groupReq
	if c.ShouldBindJSON(&req) != nil {
		fail(c, 400, "请求格式错误")
		return
	}
	if msg := req.validate(0); msg != "" {
		fail(c, 400, msg)
		return
	}
	g := DeviceGroup{Type: req.Type, Token: randHex(20), TunnelKey: randHex(24)}
	req.apply(&g)
	if dbFail(c, db.Create(&g).Error) {
		return
	}
	c.JSON(200, g)
}

func handleAdminUpdateGroup(c *gin.Context) {
	var g DeviceGroup
	if db.First(&g, c.Param("id")).Error != nil {
		fail(c, 404, "设备组不存在")
		return
	}
	var req groupReq
	if c.ShouldBindJSON(&req) != nil {
		fail(c, 400, "请求格式错误")
		return
	}
	req.Type = g.Type // 类型创建后不可修改
	if msg := req.validate(g.ID); msg != "" {
		fail(c, 400, msg)
		return
	}
	req.apply(&g)
	if dbFail(c, db.Save(&g).Error) {
		return
	}
	nudgeAll()
	c.JSON(200, g)
}

func handleAdminDeleteGroup(c *gin.Context) {
	var g DeviceGroup
	if db.First(&g, c.Param("id")).Error != nil {
		fail(c, 404, "设备组不存在")
		return
	}
	var n int64
	db.Model(&Rule{}).Where("in_group_id = ? OR out_group_id = ?", g.ID, g.ID).Count(&n)
	if n > 0 {
		fail(c, 400, "仍有转发规则在使用该设备组，请先删除这些规则")
		return
	}
	db.Delete(&Node{}, "group_id = ?", g.ID)
	db.Model(&DeviceGroup{}).Where("failover_group_id = ?", g.ID).Update("failover_group_id", 0)
	db.Delete(&g)
	c.JSON(200, gin.H{"ok": true})
}

// handleAdminResetGroupToken 重置对接密钥，已对接的节点需要用新密钥重新对接。
func handleAdminResetGroupToken(c *gin.Context) {
	var g DeviceGroup
	if db.First(&g, c.Param("id")).Error != nil {
		fail(c, 404, "设备组不存在")
		return
	}
	g.Token, g.TunnelKey = randHex(20), randHex(24)
	if dbFail(c, db.Save(&g).Error) {
		return
	}
	kickGroup(g.ID)
	nudgeAll()
	c.JSON(200, g)
}

// ---------- 节点 ----------

func handleAdminListNodes(c *gin.Context) {
	var nodes []Node
	db.Order("group_id, id").Find(&nodes)
	type row struct {
		Node
		Online   bool            `json:"online"`
		Status   json.RawMessage `json:"status"`   // 探针数据，节点原样上报
		Terminal bool            `json:"terminal"` // 节点是否开启了在线终端且长连接在线
	}
	out := make([]row, 0, len(nodes))
	for _, n := range nodes {
		lv := getLive(n.ID)
		r := row{n, n.online(), lv.Status, lv.Terminal && nodeConnected(n.ID)}
		if !r.Online || len(r.Status) == 0 {
			r.Status = json.RawMessage("null")
		}
		out = append(out, r)
	}
	c.JSON(200, out)
}

func handleAdminDeleteNode(c *gin.Context) {
	db.Delete(&Node{}, "id = ?", c.Param("id"))
	c.JSON(200, gin.H{"ok": true})
}

// handleAdminDeleteOfflineNodes 在删除时用探针页的在线时限检查最后心跳。
func handleAdminDeleteOfflineNodes(c *gin.Context) {
	var req struct {
		ID  uint `json:"id"`
		All bool `json:"all"`
	}
	if c.ShouldBindJSON(&req) != nil || (req.ID == 0 && !req.All) || (req.ID != 0 && req.All) {
		fail(c, 400, "请选择一个节点或所有离线节点")
		return
	}
	q := db.Where("last_seen <= ?", time.Now().Add(-nodeOnlineWindow))
	if !req.All {
		q = q.Where("id = ?", req.ID)
	}
	result := q.Delete(&Node{})
	if dbFail(c, result.Error) {
		return
	}
	if !req.All && result.RowsAffected == 0 {
		fail(c, 409, "节点已恢复在线或已被删除")
		return
	}
	c.JSON(200, gin.H{"deleted": result.RowsAffected})
}

// handleAdminNodeWeight 设置出口节点的负载均衡权重。0 表示恢复默认（节点启动参数 -weight，没有则为 1）。
func handleAdminNodeWeight(c *gin.Context) {
	var req struct {
		Weight int `json:"weight"`
	}
	if c.ShouldBindJSON(&req) != nil || req.Weight < 0 || req.Weight > 10000 {
		fail(c, 400, "权重应为 0 到 10000 的整数")
		return
	}
	var n Node
	if db.First(&n, "id = ?", c.Param("id")).Error != nil {
		fail(c, 404, "节点不存在")
		return
	}
	if dbFail(c, db.Model(&n).Update("panel_weight", req.Weight).Error) {
		return
	}
	nudgeAll()
	n.PanelWeight = req.Weight
	c.JSON(200, gin.H{"ok": true, "weight": n.weight()})
}

// handleAdminNodeNote 设置节点的备注。
func handleAdminNodeNote(c *gin.Context) {
	var req struct {
		Note string `json:"note"`
	}
	if c.ShouldBindJSON(&req) != nil {
		fail(c, 400, "请求格式错误")
		return
	}
	req.Note = strings.TrimSpace(req.Note)
	if len([]rune(req.Note)) > 80 {
		fail(c, 400, "备注最多 80 个字")
		return
	}
	var n Node
	if db.First(&n, "id = ?", c.Param("id")).Error != nil {
		fail(c, 404, "节点不存在")
		return
	}
	if dbFail(c, db.Model(&n).Update("note", req.Note).Error) {
		return
	}
	c.JSON(200, gin.H{"ok": true, "note": req.Note})
}

// handleAdminResetGroupTraffic 清空选中设备组的已用流量统计。
func handleAdminResetGroupTraffic(c *gin.Context) {
	var req struct {
		IDs []uint `json:"ids"`
	}
	if c.ShouldBindJSON(&req) != nil || len(req.IDs) == 0 {
		fail(c, 400, "请求格式错误")
		return
	}
	db.Model(&DeviceGroup{}).Where("id IN ?", req.IDs).Update("traffic_used", 0)
	c.JSON(200, gin.H{"ok": true})
}

// handleAdminClean 清理无效数据。
//
//	users: 删除「已过期、且没有任何转发规则」的普通用户
//	rules: 删除入口或出口设备组已经不存在的转发规则
func handleAdminClean(c *gin.Context) {
	var req struct {
		Kind string `json:"kind"`
	}
	if c.ShouldBindJSON(&req) != nil {
		fail(c, 400, "请求格式错误")
		return
	}
	deleted := 0
	switch req.Kind {
	case "users":
		var users []User
		db.Where("admin = ? AND expire_at IS NOT NULL AND expire_at < ?", false, time.Now()).Find(&users)
		for _, u := range users {
			var n int64
			db.Model(&Rule{}).Where("user_id = ?", u.ID).Count(&n)
			if n > 0 {
				continue
			}
			db.Delete(&Session{}, "user_id = ?", u.ID)
			db.Delete(&TrafficDaily{}, "user_id = ?", u.ID)
			db.Delete(&u)
			deleted++
		}
	case "rules":
		var rules []Rule
		db.Find(&rules)
		for _, r := range rules {
			if loadGroup(r.InGroupID) != nil && (r.OutGroupID == 0 || loadGroup(r.OutGroupID) != nil) {
				continue
			}
			db.Delete(&r)
			clearRuleStatus(r.ID)
			deleted++
		}
		nudgeAll()
	default:
		fail(c, 400, "不支持的清理类型")
		return
	}
	c.JSON(200, gin.H{"deleted": deleted})
}

// freeTunnelPort 给新的出口组挑一个隧道端口：随机，避开其他设备组已经用了的，
// 也避开系统给出站连接临时分配的端口范围（Linux 默认 32768 起）。
func freeTunnelPort() int {
	var used []int
	db.Model(&DeviceGroup{}).Where("tunnel_port > 0").Pluck("tunnel_port", &used)
	taken := map[int]bool{}
	for _, p := range used {
		taken[p] = true
	}
	for {
		if p := 10000 + rand.IntN(22000); !taken[p] {
			return p
		}
	}
}

// handleAdminSetGroupCat 把选中的设备组放进某个分组（0 表示未分组）。分组列表保存在设置项 dg_cats 里。
func handleAdminSetGroupCat(c *gin.Context) {
	var req struct {
		IDs []uint `json:"ids"`
		Cat uint   `json:"cat"`
	}
	if c.ShouldBindJSON(&req) != nil || len(req.IDs) == 0 {
		fail(c, 400, "请求格式错误")
		return
	}
	db.Model(&DeviceGroup{}).Where("id IN ?", req.IDs).Update("cat_id", req.Cat)
	c.JSON(200, gin.H{"ok": true})
}
