package logic

import (
	"testing"

	"pocket48-bot/internal/config"
)

func TestBuildWeiboSuperCountImagesOnlyConfiguredPlans(t *testing.T) {
	sections := []weiboSuperCountHTMLSection{
		{GroupKey: "eight", Title: "八小妹"},
		{GroupKey: "two", Title: "哈two哈"},
		{GroupKey: "three", Title: "第三组"},
		{GroupKey: "four", Title: "第四组"},
	}
	plans := map[string]*config.WeiboSuperCountImageGroupInfo{
		"image-1": {Name: "前两组", GroupKeys: []string{"eight", "two"}},
		"image-2": {Name: "第三组", GroupKeys: []string{"three"}},
	}

	images := buildWeiboSuperCountImages(plans, sections)
	// Only configured plans produce images; unassigned groups are omitted.
	if len(images) != 2 {
		t.Fatalf("got %d images, want exactly the two configured plans", len(images))
	}
	if images[0].Title != "前两组" || len(images[0].Sections) != 2 {
		t.Fatalf("first image mismatch: %#v", images[0])
	}
	if images[1].Title != "第三组" || len(images[1].Sections) != 1 {
		t.Fatalf("second image mismatch: %#v", images[1])
	}
}

func TestBuildWeiboSuperCountImagesKeepsLegacySingleImage(t *testing.T) {
	sections := []weiboSuperCountHTMLSection{{GroupKey: "one"}, {GroupKey: "two"}}
	images := buildWeiboSuperCountImages(nil, sections)
	if len(images) != 1 || len(images[0].Sections) != 2 {
		t.Fatalf("legacy layout changed: %#v", images)
	}
}
