package main

import (
	"fmt"

	"github.com/gin-gonic/gin"
)

// 邀请注册与返利。
//
// 站点设置里的「邀请注册策略」：0 无限制（有没有邀请码都能注册），1 不允许邀请注册（忽略邀请码），
// 2 仅开放邀请注册（必须有有效的邀请码）。
// 「邀请设置」：启用后，被邀请的用户充值到账时，邀请人按返佣比例获得余额。
// 循环返佣关闭时只对被邀请人的第一笔充值返利，开启时每一笔都返。
// 强制绑定 Telegram 开启时，邀请人必须绑定了 Telegram 才能拿到返利。

type inviteConfig struct {
	Enabled bool    `json:"enabled"`
	Loop    bool    `json:"loop"`
	ForceTG bool    `json:"force_tg"`
	Rate    float64 `json:"rate"`
}

func loadInvite() inviteConfig {
	var c inviteConfig
	getKV("invite", &c)
	return c
}

// inviteCodeOf 返回用户的邀请码，没有就生成一个。
func inviteCodeOf(u *User) string {
	if u.InviteCode == "" {
		u.InviteCode = randHex(5)
		db.Model(u).Update("invite_code", u.InviteCode)
	}
	return u.InviteCode
}

// resolveInviter 在注册时根据邀请码找到邀请人。返回 (邀请人ID, 是否允许注册, 提示)。
func resolveInviter(code string) (uint, bool, string) {
	policy := loadSite().RegisterPolicy
	if policy == 1 || !loadInvite().Enabled {
		if policy == 2 {
			return 0, false, "本站仅开放邀请注册，但邀请功能没有启用"
		}
		return 0, true, ""
	}
	var inviter User
	if code != "" && db.First(&inviter, "invite_code = ?", code).Error == nil {
		return inviter.ID, true, ""
	}
	if policy == 2 {
		return 0, false, "本站仅开放邀请注册，请使用有效的邀请链接"
	}
	return 0, true, ""
}

// inviteRebate 在被邀请人的充值到账后给邀请人返利。调用方已持有 shopMu。
func inviteRebate(o *Order) {
	cfg := loadInvite()
	if !cfg.Enabled || cfg.Rate <= 0 {
		return
	}
	var u, inviter User
	if db.First(&u, o.UserID).Error != nil || u.InviterID == 0 || db.First(&inviter, u.InviterID).Error != nil {
		return
	}
	if cfg.ForceTG && inviter.TGChatID == 0 {
		return
	}
	if !cfg.Loop {
		var n int64
		if db.Model(&Order{}).Where("user_id = ? AND type = ? AND status = ?", u.ID, "充值", "已完成").Count(&n); n > 1 {
			return // 不是第一笔充值
		}
	}
	amount := float64(int(o.Amount*cfg.Rate+0.5)) / 100
	if amount <= 0 {
		return
	}
	db.Model(&inviter).Update("balance", gormExprAdd("balance", amount))
	addOrder(inviter.ID, "邀请返利", fmt.Sprintf("%s (#%d) 充值 %.2f 元", u.Username, u.ID, o.Amount), amount)
	notifyUser(&inviter, fmt.Sprintf("你邀请的用户 %s 充值了 %.2f 元，你获得返利 %.2f 元。", u.Username, o.Amount, amount))
}

// handleMyInvite 返回当前用户的邀请码和已邀请人数。
func handleMyInvite(c *gin.Context) {
	u := freshUser(c)
	var n int64
	db.Model(&User{}).Where("inviter_id = ?", u.ID).Count(&n)
	cfg := loadInvite()
	c.JSON(200, gin.H{"enabled": cfg.Enabled && loadSite().RegisterPolicy != 1, "code": inviteCodeOf(u), "invited": n, "rate": cfg.Rate})
}

// handleAdminUserInvite 管理员查看某个用户的邀请信息。
func handleAdminUserInvite(c *gin.Context) {
	var u User
	if db.First(&u, c.Param("id")).Error != nil {
		fail(c, 404, "用户不存在")
		return
	}
	invited := []User{}
	db.Select("id", "username", "created_at").Where("inviter_id = ?", u.ID).Order("id desc").Find(&invited)
	var inviter User
	db.Select("id", "username").First(&inviter, u.InviterID)
	c.JSON(200, gin.H{"code": inviteCodeOf(&u), "invited": invited, "inviter": inviter.Username, "inviter_id": u.InviterID})
}
