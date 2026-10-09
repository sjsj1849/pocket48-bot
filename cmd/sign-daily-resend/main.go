// One-shot: 补发超话**签到监控**日报（按新的小时表口径重新生成并推送）。
//
// 用法：./sign-daily-resend 2026-10-07
//
// 为什么要有它：日报每天只在 23:5x 自动发一次，改完表格样式要等 24 小时
// 才能看到效果；而线上日报出过错（最后一整行显示 +0），需要立刻补发一份
// 给用户确认。这里复用运行 bot 完全相同的同一份统计与同一套出站通道。
package main

import (
	"fmt"
	"log"
	"os"
	"strings"

	"pocket48-bot/internal/config"
	"pocket48-bot/internal/logic"
)

func main() {
	cfgPath := "/root/pocket48-bot/config.json"
	if v := strings.TrimSpace(os.Getenv("POCKET48_CONFIG")); v != "" {
		cfgPath = v
	}
	date := ""
	if len(os.Args) > 1 {
		date = strings.TrimSpace(os.Args[1])
	}
	if date == "" {
		log.Fatalf("usage: %s YYYY-MM-DD", os.Args[0])
	}
	cfg, err := config.LoadConfig(cfgPath)
	if err != nil {
		log.Fatalf("load config: %v", err)
	}
	if err := logic.ResendSignMonitorDailyReport(cfg, date); err != nil {
		log.Fatalf("resend: %v", err)
	}
	fmt.Println("done")
}
