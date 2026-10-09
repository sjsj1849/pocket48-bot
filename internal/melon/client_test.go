package melon

import (
	"context"
	"os"
	"testing"
)

func TestParseAlbumArtistNotes(t *testing.T) {
	data := []byte(`{"response":{"ARTISTNOTELIST":[{"ALBUMID":"11858555","ARTISTID":"4099916","ARTISTNOTE":"각자의 STYLE을 맘껏 뽐내며 살아갑시다!","ARTISTNAME":"지우 (JIWOO)","ARTISTIMG":"https://cdnimg.melon.co.kr/jiwoo.jpg","ISSUEDATE":"2025.06.18"},{"ALBUMID":"11858555","ARTISTID":"4099917","ARTISTNOTE":"많이 들어주세요!","ARTISTNAME":"카르멘 (CARMEN)","ARTISTIMG":"https://cdnimg.melon.co.kr/carmen.jpg","ISSUEDATE":"2025.06.18"}]}}`)
	events, err := parseAlbumArtistNotes(Hearts2HeartsArtistID, "11858555", "https://cdnimg.melon.co.kr/cover.jpg/melon/resize/220", data)
	if err != nil || len(events) != 2 {
		t.Fatalf("events=%#v err=%v", events, err)
	}
	event := events[0]
	if event.Kind != "artist_note" || event.Title != "Artist Note · 지우 (JIWOO)" || event.Time == 0 {
		t.Fatalf("event=%#v", event)
	}
	if event.Images[1] != "https://cdnimg.melon.co.kr/cover.jpg" {
		t.Fatalf("images=%#v", event.Images)
	}
}

func TestParseMusicWaveKeepsConversationOrder(t *testing.T) {
	data := []byte(`{"artistComments":[{"COMMENTSEQ":12,"ARTISTID":4099919,"ARTISTCOMMENT":"{\"imgUrl\":\"https://cdnimg.melon.co.kr/stella.jpg\",\"messageType\":\"artist\",\"regDate\":\"1790337610953\",\"artistId\":\"4099919\",\"artistName\":\"스텔라 (STELLA)\",\"comment\":\"바이\"}","ATTACHIMAGES":[]},{"COMMENTSEQ":11,"ARTISTID":4099918,"ARTISTCOMMENT":"{\"imgUrl\":\"https://cdnimg.melon.co.kr/yuha.jpg\",\"messageType\":\"artist\",\"regDate\":\"1790337603982\",\"artistId\":\"4099918\",\"artistName\":\"유하 (YUHA)\",\"comment\":\"바이이ㅣ\"}","ATTACHIMAGES":[]},{"COMMENTSEQ":13,"ARTISTID":4096106,"ARTISTCOMMENT":"{\"messageType\":\"artist\",\"regDate\":\"1790337620000\",\"artistId\":\"4096106\",\"artistName\":\"Hearts2Hearts\",\"comment\":\"내일 또 만나요\"}","ATTACHIMAGES":[]}]}`)
	events, err := parseMusicWave(Hearts2HeartsArtistID, data)
	if err != nil || len(events) != 2 {
		t.Fatalf("events=%#v err=%v", events, err)
	}
	if events[0].Author != "유하 (YUHA)" || events[1].Author != "스텔라 (STELLA)" || events[0].URL != Hearts2HeartsMusicWaveURL {
		t.Fatalf("conversation order=%#v", events)
	}
}

func TestParseTimeline(t *testing.T) {
	html := []byte(`
<div class="box_act album">
 <p class="box_desc">Moonride 앨범이 발매되었습니다.</p>
 <a href="javascript:melon.link.goAlbumDetail('14531063');"><img src="https://cdnimg.melon.co.kr/a.jpg/melon/resize/104/quality/80" /></a>
 <p class="reg_date"><strong>등록일</strong>2026년 09월 09일</p>
</div><!-- ▲▲▲ 타임라인 앨범발매 ▲▲▲ -->
<div class="box_act vdo">
 <p class="box_desc"><span>Moonride</span> 뮤직비디오가 공개되었습니다.</p>
 <a href="javascript:melon.link.goMvDetail('menu', '50291985', 'video', '');"></a>
 <p class="reg_date">2026년 09월 09일</p>
</div><!-- ▲▲▲ 타임라인 영상/생방송 ▲▲▲ -->`)
	events := parseTimeline(Hearts2HeartsArtistID, html)
	if len(events) != 2 {
		t.Fatalf("events=%d: %#v", len(events), events)
	}
	if events[0].ID != "album:14531063" || events[0].Body != "Moonride 앨범이 발매되었습니다." {
		t.Fatalf("album=%#v", events[0])
	}
	if events[1].ID != "video:50291985" || events[1].Time == 0 {
		t.Fatalf("video=%#v", events[1])
	}
	if events[0].Images[0] != "https://cdnimg.melon.co.kr/a.jpg" {
		t.Fatalf("image=%q", events[0].Images[0])
	}
}

func TestParseTimelineUsesMagazineHeadline(t *testing.T) {
	html := []byte(`
<div class="box_act musicstory">
 <p class="box_desc">멜론매거진이 등록되었습니다.</p>
 <div class="musicstory_wrap">
  <a href="javascript:melon.link.goMstoryDetail(17281);" title="cover" class="wrap_thumb"><img src="https://cdnimg.melon.co.kr/cover.jpg" /></a>
  <div class="musicstory_info"><a href="javascript:melon.link.goMstoryDetail(17281);">9월 차트에서 포착한 다양한 행보&#128099;</a></div>
 </div>
 <p class="reg_date">2026년 09월 30일</p>
</div><!-- ▲▲▲ 멜론매거진 ▲▲▲ -->`)
	events := parseTimeline(Hearts2HeartsArtistID, html)
	if len(events) != 1 {
		t.Fatalf("events=%d: %#v", len(events), events)
	}
	if events[0].ID != "magazine:17281" || events[0].Body != "9월 차트에서 포착한 다양한 행보👣" {
		t.Fatalf("magazine=%#v", events[0])
	}
}

func TestRuntimeFirstScanBuildsBaseline(t *testing.T) {
	sub := Subscription{ArtistID: Hearts2HeartsArtistID, Releases: true, ArtistNotes: true}
	events := []Event{{ID: "album:1", Kind: "album", ArtistID: Hearts2HeartsArtistID}}
	if got := Pending(Cursor{}, sub, events); len(got) != 0 {
		t.Fatalf("first scan pending=%#v", got)
	}
	cursor := Advance(Cursor{}, sub, events)
	if got := Pending(cursor, sub, events); len(got) != 0 {
		t.Fatalf("baseline repeated=%#v", got)
	}
	newEvents := append(events, Event{ID: "album:2", Kind: "album", ArtistID: Hearts2HeartsArtistID})
	if got := Pending(cursor, sub, newEvents); len(got) != 1 || got[0].ID != "album:2" {
		t.Fatalf("new pending=%#v", got)
	}
}

func TestNormalizeArtistID(t *testing.T) {
	got, err := NormalizeArtistID("https://www.melon.com/artist/detail.htm?artistId=4096106")
	if err != nil || got != Hearts2HeartsArtistID {
		t.Fatalf("got=%q err=%v", got, err)
	}
	if _, err := NormalizeArtistID("https://example.com/artist?artistId=4096106"); err == nil {
		t.Fatal("foreign host accepted")
	}
}

func TestLiveHearts2HeartsTimeline(t *testing.T) {
	if os.Getenv("MELON_LIVE_TEST") == "" {
		t.Skip("set MELON_LIVE_TEST=1 to call Melon")
	}
	artist, err := (Client{}).Lookup(context.Background(), Hearts2HeartsArtistID)
	if err != nil {
		t.Fatal(err)
	}
	if artist.Name == "" {
		t.Fatal("empty artist name")
	}
	events, err := (Client{}).Timeline(context.Background(), Hearts2HeartsArtistID)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) == 0 || events[0].ID == "" || events[0].URL == "" {
		t.Fatalf("invalid events: %#v", events)
	}
	foundNote := false
	for _, event := range events {
		if event.Kind == "artist_note" {
			foundNote = true
			break
		}
	}
	if !foundNote {
		t.Fatal("current Hearts2Hearts timeline has no parsed Artist Note")
	}
	wave, err := (Client{}).MusicWave(context.Background(), Hearts2HeartsArtistID)
	if err != nil {
		t.Fatal(err)
	}
	if len(wave) == 0 || wave[0].Kind != "music_wave" || wave[0].Author == "" || wave[0].URL != Hearts2HeartsMusicWaveURL {
		t.Fatalf("invalid Music Wave events: %#v", wave)
	}
}
