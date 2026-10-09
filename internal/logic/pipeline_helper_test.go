package logic

import (
	"os/exec"
	"strings"
)

// runCommand 供 pipeline 测试用的小工具。
func runCommand(name string, args ...string) (string, error) {
	out, err := exec.Command(name, args...).Output()
	return strings.TrimSpace(string(out)), err
}
