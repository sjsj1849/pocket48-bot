import unittest
from adapter import primary_tweet_ids, username

class AdapterTests(unittest.TestCase):
    def test_profile_link_and_exact_identification(self):
        self.assertEqual(username('https://x.com/nekomo_st?s=11'), 'nekomo_st')
        self.assertEqual(username('@nekomo_st'), 'nekomo_st')
        with self.assertRaises(ValueError):
            username('https://x.com/nekomo_st/status/123')

    def test_nested_quotes_are_not_independent_events(self):
        payload={'entries':[{'tweet_results':{'result':{'__typename':'Tweet','rest_id':'1999999999999999999','quoted_status_result':{'result':{'__typename':'Tweet','rest_id':'1888888888888888888'}}}}}]}
        self.assertEqual(primary_tweet_ids(payload), {'1999999999999999999'})

if __name__ == '__main__':
    unittest.main()
