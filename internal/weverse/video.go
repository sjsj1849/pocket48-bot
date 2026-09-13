package weverse

import (
	"context"
	"fmt"
	"net/url"
	"strings"
)

type VideoAttachment struct {
	ID       string `json:"id"`
	CoverURL string `json:"coverUrl,omitempty"`
	URL      string `json:"url,omitempty"`
	Error    string `json:"error,omitempty"`
}

func eventVideos(p Object) []VideoAttachment {
	items := []VideoAttachment{}
	seen := map[string]bool{}
	for _, v := range list(p["orderedAttachments"]) {
		a := obj(v)
		if !strings.EqualFold(str(a["type"]), "video") {
			continue
		}
		d := obj(a["data"])
		id := str(d["videoId"])
		if seen[id] {
			continue
		}
		seen[id] = true
		cover := text(obj(d["uploadInfo"]), "imageUrl")
		if !strings.HasPrefix(cover, "https://") {
			cover = ""
		}
		direct := text(d, "url", "videoUrl")
		if !strings.HasPrefix(direct, "https://") {
			direct = ""
		}
		items = append(items, VideoAttachment{ID: id, CoverURL: cover, URL: direct})
	}
	return items
}
func (c *Client) ResolveEventVideos(ctx context.Context, e *Event) {
	for i := range e.Videos {
		v := &e.Videos[i]
		if v.URL != "" {
			continue
		}
		if !idRE.MatchString(v.ID) {
			v.Error = "视频标识无法解析"
			continue
		}
		var reply Object
		if err := c.call(ctx, "/cvideo/v1.0/cvideo-"+v.ID+"/playInfo?videoId="+url.QueryEscape(v.ID), true, &reply); err != nil {
			v.Error = "视频播放信息暂不可用"
			continue
		}
		var err error
		v.URL, err = videoSource(obj(reply["playInfo"]))
		if err != nil {
			v.Error = err.Error()
		}
	}
}
func videoSource(info Object) (string, error) {
	best := ""
	var quality int64
	for _, item := range list(obj(info["videos"])["list"]) {
		v := obj(item)
		source := str(v["source"])
		if !strings.HasPrefix(source, "https://") {
			continue
		}
		if num(v["size"]) > 50*1024*1024 {
			continue
		}
		encoding := obj(v["encodingOption"])
		if str(encoding["isEncodingComplete"]) == "false" {
			continue
		}
		height := num(encoding["height"])
		if best == "" || height > quality {
			best = source
			quality = height
		}
	}
	if best == "" {
		return "", fmt.Errorf("暂无适合 QQ 转发的视频文件，请打开原帖观看")
	}
	return best, nil
}
