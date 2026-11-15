package main

import (
	"bytes"
	"crypto/md5"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// 在线充值。支持的支付通道类型：
//
//	epay       易支付（彩虹易支付协议）。URL 填 https://域名/submit.php（用户跳转到收银台），
//	           或 https://域名/mapi.php?type=alipay&device=pc（由面板下单，返回支付链接/二维码）
//	epusdt     Epusdt / Bepusdt。URL 填完整的下单接口，Secret 填 Api Token
//	tokenpay   TokenPay。URL 填 https://域名/CreateOrder?currency=USDT_TRC20，Secret 填 Api Token
//	cryptomus  Cryptomus。PID 填 merchant-id，Secret 填 payment-key；URL 里可带 ?currency=CNY&subtract=0
//
// 流程：用户发起充值 -> 建一张「待支付」的订单 -> 到网关付款 -> 网关回调面板 ->
// 验签、核对金额 -> 订单改为「已完成」并给用户加余额。同一张订单只会入账一次。

type payChannel struct {
	Name         string  `json:"name"`
	Type         string  `json:"type"`
	Enabled      bool    `json:"enabled"`
	URL          string  `json:"url"`
	PID          string  `json:"pid"`
	Secret       string  `json:"secret"`
	CallbackHost string  `json:"callback_host"`
	Fee          float64 `json:"fee"` // 用户额外承担的手续费百分比
}

type payConfig struct {
	MinRecharge float64      `json:"min_recharge"`
	Channels    []payChannel `json:"channels"`
}

func loadPay() payConfig {
	cfg := payConfig{MinRecharge: 10}
	getKV("pay", &cfg)
	return cfg
}

func (c *payConfig) channel(name string) *payChannel {
	for i := range c.Channels {
		if c.Channels[i].Name == name {
			return &c.Channels[i]
		}
	}
	return nil
}

var payHTTP = &http.Client{Timeout: 20 * time.Second}

func md5hex(s string) string {
	h := md5.Sum([]byte(s))
	return hex.EncodeToString(h[:])
}

func money(v float64) string { return strconv.FormatFloat(math.Round(v*100)/100, 'f', 2, 64) }

// signString 把非空参数按键名排序后拼成 k=v&k=v，签名字段本身不参与。
func signString(params map[string]string, skip ...string) string {
	keys := make([]string, 0, len(params))
	for k, v := range params {
		if v == "" {
			continue
		}
		skipped := false
		for _, s := range skip {
			skipped = skipped || strings.EqualFold(k, s)
		}
		if !skipped {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = k + "=" + params[k]
	}
	return strings.Join(parts, "&")
}

// jsonParams 把回调的 JSON 展开成字符串参数。数字保留网关发来时的原样写法（如 15.00），否则验签会对不上。
func jsonParams(body []byte) (map[string]string, error) {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var raw map[string]any
	if err := dec.Decode(&raw); err != nil {
		return nil, err
	}
	out := map[string]string{}
	for k, v := range raw {
		switch x := v.(type) {
		case nil:
		case string:
			out[k] = x
		case json.Number:
			out[k] = x.String()
		default:
			out[k] = fmt.Sprint(x)
		}
	}
	return out, nil
}

// payResult 是下单后告诉前端怎么去付款。
type payResult struct {
	URL    string `json:"url,omitempty"`    // 跳转到这个地址付款
	QRCode string `json:"qrcode,omitempty"` // 或者显示这个二维码内容
}

func postJSON(endpoint string, body any, headers map[string]string, into any) error {
	raw, _ := json.Marshal(body)
	req, _ := http.NewRequest("POST", endpoint, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	res, err := payHTTP.Do(req)
	if err != nil {
		return fmt.Errorf("连接支付网关失败: %w", err)
	}
	defer res.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if json.Unmarshal(data, into) != nil {
		return fmt.Errorf("支付网关返回了无法识别的内容 (HTTP %d)", res.StatusCode)
	}
	return nil
}

// createPayment 向网关下单。notify 是网关回调面板的地址，back 是用户付完跳回的地址。
func createPayment(ch *payChannel, o *Order, notify, back, clientIP string) (*payResult, error) {
	u, err := url.Parse(ch.URL)
	if err != nil || u.Host == "" {
		return nil, errors.New("支付通道的 URL 配置有误，需要是完整的接口地址")
	}
	q := u.Query()
	endpoint := *u
	endpoint.RawQuery = ""
	amount := money(o.PayAmount)

	switch ch.Type {
	case "epay":
		p := map[string]string{"pid": ch.PID, "out_trade_no": o.No, "notify_url": notify, "return_url": back,
			"name": "账户充值", "money": amount}
		for _, k := range []string{"type", "device"} {
			if v := q.Get(k); v != "" {
				p[k] = v
			}
		}
		mapi := strings.Contains(u.Path, "mapi.php") || q.Get("mapi") == "1"
		if mapi {
			p["clientip"] = clientIP
		}
		form := url.Values{}
		for k, v := range p {
			form.Set(k, v)
		}
		form.Set("sign", md5hex(signString(p)+ch.Secret))
		form.Set("sign_type", "MD5")
		if !mapi {
			// submit 模式：让用户直接带着参数跳到网关的收银台
			return &payResult{URL: endpoint.String() + "?" + form.Encode()}, nil
		}
		res, err := payHTTP.PostForm(endpoint.String(), form)
		if err != nil {
			return nil, fmt.Errorf("连接支付网关失败: %w", err)
		}
		defer res.Body.Close()
		var r struct {
			Code      json.Number `json:"code"`
			Msg       string      `json:"msg"`
			PayURL    string      `json:"payurl"`
			QRCode    string      `json:"qrcode"`
			URLScheme string      `json:"urlscheme"`
		}
		if json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&r) != nil {
			return nil, errors.New("支付网关返回了无法识别的内容")
		}
		if r.Code.String() != "1" {
			return nil, fmt.Errorf("支付网关拒绝下单: %s", r.Msg)
		}
		if r.PayURL != "" {
			return &payResult{URL: r.PayURL}, nil
		}
		if r.QRCode != "" {
			return &payResult{QRCode: r.QRCode}, nil
		}
		return &payResult{URL: r.URLScheme}, nil

	case "epusdt":
		amt, _ := strconv.ParseFloat(amount, 64)
		p := map[string]string{"order_id": o.No, "amount": strconv.FormatFloat(amt, 'f', -1, 64),
			"notify_url": notify, "redirect_url": back}
		body := map[string]any{"order_id": o.No, "amount": amt, "notify_url": notify, "redirect_url": back,
			"signature": md5hex(signString(p) + ch.Secret)}
		var r struct {
			StatusCode int    `json:"status_code"`
			Message    string `json:"message"`
			Data       struct {
				PaymentURL string `json:"payment_url"`
			} `json:"data"`
		}
		if err := postJSON(ch.URL, body, nil, &r); err != nil {
			return nil, err
		}
		if r.StatusCode != 200 || r.Data.PaymentURL == "" {
			return nil, fmt.Errorf("支付网关拒绝下单: %s", r.Message)
		}
		return &payResult{URL: r.Data.PaymentURL}, nil

	case "tokenpay":
		cur := q.Get("currency")
		if cur == "" {
			cur = "USDT_TRC20"
		}
		p := map[string]string{"OutOrderId": o.No, "OrderUserKey": strconv.Itoa(int(o.UserID)), "ActualAmount": amount,
			"Currency": cur, "NotifyUrl": notify, "RedirectUrl": back}
		body := map[string]any{}
		for k, v := range p {
			body[k] = v
		}
		body["Signature"] = md5hex(signString(p) + ch.Secret)
		var r struct {
			Success bool   `json:"success"`
			Message string `json:"message"`
			Data    string `json:"data"`
		}
		if err := postJSON(endpoint.String(), body, nil, &r); err != nil {
			return nil, err
		}
		if !r.Success || r.Data == "" {
			return nil, fmt.Errorf("支付网关拒绝下单: %s", r.Message)
		}
		return &payResult{URL: r.Data}, nil

	case "cryptomus":
		cur := q.Get("currency")
		if cur == "" {
			cur = "CNY"
		}
		body := map[string]any{"amount": amount, "currency": cur, "order_id": o.No, "url_callback": notify, "url_return": back}
		if s := q.Get("subtract"); s != "" {
			body["subtract"] = s
		}
		var r struct {
			State   int    `json:"state"`
			Message string `json:"message"`
			Result  struct {
				URL string `json:"url"`
			} `json:"result"`
		}
		if err := cryptomusCall(ch, "/v1/payment", body, &r); err != nil {
			return nil, err
		}
		if r.State != 0 || r.Result.URL == "" {
			return nil, fmt.Errorf("支付网关拒绝下单: %s", r.Message)
		}
		return &payResult{URL: r.Result.URL}, nil
	}
	return nil, fmt.Errorf("暂不支持 %s 类型的支付通道", ch.Type)
}

// cryptomusBase 是 Cryptomus 的接口地址，可以用环境变量 CRYPTOMUS_API_BASE 覆盖（测试用）。
var cryptomusBase = func() string {
	if v := os.Getenv("CRYPTOMUS_API_BASE"); v != "" {
		return strings.TrimRight(v, "/")
	}
	return "https://api.cryptomus.com"
}()

func cryptomusCall(ch *payChannel, path string, body any, into any) error {
	raw, _ := json.Marshal(body)
	sign := md5hex(base64.StdEncoding.EncodeToString(raw) + ch.Secret)
	req, _ := http.NewRequest("POST", cryptomusBase+path, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("merchant", ch.PID)
	req.Header.Set("sign", sign)
	res, err := payHTTP.Do(req)
	if err != nil {
		return fmt.Errorf("连接支付网关失败: %w", err)
	}
	defer res.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if json.Unmarshal(data, into) != nil {
		return fmt.Errorf("支付网关返回了无法识别的内容 (HTTP %d)", res.StatusCode)
	}
	return nil
}

// verifyNotify 校验网关回调。返回 (已支付, 网关的交易号, 应答给网关的文本)。
// 验签不通过或金额不符时返回错误；验签通过但还没支付成功时 paid 为 false。
func verifyNotify(ch *payChannel, o *Order, c *gin.Context) (paid bool, tradeNo, ack string, err error) {
	sameMoney := func(s string) bool {
		v, e := strconv.ParseFloat(s, 64)
		return e == nil && math.Abs(v-o.PayAmount) < 0.005
	}
	switch ch.Type {
	case "epay":
		c.Request.ParseForm()
		p := map[string]string{}
		for k, v := range c.Request.Form {
			if len(v) > 0 {
				p[k] = v[0]
			}
		}
		if !strings.EqualFold(md5hex(signString(p, "sign", "sign_type")+ch.Secret), p["sign"]) {
			return false, "", "", errors.New("签名不符")
		}
		if p["out_trade_no"] != o.No || !sameMoney(p["money"]) {
			return false, "", "", errors.New("订单号或金额不符")
		}
		return p["trade_status"] == "TRADE_SUCCESS", p["trade_no"], "success", nil

	case "epusdt", "tokenpay":
		body, _ := io.ReadAll(io.LimitReader(c.Request.Body, 1<<20))
		p, e := jsonParams(body)
		if e != nil {
			return false, "", "", errors.New("回调内容不是 JSON")
		}
		sigKey, idKey, amtKey := "signature", "order_id", "amount"
		if ch.Type == "tokenpay" {
			sigKey, idKey, amtKey = "Signature", "OutOrderId", "ActualAmount"
		}
		if !strings.EqualFold(md5hex(signString(p, sigKey)+ch.Secret), p[sigKey]) {
			return false, "", "", errors.New("签名不符")
		}
		if p[idKey] != o.No || !sameMoney(p[amtKey]) {
			return false, "", "", errors.New("订单号或金额不符")
		}
		if ch.Type == "tokenpay" {
			return p["Status"] == "1", p["Id"], "ok", nil
		}
		return p["status"] == "2", p["trade_id"], "ok", nil

	case "cryptomus":
		// 不依赖回调内容，直接向网关查询这张订单的状态
		var r struct {
			State  int `json:"state"`
			Result struct {
				UUID          string `json:"uuid"`
				OrderID       string `json:"order_id"`
				PaymentStatus string `json:"payment_status"`
			} `json:"result"`
		}
		if e := cryptomusCall(ch, "/v1/payment/info", map[string]any{"order_id": o.No}, &r); e != nil {
			return false, "", "", e
		}
		if r.State != 0 || r.Result.OrderID != o.No {
			return false, "", "", errors.New("网关上查不到这张订单")
		}
		st := r.Result.PaymentStatus
		return st == "paid" || st == "paid_over", r.Result.UUID, "ok", nil
	}
	return false, "", "", errors.New("不支持的支付通道")
}

// requestBase 返回用户访问面板用的地址（协议 + 域名），用来拼回调地址。
func requestBase(c *gin.Context, ch *payChannel) string {
	if h := strings.TrimRight(strings.TrimSpace(ch.CallbackHost), "/"); h != "" {
		return h
	}
	scheme := "http"
	if c.Request.TLS != nil || c.GetHeader("X-Forwarded-Proto") == "https" {
		scheme = "https"
	}
	return scheme + "://" + c.Request.Host
}

func handleRecharge(c *gin.Context) {
	var req struct {
		Amount  float64 `json:"amount"`
		Channel string  `json:"channel"`
	}
	if c.ShouldBindJSON(&req) != nil {
		fail(c, 400, "请求格式错误")
		return
	}
	cfg := loadPay()
	ch := cfg.channel(req.Channel)
	if ch == nil || !ch.Enabled {
		fail(c, 400, "请选择支付通道")
		return
	}
	req.Amount = math.Round(req.Amount*100) / 100
	if req.Amount < cfg.MinRecharge || req.Amount > 1e7 {
		fail(c, 400, fmt.Sprintf("最小充值金额为 %.2f 元", cfg.MinRecharge))
		return
	}
	u := me(c)
	o := Order{No: time.Now().Format("20060102150405") + randHex(4), UserID: u.ID, Type: "充值", Status: "待支付",
		Amount: req.Amount, PayAmount: math.Round(req.Amount*(100+max(ch.Fee, 0))) / 100, Channel: ch.Name,
		Info: "通过 " + ch.Name + " 充值"}
	if dbFail(c, db.Create(&o).Error) {
		return
	}
	base := requestBase(c, ch)
	res, err := createPayment(ch, &o, base+"/api/v1/guest/pay/notify/"+o.No, base+"/#/orders", c.ClientIP())
	if err != nil {
		db.Model(&o).Update("status", "下单失败")
		fail(c, 502, err.Error())
		return
	}
	c.JSON(200, gin.H{"order": o, "pay": res})
}

// handlePayNotify 是支付网关回调的地址，不需要登录，安全性靠验签保证。
func handlePayNotify(c *gin.Context) {
	shopMu.Lock()
	defer shopMu.Unlock()
	var o Order
	if db.First(&o, "no = ? AND type = ?", c.Param("no"), "充值").Error != nil {
		c.String(404, "order not found")
		return
	}
	cfg := loadPay()
	ch := cfg.channel(o.Channel)
	if ch == nil {
		c.String(400, "channel not found")
		return
	}
	paid, tradeNo, ack, err := verifyNotify(ch, &o, c)
	if err != nil {
		c.String(400, "fail: "+err.Error())
		return
	}
	if paid && o.Status == "待支付" {
		// 只有「待支付」的订单会入账，所以网关重复回调不会重复加余额
		now := time.Now()
		o.Status, o.PaidAt, o.TradeNo = "已完成", &now, tradeNo
		db.Save(&o)
		db.Model(&User{}).Where("id = ?", o.UserID).Update("balance", gormExprAdd("balance", o.Amount))
		afterRecharge(&o)
	}
	c.String(200, ack)
}
