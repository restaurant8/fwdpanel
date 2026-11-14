package main

import (
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

var lbModes = map[string]bool{"roundrobin": true, "random": true, "failover": true}

// 规则同步状态只保存在内存里：规则 ID -> 节点 ID -> 该节点最近一次上报。
type nodeRuleStatus struct {
	OK  bool
	Msg string
	At  time.Time
}

var (
	statusMu   sync.Mutex
	ruleStatus = map[uint]map[uint]nodeRuleStatus{}
)

func setRuleStatus(ruleID, nodeID uint, ok bool, msg string) {
	statusMu.Lock()
	defer statusMu.Unlock()
	m := ruleStatus[ruleID]
	if m == nil {
		m = map[uint]nodeRuleStatus{}
		ruleStatus[ruleID] = m
	}
	m[nodeID] = nodeRuleStatus{ok, msg, time.Now()}
}

func clearRuleStatus(ruleID uint) {
	statusMu.Lock()
	delete(ruleStatus, ruleID)
	statusMu.Unlock()
}

// getRuleStatus 汇总各入口节点的上报：unsynced / ok / failed。
func getRuleStatus(ruleID uint) (string, string) {
	statusMu.Lock()
	defer statusMu.Unlock()
	status, msg := "unsynced", ""
	for _, s := range ruleStatus[ruleID] {
		if time.Since(s.At) > nodeOnlineWindow {
			continue
		}
		if !s.OK {
			return "failed", s.Msg
		}
		status = "ok"
	}
	return status, msg
}

type ruleDTO struct {
	Rule
	Username  string `json:"username"`
	Status    string `json:"status"`
	StatusMsg string `json:"status_msg"`
}

type ruleReq struct {
	Name       string   `json:"name"`
	InGroupID  uint     `json:"in_group_id"`
	OutGroupID uint     `json:"out_group_id"`
	ListenPort int      `json:"listen_port"`
	Targets    string   `json:"targets"`
	LB         string   `json:"lb"`
	Paused     bool     `json:"paused"`
	AcceptPP   bool     `json:"accept_pp"`
	SendPP     int      `json:"send_pp"`
	IPLimit    int      `json:"ip_limit"`
	ConnLimit  int      `json:"conn_limit"`
	SpeedLimit int64    `json:"speed_limit"`
	GroupID    uint     `json:"group_id"`
	TLS        *ruleTLS `json:"tls"`
}

func normalizeTargets(s string) (string, error) {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		host, port, err := net.SplitHostPort(line)
		if err != nil || host == "" {
			return "", fmt.Errorf("目标地址格式错误: %s（应为 host:port）", line)
		}
		if p, err := strconv.Atoi(port); err != nil || p < 1 || p > 65535 {
			return "", fmt.Errorf("目标端口错误: %s", line)
		}
		out = append(out, line)
	}
	if len(out) == 0 {
		return "", fmt.Errorf("至少需要一个目标地址")
	}
	if len(out) > 16 {
		return "", fmt.Errorf("目标地址最多 16 个")
	}
	return strings.Join(out, "\n"), nil
}

func portTaken(inGroup uint, port int, exceptID uint) bool {
	var n int64
	db.Model(&Rule{}).Where("in_group_id = ? AND listen_port = ? AND id <> ?", inGroup, port, exceptID).Count(&n)
	return n > 0
}

func randomPort(in *DeviceGroup) (int, error) {
	spans, _ := parsePortRange(in.PortRange)
	for i := 0; i < 200 && len(spans) > 0; i++ {
		sp := spans[rand.IntN(len(spans))]
		p := sp.lo + rand.IntN(sp.hi-sp.lo+1)
		if !portTaken(in.ID, p, 0) {
			return p, nil
		}
	}
	return 0, fmt.Errorf("无法在入口的端口范围内分配空闲端口")
}

// applyRuleReq 校验请求并写入 rule（不保存）。
func applyRuleReq(r *Rule, req *ruleReq) error {
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" || len(req.Name) > 64 {
		return fmt.Errorf("规则名称需为 1-64 个字符")
	}
	var in DeviceGroup
	if db.First(&in, "id = ? AND type = ?", req.InGroupID, GroupIn).Error != nil {
		return fmt.Errorf("入口设备组不存在")
	}
	var owner User
	if db.First(&owner, r.UserID).Error != nil {
		return fmt.Errorf("规则所属用户不存在")
	}
	var out *DeviceGroup
	if req.OutGroupID != 0 {
		out = &DeviceGroup{}
		if db.First(out, "id = ? AND type IN ?", req.OutGroupID, []string{GroupOut, GroupChain}).Error != nil {
			return fmt.Errorf("出口设备组不存在")
		}
		if outPath(out, loadGroup) == nil {
			return fmt.Errorf("链式出口「%s」的下一跳没有配置完整", out.Name)
		}
	}
	if err := checkRulePerm(&owner, &in, out); err != nil {
		return err
	}
	if req.SpeedLimit < 0 || req.IPLimit < 0 || req.ConnLimit < 0 {
		return fmt.Errorf("限制不能为负数")
	}
	if err := checkRuleTLS(r, req.TLS, &in, &owner); err != nil {
		return err
	}
	if req.SendPP < 0 || req.SendPP > 2 {
		return fmt.Errorf("发送 Proxy Protocol 需为 0（关闭）、1 或 2")
	}
	if req.GroupID != 0 && db.First(&RuleGroup{}, "id = ? AND user_id = ?", req.GroupID, r.UserID).Error != nil {
		return fmt.Errorf("规则分组不存在")
	}
	targets, err := normalizeTargets(req.Targets)
	if err != nil {
		return err
	}
	if req.LB == "" {
		req.LB = "random"
	}
	if !lbModes[req.LB] {
		return fmt.Errorf("不支持的负载均衡策略")
	}
	port := req.ListenPort
	if port == 0 {
		// 更新时保持原端口（同组内），新建或换组时随机分配
		if r.ID != 0 && r.InGroupID == req.InGroupID {
			port = r.ListenPort
		} else if port, err = randomPort(&in); err != nil {
			return err
		}
	}
	if port < 1 || port > 65535 {
		return fmt.Errorf("监听端口需在 1-65535 之间")
	}
	if spans, _ := parsePortRange(in.PortRange); !owner.Admin && !portInRange(spans, port) {
		pr := in.PortRange
		if pr == "" {
			pr = defaultPortRange
		}
		return fmt.Errorf("端口 %d 不在入口允许的范围内（%s）", port, pr)
	}
	if portTaken(req.InGroupID, port, r.ID) {
		return fmt.Errorf("端口 %d 在该入口组已被占用", port)
	}
	r.Name, r.InGroupID, r.OutGroupID = req.Name, req.InGroupID, req.OutGroupID
	r.ListenPort, r.Targets, r.LB, r.Paused = port, targets, req.LB, req.Paused
	r.SpeedLimit, r.GroupID = req.SpeedLimit, req.GroupID
	r.AcceptPP, r.SendPP, r.IPLimit, r.ConnLimit = req.AcceptPP, req.SendPP, req.IPLimit, req.ConnLimit
	r.TLS, r.TLSConfig = nil, ""
	if req.TLS != nil && req.TLS.Mode != tlsOff {
		raw, _ := json.Marshal(req.TLS)
		r.TLS, r.TLSConfig = req.TLS, string(raw)
	}
	return nil
}

func checkQuota(u *User, adding int) error {
	if u.MaxRules <= 0 {
		return nil
	}
	var n int64
	db.Model(&Rule{}).Where("user_id = ?", u.ID).Count(&n)
	if int(n)+adding > u.MaxRules {
		return fmt.Errorf("规则数量超出限制（最多 %d 条）", u.MaxRules)
	}
	return nil
}

func handleListRules(c *gin.Context) {
	u := me(c)
	q := db.Order("id desc")
	if !(u.Admin && c.Query("all") == "1") {
		q = q.Where("user_id = ?", u.ID)
	}
	var rules []Rule
	q.Find(&rules)
	names := map[uint]string{}
	var users []User
	db.Select("id", "username").Find(&users)
	for _, x := range users {
		names[x.ID] = x.Username
	}
	out := make([]ruleDTO, 0, len(rules))
	for _, r := range rules {
		st, msg := getRuleStatus(r.ID)
		if r.Paused {
			st, msg = "paused", ""
		}
		out = append(out, ruleDTO{r, names[r.UserID], st, msg})
	}
	c.JSON(200, out)
}

func handleCreateRule(c *gin.Context) {
	var req ruleReq
	if c.ShouldBindJSON(&req) != nil {
		fail(c, 400, "请求格式错误")
		return
	}
	u := me(c)
	if err := checkQuota(u, 1); err != nil {
		fail(c, 400, err.Error())
		return
	}
	r := Rule{UserID: u.ID}
	if err := applyRuleReq(&r, &req); err != nil {
		fail(c, 400, err.Error())
		return
	}
	db.Create(&r)
	nudgeAll()
	c.JSON(200, r)
}

// loadOwnRule 读取规则并检查归属（管理员可操作所有规则）。
func loadOwnRule(c *gin.Context) *Rule {
	var r Rule
	if db.First(&r, c.Param("id")).Error != nil {
		fail(c, 404, "规则不存在")
		return nil
	}
	if u := me(c); r.UserID != u.ID && !u.Admin {
		fail(c, 404, "规则不存在")
		return nil
	}
	return &r
}

func handleUpdateRule(c *gin.Context) {
	r := loadOwnRule(c)
	if r == nil {
		return
	}
	var req ruleReq
	if c.ShouldBindJSON(&req) != nil {
		fail(c, 400, "请求格式错误")
		return
	}
	if err := applyRuleReq(r, &req); err != nil {
		fail(c, 400, err.Error())
		return
	}
	db.Save(r)
	clearRuleStatus(r.ID)
	nudgeAll()
	c.JSON(200, r)
}

func handleDeleteRule(c *gin.Context) {
	r := loadOwnRule(c)
	if r == nil {
		return
	}
	db.Delete(r)
	clearRuleStatus(r.ID)
	nudgeAll()
	c.JSON(200, gin.H{"ok": true})
}

// handleBatchRules 批量导入，一行一个：名称#监听端口#目标地址#目标端口
// 监听端口留空则随机分配。
func handleBatchRules(c *gin.Context) {
	var req struct {
		InGroupID  uint   `json:"in_group_id"`
		OutGroupID uint   `json:"out_group_id"`
		Lines      string `json:"lines"`
		// Rules 是"批量导出"生成的 JSON。条目自带入口/出口时用自带的，否则用上面选的
		Rules []ruleReq `json:"rules"`
	}
	if c.ShouldBindJSON(&req) != nil {
		fail(c, 400, "请求格式错误")
		return
	}
	u := me(c)
	created := 0
	errs := []string{}
	for i, rr := range req.Rules {
		if err := checkQuota(u, 1); err != nil {
			errs = append(errs, err.Error())
			break
		}
		if rr.InGroupID == 0 {
			rr.InGroupID, rr.OutGroupID = req.InGroupID, req.OutGroupID
		}
		rr.GroupID = 0 // 分组是导出者自己的，不跟着走
		r := Rule{UserID: u.ID}
		if err := applyRuleReq(&r, &rr); err != nil {
			errs = append(errs, fmt.Sprintf("第 %d 条「%s」: %s", i+1, rr.Name, err.Error()))
			continue
		}
		db.Create(&r)
		created++
	}
	for i, line := range strings.Split(req.Lines, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		bad := func(msg string) { errs = append(errs, fmt.Sprintf("第 %d 行: %s", i+1, msg)) }
		parts := strings.Split(line, "#")
		if len(parts) != 4 {
			bad("格式应为 名称#监听端口#目标地址#目标端口")
			continue
		}
		port := 0
		if parts[1] != "" {
			var err error
			if port, err = strconv.Atoi(parts[1]); err != nil || port < 1 {
				bad("监听端口错误")
				continue
			}
		}
		if err := checkQuota(u, 1); err != nil {
			bad(err.Error())
			break
		}
		r := Rule{UserID: u.ID}
		rr := ruleReq{Name: parts[0], InGroupID: req.InGroupID, OutGroupID: req.OutGroupID,
			ListenPort: port, Targets: net.JoinHostPort(strings.Trim(parts[2], "[]"), parts[3])}
		if err := applyRuleReq(&r, &rr); err != nil {
			bad(err.Error())
			continue
		}
		db.Create(&r)
		created++
	}
	nudgeAll()
	c.JSON(200, gin.H{"created": created, "errors": errs})
}

// handleBatchAction 对选中的多条规则执行同一个操作。
//
//	pause / resume        暂停 / 恢复
//	delete                删除
//	reset_traffic         清空规则的已用流量统计（不退还用户已扣的流量）
//	set_in / set_out      切换入口 / 出口设备组（value 为设备组 ID，set_out 的 0 表示单端）
//	set_group             移动到规则分组（value 为分组 ID，0 表示未分组）
func handleBatchAction(c *gin.Context) {
	var req struct {
		IDs    []uint `json:"ids"`
		Action string `json:"action"`
		Value  uint   `json:"value"`
	}
	if c.ShouldBindJSON(&req) != nil || len(req.IDs) == 0 || len(req.IDs) > 1000 {
		fail(c, 400, "请求格式错误")
		return
	}
	u := me(c)
	done := 0
	errs := []string{}
	for _, id := range req.IDs {
		var r Rule
		if db.First(&r, id).Error != nil || (r.UserID != u.ID && !u.Admin) {
			errs = append(errs, fmt.Sprintf("规则 #%d 不存在", id))
			continue
		}
		rr := ruleReq{Name: r.Name, InGroupID: r.InGroupID, OutGroupID: r.OutGroupID, ListenPort: r.ListenPort,
			Targets: r.Targets, LB: r.LB, Paused: r.Paused, SpeedLimit: r.SpeedLimit, GroupID: r.GroupID,
			AcceptPP: r.AcceptPP, SendPP: r.SendPP, IPLimit: r.IPLimit, ConnLimit: r.ConnLimit, TLS: r.TLS}
		switch req.Action {
		case "delete":
			db.Delete(&r)
			clearRuleStatus(r.ID)
			done++
			continue
		case "reset_traffic":
			db.Model(&r).UpdateColumn("traffic_used", 0)
			done++
			continue
		case "pause":
			rr.Paused = true
		case "resume":
			rr.Paused = false
		case "set_in":
			rr.InGroupID = req.Value
		case "set_out":
			rr.OutGroupID = req.Value
		case "set_group":
			rr.GroupID = req.Value
		default:
			fail(c, 400, "不支持的操作")
			return
		}
		if err := applyRuleReq(&r, &rr); err != nil {
			errs = append(errs, fmt.Sprintf("规则「%s」: %s", r.Name, err.Error()))
			continue
		}
		db.Save(&r)
		clearRuleStatus(r.ID)
		done++
	}
	nudgeAll()
	c.JSON(200, gin.H{"done": done, "errors": errs})
}

// ---------- 规则分组 ----------

func handleListRuleGroups(c *gin.Context) {
	groups := []RuleGroup{}
	db.Where("user_id = ?", me(c).ID).Order("id").Find(&groups)
	c.JSON(200, groups)
}

// handleSaveRuleGroup 同时处理新建 (POST) 和改名 (PUT)。
func handleSaveRuleGroup(c *gin.Context) {
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
	g := RuleGroup{UserID: me(c).ID}
	if id := c.Param("id"); id != "" && db.First(&g, "id = ? AND user_id = ?", id, g.UserID).Error != nil {
		fail(c, 404, "分组不存在")
		return
	}
	g.Name = req.Name
	db.Save(&g)
	c.JSON(200, g)
}

func handleDeleteRuleGroup(c *gin.Context) {
	var g RuleGroup
	if db.First(&g, "id = ? AND user_id = ?", c.Param("id"), me(c).ID).Error != nil {
		fail(c, 404, "分组不存在")
		return
	}
	// 组内规则回到未分组
	db.Model(&Rule{}).Where("group_id = ?", g.ID).Update("group_id", 0)
	db.Delete(&g)
	c.JSON(200, gin.H{"ok": true})
}
