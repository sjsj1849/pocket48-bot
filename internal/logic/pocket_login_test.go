package logic

import "testing"

func TestPocketMobileHelpers(t *testing.T) {
	if !isValidPocketMobile("13800138000") {
		t.Fatal("expected valid mobile to pass")
	}
	if isValidPocketMobile("1380013800") || isValidPocketMobile("not-a-mobile") {
		t.Fatal("expected invalid mobile to fail")
	}
	if got := maskMobile("13800138000"); got != "138****8000" {
		t.Fatalf("unexpected masked mobile: %s", got)
	}
}
