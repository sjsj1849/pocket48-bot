#!/usr/bin/env python3
"""Recover mailbox access, then verify X using only a fresh matching email."""
import contextlib
import datetime
import fcntl
import json
import os
from pathlib import Path
import re
import subprocess
import sys
import tempfile
import time

ROOT = Path(__file__).resolve().parents[2]
STORAGE = ROOT / 'storage/x'
sys.path.insert(0, os.environ.get('AGENTLY_SCRIPT_DIR', '/root/.hermes/scripts'))
import agently_important_mail as mail
from agently_mail_runtime import gate, MailboxCLIError


def write_private(path, value):
    path.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
    fd, temporary = tempfile.mkstemp(dir=path.parent, prefix='.x-login-')
    try:
        with os.fdopen(fd, 'w') as stream:
            json.dump(value, stream, ensure_ascii=False)
        os.replace(temporary, path)
    finally:
        if os.path.exists(temporary):
            os.unlink(temporary)


def status(phase, message, **extra):
    value = {'phase': phase, 'message': message, 'lastCheck': int(time.time()*1000), **extra}
    write_private(STORAGE / 'login-status.json', value)
    print(f'X login phase={phase}', flush=True)


def rpc(command):
    result = subprocess.run(['node', str(ROOT / 'sidecar/x-monitor/browser-rpc.cjs'), command], capture_output=True, text=True, timeout=85)
    if result.returncode:
        raise RuntimeError('browser_rpc_failed')
    return json.loads(result.stdout)


def timestamp(value):
    try:
        parsed = datetime.datetime.fromisoformat(str(value).replace('Z', '+00:00'))
        if parsed.tzinfo is None:
            parsed = parsed.replace(tzinfo=datetime.timezone.utc)
        return parsed.timestamp()
    except (ValueError, TypeError):
        return 0


def recipients(message):
    value = message.get('to') or []
    if isinstance(value, str):
        return {part.lower() for part in re.findall(r'[\w.+-]+@[\w.-]+', value)}
    return {str(item.get('email') if isinstance(item, dict) else item).lower() for item in value}


def from_x(message):
    sender = message.get('from') or {}
    address = sender.get('email', '') if isinstance(sender, dict) else str(sender)
    domain = address.rsplit('@', 1)[-1].strip('> ').lower()
    return domain == 'x.com' or domain.endswith('.x.com') or domain == 'twitter.com' or domain.endswith('.twitter.com')


def verification_code(message):
    text = mail.strip_html(' '.join(str(message.get(key) or '') for key in ['subject', 'snippet', 'body']))
    if not re.search(r'verification|confirmation|security|one[- ]time|\bcode\b|验证码|验证代码', text, re.I):
        return None
    candidates = re.findall(r'(?:验证码|验证代码|(?:verification|confirmation|security)?\s*code)(?:\s*(?:is|为|是|:|：))?\s*([^\w]{0,8})([A-Za-z0-9]{6})\b', text, re.I)
    codes = {value for _, value in candidates if value.lower() not in {'within', 'please', 'verify', 'emails', 'should'}}
    if not codes:
        codes = set(re.findall(r'(?<![A-Za-z0-9])\d{6}(?![A-Za-z0-9])', text))
    return next(iter(codes)) if len(codes) == 1 else None


def find_code(messages, email, requested_at, already_read):
    for metadata in reversed(messages):
        received = timestamp(metadata.get('created_at'))
        if received < requested_at - 2 or received > time.time() + 60 or not from_x(metadata):
            continue
        identity = metadata.get('message_id')
        if not identity or identity in already_read:
            continue
        target_matches = email.lower() in recipients(metadata)
        code = verification_code(metadata) if target_matches else None
        if code:
            return code, received
        message = mail.read_message(identity)
        # Do not mark a failed read seen: read_message raises on API errors.
        already_read.add(identity)
        received = timestamp(message.get('created_at')) or received
        if email.lower() not in recipients(message) or not from_x(message) or received < requested_at - 2:
            continue
        code = verification_code(message)
        if code:
            return code, received
    return None


@contextlib.contextmanager
def mailbox_for_verification():
    # Keep the short OTP window free of background watch/list requests.
    active = subprocess.run(['systemctl', 'is-active', '--quiet', 'agently-important-mail-watch']).returncode == 0
    if active:
        subprocess.run(['systemctl', 'stop', 'agently-important-mail-watch'], check=True)
    try:
        yield
    finally:
        if active:
            subprocess.run(['systemctl', 'start', 'agently-important-mail-watch'], check=True)


def save_session():
    result = rpc('x_panel_sync')
    session = result.get('session') or {}
    cookies = session.get('cookies') or []
    if {cookie.get('name') for cookie in cookies} != {'auth_token', 'ct0'}:
        return False
    session['updatedAt'] = int(time.time()*1000)
    write_private(STORAGE / 'session.json', session)
    write_private(STORAGE / 'login-account.json', {'email': json.loads((STORAGE / 'login-input.json').read_text())['email']})
    status('authenticated', 'X 已登录，登录态已保存，正在验证采集。')
    # A successful login must never be repeated merely because collection fails.
    request = json.dumps({'operation': 'lookup', 'query': 'nekomo_st'})
    try:
        result = subprocess.run([str(ROOT / 'sidecar/x-monitor/.venv/bin/python'), str(ROOT / 'sidecar/x-monitor/collector.py')], input=request, capture_output=True, text=True, timeout=105)
        data = json.loads(result.stdout)
        if data.get('ok') and (data.get('data') or {}).get('user'):
            write_private(STORAGE / 'login-test.json', data['data'])
            status('authenticated', 'X 已登录，已验证可以读取 @nekomo_st 的用户资料。', lookupVerified=True)
        else:
            status('authenticated', 'X 已登录，登录态已保存；时间线采集仍需进一步验证。', lookupVerified=False)
    except Exception:
        status('authenticated', 'X 已登录，登录态已保存；时间线采集仍需进一步验证。', lookupVerified=False)
    (STORAGE / 'login-code.json').unlink(missing_ok=True)
    (STORAGE / 'login-input.json').unlink(missing_ok=True)
    return True


def main():
    STORAGE.mkdir(parents=True, exist_ok=True, mode=0o700)
    os.chmod(STORAGE, 0o700)
    lock = os.open(STORAGE / 'login-worker.lock', os.O_RDWR | os.O_CREAT, 0o600)
    try:
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
    except BlockingIOError:
        return
    credentials = json.loads((STORAGE / 'login-input.json').read_text())
    email = credentials['email']
    try:
        if save_session():
            return
    except Exception:
        pass
    attempts = 0
    while attempts < 3:
        status('waiting_mail_api', '正在确认邮箱可读，恢复后自动读取新验证码并登录 X。', nextRetryAt=int((time.time()+gate.remaining())*1000))
        try:
            mail.list_messages(limit=20)
        except MailboxCLIError as error:
            if gate.remaining() <= 0:
                gate.failure(error)
            message = '邮箱接口仍在限流，等待恢复后自动读取新验证码登录 X。' if error.code == 7 else '邮箱暂时无法读取，稍后重试自动登录 X。'
            if error.code == 3:
                message = '邮箱授权需要更新，授权恢复后继续自动登录 X。'
            status('waiting_mail_api', message, errorCode=error.code, nextRetryAt=int((time.time()+gate.remaining())*1000))
            gate.wait()
            continue
        attempts += 1
        with mailbox_for_verification():
            status('logging_in', '邮箱可以读取，正在为 X 登录请求新验证码。', attempts=attempts)
            requested_at = time.time()
            result = rpc('x_panel_login')
            stage = result.get('stage')
            if stage == 'authenticated' and save_session():
                return
            if stage != 'awaiting_code':
                status(stage or 'browser_error', 'X 网页要求进一步验证，请检查面板浏览器；不会重复请求验证码。')
                return
            status('awaiting_code', '正在等待发给此 X 账号邮箱的新验证码，收到后自动验证。', requestedAt=int(requested_at*1000))
            deadline = requested_at + 480
            already_read = set()
            while time.time() < deadline:
                if gate.remaining() > deadline-time.time():
                    break
                try:
                    messages = mail.list_messages(limit=20)
                    match = find_code(messages, email, requested_at, already_read)
                except MailboxCLIError:
                    continue
                if match:
                    code, received = match
                    write_private(STORAGE / 'login-code.json', {'code': code, 'createdAt': int(received*1000)})
                    result = rpc('x_panel_verify')
                    (STORAGE / 'login-code.json').unlink(missing_ok=True)
                    if result.get('stage') == 'authenticated' and save_session():
                        return
                    status(result.get('stage') or 'browser_error', '验证码已提交，X 仍要求进一步验证；不会重复提交。')
                    return
                time.sleep(min(60, max(0, deadline-time.time())))
        status('waiting_mail_api', '本次未取得有效验证码，稍后重新确认邮箱可读；不会使用旧验证码。')
        time.sleep(1800)
    status('mail_forwarding_check_required', '邮箱恢复可读，但没有收到此账号的新验证码，需要核对这个邮箱的中转设置。')


if __name__ == '__main__':
    try:
        main()
    except Exception as error:
        # Never log exception messages, email contents, passwords or cookies.
        status('worker_error', '自动登录流程出现异常，已保留浏览器页面供检查。', errorClass=type(error).__name__)
        raise SystemExit(1)
