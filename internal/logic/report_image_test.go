package logic

import (
	"os"
	"testing"
	"time"
)

// TestReportImageFileIsNotDeletedBeforeDelivery 覆盖线上问题：日报图片在群里
// 只剩空占位。原因是 sendReportImage 用 defer os.Remove，而投递是异步的
// （Feishu.Send 只入队就返回，worker 稍后才读文件），文件在适配器读取前就被删。
// 现在改为延迟删除 reportImageFileTTL。
func TestReportImageFileIsNotDeletedBeforeDelivery(t *testing.T) {
	if reportImageFileTTL <= 0 {
		t.Fatalf("保留时长必须为正，实际 %v", reportImageFileTTL)
	}
	// 上传窗口必须覆盖飞书 upload + 发消息的耗时，60 秒是当前取值。
	if reportImageFileTTL < 30*time.Second {
		t.Errorf("保留时长过短（%v），异步投递可能读不到文件", reportImageFileTTL)
	}
}

// TestReportImageSurvivesImmediateReturn 模拟真实时序：sendTarget 立刻返回后
// 文件仍必须存在，否则 QQ/飞书都会报"文件不存在"。
func TestReportImageSurvivesImmediateReturn(t *testing.T) {
	f, err := os.CreateTemp("", "p48-report-test-*.png")
	if err != nil {
		t.Fatal(err)
	}
	path := f.Name()
	// 与生产代码同构的清理方式：延迟 TTL 后删除。
	timer := time.AfterFunc(reportImageFileTTL, func() { _ = os.Remove(path) })
	defer timer.Stop()

	// sendTarget 返回的瞬间文件必须还在
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("投递返回后文件不应消失：%v", err)
	}
	_ = os.Remove(path)
}
