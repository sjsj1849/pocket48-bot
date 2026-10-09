package logic

import (
	"reflect"
	"strings"
	"testing"

	"pocket48-bot/internal/config"
)

// 回归：飞书上敲 "bot login sms <手机号>" / "bot code <验证码>" 曾经毫无反应。
//
// 根因不是配置，是 handleFeishuIncoming 只做「抽链接」，抽不到就 return，
// 从来没有任何命令分派 —— QQ 那套 CmdRegistry 根本没接到飞书。
func TestParseFeishuCommandAcceptsBotPrefixedForms(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"bot login sms 17508287057", []string{"login", "sms", "17508287057"}},
		{"BOT login SMS 17508287057", []string{"login", "sms", "17508287057"}},
		{"Bot Login Sms 17508287057", []string{"login", "sms", "17508287057"}},
		{"login sms 17508287057", []string{"login", "sms", "17508287057"}},
		{"bot code 123456", []string{"code", "123456"}},
		{"code 123456", []string{"code", "123456"}},
		{"  bot   login   sms   17508287057  ", []string{"login", "sms", "17508287057"}},
		{"bot whoami", []string{"whoami"}},
	}
	for _, c := range cases {
		got, ok := parseFeishuCommand(c.in)
		if !ok {
			t.Errorf("parseFeishuCommand(%q) 未识别为命令", c.in)
			continue
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("parseFeishuCommand(%q) = %v，期望 %v", c.in, got, c.want)
		}
	}
}

func TestParseFeishuCommandAcceptsHelp(t *testing.T) {
	for _, in := range []string{"bot help", "help", "帮助", "?", "bot 帮助"} {
		got, ok := parseFeishuCommand(in)
		if !ok || len(got) == 0 || got[0] != "help" {
			t.Errorf("parseFeishuCommand(%q) = %v/%v，期望 help", in, got, ok)
		}
	}
}

// 闲聊和纯链接绝不能被当成命令吞掉 —— 链接要走提取链路。
func TestParseFeishuCommandRejectsNonCommands(t *testing.T) {
	for _, in := range []string{
		"",
		"   ",
		"https://weibo.com/6694957538/5351887681618773",
		"帮我看看这个链接",
		"bot",
		"bot 不存在的命令 123",
		"botunited",
	} {
		if got, ok := parseFeishuCommand(in); ok {
			t.Errorf("parseFeishuCommand(%q) 被误判为命令：%v", in, got)
		}
	}
}

// 群里 @ 机器人 会带占位符，必须先剥掉再解析命令。
func TestParseFeishuCommandStripsMentions(t *testing.T) {
	cases := []string{
		"<at user_id=\"ou_x\">Bot</at> login sms 17508287057",
		"<at id=\"ou_x\"></at> bot login sms 17508287057",
		"@_user_1 bot code 123456",
	}
	for _, in := range cases {
		got, ok := parseFeishuCommand(in)
		if !ok {
			t.Errorf("parseFeishuCommand(%q) 未识别为命令", in)
			continue
		}
		if strings.Contains(strings.Join(got, " "), "ou_x") || strings.Contains(strings.Join(got, " "), "_user_") {
			t.Errorf("parseFeishuCommand(%q) = %v，@ 占位符没剥干净", in, got)
		}
	}
}

// 白名单取自 FEISHU_PRIVATE_ROUTES（QQ 号=open_id），不新增配置项，
// 两个入口不会出现「A 是管理员 B 不是」的分叉。
func TestFeishuAdminIDByOpenID(t *testing.T) {
	b := &Bot{cfg: &config.Config{
		FeishuPrivateRoutes: "2063428750=ou_62e0b39938b2e1309ad569bcf5b092ca, 3808515247=ou_abc",
	}}
	if id, ok := b.feishuAdminIDByOpenID("ou_62e0b39938b2e1309ad569bcf5b092ca"); !ok || id != 2063428750 {
		t.Errorf("反查失败：%d/%v，期望 2063428750", id, ok)
	}
	if id, ok := b.feishuAdminIDByOpenID("ou_abc"); !ok || id != 3808515247 {
		t.Errorf("第二个管理员反查失败：%d/%v", id, ok)
	}
	if _, ok := b.feishuAdminIDByOpenID("ou_unknown"); ok {
		t.Error("陌生 open_id 不该通过白名单")
	}
	if _, ok := b.feishuAdminIDByOpenID(""); ok {
		t.Error("空 open_id 不该通过白名单")
	}
}
