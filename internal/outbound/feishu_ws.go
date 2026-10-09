package outbound

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// FeishuWSOptions 配置飞书入站长连接。
//
// 导出是因为 logic 层要构造它 —— 入站接线不能留在 outbound 包里。
type FeishuWSOptions struct {
	AppID     string
	AppSecret string
	// Script 是 sidecar 脚本路径（sidecar/feishu-ws/feishu_ws.py）。
	Script string
	// OnEvent 收到消息时调用，在读 stdout 的 goroutine 里执行，
	// 实现方必须自己保证不阻塞（耗时活丢给别的 goroutine）。
	OnEvent func(ev FeishuIncomingEvent)
	Logger  *log.Logger
}

// FeishuIncomingEvent 是一条入站消息的必要字段。
type FeishuIncomingEvent struct {
	ChatID     string `json:"chatId"`
	MessageID  string `json:"messageId"`
	SenderID   string `json:"senderId"`
	Text       string `json:"text"`
	ChatType   string `json:"chatType"`
	MentionBot bool   `json:"mentionBot"`
}

// FeishuWSClient 管理飞书长连接 sidecar 进程。
type FeishuWSClient struct {
	opt FeishuWSOptions

	mu     sync.Mutex
	cancel context.CancelFunc
	events int
}

// NewFeishuWSClient 创建客户端（不会自动连接）。
func NewFeishuWSClient(opt FeishuWSOptions) *FeishuWSClient {
	if opt.Logger == nil {
		opt.Logger = log.Default()
	}
	return &FeishuWSClient{opt: opt}
}

// Start 阻塞运行，直到 ctx 取消或 Stop 被调用。
//
// 重连策略：固定 10 秒，**不做指数退避** —— 长连接断开通常是
// 网络抖动或飞书侧重启，退避只会让恢复变慢。
func (c *FeishuWSClient) Start(ctx context.Context) {
	if c == nil {
		return
	}
	child, cancel := context.WithCancel(ctx)
	c.mu.Lock()
	c.cancel = cancel
	c.mu.Unlock()
	defer cancel()

	for {
		if child.Err() != nil {
			return
		}
		if err := c.serveOnce(child); err != nil {
			c.opt.Logger.Printf("[Feishu-WS] 长连接断开：%v，10 秒后重连", err)
		}
		select {
		case <-child.Done():
			return
		case <-time.After(10 * time.Second):
		}
	}
}

// Stop 主动关闭。
func (c *FeishuWSClient) Stop() {
	if c == nil {
		return
	}
	c.mu.Lock()
	cancel := c.cancel
	c.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (c *FeishuWSClient) serveOnce(ctx context.Context) error {
	if strings.TrimSpace(c.opt.Script) == "" {
		return fmt.Errorf("sidecar 脚本未配置")
	}
	cmd := exec.CommandContext(ctx, "python3", c.opt.Script)
	// 凭据走环境变量，不进命令行 —— ps 能看到 argv。
	cmd.Env = append(os.Environ(),
		"FEISHU_APP_ID="+c.opt.AppID,
		"FEISHU_APP_SECRET="+c.opt.AppSecret,
	)
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("启动 sidecar 失败: %w", err)
	}
	c.opt.Logger.Printf("[Feishu-WS] sidecar 已启动 pid=%d", cmd.Process.Pid)

	// sidecar 的日志走 stderr，转发到 bot.log。
	go func() {
		sc := bufio.NewScanner(stderr)
		for sc.Scan() {
			c.opt.Logger.Printf("[Feishu-WS] %s", sc.Text())
		}
	}()

	// 探活：每 30 秒 ping 一次，纯粹为了在日志里体现连接存活。
	go func() {
		t := time.NewTicker(30 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if _, err := stdin.Write([]byte("ping\n")); err != nil {
					return
				}
			}
		}
	}()

	// 读事件行。sidecar 主动写 stdout，不需要等请求。
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		c.handleLine(line)
	}
	err = sc.Err()
	_ = cmd.Wait()
	if err == nil {
		err = fmt.Errorf("sidecar 正常退出")
	}
	return err
}

// handleLine 解析 sidecar 的一行输出。
func (c *FeishuWSClient) handleLine(line string) {
	var env struct {
		Type  string               `json:"type"`
		Error string               `json:"error"`
		Event *FeishuIncomingEvent `json:"event"`
	}
	if err := json.Unmarshal([]byte(line), &env); err != nil {
		return
	}
	switch env.Type {
	case "pong":
		return
	case "error":
		c.opt.Logger.Printf("[Feishu-WS] sidecar 报错：%s", env.Error)
		return
	case "event":
		if env.Event == nil || c.opt.OnEvent == nil {
			return
		}
		ev := *env.Event
		if strings.TrimSpace(ev.Text) == "" {
			return
		}
		c.mu.Lock()
		c.events++
		c.mu.Unlock()
		c.opt.OnEvent(ev)
	}
}
