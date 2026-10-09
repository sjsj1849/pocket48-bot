import importlib.util
import unittest
from pathlib import Path


MODULE_PATH = Path(__file__).with_name("douyin-native-fetch.py")
SPEC = importlib.util.spec_from_file_location("douyin_native_fetch", MODULE_PATH)
MODULE = importlib.util.module_from_spec(SPEC)
assert SPEC.loader is not None
SPEC.loader.exec_module(MODULE)


class DouyinNativeFetchTest(unittest.TestCase):
    def test_selects_highest_bitrate_variant(self):
        video = {
            "play_addr": {"url_list": ["https://video/fallback.mp4"]},
            "bit_rate": [
                {"bit_rate": 800_000, "play_addr": {"url_list": ["https://video/720.mp4"]}},
                {"bit_rate": 2_400_000, "play_addr": {"url_list": ["https://video/1080.mp4"]}},
                {"bit_rate": 1_600_000, "play_addr": {"url_list": ["https://video/other.mp4"]}},
            ],
        }
        self.assertEqual(
            MODULE.highest_bitrate_video(video),
            ("https://video/1080.mp4", 2_400_000),
        )

    def test_normalizes_watermark_free_image_album(self):
        post = MODULE.normalize_item({
            "aweme_id": "123",
            "aweme_type": 68,
            "images": [{
                "watermark_free_download_url_list": ["https://image/original.jpeg"],
                "url_list": ["https://image/preview.jpeg"],
            }],
        })
        self.assertEqual(post["type"], "note")
        self.assertEqual(post["images"], ["https://image/original.jpeg"])
        self.assertEqual(post["videoUrl"], "")

    def test_extracts_live_photo_motion_as_video(self):
        post = MODULE.normalize_item({
            "aweme_id": "124",
            "aweme_type": 68,
            "image_post_info": {"images": [{
                "display_image": {"url_list": ["https://image/live.webp"]},
                "video": {"bit_rate": [
                    {"bit_rate": 600_000, "play_addr": {"url_list": ["https://video/live-low.mp4"]}},
                    {"bit_rate": 1_200_000, "play_addr": {"url_list": ["https://video/live-high.mp4"]}},
                ]},
            }]},
        })
        self.assertEqual(post["images"], ["https://image/live.webp"])
        self.assertEqual(post["livePhotoVideos"], ["https://video/live-high.mp4"])


if __name__ == "__main__":
    unittest.main()
