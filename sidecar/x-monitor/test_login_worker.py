import importlib.util
from pathlib import Path
import time
import json
import tempfile
import unittest
from unittest.mock import patch
spec=importlib.util.spec_from_file_location('login_worker',Path(__file__).with_name('login-worker.py'))
worker=importlib.util.module_from_spec(spec)
spec.loader.exec_module(worker)

class LoginTests(unittest.TestCase):
 def test_upstream_login_block_stops_mail_and_browser_requests(self):
  with tempfile.TemporaryDirectory() as directory:
   storage=Path(directory)
   (storage/'login-status.json').write_text(json.dumps({'phase':'login_temporarily_blocked'}))
   with patch.object(worker,'STORAGE',storage),patch.object(worker.mail,'list_messages') as mail_read,patch.object(worker,'rpc') as browser_rpc:
    worker.main()
    mail_read.assert_not_called()
    browser_rpc.assert_not_called()

 def test_code_formats_and_ambiguity(self):
  self.assertEqual(worker.verification_code({'subject':'Your X confirmation code is 123456'}),'123456')
  self.assertEqual(worker.verification_code({'body':'<p>Verification code:</p><b>A2bcD3</b>'}),'A2bcD3')
  self.assertIsNone(worker.verification_code({'body':'Use code within ten minutes. 123456 or 654321'}))
  self.assertIsNone(worker.verification_code({'body':'This is an ordinary message.'}))

 def test_target_sender_and_freshness_are_required(self):
  now=time.time()
  def message(address,age=0,sender='info@x.com'):
   return {'message_id':'test','created_at':time.strftime('%Y-%m-%dT%H:%M:%SZ',time.gmtime(now-age)),'to':[{'email':address}],'from':{'email':sender},'subject':'Your confirmation code is 123456'}
  with patch.object(worker.mail,'read_message',return_value=message('other@example.com')):
   self.assertIsNone(worker.find_code([message('other@example.com')],'target@example.com',now,set()))
  with patch.object(worker.mail,'read_message',side_effect=AssertionError('unnecessary read')):
   self.assertIsNone(worker.find_code([message('target@example.com',age=120)],'target@example.com',now,set()))
   self.assertIsNone(worker.find_code([message('target@example.com',sender='info@evilx.com')],'target@example.com',now,set()))
   result=worker.find_code([message('target@example.com')],'target@example.com',now,set())
   self.assertEqual(result[0],'123456')

if __name__=='__main__':unittest.main()
