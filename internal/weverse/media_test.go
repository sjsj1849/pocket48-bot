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
