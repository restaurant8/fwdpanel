package main

import (
	"time"

	"gorm.io/gorm"
)

// User 面板用户。流量单位均为字节，TrafficLimit 为 0 表示不限。
type User struct {
	ID           uint    `gorm:"primaryKey" json:"id"`
	Username     string  `gorm:"uniqueIndex;size:64" json:"username"`
	PasswordHash string  `json:"-"`
	Admin        bool    `json:"admin"`
	Enabled      bool    `json:"enabled"`
	TrafficLimit int64   `json:"traffic_limit"`
	TrafficUsed  int64   `json:"traffic_used"`
	MaxRules     int     `json:"max_rules"`
	GroupID      uint    `json:"group_id"`    // 用户组，0 表示未分组
	SpeedLimit   int64   `json:"speed_limit"` // 字节/秒，0 不限；每个入口节点、每个方向单独计算
	IPLimit      int     `json:"ip_limit"`    // 同时在线的来源 IP 数，0 不限；每个入口节点单独计算
	ConnLimit    int     `json:"conn_limit"`  // 并发连接数，0 不限；每个入口节点单独计算
	Balance      float64 `json:"balance"`     // 钱包余额（元）
	Note         string  `json:"note"`        // 备注，仅管理员可见
	AutoRenew    bool    `json:"auto_renew"`
	PlanID       uint    `json:"plan_id"` // 当前套餐，0 表示没有
	// 邀请：自己的邀请码、邀请人
	InviteCode string `gorm:"size:16;index" json:"-"`
	InviterID  uint   `json:"inviter_id"`
	// Telegram：绑定的聊天 ID（0 表示未绑定）、待使用的绑定码、是否关闭推送
	TGChatID   int64      `json:"tg_chat_id"`
	TGBindCode string     `gorm:"size:16" json:"-"`
	PushOff    bool       `json:"push_off"`
	ExpireAt   *time.Time `json:"expire_at"`
	CreatedAt  time.Time  `json:"created_at"`
}

// usable 判断用户的规则当前是否允许下发到节点。
func (u *User) usable() bool {
	if !u.Enabled {
		return false
	}
	if u.ExpireAt != nil && time.Now().After(*u.ExpireAt) {
		return false
	}
	if u.TrafficLimit > 0 && u.TrafficUsed >= u.TrafficLimit {
		return false
	}
	return true
}

const (
	GroupMonitor = "monitor" // 仅监控：节点只上报探针数据
	GroupIn      = "in"      // 入口：监听端口
	GroupOut     = "out"     // 出口：连接目标
	GroupChain   = "chain"   // 链式出口：把流量转交给下一跳出口组
)

func isOutType(t string) bool { return t == GroupOut || t == GroupChain }

// DeviceGroup 设备组。入口组负责监听端口，出口组负责连接目标。
type DeviceGroup struct {
	ID          uint    `gorm:"primaryKey" json:"id"`
	Name        string  `gorm:"size:64" json:"name"`
	Type        string  `gorm:"size:16" json:"type"`
	Token       string  `gorm:"uniqueIndex;size:64" json:"token"`
	Ratio       float64 `json:"ratio"`
	TunnelPort  int     `json:"tunnel_port"`
	TunnelKey   string  `json:"-"`
	TunnelProto string  `gorm:"size:16" json:"tunnel_proto"` // 出口组：tls_simple / ws / http
	// AllowGroups 是允许使用该设备组的用户组 ID（逗号分隔），空表示所有用户可用
	AllowGroups   string `json:"-"`
	AllowGroupIDs []uint `gorm:"-" json:"allow_groups"`
	// BlockUDP 仅入口组使用：只转发 TCP，不监听 UDP
	BlockUDP bool `json:"block_udp"`
	// OutLimit 仅入口组使用，见 parseOutLimit
	OutLimit string `json:"out_limit"`
	// FailoverGroupID 仅出口组使用：组内机器全部故障时改用这个出口组
	FailoverGroupID uint `json:"failover_group_id"`
	// Hops 仅链式出口使用：依次经过的出口组 ID（逗号分隔，1~3 个）。链式出口本身没有节点。
	Hops   string `json:"-"`
	HopIDs []uint `gorm:"-" json:"hops"`
	// InLimit 仅出口/链式出口使用：允许使用本出口的入口组 ID（逗号分隔），空表示不限
	InLimit string `json:"in_limit"`
	// 以下仅入口组使用
	ConnectAddr  string `json:"connect_addr"`   // 展示给用户的连接地址，纯展示
	PortRange    string `json:"port_range"`     // 允许的监听端口，如 2000-65535 或 443,10000-65535
	DirectPolicy int    `json:"direct_policy"`  // 直接转发：0 禁止，1 可选，2 强制
	UDPSmartBind bool   `json:"udp_smart_bind"` // UDP 源进源出（目前只保存）
	// HideInStatus 在节点状态页隐藏：0 不隐藏，1 对非管理员隐藏，2 对所有人隐藏
	HideInStatus int `json:"hide_in_status"`
	// CatID 是设备组所属的分组（只用于在管理页面里整理和筛选），0 表示未分组
	CatID uint `json:"cat_id"`
	// OwnerID 不为 0 表示这是用户自带的出口，只有这个用户自己能用
	OwnerID uint `json:"owner_id"`
	// Config 是「高级编辑」保存的 JSON，见 groupConfig
	Config string `json:"config"`
	// OfflineDrain 仅出口组：负载下线秒数（目前只保存）
	OfflineDrain int       `json:"offline_drain"`
	TrafficUsed  int64     `json:"traffic_used"`
	Note         string    `json:"note"`
	CreatedAt    time.Time `json:"created_at"`
}

func (g *DeviceGroup) AfterFind(*gorm.DB) error {
	g.AllowGroupIDs = parseIDs(g.AllowGroups)
	g.HopIDs = parseIDs(g.Hops)
	return nil
}

// UserGroup 用户组，用来控制哪些用户能用哪些设备组。
type UserGroup struct {
	ID   uint   `gorm:"primaryKey" json:"id"`
	Name string `gorm:"size:64" json:"name"`
}

// Node 对接到某个设备组的节点服务器。
type Node struct {
	ID        uint   `gorm:"primaryKey" json:"id"`
	GroupID   uint   `gorm:"uniqueIndex:idx_node_uid" json:"group_id"`
	UID       string `gorm:"uniqueIndex:idx_node_uid;size:64" json:"uid"`
	Name      string `json:"name"`
	IP        string `json:"ip"`
	Advertise string `json:"advertise"`
	Version   string `json:"version"`
	Conns     int    `json:"conns"`
	// 出口节点自己指定的隧道端口和权重，0 表示用设备组的端口 / 权重 1
	TunnelPort int       `json:"tunnel_port"`
	Weight     int       `json:"weight"`
	RevPort    int       `json:"rev_port"` // 入口节点的反向隧道监听端口，0 表示没有开启
	IP6        string    `json:"ip6"`      // 节点上报的公网 IPv6 地址，没有则为空
	LastSeen   time.Time `json:"last_seen"`
	CreatedAt  time.Time `json:"created_at"`
}

// nodeOnlineWindow 是节点多久没有同步就算离线，可用启动参数 -offline-node-time 调整。
var nodeOnlineWindow = 35 * time.Second

func (n *Node) online() bool { return time.Since(n.LastSeen) < nodeOnlineWindow }

// host 返回入口连接该出口节点时使用的地址。
func (n *Node) host() string {
	if n.Advertise != "" {
		return n.Advertise
	}
	return n.IP
}

// Rule 转发规则。OutGroupID 为 0 表示单端转发（入口直连目标）。
type Rule struct {
	ID         uint   `gorm:"primaryKey" json:"id"`
	UserID     uint   `gorm:"index" json:"user_id"`
	Name       string `gorm:"size:64" json:"name"`
	GroupID    uint   `json:"group_id"` // 规则分组（RuleGroup），0 表示未分组
	InGroupID  uint   `gorm:"index" json:"in_group_id"`
	OutGroupID uint   `gorm:"index" json:"out_group_id"`
	ListenPort int    `json:"listen_port"`
	Targets    string `json:"targets"` // 一行一个 host:port
	LB         string `gorm:"size:16" json:"lb"`
	AcceptPP   bool   `json:"accept_pp"`  // 入口先从客户端连接里读 Proxy Protocol 头
	SendPP     int    `json:"send_pp"`    // 向目标发送 Proxy Protocol：0 关闭，1 = v1，2 = v2
	IPLimit    int    `json:"ip_limit"`   // 本规则同时在线的来源 IP 数，0 不限
	ConnLimit  int    `json:"conn_limit"` // 本规则的并发连接数，0 不限
	// TLS 入站设置，JSON 存在 TLSConfig 里，读出时解析到 TLS
	TLSConfig   string    `json:"-"`
	TLS         *ruleTLS  `gorm:"-" json:"tls"`
	SpeedLimit  int64     `json:"speed_limit"` // 字节/秒，0 不限
	Paused      bool      `json:"paused"`
	TrafficUsed int64     `json:"traffic_used"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// RuleGroup 用户给自己的规则建的分组，只用于整理和筛选。
type RuleGroup struct {
	ID     uint   `gorm:"primaryKey" json:"id"`
	UserID uint   `gorm:"index" json:"user_id"`
	Name   string `gorm:"size:64" json:"name"`
}

// KV 保存不需要单独建表的设置：站点设置、支付通道、套餐、兑换码、推送设置等，值是 JSON。
type KV struct {
	Key   string `gorm:"primaryKey;size:64"`
	Value string
}

// GroupTrafficDaily 每个入口设备组每天的实际流量，用于仪表盘的节点流量排行。
type GroupTrafficDaily struct {
	Date    string `gorm:"primaryKey;size:10"`
	GroupID uint   `gorm:"primaryKey"`
	Bytes   int64
}

type Session struct {
	Token    string `gorm:"primaryKey;size:64"`
	UserID   uint   `gorm:"index"`
	ExpireAt time.Time
}

// TrafficDaily 每个用户每天的实际流量（由入口上报）。
type TrafficDaily struct {
	Date   string `gorm:"primaryKey;size:10" json:"date"`
	UserID uint   `gorm:"primaryKey" json:"user_id"`
	Bytes  int64  `json:"bytes"`
}
