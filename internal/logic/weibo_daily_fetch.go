package logic

import (
	"fmt"
	"pocket48-bot/internal/monitor"
	"time"
)

// Never use a response completed after midnight as yesterday's daily count.
func fetchWeiboDailyCount(deadline time.Time, fetch func() (*monitor.WeiboSuperCountResult, error)) (*monitor.WeiboSuperCountResult, error) {
	attempts := 1
	if !deadline.IsZero() {
		attempts = 3
	}
	var lastErr error
	for n := 0; n < attempts; n++ {
		if !deadline.IsZero() && !time.Now().Before(deadline) {
			return nil, fmt.Errorf("当日采集窗口已结束，未使用次日数据")
		}
		result, err := fetch()
		if !deadline.IsZero() && !time.Now().Before(deadline) {
			return nil, fmt.Errorf("响应超过当日采集截止时间，未使用次日数据")
		}
		if err == nil {
			return result, nil
		}
		lastErr = err
		if n+1 < attempts {
			time.Sleep(time.Second)
		}
	}
	return nil, lastErr
}
