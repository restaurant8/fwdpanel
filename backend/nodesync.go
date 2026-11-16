package main

import (
	"encoding/json"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const syncInterval = 10 // 秒

type syncReq struct {
	UID       string `json:"uid"`
	Name      string `json:"name"`
	Version   string `json:"version"`
	Advertise string `json:"advertise"`
	Conns     int    `json:"conns"`
	// 入口节点上报的各规则监听状态
	Rules map[uint]struct {
		OK  bool   `json:"ok"`
		Msg string `json:"msg"`
	} `json:"rules"`
	// 自上次同步以来各规则的流量增量（字节，上下行合计）
	Traffic map[uint]int64 `json:"traffic"`
	// 探针数据，面板不解析，原样交给前端
	Status json.RawMessage `json:"status"`
	// 节点是否开启了在线终端
	Terminal bool `json:"terminal"`
	// 出口节点自己指定的隧道端口和权重
	TunnelPort int    `json:"tunnel_port"`
	Weight     int    `json:"weight"`
	RevPort    int    `json:"rev_port"`
	IPv6       string `json:"ipv6"`
}

type syncOut struct {
	Proto    string   `json:"proto"`
	Key      string   `json:"key"`
	Addrs    []string `json:"addrs"`
	Weights  []int    `json:"weights,omitempty"` // 与 addrs 一一对应
	Mux      bool     `json:"mux"`
	WSHost   string   `json:"ws_host,omitempty"`
	WSPath   string   `json:"ws_path,omitempty"`
	Fallback *syncOut `json:"fallback,omitempty"` // 出口故障转移组
	// 反向隧道：入口不主动连出口，而是使用出口连进来的连接
	Reverse bool `json:"reverse,omitempty"`
	GroupID uint `json:"group_id,omitempty"`
	// 故障判定参数，来自入口组的高级配置
	MaxFail     int `json:"max_fail,omitempty"`
	FailTimeout int `json:"fail_timeout,omitempty"`
}

// syncTLS 是下发给入口节点的 TLS 入站设置。
type syncTLS struct {
	ruleTLS
	SNIs           []string `json:"snis"`
	RejectEmptySNI bool     `json:"reject_empty_sni,omitempty"`
}

// syncFilter 是入口组高级配置里的入站屏蔽设置。
type syncFilter struct {
	BlockedProtocol []string `json:"blocked_protocol,omitempty"`
	BlockedHost     []string `json:"blocked_host,omitempty"`
	AllowedHost     []string `json:"allowed_host,omitempty"`
	BlockedPath     []string `json:"blocked_path,omitempty"`
}

type syncRule struct {
	ID         uint        `json:"id"`
	ListenPort int         `json:"listen_port,omitempty"`
	Proto      string      `json:"proto,omitempty"`
	Targets    []string    `json:"targets"`
	LB         string      `json:"lb,omitempty"`
	UserID     uint        `json:"user_id,omitempty"`
	UserSpeed  int64       `json:"user_speed,omitempty"`
	RuleSpeed  int64       `json:"rule_speed,omitempty"`
	IPLimit    int         `json:"ip_limit,omitempty"`
	UserConns  int         `json:"user_conn_limit,omitempty"`
	Next       *syncOut    `json:"next,omitempty"` // 出口侧：链式出口里本跳之后的下一跳
	RuleIPs    int         `json:"rule_ip_limit,omitempty"`
	ConnLimit  int         `json:"conn_limit,omitempty"`
	AcceptPP   bool        `json:"accept_pp,omitempty"`
	SendPP     int         `json:"send_pp,omitempty"`
	Out        *syncOut    `json:"out,omitempty"`
	Filter     *syncFilter `json:"filter,omitempty"`
	TLS        *syncTLS    `json:"tls,omitempty"`
}

type syncResp struct {
	Type        string `json:"type"`
	Interval    int    `json:"interval"`
	TunnelPort  int    `json:"tunnel_port,omitempty"`
	TunnelKey   string `json:"tunnel_key,omitempty"`
	TunnelProto string `json:"tunnel_proto,omitempty"`
	// 出口侧：本组 ID，以及需要主动连过去的入口反向地址
	GroupID uint       `json:"group_id,omitempty"`
	Reverse []string   `json:"reverse,omitempty"`
	Rules   []syncRule `json:"rules"`
}

// handleNodeSync 是节点与面板之间的主要接口：
// 节点上报状态与流量，面板返回该节点应当生效的全部规则。
func handleNodeSync(c *gin.Context) {
	tok := bearer(c)
	var g DeviceGroup
	if tok == "" || db.First(&g, "token = ?", tok).Error != nil {
		fail(c, 401, "对接密钥无效")
		return
	}
	var req syncReq
	if c.ShouldBindJSON(&req) != nil || req.UID == "" || len(req.UID) > 64 || len(req.Status) > 8192 {
		fail(c, 400, "请求格式错误")
		return
	}

	var node Node
	db.Where(Node{GroupID: g.ID, UID: req.UID}).FirstOrCreate(&node)
	node.Name, node.Version, node.Advertise = req.Name, req.Version, req.Advertise
	node.IP, node.Conns, node.LastSeen = c.ClientIP(), req.Conns, time.Now()
	node.TunnelPort, node.Weight, node.RevPort = req.TunnelPort, req.Weight, req.RevPort
	node.IP6 = ""
	if ip := net.ParseIP(req.IPv6); ip != nil && ip.To4() == nil {
		node.IP6 = ip.String()
	}
	db.Save(&node)
	setLive(node.ID, nodeLive{Status: req.Status, Terminal: req.Terminal})

	recordTraffic(&g, req.Traffic)

	resp := syncResp{Type: g.Type, Interval: syncInterval, Rules: []syncRule{}}
	if g.Type != GroupIn && g.Type != GroupOut {
		// 仅监控（以及没有节点的链式出口）：只收探针数据
		c.JSON(200, resp)
		return
	}

	groups := map[uint]*DeviceGroup{g.ID: &g}
	group := func(id uint) *DeviceGroup {
		if x, ok := groups[id]; ok {
			return x
		}
		x := loadGroup(id)
		groups[id] = x
		return x
	}
	myCfg := g.cfg() // 本节点所在设备组的高级配置
	// outFor 返回连接某个出口组所需的信息；withFailover 时带上它的故障转移组
	outs := map[[2]uint]*syncOut{}
	outFor := func(og *DeviceGroup, withFailover bool) *syncOut {
		key := [2]uint{og.ID, 0}
		if withFailover {
			key[1] = 1
		}
		o, ok := outs[key]
		if !ok {
			v6 := g.Type == GroupIn && prefersV6(myCfg.IPv6Group, og.ID)
			o = outInfo(og, v6)
			if fb := group(og.FailoverGroupID); withFailover && fb != nil {
				o.Fallback = outInfo(fb, g.Type == GroupIn && prefersV6(myCfg.IPv6Group, fb.ID))
				o.Fallback.MaxFail, o.Fallback.FailTimeout = myCfg.MaxFail, myCfg.FailTimeoutSec
			}
			o.MaxFail, o.FailTimeout = myCfg.MaxFail, myCfg.FailTimeoutSec
			// 本入口节点开了反向端口，且入口组配置了这个出口组走反向隧道
			if g.Type == GroupIn && node.RevPort > 0 && wantsReverse(myCfg.ReverseGroup, og.ID) {
				o.Reverse, o.GroupID = true, og.ID
			}
			outs[key] = o
		}
		return o
	}
	var filter *syncFilter
	if len(myCfg.BlockedProtocol)+len(myCfg.BlockedHost)+len(myCfg.AllowedHost)+len(myCfg.BlockedPath) > 0 {
		filter = &syncFilter{myCfg.BlockedProtocol, myCfg.BlockedHost, myCfg.AllowedHost, myCfg.BlockedPath}
	}

	var rules []Rule
	if g.Type == GroupIn {
		for id, s := range req.Rules {
			setRuleStatus(id, node.ID, s.OK, s.Msg)
		}
		db.Where("in_group_id = ? AND paused = ?", g.ID, false).Find(&rules)
	} else {
		resp.TunnelPort, resp.TunnelKey, resp.TunnelProto = g.TunnelPort, g.TunnelKey, tunnelProto(&g)
		resp.GroupID, resp.Reverse = g.ID, reverseTargets(g.ID)
		db.Where("out_group_id <> 0 AND paused = ?", false).Find(&rules)
	}

	users := map[uint]*User{}
	for _, r := range rules {
		u, ok := users[r.UserID]
		if !ok {
			u = &User{}
			if db.First(u, r.UserID).Error != nil {
				u = nil
			}
			users[r.UserID] = u
		}
		if u == nil || !u.usable() {
			continue
		}
		in := group(r.InGroupID)
		var out *DeviceGroup
		var path []*DeviceGroup
		chain := false
		if r.OutGroupID != 0 {
			out = group(r.OutGroupID)
			if path = outPath(out, group); path == nil {
				continue // 出口组已不存在，或链式出口没配置完整
			}
			chain = out.Type == GroupChain
			// 出口节点只拿经过自己的规则
			if g.Type == GroupOut && !pathServedBy(path, g.ID, !chain) {
				continue
			}
		}
		if in == nil || checkRulePerm(u, in, out) != nil {
			continue
		}
		sr := syncRule{ID: r.ID, Targets: strings.Split(r.Targets, "\n")}
		if g.Type == GroupIn {
			sr.ListenPort, sr.LB, sr.Proto = r.ListenPort, r.LB, "both"
			if in.BlockUDP {
				sr.Proto = "tcp"
			}
			sr.RuleIPs, sr.ConnLimit, sr.AcceptPP, sr.SendPP = r.IPLimit, r.ConnLimit, r.AcceptPP, r.SendPP
			sr.UserID, sr.UserSpeed, sr.RuleSpeed, sr.IPLimit = u.ID, u.SpeedLimit, r.SpeedLimit, u.IPLimit
			sr.UserConns = u.ConnLimit
			sr.Filter = filter
			if r.TLS != nil {
				sr.TLS = &syncTLS{ruleTLS: *r.TLS, SNIs: r.TLS.sniList(), RejectEmptySNI: myCfg.TLSRejectEmptySNI}
			}
			if out != nil {
				sr.Out = outFor(path[0], !chain)
			}
		} else {
			// 链式出口：本组不是最后一跳时，把流量转交给下一跳
			for i, p := range path {
				if p.ID == g.ID && i+1 < len(path) {
					sr.Next = outFor(path[i+1], false)
				}
			}
		}
		resp.Rules = append(resp.Rules, sr)
	}
	c.JSON(200, resp)
}

// tunnelProto 把界面上的协议名映射成节点端实际使用的传输方式。
// tls_simple -> tls；ws 和 http 都走 WebSocket 传输（节点端没有单独实现 http 伪装）。
func tunnelProto(g *DeviceGroup) string {
	if g.TunnelProto == "ws" || g.TunnelProto == "http" {
		return "ws"
	}
	return "tls"
}

func wantsReverse(list []uint, outID uint) bool {
	return containsID(list, 0) || containsID(list, outID)
}

// reverseTargets 返回出口组 outID 需要主动连过去的入口反向地址：
// 所有把它列进 reverse_group 的入口组里，开了反向端口的在线节点。
func reverseTargets(outID uint) []string {
	var ins []DeviceGroup
	db.Where("type = ?", GroupIn).Find(&ins)
	out := []string{}
	for i := range ins {
		if !wantsReverse(ins[i].cfg().ReverseGroup, outID) {
			continue
		}
		var nodes []Node
		db.Where("group_id = ? AND rev_port > 0", ins[i].ID).Find(&nodes)
		for j := range nodes {
			if nodes[j].online() {
				out = append(out, net.JoinHostPort(nodes[j].host(), strconv.Itoa(nodes[j].RevPort)))
			}
		}
	}
	return out
}

// prefersV6 解读入口组的 ipv6_group：列表里有 0 表示"全部出口优先 IPv6，其余列出的例外"，
// 否则表示"只有列出的出口优先 IPv6"。
func prefersV6(list []uint, outID uint) bool {
	if !containsID(list, 0) {
		return containsID(list, outID)
	}
	for _, id := range list {
		if id != 0 && id == outID {
			return false
		}
	}
	return true
}

// connectHost 决定入口连接某个出口节点时用哪个地址，优先级从高到低：
//  1. 节点自己指定的连接地址（-advertise）
//  2. 出口组 connect_type = dyn_ip4：IPv4
//  3. 入口组的 ipv6_group 选中了这个出口组：IPv6，节点没有 IPv6 时回落到 IPv4
//  4. 出口组 connect_type = dyn_ip6：IPv6，节点没有 IPv6 时不使用这个节点（不回落）
//  5. 默认：面板看到的节点地址
//
// 返回空串表示这个节点不可用。
func connectHost(n *Node, connectType string, preferV6 bool) string {
	switch {
	case n.Advertise != "":
		return n.Advertise
	case connectType == "dyn_ip4":
		return n.IP
	case preferV6 && n.IP6 != "":
		return n.IP6
	case connectType == "dyn_ip6":
		return n.IP6
	}
	return n.IP
}

// outInfo 返回入口连接某个出口组所需的信息：协议、密钥，以及每个在线节点的地址和权重。
// 出口组设置了 static 连接地址时，所有入口都固定连那个地址。preferV6 来自入口组的 ipv6_group。
func outInfo(g *DeviceGroup, preferV6 bool) *syncOut {
	cfg := g.cfg()
	o := &syncOut{Proto: tunnelProto(g), Key: g.TunnelKey, Addrs: []string{}, Mux: cfg.Mux == nil || *cfg.Mux}
	if cfg.WS != nil {
		o.WSHost, o.WSPath = cfg.WS.Host, cfg.WS.Path
	}
	if cfg.ConnectType == "static" {
		port := cfg.ConnectPort
		if port == 0 {
			port = g.TunnelPort
		}
		o.Addrs = append(o.Addrs, net.JoinHostPort(cfg.ConnectAddress, strconv.Itoa(port)))
		return o
	}
	var nodes []Node
	db.Where("group_id = ?", g.ID).Order("id").Find(&nodes)
	for i := range nodes {
		n := &nodes[i]
		if !n.online() {
			continue
		}
		host := connectHost(n, cfg.ConnectType, preferV6)
		if host == "" {
			continue
		}
		port, weight := g.TunnelPort, 1
		if n.TunnelPort > 0 {
			port = n.TunnelPort
		}
		if n.Weight > 0 {
			weight = n.Weight
		}
		o.Addrs = append(o.Addrs, net.JoinHostPort(host, strconv.Itoa(port)))
		o.Weights = append(o.Weights, weight)
	}
	return o
}

// recordTraffic 记账：
//   - 规则流量、每日统计只取入口上报的实际流量
//   - 用户已用流量 = 入口实际流量*入口倍率 + 出口实际流量*出口倍率
func recordTraffic(g *DeviceGroup, traffic map[uint]int64) {
	today := time.Now().Format("2006-01-02")
	var total int64
	for id, d := range traffic {
		if d <= 0 {
			continue
		}
		var r Rule
		if db.First(&r, id).Error != nil {
			continue
		}
		if g.Type == GroupIn && r.InGroupID != g.ID {
			continue
		}
		ratio := g.Ratio
		if g.Type != GroupIn {
			// 出口侧：必须是这条规则链路上的一跳，或（非链式时）某一跳的故障转移组
			out := loadGroup(r.OutGroupID)
			path := outPath(out, loadGroup)
			chain := out != nil && out.Type == GroupChain
			if g.Type != GroupOut || !pathServedBy(path, g.ID, !chain) {
				continue
			}
			if chain {
				// 链式出口按链式出口本身的倍率计费一次（取第一跳的上报），忽略各跳自己的倍率
				ratio = 0
				if path[0].ID == g.ID {
					ratio = out.Ratio
				}
			}
		}
		total += d
		db.Model(&User{}).Where("id = ?", r.UserID).
			Update("traffic_used", gorm.Expr("traffic_used + ?", int64(float64(d)*ratio)))
		if g.Type == GroupIn {
			db.Model(&Rule{}).Where("id = ?", r.ID).
				UpdateColumn("traffic_used", gorm.Expr("traffic_used + ?", d))
			db.Clauses(clause.OnConflict{
				Columns:   []clause.Column{{Name: "date"}, {Name: "user_id"}},
				DoUpdates: clause.Assignments(map[string]any{"bytes": gorm.Expr("traffic_dailies.bytes + ?", d)}),
			}).Create(&TrafficDaily{Date: today, UserID: r.UserID, Bytes: d})
			db.Clauses(clause.OnConflict{
				Columns:   []clause.Column{{Name: "date"}, {Name: "group_id"}},
				DoUpdates: clause.Assignments(map[string]any{"bytes": gorm.Expr("group_traffic_dailies.bytes + ?", d)}),
			}).Create(&GroupTrafficDaily{Date: today, GroupID: g.ID, Bytes: d})
		}
	}
	if total > 0 {
		db.Model(&DeviceGroup{}).Where("id = ?", g.ID).
			Update("traffic_used", gorm.Expr("traffic_used + ?", total))
	}
}
