package logic

// 各平台采集侧的视频选档。
//
// 2026-10-04 修复：X 采集与 Instagram 采集都把上限硬编码成
// Bitrate <= 2500000，导致永远只能取到约 720P 那一档，
// 高清档即使存在也永远选不中（和 B 站 platform=html5 锁死 360P 是同类 bug）。
//
// 硬编码码率的问题是它既不反映真实画质，也不反映真实体积：
//   - 同码率下可能有 360P 也有 4K，码率根本不能代表分辨率
//   - 一条 10 秒的短视频可以放心用高码率，一条 10 分钟的不行
//
// 改为：按 DurationMS 估算体积，在预算内按码率择优；
// 全都超预算时退到体积最小的一档，保证至少有内容可发。

import (
	"strings"

	"pocket48-bot/internal/instagram"
	"pocket48-bot/internal/xmonitor"
)

// monitorVideoMaxBytes 是采集侧视频直链的体积预算。
//
// 采集侧和链接提取侧不同：这里只把直链交给 QQ（napcat.VideoSegment），
// 不做本地上传，所以不必卡在飞书的 30MB 上限。
// 25MiB 是权衡 QQ 客户端接收稳定性和画质的结果。
const monitorVideoMaxBytes = 25 << 20

// videoVariant 是各平台视频档位的统一视图。
type videoVariant struct {
	URL     string
	Bitrate int64
}

// pickBestMonitorVideo 按体积预算选最优档，返回选中的地址。
//
// 规则：
//  1. 过滤空地址
//  2. 用码率 × 时长估算体积，保留预算内的档
//  3. 预算内取码率最高的一档
//  4. 全都超预算时取码率最低的一档，宁可糊也不发不出
//
// 返回空字符串表示没有任何可用档。
func pickBestMonitorVideo(variants []videoVariant, durationMS int64) string {
	// seconds 向上取整，避免 10.2 秒被当成 10 秒而略微超预算。
	seconds := (durationMS + 999) / 1000
	if seconds < 1 {
		// 时长未知时按 30 秒估，宁可保守也别把最大的那档放进来。
		seconds = 30
	}

	bestIdx := -1
	fallbackIdx := -1
	for i, v := range variants {
		// 用 TrimSpace 判断而非 == ""，全空白地址同样视为不可用。
		if strings.TrimSpace(v.URL) == "" {
			continue
		}
		if fallbackIdx < 0 || v.Bitrate < variants[fallbackIdx].Bitrate {
			fallbackIdx = i
		}
		if v.Bitrate <= 0 {
			// 码率未知的档不参与择优，只作为最后兜底。
			continue
		}
		if v.Bitrate*seconds/8 > monitorVideoMaxBytes {
			continue
		}
		if bestIdx < 0 || v.Bitrate > variants[bestIdx].Bitrate {
			bestIdx = i
		}
	}
	// 返回时统一 TrimSpace：判定和取值必须一致，
	// 否则「全空白视为不可用」只生效一半。
	if bestIdx >= 0 {
		return strings.TrimSpace(variants[bestIdx].URL)
	}
	if fallbackIdx >= 0 {
		return strings.TrimSpace(variants[fallbackIdx].URL)
	}
	return ""
}

// xVideoURL 返回 X 视频的最佳可用直链。
func xVideoURL(media xmonitor.Media) string {
	return pickBestMonitorVideo(xVariants(media.Variants), media.DurationMS)
}

// xVariants 把 xmonitor.Variant 转成统一视图。
func xVariants(vs []xmonitor.Variant) []videoVariant {
	out := make([]videoVariant, 0, len(vs))
	for _, v := range vs {
		out = append(out, videoVariant{URL: strings.TrimSpace(v.URL), Bitrate: v.Bitrate})
	}
	return out
}

// instagramVideoURL 返回 Instagram 视频的最佳可用直链。
func instagramVideoURL(media instagram.Media) string {
	return pickBestMonitorVideo(igVariants(media.Variants), media.DurationMS)
}

// igVariants 把 instagram.Variant 转成统一视图。
func igVariants(vs []instagram.Variant) []videoVariant {
	out := make([]videoVariant, 0, len(vs))
	for _, v := range vs {
		out = append(out, videoVariant{URL: strings.TrimSpace(v.URL), Bitrate: v.Bitrate})
	}
	return out
}
