// Command probe_video calls the real cvideo playInfo endpoint for a known video
// id through the production resolver, so we can tell whether the missing direct
// URL is an API-shape problem or a delivery-side problem.
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"pocket48-bot/internal/weverse"
)

func main() {
	videoID := os.Args[1]
	dir := weverse.Dir("config.yaml")

	client := weverse.NewClient(dir, "")
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	e := weverse.Event{Kind: "post", PostID: "probe", Videos: []weverse.VideoAttachment{{ID: videoID}}}
	client.ResolveEventVideos(ctx, &e)

	for i, v := range e.Videos {
		fmt.Printf("videos[%d]: id=%s\n", i, v.ID)
		fmt.Printf("  URL      = %q\n", v.URL)
		fmt.Printf("  CoverURL = %q\n", v.CoverURL)
		fmt.Printf("  Error    = %q\n", v.Error)
		if v.URL != "" {
			fmt.Println("  => 有直链，可发视频")
		} else {
			fmt.Println("  => 无直链，只能发封面")
		}
	}
}