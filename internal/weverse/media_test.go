package weverse

import (
	"reflect"
	"testing"
)

func TestPhotoAttachments(t *testing.T) {
	p := Object{"postId": "123", "author": Object{"memberId": "artist"}, "orderedAttachments": []any{
		Object{"type": "photo", "data": Object{"url": "https://images.example/a.jpg"}},
		Object{"type": "photo", "data": Object{"url": "https://images.example/b.jpg"}},
		Object{"type": "video", "data": Object{"url": "https://videos.example/c.mp4"}},
		Object{"type": "photo", "data": Object{"url": "http://images.example/unsafe.jpg"}},
	}, "extension": Object{"image": Object{"photos": []any{Object{"url": "https://images.example/a.jpg"}}}}}
	e, err := eventFromPost(p, "hearts2hearts", 235)
	if err != nil {
		t.Fatal(err)
	}
	if e.Body != "" || !reflect.DeepEqual(e.Images, []string{"https://images.example/a.jpg", "https://images.example/b.jpg"}) {
		t.Fatalf("image-only post: %+v", e)
	}
	p["commentId"] = "456"
	comment, err := eventFromComment(p, "123", "hearts2hearts", 235, "")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(comment.Images, e.Images) {
		t.Fatalf("comment attachments: %+v", comment)
	}
}

func TestUploadedVideoAttachmentsAndPlayableFormat(t *testing.T) {
	p := Object{"postId": "1-180305534", "author": Object{"memberId": "artist"}, "body": "☘️💌🩷", "orderedAttachments": []any{Object{"type": "video", "data": Object{"videoId": "3-3005881", "uploadInfo": Object{"imageUrl": "https://images.example/cover.jpg"}}}}}
	e, err := eventFromPost(p, "hearts2hearts", 235)
	if err != nil || e.Kind != "post" || len(e.Videos) != 1 || e.Videos[0].ID != "3-3005881" || len(e.Images) != 0 {
		t.Fatal("video post became text-only", e, err)
	}
	source, err := videoSource(Object{"videos": Object{"list": []any{Object{"source": "https://videos.example/270.mp4", "size": float64(4000000), "encodingOption": Object{"height": float64(270)}}, Object{"source": "https://videos.example/720.mp4", "size": float64(13881378), "encodingOption": Object{"height": float64(720)}}, Object{"source": "https://videos.example/4k.mp4", "size": float64(100000000), "encodingOption": Object{"height": float64(2160)}}}}})
	if err != nil || source != "https://videos.example/720.mp4" {
		t.Fatal("wrong suitable video format", source, err)
	}
}
