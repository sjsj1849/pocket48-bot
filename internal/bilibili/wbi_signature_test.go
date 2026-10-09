package bilibili

import "testing"

// TestZZWbiVector 用固定的 img/sub key 与 wts 打印签名串，供与 Node 参考实现对照。
func TestZZWbiVector(t *testing.T) {
	img := "7cd084941338484aae1ad9425b84077c"
	sub := "493c2d56d5c063da0b26da5e473e77ea"
	keys := wbiKeys{ImgKey: img, SubKey: sub}
	t.Logf("mixinKey = %s", mixinKey(img, sub))
	// mid=946974&ps=10&pn=1&order=pubdate，固定 wts=1700000000
	got := encWbiAt(map[string]string{
		"mid":   "946974",
		"ps":    "10",
		"pn":    "1",
		"order": "pubdate",
	}, keys, 1700000000)
	t.Logf("encWbiAt = %s", got)
}
