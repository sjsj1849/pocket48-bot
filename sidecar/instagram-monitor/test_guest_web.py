import json
import unittest
from types import SimpleNamespace
from unittest.mock import Mock

import guest_web as g


class GuestWebTests(unittest.TestCase):
    def response(self, text, status=200, url="https://www.instagram.com/artist/"):
        return SimpleNamespace(text=text, status_code=status, url=url)

    def test_reads_embedded_public_profile(self):
        data = {"require": [{"xig_user_by_username": {
            "id": "42", "username": "Artist", "full_name": "Artist Name",
            "profile_pic_url": "https://cdn/avatar.jpg", "is_private": False,
        }}]}
        html = ('<script>"profile_id":"42","LSD",[],{"token":"token-1"}</script>'
                f'<script type="application/json" data-sjs>{json.dumps(data)}</script>')
        session = Mock()
        session.request.return_value = self.response(html)
        page = g.public_profile(session, "artist")
        self.assertEqual(page["id"], "42")
        self.assertEqual(page["user"]["name"], "Artist Name")
        self.assertFalse(page["user"]["protected"])

    def test_posts_use_current_web_doc_and_preserve_raw_media(self):
        item = {"pk": "123", "code": "abc", "taken_at": 100, "media_type": 1,
                "user": {"pk": "42", "username": "artist", "full_name": "Artist",
                         "is_private": False, "profile_pic_url": "https://cdn/avatar.jpg"},
                "image_versions2": {"candidates": [{"url": "https://cdn/a.jpg", "width": 100, "height": 100}]}}
        payload = {"data": {"xdt_api__v1__feed__user_timeline_graphql_connection": {"edges": [{"node": item}]}}}
        session = Mock()
        session.request.return_value = self.response(json.dumps(payload), url="https://www.instagram.com/graphql/query")
        posts = list(g.public_posts(session, "artist", {"id": "42", "lsd": "token"}))
        self.assertEqual(posts[0]._node["iphone_struct"]["pk"], "123")
        _, _, kwargs = session.request.mock_calls[0]
        self.assertEqual(kwargs["data"]["doc_id"], g.PROFILE_POSTS_DOC_ID)

    def test_malformed_graphql_is_not_an_empty_feed(self):
        session = Mock()
        session.request.return_value = self.response("{}", url="https://www.instagram.com/graphql/query")
        with self.assertRaises(g.GuestWebError) as error:
            list(g.public_posts(session, "artist", {"id": "42", "lsd": "token"}))
        self.assertEqual(error.exception.code, "guest_unavailable")


if __name__ == "__main__":
    unittest.main()
