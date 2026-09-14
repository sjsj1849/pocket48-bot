package instagram

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"time"
)

type Client struct{ Dir, ProxyURL string }
type Response struct {
	User              *User   `json:"user"`
	Users             []User  `json:"users"`
	Events            []Event `json:"events"`
	Username          string  `json:"username"`
	SessionConfigured bool    `json:"sessionConfigured"`
}
type Error struct {
	Code        string
	NextRetryAt string
}

func (e *Error) Error() string {
	switch e.Code {
	case "cooldown":
		return "Instagram 正在冷却，下一次允许请求：" + e.NextRetryAt
	case "login_blocked":
		return "Instagram 拒绝了服务器登录请求，请在自己的浏览器登录并导入 Cookie"
	case "bad_credentials":
		return "Instagram 账号密码或验证码不正确"
	case "two_factor_required":
		return "Instagram 需要二次验证码，请在面板输入验证码完成登录"
	case "two_factor_expired":
		return "二次验证已过期，请重新登录"
	case "checkpoint_required":
		return "Instagram 要求安全验证，请在自己的浏览器完成验证后再登录或导入 Cookie"
	case "access_denied":
		return "Instagram 登录账号没有查看该私密账号的权限"
	case "login_required":
		return "请先配置 Instagram 登录态"
	case "account_unavailable":
		return "Instagram 账号暂不可用，请检查登录状态或等待限流恢复"
	case "user_unavailable":
		return "Instagram 用户不存在或当前不可读取"
	case "scan_incomplete":
		return "Instagram 历史扫描尚未覆盖上次进度，保留游标等待重试"
	case "timeout":
		return "Instagram 请求超时，将稍后重试"
	case "rate_limit":
		return "Instagram 请求受到限流，稍后自动重试"
	case "invalid_session":
		return "Instagram 登录态需包含 sessionid 和 csrftoken"
	case "insecure_session_permissions":
		return "Instagram 登录态文件权限需为 0600"
	default:
		return "Instagram 采集失败，请检查登录态与网络"
	}
}

type limitedOutput struct {
	bytes.Buffer
	overflow bool
}

func (b *limitedOutput) Write(p []byte) (int, error) {
	if b.Len()+len(p) > 8<<20 {
		b.overflow = true
		return len(p), nil
	}
	return b.Buffer.Write(p)
}
func (c Client) Call(ctx context.Context, request map[string]any) (Response, error) {
	ctx, cancel := context.WithTimeout(ctx, 100*time.Second)
	defer cancel()
	request["proxyURL"] = c.ProxyURL
	data, e := json.Marshal(request)
	if e != nil {
		return Response{}, e
	}
	base := filepath.Dir(filepath.Dir(c.Dir))
	script := filepath.Join(base, "sidecar/instagram-monitor/collector.py")
	python := filepath.Join(base, "sidecar/instagram-monitor/.venv/bin/python")
	command := exec.CommandContext(ctx, python, script, "--storage-dir", c.Dir)
	command.Stdin = bytes.NewReader(data)
	var out limitedOutput
	command.Stdout = &out
	err := command.Run()
	if ctx.Err() != nil {
		return Response{}, &Error{Code: "timeout"}
	}
	if out.overflow {
		return Response{}, fmt.Errorf("Instagram 响应超过大小限制")
	}
	var envelope struct {
		OK    bool     `json:"ok"`
		Data  Response `json:"data"`
		Error struct {
			Code        string `json:"code"`
			NextRetryAt string `json:"nextRetryAt"`
		} `json:"error"`
	}
	if json.Unmarshal(out.Bytes(), &envelope) != nil {
		if _, ok := err.(*exec.Error); ok {
			return Response{}, fmt.Errorf("Instagram 采集环境未安装")
		}
		return Response{}, fmt.Errorf("Instagram 采集模块未返回有效响应")
	}
	if !envelope.OK {
		return Response{}, &Error{Code: envelope.Error.Code, NextRetryAt: envelope.Error.NextRetryAt}
	}
	if err != nil {
		return Response{}, fmt.Errorf("Instagram 采集进程异常退出")
	}
	return envelope.Data, nil
}
func (c Client) Lookup(ctx context.Context, name string) (User, error) {
	name, e := Username(name)
	if e != nil {
		return User{}, e
	}
	r, e := c.Call(ctx, map[string]any{"operation": "lookup", "query": name})
	if e != nil {
		return User{}, e
	}
	if r.User == nil {
		return User{}, &Error{Code: "user_unavailable"}
	}
	return *r.User, nil
}
func (c Client) Timeline(ctx context.Context, username string, limit int) ([]Event, error) {
	r, e := c.Call(ctx, map[string]any{"operation": "timeline", "username": username, "limit": limit, "stories": false, "reels": true})
	return r.Events, e
}

func (c Client) Collect(ctx context.Context, s Subscription, limit int, since map[string]int64) ([]Event, error) {
	r, e := c.Call(ctx, map[string]any{"operation": "timeline", "username": s.Username, "limit": limit, "stories": s.Stories, "reels": s.Reels, "posts": s.Posts, "since": since})
	return r.Events, e
}
