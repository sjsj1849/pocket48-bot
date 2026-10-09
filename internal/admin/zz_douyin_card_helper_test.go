package admin

import (
	"os"
)

// readSourceOverviewForTest 读 overview.go 源码（用于断言文案）。
func readSourceOverviewForTest() (string, error) {
	b, err := os.ReadFile("overview.go")
	if err != nil {
		return "", err
	}
	return string(b), nil
}
