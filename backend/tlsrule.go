package main

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"strings"

	"gorm.io/gorm"
)

// TLS 入站：由入口终结 TLS，再把里面的明文流量按规则转发。
//
//	0 未开启
//	1 独立规则：这条规则自己的端口监听 TLS
//	2 母规则：监听 TLS，自己不转发，按客户端的 SNI 把连接分给引用它的子规则
//	3 子规则：不监听端口，引用同一入口上某个母规则的端口，用自己的 SNI 区分
const (
	tlsOff = iota
	tlsStandalone
	tlsParent
	tlsChild
)

type ruleTLS struct {
	Mode int `json:"mode"`
	// 独立规则 / 子规则：只接受带有这些 SNI 的连接，多个用逗号分隔
	SNI string `json:"sni,omitempty"`
	// 独立规则：不校验 SNI；force_empty_sni 再加上"拒绝带 SNI 的连接"
	EmptySNI      bool `json:"empty_sni,omitempty"`
	ForceEmptySNI bool `json:"force_empty_sni,omitempty"`
	// ALPN 不为空时，只接受支持其中之一的客户端
	ALPN []string `json:"alpn,omitempty"`
	// 证书和私钥（PEM）。留空则使用节点自动生成的自签证书
	Cert string `json:"cert,omitempty"`
	Key  string `json:"key,omitempty"`
	// 子规则：母规则的端口
	SharePort int `json:"share_port,omitempty"`
	// 母规则：允许哪些用户的子规则引用，逗号分隔的用户 ID，0 或留空表示所有用户
	UID string `json:"uid,omitempty"`
}

func (t *ruleTLS) sniList() []string {
	var out []string
	for _, s := range strings.Split(t.SNI, ",") {
		if s = strings.ToLower(strings.TrimSpace(s)); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func (r *Rule) AfterFind(*gorm.DB) error {
	r.TLS = nil
	if r.TLSConfig != "" {
		var t ruleTLS
		if json.Unmarshal([]byte(r.TLSConfig), &t) == nil && t.Mode != tlsOff {
			r.TLS = &t
		}
	}
	return nil
}

func (r *Rule) tlsMode() int {
	if r.TLS == nil {
		return tlsOff
	}
	return r.TLS.Mode
}

// checkRuleTLS 校验规则的 TLS 入站设置。in 是入口组，owner 是规则的主人。
func checkRuleTLS(r *Rule, t *ruleTLS, in *DeviceGroup, owner *User) error {
	policy := in.cfg().TLSInboundPolicy
	mode := tlsOff
	if t != nil {
		mode = t.Mode
	}
	if policy >= 1 && mode == tlsOff {
		return fmt.Errorf("入口「%s」只允许 TLS 入站规则", in.Name)
	}
	if policy == 2 && (mode == tlsStandalone || mode == tlsParent) && !owner.Admin {
		return fmt.Errorf("入口「%s」只允许管理员监听 TLS 端口，请使用子规则", in.Name)
	}
	switch mode {
	case tlsOff:
		return nil
	case tlsStandalone:
		if !t.EmptySNI && len(t.sniList()) == 0 {
			return fmt.Errorf("TLS 入站: 请填写 SNI，或开启「不校验 SNI」")
		}
		if t.ForceEmptySNI && !t.EmptySNI {
			return fmt.Errorf("TLS 入站: 「拒绝带 SNI 的连接」需要同时开启「不校验 SNI」")
		}
	case tlsParent:
		if !owner.Admin {
			return fmt.Errorf("TLS 入站: 只有管理员可以创建母规则")
		}
	case tlsChild:
		snis := t.sniList()
		if len(snis) == 0 {
			return fmt.Errorf("TLS 入站: 子规则需要填写 SNI")
		}
		// 母规则必须在同一个入口组，并且允许这个用户引用
		var parents []Rule
		db.Where("in_group_id = ? AND listen_port = ?", in.ID, t.SharePort).Find(&parents)
		var parent *Rule
		for i := range parents {
			if parents[i].tlsMode() == tlsParent {
				parent = &parents[i]
			}
		}
		if parent == nil {
			return fmt.Errorf("TLS 入站: 入口「%s」上没有端口为 %d 的母规则", in.Name, t.SharePort)
		}
		if ids := parseIDs(parent.TLS.UID); len(ids) > 0 && !containsID(ids, owner.ID) && !owner.Admin {
			return fmt.Errorf("TLS 入站: 你没有被允许引用端口 %d 的母规则", t.SharePort)
		}
		// 入口设置了域名白名单时，SNI 必须在白名单内
		if allow := in.cfg().AllowedHost; len(allow) > 0 {
			for _, s := range snis {
				ok := false
				for _, a := range allow {
					ok = ok || strings.HasSuffix(s, strings.ToLower(a))
				}
				if !ok {
					return fmt.Errorf("TLS 入站: SNI %s 不在入口允许的域名范围内", s)
				}
			}
		}
		// 同一个母规则下 SNI 不能重复
		var siblings []Rule
		db.Where("in_group_id = ? AND id <> ?", in.ID, r.ID).Find(&siblings)
		for _, sib := range siblings {
			if sib.tlsMode() != tlsChild || sib.TLS.SharePort != t.SharePort {
				continue
			}
			for _, a := range sib.TLS.sniList() {
				for _, b := range snis {
					if a == b {
						return fmt.Errorf("TLS 入站: SNI %s 已被同一母规则下的其他规则使用", a)
					}
				}
			}
		}
	default:
		return fmt.Errorf("TLS 入站: 模式取值错误")
	}
	if (t.Cert == "") != (t.Key == "") {
		return fmt.Errorf("TLS 入站: 证书和私钥要么都填，要么都不填")
	}
	if t.Cert != "" {
		if _, err := tls.X509KeyPair([]byte(t.Cert), []byte(t.Key)); err != nil {
			return fmt.Errorf("TLS 入站: 证书或私钥无效: %v", err)
		}
	}
	return nil
}
