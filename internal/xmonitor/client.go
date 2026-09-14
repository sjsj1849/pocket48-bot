package xmonitor

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
	User   *User   `json:"user"`
	Users  []User  `json:"users"`
	Events []Event `json:"events"`
}
type Error struct{ Code string }

func (e *Error) Error() string {
	switch e.Code {
	case "login_required":
		return "请先配置 X 登录态"
	case "account_unavailable":
		return "X 账号暂不可用，请检查登录状态或等待限流恢复"
	case "user_unavailable":
		return "X 用户不存在或当前不可读取"
	case "timeout":
		return "X 请求超时，将稍后重试"
	case "invalid_session":
		return "X 登录态需包含 auth_token 和 ct0"
	case "insecure_session_permissions":
		return "X 登录态文件权限需为 0600"
	default:
		return "X 采集失败，请检查登录态与网络"
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
	script := filepath.Join(base, "sidecar/x-monitor/collector.py")
	python := filepath.Join(base, "sidecar/x-monitor/.venv/bin/python")
	command := exec.CommandContext(ctx, python, script, "--storage-dir", c.Dir)
	command.Stdin = bytes.NewReader(data)
	var out limitedOutput
	command.Stdout = &out
	err := command.Run()
	if ctx.Err() != nil {
		return Response{}, &Error{Code: "timeout"}
	}
	if out.overflow {
		return Response{}, fmt.Errorf("X 响应超过大小限制")
	}
	var envelope struct {
		OK    bool     `json:"ok"`
		Data  Response `json:"data"`
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if json.Unmarshal(out.Bytes(), &envelope) != nil {
		if _, ok := err.(*exec.Error); ok {
			return Response{}, fmt.Errorf("X 采集环境未安装")
		}
		return Response{}, fmt.Errorf("X 采集模块未返回有效响应")
	}
	if !envelope.OK {
		return Response{}, &Error{Code: envelope.Error.Code}
	}
	if err != nil {
		return Response{}, fmt.Errorf("X 采集进程异常退出")
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
func (c Client) Timeline(ctx context.Context, id string, limit int) ([]Event, error) {
	r, e := c.Call(ctx, map[string]any{"operation": "timeline", "userId": id, "limit": limit})
	return r.Events, e
}
