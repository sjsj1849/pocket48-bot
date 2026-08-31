package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/zalando/go-keyring"
	"golang.org/x/term"
)

const service = "pocket48-bot/douyin-live"

func main() {
	account := flag.String("account", "default", "系统凭据库中的账号名称（不是抖音账号）")
	clear := flag.Bool("clear", false, "清除该账号保存的 Cookie")
	status := flag.Bool("status", false, "仅检查该账号是否已保存 Cookie")
	flag.Parse()
	name := strings.TrimSpace(*account)
	if name == "" {
		fatal(errors.New("account 不能为空"))
	}
	if *clear {
		err := keyring.Delete(service, name)
		if err != nil && !errors.Is(err, keyring.ErrNotFound) {
			fatal(err)
		}
		fmt.Printf("已清除系统凭据：%s\n", name)
		return
	}
	if *status {
		_, err := keyring.Get(service, name)
		if errors.Is(err, keyring.ErrNotFound) {
			fmt.Printf("未保存系统凭据：%s\n", name)
			return
		}
		if err != nil {
			fatal(err)
		}
		fmt.Printf("已保存系统凭据：%s\n", name)
		return
	}
	fmt.Fprint(os.Stderr, "请输入完整抖音 Cookie（输入不会回显）：")
	secret, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(os.Stderr)
	if err != nil {
		fatal(err)
	}
	value := strings.TrimSpace(string(secret))
	for i := range secret {
		secret[i] = 0
	}
	if value == "" {
		fatal(errors.New("Cookie 不能为空"))
	}
	if err := keyring.Set(service, name, value); err != nil {
		fatal(err)
	}
	fmt.Printf("Cookie 已保存到系统凭据库：%s\n", name)
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "错误：", err)
	os.Exit(1)
}
