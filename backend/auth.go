package main

import (
	"regexp"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/bcrypt"
)

const sessionTTL = 7 * 24 * time.Hour

var usernameRe = regexp.MustCompile(`^[A-Za-z0-9_.-]{3,32}$`)

func fail(c *gin.Context, code int, msg string) {
	c.AbortWithStatusJSON(code, gin.H{"error": msg})
}

// dbFail 在写数据库出错时返回 500 并报告 true。没有它的话，写库失败会被当成成功。
func dbFail(c *gin.Context, err error) bool {
	if err == nil {
		return false
	}
	fail(c, 500, "保存失败: "+err.Error())
	return true
}

func bearer(c *gin.Context) string {
	return strings.TrimPrefix(c.GetHeader("Authorization"), "Bearer ")
}

func authRequired(c *gin.Context) {
	tok := bearer(c)
	var s Session
	if tok == "" || db.First(&s, "token = ? AND expire_at > ?", tok, time.Now()).Error != nil {
		fail(c, 401, "未登录或登录已过期")
		return
	}
	var u User
	if db.First(&u, s.UserID).Error != nil || !u.Enabled {
		fail(c, 401, "账号不可用")
		return
	}
	c.Set("user", &u)
	c.Next()
}

func adminRequired(c *gin.Context) {
	if !me(c).Admin {
		fail(c, 403, "需要管理员权限")
		return
	}
	c.Next()
}

func me(c *gin.Context) *User { return c.MustGet("user").(*User) }

type credReq struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

func newSession(c *gin.Context, u *User) {
	s := Session{Token: randHex(24), UserID: u.ID, ExpireAt: time.Now().Add(sessionTTL)}
	db.Create(&s)
	c.JSON(200, gin.H{"token": s.Token, "user": u})
}

func handleLogin(c *gin.Context) {
	var req credReq
	if c.ShouldBindJSON(&req) != nil {
		fail(c, 400, "请求格式错误")
		return
	}
	var u User
	if db.First(&u, "username = ?", req.Username).Error != nil ||
		bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(req.Password)) != nil {
		fail(c, 401, "用户名或密码错误")
		return
	}
	if !u.Enabled {
		fail(c, 403, "账号已被禁用")
		return
	}
	newSession(c, &u)
}

func validateCred(username, password string) string {
	if !usernameRe.MatchString(username) {
		return "用户名需为 3-32 位字母、数字或 _ . -"
	}
	if len(password) < 6 || len(password) > 72 {
		return "密码长度需为 6-72 位"
	}
	return ""
}

func handleRegister(c *gin.Context) {
	if !registerAllowed() {
		fail(c, 403, "本站未开放注册")
		return
	}
	var req credReq
	if c.ShouldBindJSON(&req) != nil {
		fail(c, 400, "请求格式错误")
		return
	}
	if msg := validateCred(req.Username, req.Password); msg != "" {
		fail(c, 400, msg)
		return
	}
	u := User{Username: req.Username, PasswordHash: hashPassword(req.Password), Enabled: true}
	if db.Create(&u).Error != nil {
		fail(c, 409, "用户名已存在")
		return
	}
	newSession(c, &u)
}

func handleLogout(c *gin.Context) {
	db.Delete(&Session{}, "token = ?", bearer(c))
	c.JSON(200, gin.H{"ok": true})
}

func handleMe(c *gin.Context) { c.JSON(200, me(c)) }

func handleChangePassword(c *gin.Context) {
	var req struct {
		Old string `json:"old"`
		New string `json:"new"`
	}
	if c.ShouldBindJSON(&req) != nil {
		fail(c, 400, "请求格式错误")
		return
	}
	u := me(c)
	if bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(req.Old)) != nil {
		fail(c, 400, "原密码错误")
		return
	}
	if len(req.New) < 6 || len(req.New) > 72 {
		fail(c, 400, "密码长度需为 6-72 位")
		return
	}
	db.Model(u).Update("password_hash", hashPassword(req.New))
	// 改密后踢掉其他会话
	db.Delete(&Session{}, "user_id = ? AND token <> ?", u.ID, bearer(c))
	c.JSON(200, gin.H{"ok": true})
}
