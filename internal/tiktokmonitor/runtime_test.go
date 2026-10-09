package tiktokmonitor

import (
	"os"
	"strconv"
	"testing"
	"time"
)

func video(id string, ts int64, dur float64) Video {
	return Video{ID: id, CreateTime: ts, Duration: dur, AuthorName: "hearts2hearts"}
}

// 首轮不能把历史作品全推一遍 —— 这是最容易被忽略的漏推/扰民来源。
func TestPendingSkipsFirstScan(t *testing.T) {
	videos := []Video{
		video("100", 1000, 10),
		video("101", 2000, 20),
	}
	c := Cursor{Username: DefaultUser}
	if got := Pending(c, DefaultUser, videos); got != nil {
		t.Fatalf("首轮应返回 nil（不推历史），实际 %d 条", len(got))
	}
}

// 首轮建立游标后，第二轮只应返回水位线之后的新作品。
func TestPendingReturnsOnlyNewerThanHighWater(t *testing.T) {
	videos := []Video{
		video("100", 1000, 10),
		video("101", 2000, 20),
		video("102", 3000, 30),
	}
	c := Advance(Cursor{}, DefaultUser, videos, []string{"100", "101"})
	if !c.Ready {
		t.Fatal("Advance 后应标记 Ready")
	}
	if c.HighWater != 2000 {
		t.Fatalf("水位线应为 2000，实际 %d", c.HighWater)
	}
	got := Pending(c, DefaultUser, videos)
	if len(got) != 1 || got[0].ID != "102" {
		t.Fatalf("只应返回 102，实际 %+v", got)
	}
}

// 已见但时间相同的不该重复出现（防同秒发布）。
func TestPendingDoesNotRepeatSeen(t *testing.T) {
	videos := []Video{video("100", 1000, 10), video("101", 1000, 10)}
	c := Advance(Cursor{}, DefaultUser, videos, []string{"100", "101"})
	if got := Pending(c, DefaultUser, videos); len(got) != 0 {
		t.Fatalf("都已处理过，不该再推，实际 %+v", got)
	}
}

// 换了监控对象必须重新开始，否则会把旧对象历史当新内容推出去。
func TestPendingResetsOnUsernameChange(t *testing.T) {
	videos := []Video{video("100", 9000, 10)}
	c := Advance(Cursor{}, "someone", videos, []string{"100"})
	if got := Pending(c, "another", videos); got != nil {
		t.Fatalf("换对象后应返回 nil，实际 %+v", got)
	}
}

// 发送失败的作品绝不能推进水位线，否则永久丢失。
func TestAdvanceOnlyMovesForProcessed(t *testing.T) {
	videos := []Video{
		video("100", 1000, 10),
		video("101", 2000, 20),
		video("102", 3000, 30),
	}
	// 只成功发了 100，101 发送失败
	c := Advance(Cursor{}, DefaultUser, videos, []string{"100"})
	if c.HighWater != 1000 {
		t.Fatalf("水位线应停在 1000（只处理了 100），实际 %d", c.HighWater)
	}
	// 下一轮 101 和 102 都该被当成新内容
	got := Pending(c, DefaultUser, videos)
	if len(got) != 2 {
		t.Fatalf("失败的 101 与未处理的 102 都应重推，实际 %d 条: %+v", len(got), got)
	}
	if got[0].ID != "101" || got[1].ID != "102" {
		t.Fatalf("应按时间正序返回 101/102，实际 %+v", got)
	}
}

// 秒数取整口径必须与抖音/B站一致：116.266667 -> 116。
func TestSecondsRoundsToMatchOtherPlatforms(t *testing.T) {
	cases := []struct {
		dur  float64
		want int
	}{
		{116.266667, 116}, // 实测 TikTok 与抖音/B站同一支片子
		{0, 0},
		{-1, 0},
		{7.4, 7},
		{83.5, 84},
	}
	for _, tc := range cases {
		if got := Seconds(Video{Duration: tc.dur}); got != tc.want {
			t.Errorf("Seconds(%v) = %d, want %d", tc.dur, got, tc.want)
		}
	}
}

// Seen 超上限时丢最旧的，且不能把 id 非法的登记进去。
func TestAdvancePrunesAndValidates(t *testing.T) {
	videos := make([]Video, 0, MaxSeen+50)
	processed := make([]string, 0, MaxSeen+50)
	base := time.Now().Unix()
	for i := 0; i < MaxSeen+50; i++ {
		v := video(strconv.FormatInt(7000000000000000000+int64(i), 10), base-int64(i), 10)
		videos = append(videos, v)
		processed = append(processed, v.ID)
	}
	// 混入一个非法 id
	processed = append(processed, "not-a-number")

	c := Advance(Cursor{}, DefaultUser, videos, processed)
	if len(c.Seen) != MaxSeen {
		t.Fatalf("Seen 应裁剪到 %d，实际 %d", MaxSeen, len(c.Seen))
	}
	for _, id := range c.Seen {
		if id == "not-a-number" {
			t.Fatal("非法 id 不应被登记")
		}
	}
	// 最新的那条必须在（base 秒，不裁剪）
	if c.Seen[0] != "7000000000000000000" {
		t.Fatalf("最新的作品应保留在首位，实际 %s", c.Seen[0])
	}
}

// 游标落盘再读回来必须一致 —— 靠这个保证重启后不重复推。
func TestStateRoundTrip(t *testing.T) {
	dir := t.TempDir()
	videos := []Video{video("100", 1000, 10), video("101", 2000, 116.266667)}
	c := Advance(Cursor{}, DefaultUser, videos, []string{"100", "101"})

	st := State{Cursors: map[string]Cursor{DefaultUser: c}}
	if err := st.Save(dir); err != nil {
		t.Fatalf("保存失败: %v", err)
	}
	back := LoadState(dir)
	got := back.Cursors[DefaultUser]
	if got.HighWater != c.HighWater || len(got.Seen) != 2 || !got.Ready {
		t.Fatalf("落盘前后不一致: %+v vs %+v", got, c)
	}
	// 重启后不该把 100/101 再推一遍
	if p := Pending(got, DefaultUser, videos); len(p) != 0 {
		t.Fatalf("重启后不该重推历史，实际 %+v", p)
	}
}

// 状态文件损坏要按空状态处理，不能让监控整体崩掉。
func TestLoadStateToleratesCorruptFile(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir+"/state.json", "{ 这不是合法 JSON")
	st := LoadState(dir)
	if st.Cursors == nil {
		t.Fatal("损坏文件应返回非 nil 的空 Cursors")
	}
	if len(st.Cursors) != 0 {
		t.Fatalf("应为空，实际 %d", len(st.Cursors))
	}
}

func TestNoteFailureDoesNotAdvanceWaterMark(t *testing.T) {
	videos := []Video{video("100", 5000, 10)}
	c := Advance(Cursor{}, DefaultUser, videos, []string{"100"})
	c = NoteFailure(c, DefaultUser, "接口限流")

	if c.HighWater != 5000 {
		t.Fatalf("失败不应改水位线，实际 %d", c.HighWater)
	}
	if c.FailCount != 1 {
		t.Fatalf("应记录 1 次失败，实际 %d", c.FailCount)
	}
	if c.LastError != "接口限流" {
		t.Fatalf("应保留错误原因，实际 %q", c.LastError)
	}
	if c.LastScan.IsZero() {
		t.Fatal("应记录扫描时间")
	}
}

func TestValidateUserName(t *testing.T) {
	ok := []string{"hearts2hearts", "ab", "a.b_c-d", "@hearts2hearts", "user123"}
	for _, s := range ok {
		if err := ValidateUserName(s); err != nil {
			t.Errorf("ValidateUserName(%q) 应通过，实际 %v", s, err)
		}
	}
	bad := []string{"", "@", "a", "with space", "user/name", "../etc", "查询", "a?b=1"}
	for _, s := range bad {
		if err := ValidateUserName(s); err == nil {
			t.Errorf("ValidateUserName(%q) 应拒绝", s)
		}
	}
}

func TestLinkUsesAuthorName(t *testing.T) {
	v := video("7692680350803250438", 1000, 116)
	got := Link(v)
	want := "https://www.tiktok.com/@hearts2hearts/video/7692680350803250438"
	if got != want {
		t.Fatalf("Link = %q, want %q", got, want)
	}
	// 作者名缺失时退回默认账号，别拼出 @/video/ 这种坏链接
	if got := Link(Video{ID: "1"}); got != "https://www.tiktok.com/@hearts2hearts/video/1" {
		t.Fatalf("作者名缺失时链接异常: %q", got)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("写文件失败: %v", err)
	}
}
