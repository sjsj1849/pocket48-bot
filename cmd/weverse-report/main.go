package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"pocket48-bot/internal/config"
	"pocket48-bot/internal/logic"
	"pocket48-bot/internal/weverse"
	"time"
)

func must(e error) {
	if e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
}
func main() {
	now := time.Now().In(weverse.ReportLocation)
	cfgPath := flag.String("config", "/root/pocket48-bot/config.json", "BOT 配置路径")
	kind := flag.String("kind", "monthly", "weekly | monthly | firstHalf | annual")
	date := flag.String("date", now.Format("2006-01-02"), "周报所在周的任意日期（YYYY-MM-DD）")
	year := flag.Int("year", now.Year(), "年份")
	month := flag.Int("month", int(now.Month()), "月份")
	refresh := flag.Bool("refresh-posts", false, "刷新已记录主帖的累计互动与附件")
	backfill := flag.Bool("backfill", false, "回采当期可见历史，不转发旧消息")
	backfillNotifications := flag.Bool("backfill-notifications", false, "仅从通知历史回采成员回复，不扫描成员主帖")
	backfillMemberComments := flag.Bool("backfill-member-comments", false, "从成员主页评论历史回采全部成员回复，不扫描成员主帖")
	send := flag.Bool("send", false, "发送报表邮件及 Excel/PNG 附件")
	enable := flag.Bool("enable", false, "启用月报、上半年报和年报")
	out := flag.String("out", "", "可选附件输出目录")
	flag.Parse()
	cfg, e := config.LoadConfig(*cfgPath)
	must(e)
	dir := weverse.Dir(*cfgPath)
	settings, e := weverse.LoadReportSettings(dir)
	must(e)
	if *enable {
		settings.Enabled = true
		settings.Monthly = true
		settings.FirstHalf = true
		settings.Annual = true
		must(weverse.SaveReportSettings(dir, settings))
	}
	var period weverse.ReportPeriod
	if *kind == "weekly" {
		period, e = weverse.NewWeeklyReportPeriod(*date)
	} else {
		period, e = weverse.NewReportPeriod(*kind, *year, *month)
	}
	must(e)
	h, e := weverse.OpenHistory(dir)
	must(e)
	defer h.Close()
	if *backfill {
		monitor, e := weverse.LoadSettings(dir)
		must(e)
		slug := ""
		for _, s := range monitor.Subscriptions {
			if s.CommunityID == settings.CommunityID {
				slug = s.Slug
				break
			}
		}
		if slug == "" {
			must(fmt.Errorf("请先订阅报表目标社区"))
		}
		client := weverse.NewClient(dir, monitor.ProxyURL)
		defer client.HTTP.CloseIdleConnections()
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
		defer cancel()
		last := 0
		e = client.BackfillReport(ctx, h, settings, period, slug, func(p weverse.BackfillProgress) {
			_ = weverse.Write(dir, "report-backfill.json", p)
			if p.Done >= last+25 {
				fmt.Printf("回采主帖 %d/%d，记录 %d 条当期内容\n", p.Done, p.Total, p.Records)
				last = p.Done
			}
		})
		progress := weverse.BackfillProgress{}
		_ = weverse.Read(dir, "report-backfill.json", &progress)
		progress.Running = false
		progress.UpdatedAt = time.Now().Format(time.RFC3339)
		if e != nil {
			progress.Error = e.Error()
		}
		_ = weverse.Write(dir, "report-backfill.json", progress)
		must(e)
	}
	if *backfillNotifications {
		monitor, e := weverse.LoadSettings(dir)
		must(e)
		slug := ""
		for _, subscription := range monitor.Subscriptions {
			if subscription.CommunityID == settings.CommunityID {
				slug = subscription.Slug
				break
			}
		}
		if slug == "" {
			must(fmt.Errorf("请先订阅报表目标社区"))
		}
		client := weverse.NewClient(dir, monitor.ProxyURL)
		defer client.HTTP.CloseIdleConnections()
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
		defer cancel()
		parsed, inaccessible, e := client.BackfillReportNotificationComments(ctx, h, settings, period, slug)
		must(e)
		fmt.Printf("通知历史解析 %d 条当期成员回复，%d 个通知关联帖子当前不可访问\n", parsed, inaccessible)
	}
	if *backfillMemberComments {
		monitor, e := weverse.LoadSettings(dir)
		must(e)
		slug := ""
		for _, subscription := range monitor.Subscriptions {
			if subscription.CommunityID == settings.CommunityID {
				slug = subscription.Slug
				break
			}
		}
		if slug == "" {
			must(fmt.Errorf("请先订阅报表目标社区"))
		}
		client := weverse.NewClient(dir, monitor.ProxyURL)
		defer client.HTTP.CloseIdleConnections()
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
		defer cancel()
		counts, e := client.BackfillReportMemberComments(ctx, h, settings, period, slug)
		must(e)
		members, e := h.Members(settings.CommunityID)
		must(e)
		for _, member := range members {
			count := counts[member.ID]
			fmt.Printf("%s：解析 %d 条当期回复，其中回复粉丝 %d 条\n", member.Name, count.Replies, count.FanReplies)
		}
	}
	if *refresh {
		monitor, e := weverse.LoadSettings(dir)
		must(e)
		slug := ""
		for _, sub := range monitor.Subscriptions {
			if sub.CommunityID == settings.CommunityID {
				slug = sub.Slug
				break
			}
		}
		if slug == "" {
			must(fmt.Errorf("请先订阅报表社区"))
		}
		client := weverse.NewClient(dir, monitor.ProxyURL)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		e = client.RefreshReportPosts(ctx, h, settings, period, slug)
		cancel()
		client.HTTP.CloseIdleConnections()
		must(e)
	}
	r, e := h.BuildReport(settings, period)
	must(e)
	if len(r.Members) == 0 {
		must(fmt.Errorf("尚未采集成员名单"))
	}
	if *out != "" {
		must(os.MkdirAll(*out, 0700))
		xlsx, e := r.XLSX()
		must(e)
		must(os.WriteFile(filepath.Join(*out, "weverse-"+period.Key+".xlsx"), xlsx, 0600))
		png, e := logic.WeverseReportPNG(r)
		must(e)
		must(os.WriteFile(filepath.Join(*out, "weverse-"+period.Key+".png"), png, 0600))
		must(os.WriteFile(filepath.Join(*out, "report.html"), []byte(logic.WeverseReportHTML(r)), 0600))
	}
	fmt.Printf("%s，%d 位成员，%d 条活动，回采完整=%t\n", r.DisplayTitle(), len(r.Members), len(r.Events), r.Complete)
	if *send {
		must(logic.SendWeverseReport(cfg, r))
		fmt.Println("报表邮件与附件已发送")
	}
}
