package main

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// 商城：套餐、兑换码保存在 KV 里（由管理页面维护），这里实现购买、兑换、续费和订单。
// 套餐用钱包余额购买；余额通过在线充值（见 pay.go）或管理员「手动记账」增加。

const (
	planPack      = "不限时流量包"
	planPackStack = "不限时流量包（可叠加）"
	planMonthly   = "月付"
	planDaily     = "日付"
)

type plan struct {
	ID        uint    `json:"id"`
	Name      string  `json:"name"`
	Hidden    bool    `json:"hidden"`
	Price     float64 `json:"price"`
	Type      string  `json:"type"`
	Duration  int     `json:"duration"` // 月付/日付：一次购买的月数或天数
	GroupID   uint    `json:"group_id"`
	TrafficGB float64 `json:"traffic_gb"`
	MaxRules  int     `json:"max_rules"`
	Speed     float64 `json:"speed"` // Mbps
	IPLimit   int     `json:"ip_limit"`
	ConnLimit int     `json:"conn_limit"`
	Desc      string  `json:"desc"`
}

func (p *plan) periodic() bool { return p.Type == planMonthly || p.Type == planDaily }

// extend 返回从 from 起延长一个周期后的时间。
func (p *plan) extend(from time.Time) time.Time {
	n := max(p.Duration, 1)
	if p.Type == planDaily {
		return from.AddDate(0, 0, n)
	}
	return from.AddDate(0, n, 0)
}

type redeemCode struct {
	ID       uint    `json:"id"`
	Code     string  `json:"code"`
	PlanID   uint    `json:"plan_id"`
	Discount float64 `json:"discount"` // 折扣比例，100 表示免费
	Remain   int     `json:"remain"`
}

// Order 订单与账目记录。
type Order struct {
	ID        uint       `gorm:"primaryKey" json:"id"`
	No        string     `gorm:"uniqueIndex;size:32" json:"no"`
	UserID    uint       `gorm:"index" json:"user_id"`
	Info      string     `json:"info"`
	Amount    float64    `json:"amount"`                // 元。购买为正数（扣余额），手动记账正数为加余额
	Type      string     `gorm:"size:16" json:"type"`   // 购买套餐 / 续费 / 兑换 / 手动记账
	Status    string     `gorm:"size:16" json:"status"` // 已完成
	CreatedAt time.Time  `json:"created_at"`
	PaidAt    *time.Time `json:"paid_at"`
	// 以下只有充值订单使用
	Channel   string  `json:"channel"`    // 支付通道名称
	PayAmount float64 `json:"pay_amount"` // 用户实际要付的金额（含手续费）
	TradeNo   string  `json:"trade_no"`   // 网关的交易号
}

func gormExprAdd(col string, v float64) any { return gorm.Expr(col+" + ?", v) }

// afterRecharge 在充值到账后调用（邀请返利等在这里处理）。
func afterRecharge(o *Order) { inviteRebate(o) }

var shopMu sync.Mutex // 购买、兑换、记账都会改余额或兑换码次数，串行执行

func loadPlans() []plan {
	var ps []plan
	getKV("plans", &ps)
	return ps
}

func findPlan(id uint) *plan {
	for _, p := range loadPlans() {
		if p.ID == id {
			return &p
		}
	}
	return nil
}

func addOrder(userID uint, typ, info string, amount float64) {
	now := time.Now()
	db.Create(&Order{No: now.Format("20060102150405") + randHex(4), UserID: userID, Info: info, Amount: amount,
		Type: typ, Status: "已完成", PaidAt: &now})
}

// applyPlan 把套餐的内容生效到用户身上（不涉及扣款）。renew 为 true 表示续费同一个周期套餐。
func applyPlan(u *User, p *plan, renew bool) {
	traffic := int64(p.TrafficGB * (1 << 30))
	if p.Type == planPackStack && u.PlanID != 0 {
		// 可叠加的流量包：在现有基础上加流量，不动其他设置
		u.TrafficLimit += traffic
		return
	}
	u.PlanID, u.GroupID, u.MaxRules = p.ID, p.GroupID, p.MaxRules
	u.SpeedLimit, u.IPLimit, u.ConnLimit = int64(p.Speed*125000), p.IPLimit, p.ConnLimit
	u.TrafficLimit, u.TrafficUsed = traffic, 0
	switch {
	case !p.periodic():
		u.ExpireAt = nil
	case renew && u.ExpireAt != nil && u.ExpireAt.After(time.Now()):
		t := p.extend(*u.ExpireAt) // 没过期就续：在原到期时间上顺延
		u.ExpireAt = &t
	default:
		t := p.extend(time.Now())
		u.ExpireAt = &t
	}
}

// purchase 扣余额、应用套餐、记订单。price 是实际要付的金额。
func purchase(u *User, p *plan, price float64, typ, info string, renew bool) error {
	if u.Balance < price {
		return fmt.Errorf("余额不足：需要 %.2f 元，当前余额 %.2f 元", price, u.Balance)
	}
	u.Balance -= price
	applyPlan(u, p, renew)
	if err := db.Save(u).Error; err != nil {
		return err
	}
	addOrder(u.ID, typ, info, price)
	nudgeAll() // 用户组、限速、流量变了，让节点马上同步
	return nil
}

func freshUser(c *gin.Context) *User {
	var u User
	db.First(&u, me(c).ID)
	return &u
}

func handleBuyPlan(c *gin.Context) {
	var req struct {
		PlanID uint `json:"plan_id"`
	}
	if c.ShouldBindJSON(&req) != nil {
		fail(c, 400, "请求格式错误")
		return
	}
	shopMu.Lock()
	defer shopMu.Unlock()
	p := findPlan(req.PlanID)
	if p == nil || p.Hidden {
		fail(c, 404, "套餐不存在")
		return
	}
	u := freshUser(c)
	if err := purchase(u, p, p.Price, "购买套餐", p.Name, false); err != nil {
		fail(c, 400, err.Error())
		return
	}
	c.JSON(200, u)
}

func handleRenew(c *gin.Context) {
	shopMu.Lock()
	defer shopMu.Unlock()
	u := freshUser(c)
	p := findPlan(u.PlanID)
	if p == nil || !p.periodic() {
		fail(c, 400, "当前没有可以续费的套餐（只有月付/日付套餐可以续费）")
		return
	}
	if err := purchase(u, p, p.Price, "续费", p.Name, true); err != nil {
		fail(c, 400, err.Error())
		return
	}
	c.JSON(200, u)
}

func handleRedeem(c *gin.Context) {
	var req struct {
		Code string `json:"code"`
	}
	if c.ShouldBindJSON(&req) != nil || strings.TrimSpace(req.Code) == "" {
		fail(c, 400, "请输入兑换码")
		return
	}
	shopMu.Lock()
	defer shopMu.Unlock()
	var codes []redeemCode
	getKV("redeem", &codes)
	idx := -1
	for i := range codes {
		if codes[i].Code == strings.TrimSpace(req.Code) && codes[i].Remain > 0 {
			idx = i
		}
	}
	if idx < 0 {
		fail(c, 400, "兑换码无效或已用完")
		return
	}
	p := findPlan(codes[idx].PlanID)
	if p == nil {
		fail(c, 400, "这个兑换码对应的套餐已经不存在")
		return
	}
	price := p.Price * (1 - min(max(codes[idx].Discount, 0), 100)/100)
	u := freshUser(c)
	if err := purchase(u, p, price, "兑换", fmt.Sprintf("%s（兑换码 %s）", p.Name, codes[idx].Code), false); err != nil {
		fail(c, 400, err.Error())
		return
	}
	codes[idx].Remain--
	saveKV("redeem", codes)
	c.JSON(200, u)
}

func handleMyOrders(c *gin.Context) {
	orders := []Order{}
	db.Where("user_id = ?", me(c).ID).Order("id desc").Limit(500).Find(&orders)
	c.JSON(200, orders)
}

func handleAdminOrders(c *gin.Context) {
	orders := []Order{}
	db.Order("id desc").Limit(2000).Find(&orders)
	names := map[uint]string{}
	var users []User
	db.Select("id", "username").Find(&users)
	for _, u := range users {
		names[u.ID] = u.Username
	}
	type row struct {
		Order
		Username string `json:"username"`
	}
	out := make([]row, 0, len(orders))
	for _, o := range orders {
		out = append(out, row{o, names[o.UserID]})
	}
	c.JSON(200, out)
}

// handleAdminManualOrder 手动记账：给用户加减余额并留下记录，负数代表扣除。
func handleAdminManualOrder(c *gin.Context) {
	var req struct {
		UserID uint    `json:"user_id"`
		Info   string  `json:"info"`
		Amount float64 `json:"amount"`
	}
	if c.ShouldBindJSON(&req) != nil || req.Amount == 0 {
		fail(c, 400, "请填写用户 ID 和不为 0 的金额")
		return
	}
	shopMu.Lock()
	defer shopMu.Unlock()
	var u User
	if db.First(&u, req.UserID).Error != nil {
		fail(c, 404, "用户不存在")
		return
	}
	if u.Balance+req.Amount < 0 {
		fail(c, 400, fmt.Sprintf("扣除后余额会变成负数（当前 %.2f 元）", u.Balance))
		return
	}
	balance := u.Balance + req.Amount // 先算好：下面的 Update 会把新值写回 u
	if dbFail(c, db.Model(&u).Update("balance", balance).Error) {
		return
	}
	addOrder(u.ID, "手动记账", req.Info, req.Amount)
	c.JSON(200, gin.H{"balance": balance})
}

func handleAdminDeleteOrders(c *gin.Context) {
	var req struct {
		IDs []uint `json:"ids"`
	}
	if c.ShouldBindJSON(&req) != nil || len(req.IDs) == 0 {
		fail(c, 400, "请求格式错误")
		return
	}
	db.Delete(&Order{}, "id IN ?", req.IDs)
	c.JSON(200, gin.H{"ok": true})
}

// handleAdminSetUserPlan 管理员直接给用户指定套餐（不扣款）。plan_id 为 0 表示清除套餐标记。
func handleAdminSetUserPlan(c *gin.Context) {
	var req struct {
		PlanID uint `json:"plan_id"`
	}
	var u User
	if c.ShouldBindJSON(&req) != nil || db.First(&u, c.Param("id")).Error != nil {
		fail(c, 400, "请求有误或用户不存在")
		return
	}
	if req.PlanID == 0 {
		db.Model(&u).Update("plan_id", 0)
		c.JSON(200, gin.H{"ok": true})
		return
	}
	p := findPlan(req.PlanID)
	if p == nil {
		fail(c, 404, "套餐不存在")
		return
	}
	applyPlan(&u, p, false)
	if dbFail(c, db.Save(&u).Error) {
		return
	}
	addOrder(u.ID, "手动记账", "管理员指定套餐: "+p.Name, 0)
	nudgeAll()
	c.JSON(200, u)
}

// autoRenew 每小时运行：开启了自动续费的用户，周期套餐在 24 小时内到期或流量用完时，余额够就续费。
func autoRenew() {
	shopMu.Lock()
	defer shopMu.Unlock()
	var users []User
	db.Where("auto_renew = ? AND plan_id > 0", true).Find(&users)
	for i := range users {
		u := &users[i]
		p := findPlan(u.PlanID)
		if p == nil || !p.periodic() || u.Balance < p.Price {
			continue
		}
		expiring := u.ExpireAt != nil && time.Until(*u.ExpireAt) < 24*time.Hour
		exhausted := u.TrafficLimit > 0 && u.TrafficUsed >= u.TrafficLimit
		if expiring || exhausted {
			// 流量用完但还没到期时，从现在起重新算一个周期
			purchase(u, p, p.Price, "续费", p.Name+"（自动续费）", expiring)
		}
	}
}
