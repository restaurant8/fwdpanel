package main

import (
	"fmt"
	"strconv"
	"strings"
)

const noDirectWord = "禁止单端"

func parseIDs(s string) []uint {
	out := []uint{}
	for _, p := range strings.Split(s, ",") {
		if n, err := strconv.ParseUint(strings.TrimSpace(p), 10, 32); err == nil && n > 0 {
			out = append(out, uint(n))
		}
	}
	return out
}

func joinIDs(ids []uint) string {
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = strconv.FormatUint(uint64(id), 10)
	}
	return strings.Join(parts, ",")
}

// groupAllowsUser 判断用户能否使用某个设备组。管理员不受限制。
func groupAllowsUser(g *DeviceGroup, u *User) bool {
	if g.OwnerID != 0 {
		return g.OwnerID == u.ID // 用户自带的出口只归本人使用，管理员也不能拿来建规则
	}
	if u.Admin {
		return true
	}
	ids := parseIDs(g.AllowGroups)
	if len(ids) == 0 {
		return true
	}
	for _, id := range ids {
		if id == u.GroupID {
			return true
		}
	}
	return false
}

// outLimit 是入口组"限制出口"设置的解析结果。写法：
//
//	禁止单端            禁止单端转发，出口不限
//	1,2,3              只允许使用 1、2、3 号出口，单端仍可用
//	1,2,3,禁止单端      只允许使用 1、2、3 号出口，且禁止单端
//	一个不存在的 ID     相当于只允许单端
type outLimit struct {
	NoDirect bool
	IDs      []uint
}

func parseOutLimit(s string) (outLimit, error) {
	var o outLimit
	for _, p := range strings.Split(strings.ReplaceAll(s, "，", ","), ",") {
		p = strings.TrimSpace(p)
		switch {
		case p == "":
		case p == noDirectWord:
			o.NoDirect = true
		default:
			n, err := strconv.ParseUint(p, 10, 32)
			if err != nil || n == 0 {
				return o, fmt.Errorf("限制出口写法错误: %q（应为出口组 ID 或"+noDirectWord+"，逗号分隔）", p)
			}
			o.IDs = append(o.IDs, uint(n))
		}
	}
	return o, nil
}

func (o outLimit) allows(outID uint) bool {
	if outID == 0 {
		return !o.NoDirect
	}
	if len(o.IDs) == 0 {
		return true
	}
	for _, id := range o.IDs {
		if id == outID {
			return true
		}
	}
	return false
}

// checkRulePerm 检查用户能否把规则建在这对入口/出口上（out 为 nil 表示单端）。
// 创建规则和每次向节点下发规则时都会检查，所以管理员收紧权限后旧规则也会停用。
func checkRulePerm(u *User, in, out *DeviceGroup) error {
	if !groupAllowsUser(in, u) {
		return fmt.Errorf("无权使用入口设备组「%s」", in.Name)
	}
	lim, _ := parseOutLimit(in.OutLimit)
	if out == nil {
		// 直接转发策略：0 禁止，1 可选，2 强制
		if in.DirectPolicy == 0 || !lim.allows(0) {
			return fmt.Errorf("入口「%s」禁止直接转发，请选择出口", in.Name)
		}
		return nil
	}
	if in.DirectPolicy == 2 {
		return fmt.Errorf("入口「%s」只允许直接转发，不能选择出口", in.Name)
	}
	if !lim.allows(out.ID) {
		return fmt.Errorf("入口「%s」不允许使用出口「%s」", in.Name, out.Name)
	}
	if ids := parseIDs(out.InLimit); len(ids) > 0 && !containsID(ids, in.ID) {
		return fmt.Errorf("出口「%s」不允许入口「%s」使用", out.Name, in.Name)
	}
	if !groupAllowsUser(out, u) {
		return fmt.Errorf("无权使用出口设备组「%s」", out.Name)
	}
	return nil
}

const maxChainHops = 3

// outPath 返回规则选的出口实际要经过的各个出口组（按顺序）。
// 普通出口就是它自己；链式出口是它配置的 1~3 跳，链式出口本身不在其中（它没有节点）。
// 配置不完整时返回 nil。load 用来按 ID 取设备组，取不到返回 nil。
func outPath(first *DeviceGroup, load func(uint) *DeviceGroup) []*DeviceGroup {
	if first == nil {
		return nil
	}
	if first.Type == GroupOut {
		return []*DeviceGroup{first}
	}
	ids := parseIDs(first.Hops)
	if first.Type != GroupChain || len(ids) == 0 || len(ids) > maxChainHops {
		return nil
	}
	path := make([]*DeviceGroup, 0, len(ids))
	for _, id := range ids {
		g := load(id)
		if g == nil || g.Type != GroupOut {
			return nil
		}
		path = append(path, g)
	}
	return path
}

func loadGroup(id uint) *DeviceGroup {
	g := &DeviceGroup{}
	if id == 0 || db.First(g, id).Error != nil {
		return nil
	}
	return g
}

// pathServedBy 判断设备组 id 是否承接这条链路的流量：是其中一跳，
// 或者（withFailover 时）是其中某一跳的故障转移组。链式出口不使用故障转移组。
func pathServedBy(path []*DeviceGroup, id uint, withFailover bool) bool {
	for _, p := range path {
		if p.ID == id || (withFailover && p.FailoverGroupID == id) {
			return true
		}
	}
	return false
}

type portSpan struct{ lo, hi int }

const defaultPortRange = "2000-65535"

// parsePortRange 解析 "2000-65535" 或 "443,10000-65535" 这样的端口范围，空串用默认范围。
func parsePortRange(s string) ([]portSpan, error) {
	if strings.TrimSpace(s) == "" {
		s = defaultPortRange
	}
	var out []portSpan
	for _, p := range strings.Split(strings.ReplaceAll(s, "，", ","), ",") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		lo, hi, isRange := strings.Cut(p, "-")
		a, err1 := strconv.Atoi(strings.TrimSpace(lo))
		b := a
		var err2 error
		if isRange {
			b, err2 = strconv.Atoi(strings.TrimSpace(hi))
		}
		if err1 != nil || err2 != nil || a < 1 || b > 65535 || a > b {
			return nil, fmt.Errorf("端口范围写法错误: %q（示例: 2000-65535 或 443,10000-65535）", p)
		}
		out = append(out, portSpan{a, b})
	}
	return out, nil
}

func portInRange(spans []portSpan, port int) bool {
	for _, s := range spans {
		if port >= s.lo && port <= s.hi {
			return true
		}
	}
	return false
}

func containsID(ids []uint, id uint) bool {
	for _, x := range ids {
		if x == id {
			return true
		}
	}
	return false
}
