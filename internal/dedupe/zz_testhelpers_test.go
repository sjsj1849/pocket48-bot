package dedupe

import "time"

// 固定时间基准：所有相对时刻都从这里算，避免 time.Now() 漂移导致
// 「间隔 < 5 分钟」这类判定在两次运行之间结果不同。
var zzNow = time.Now().UnixMilli()

// zzAgo 返回 n 分钟前的时间戳（毫秒）。
func zzAgo(min int) int64 { return zzNow - int64(min)*60*1000 }

// zzSelf 是测试里的「本条」时间点。
//
// ★ 2026-10-08 由 30 分钟前改为 5 分钟前。
//
//	很多用例把条目登记在 zzAgo(10)、本条时刻取 zzSelf()：
//	30 分钟前 ⇒ gap = 20 分钟，**正好落在 MatchWindowMillis(20 分钟)之外**
//	⇒ 新增的「一级指纹判定也要受窗口约束」上线后全部判不重复。
//	这是真实回归而非测试写错：20 分钟前的同名作品确实不该算重复，
//	所以修的是基准值（让 gap 落回窗口内），不是判据。
//	窗口外那条另有 zzAgo(20) 的用例专门守着（gap = 30 分钟 ⇒ 不判重）。
func zzSelf() int64 { return zzAgo(5) }
