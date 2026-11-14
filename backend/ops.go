package main

import (
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

var allModels = []any{&User{}, &UserGroup{}, &DeviceGroup{}, &Node{}, &Rule{}, &RuleGroup{}, &KV{},
	&GroupTrafficDaily{}, &Session{}, &TrafficDaily{}}

// openDB 按连接串打开数据库：
//
//	panel.db 或 sqlite3://panel.db                                   SQLite（默认）
//	mysql://用户:密码@tcp(127.0.0.1:3306)/库名?charset=utf8mb4&parseTime=true
//	postgres://host=localhost user=x password=x dbname=x port=5432 sslmode=disable
func openDB(dsn string) (*gorm.DB, error) {
	cfg := &gorm.Config{Logger: logger.New(log.Default(),
		logger.Config{LogLevel: logger.Warn, IgnoreRecordNotFoundError: true, SlowThreshold: time.Second})}
	switch {
	case strings.HasPrefix(dsn, "mysql://"):
		return gorm.Open(mysql.Open(strings.TrimPrefix(dsn, "mysql://")), cfg)
	case strings.HasPrefix(dsn, "postgres://"):
		rest := strings.TrimPrefix(dsn, "postgres://")
		if !strings.Contains(rest, "host=") {
			rest = dsn // 标准的 postgres://user:pass@host/db 形式
		}
		return gorm.Open(postgres.Open(rest), cfg)
	}
	d, err := gorm.Open(sqlite.Open(sqlitePath(dsn)+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)"), cfg)
	if err == nil {
		if sqlDB, e := d.DB(); e == nil {
			sqlDB.SetMaxOpenConns(1)
		}
	}
	return d, err
}

func sqlitePath(dsn string) string { return strings.TrimPrefix(dsn, "sqlite3://") }
func isSQLite(dsn string) bool {
	return !strings.HasPrefix(dsn, "mysql://") && !strings.HasPrefix(dsn, "postgres://")
}

// ---------- 命令行工具 ----------

// cliResetPassword 给指定用户换一个随机密码并打印出来。
func cliResetPassword(username string) {
	var u User
	if db.First(&u, "username = ?", username).Error != nil {
		log.Fatalf("用户 %s 不存在", username)
	}
	pw := randHex(8)
	db.Model(&u).Update("password_hash", hashPassword(pw))
	db.Delete(&Session{}, "user_id = ?", u.ID)
	fmt.Printf("用户 %s 的新密码: %s\n", username, pw)
}

// cliClean：1 清理失效规则（设备组已不存在），2 清理已过期且没有规则的普通用户。
func cliClean(kind int) {
	n := 0
	switch kind {
	case 1:
		var rules []Rule
		db.Find(&rules)
		for _, r := range rules {
			if loadGroup(r.InGroupID) == nil || (r.OutGroupID != 0 && loadGroup(r.OutGroupID) == nil) {
				db.Delete(&r)
				n++
			}
		}
	case 2:
		var users []User
		db.Where("admin = ? AND expire_at IS NOT NULL AND expire_at < ?", false, time.Now()).Find(&users)
		for _, u := range users {
			var c int64
			if db.Model(&Rule{}).Where("user_id = ?", u.ID).Count(&c); c == 0 {
				db.Delete(&Session{}, "user_id = ?", u.ID)
				db.Delete(&u)
				n++
			}
		}
	default:
		log.Fatal("-clean 的取值: 1 清理失效规则，2 清理无效用户")
	}
	fmt.Printf("已清理 %d 项\n", n)
}

func copyTable[T any](dst *gorm.DB) error {
	var rows []T
	if err := db.Find(&rows).Error; err != nil {
		return err
	}
	if err := dst.Session(&gorm.Session{AllowGlobalUpdate: true}).Delete(new(T)).Error; err != nil {
		return err
	}
	if len(rows) == 0 {
		return nil
	}
	return dst.CreateInBatches(rows, 200).Error
}

// cliCopyDatabase 把当前数据库的全部数据复制到另一个数据库（目标库里的同名表会被清空覆盖）。
func cliCopyDatabase(dsn string) {
	dst, err := openDB(dsn)
	if err != nil {
		log.Fatalf("打开目标数据库失败: %v", err)
	}
	if err := dst.AutoMigrate(allModels...); err != nil {
		log.Fatalf("在目标数据库建表失败: %v", err)
	}
	steps := []struct {
		name string
		run  func(*gorm.DB) error
	}{
		{"用户", copyTable[User]}, {"用户组", copyTable[UserGroup]}, {"设备组", copyTable[DeviceGroup]},
		{"节点", copyTable[Node]}, {"规则", copyTable[Rule]}, {"规则分组", copyTable[RuleGroup]},
		{"设置", copyTable[KV]}, {"设备组流量", copyTable[GroupTrafficDaily]}, {"用户流量", copyTable[TrafficDaily]},
	}
	for _, s := range steps {
		if err := s.run(dst); err != nil {
			log.Fatalf("复制「%s」失败: %v", s.name, err)
		}
		fmt.Printf("已复制: %s\n", s.name)
	}
	fmt.Println("完成。登录会话没有复制，用户需要重新登录。")
}

// ---------- 频率限制 ----------

// rateLimiter 是固定窗口计数：每个 key 在 window 内最多 limit 次。
type rateLimiter struct {
	limit  int
	window time.Duration
	mu     sync.Mutex
	m      map[string]*rateSlot
}

type rateSlot struct {
	start time.Time
	n     int
}

// parseRate 解析 "次数/秒数"，例如 "5/5"；空串或 "0" 表示不限制。
func parseRate(s string) *rateLimiter {
	a, b, ok := strings.Cut(s, "/")
	limit, err1 := strconv.Atoi(strings.TrimSpace(a))
	secs, err2 := strconv.Atoi(strings.TrimSpace(b))
	if !ok || err1 != nil || err2 != nil || limit <= 0 || secs <= 0 {
		return nil
	}
	rl := &rateLimiter{limit: limit, window: time.Duration(secs) * time.Second, m: map[string]*rateSlot{}}
	go func() {
		for range time.Tick(time.Minute) {
			rl.mu.Lock()
			for k, s := range rl.m {
				if time.Since(s.start) > rl.window {
					delete(rl.m, k)
				}
			}
			rl.mu.Unlock()
		}
	}()
	return rl
}

func (rl *rateLimiter) allow(key string) bool {
	if rl == nil {
		return true
	}
	rl.mu.Lock()
	defer rl.mu.Unlock()
	s := rl.m[key]
	if s == nil || time.Since(s.start) > rl.window {
		rl.m[key] = &rateSlot{start: time.Now(), n: 1}
		return true
	}
	s.n++
	return s.n <= rl.limit
}

var loginLimiter, userLimiter *rateLimiter

// limitByIP 用在登录、注册这类不需要登录的接口上。
func limitByIP(c *gin.Context) {
	if !loginLimiter.allow(c.ClientIP()) {
		fail(c, 429, "操作太频繁，请稍后再试")
		return
	}
	c.Next()
}

// limitByUser 用在登录后的接口上（默认不限制）。
func limitByUser(c *gin.Context) {
	if u, ok := c.Get("user"); ok && !userLimiter.allow(strconv.Itoa(int(u.(*User).ID))) {
		fail(c, 429, "操作太频繁，请稍后再试")
		return
	}
	c.Next()
}
