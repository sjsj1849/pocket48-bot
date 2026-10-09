package logic

import "time"

func timeFromMilli(ms int64) time.Time { return time.UnixMilli(ms) }
