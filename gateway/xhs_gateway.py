#!/usr/bin/env python3
"""xiaohongshu-mcp 全局单例网关。

所有客户端只连网关（默认 127.0.0.1:18060/mcp）。真正的 xiaohongshu-mcp 跑在上游端口
（默认 127.0.0.1:18061），只给网关用。

规则：
1. 串行：同一时刻只放行一个 tools/call。背后只有一个浏览器，并发会互相打断。
2. 节奏：上一个操作结束后，至少随机间隔 MIN_GAP～MAX_GAP 秒才放下一个。
3. 扫码保护：调用 get_login_qrcode 之后进入登录窗口（最长 LOGIN_WINDOW 秒），期间其他操作直接报错返回；
   上游日志里出现「扫码登录成功」就提前结束窗口。
4. 写操作（发布、评论、回复、点赞、收藏、删 cookie）默认拒绝；请求带 X-XHS-Allow-Write: yes 才放行。
   建议只在人工逐条确认后再加这个头。
5. 排队超时：等了 QUEUE_TIMEOUT 秒还没轮到就返回错误，不无限挂起。

initialize、notifications、GET 流这类非工具调用直接透传，不排队。

配置（环境变量，都有默认值）：
  XHS_GATEWAY_LISTEN         网关监听地址，默认 127.0.0.1:18060
  XHS_GATEWAY_UPSTREAM       上游 xiaohongshu-mcp 地址，默认 127.0.0.1:18061
  XHS_GATEWAY_MIN_GAP        两次操作之间的最小间隔（秒），默认 5
  XHS_GATEWAY_MAX_GAP        两次操作之间的最大间隔（秒），默认 12
  XHS_GATEWAY_LOGIN_WINDOW   扫码登录窗口（秒），默认 250
  XHS_GATEWAY_QUEUE_TIMEOUT  排队超时（秒），默认 900
  XHS_MCP_STDERR_LOG         上游的 stderr 日志，用来发现「扫码登录成功」，
                             默认 ~/Library/Logs/xiaohongshu-mcp/stderr.log
"""
import http.client
import json
import os
import random
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer


def _addr(env, default):
    host, _, port = os.environ.get(env, default).rpartition(':')
    return (host or '127.0.0.1', int(port))


LISTEN = _addr('XHS_GATEWAY_LISTEN', '127.0.0.1:18060')
UPSTREAM = _addr('XHS_GATEWAY_UPSTREAM', '127.0.0.1:18061')
MIN_GAP = float(os.environ.get('XHS_GATEWAY_MIN_GAP', '5'))
MAX_GAP = float(os.environ.get('XHS_GATEWAY_MAX_GAP', '12'))
LOGIN_WINDOW = float(os.environ.get('XHS_GATEWAY_LOGIN_WINDOW', '250'))
QUEUE_TIMEOUT = float(os.environ.get('XHS_GATEWAY_QUEUE_TIMEOUT', '900'))
UPSTREAM_LOG = os.path.expanduser(
    os.environ.get('XHS_MCP_STDERR_LOG', '~/Library/Logs/xiaohongshu-mcp/stderr.log'))
WRITE_TOOLS = {'publish_content', 'publish_with_video', 'post_comment_to_feed', 'reply_comment_in_feed',
               'like_feed', 'favorite_feed', 'delete_cookies'}
HOP = {'host', 'content-length', 'connection', 'transfer-encoding', 'keep-alive'}

lock = threading.Lock()
state = {'next_ok': 0.0, 'login_until': 0.0, 'login_log_pos': 0}
clients = {}  # Mcp-Session-Id -> clientInfo.name


def log(msg):
    print(time.strftime('%Y-%m-%d %H:%M:%S'), msg, flush=True)


def login_active():
    if time.time() >= state['login_until']:
        return False
    try:
        with open(UPSTREAM_LOG, 'rb') as f:
            f.seek(state['login_log_pos'])
            if '扫码登录成功'.encode() in f.read():
                state['login_until'] = 0
                log('login window closed: 扫码登录成功')
                return False
    except OSError:
        pass
    return True


def rpc_error(rid, msg):
    return json.dumps({'jsonrpc': '2.0', 'id': rid, 'error': {'code': -32000, 'message': msg}},
                      ensure_ascii=False).encode()


class H(BaseHTTPRequestHandler):
    protocol_version = 'HTTP/1.1'

    def log_message(self, *a):
        pass

    def _forward(self, method, body):
        conn = http.client.HTTPConnection(*UPSTREAM, timeout=900)
        headers = {k: v for k, v in self.headers.items()
                   if k.lower() not in HOP and not k.lower().startswith('x-xhs-')}
        if body is not None:
            headers['Content-Length'] = str(len(body))
        conn.request(method, self.path, body=body, headers=headers)
        resp = conn.getresponse()
        self.send_response(resp.status)
        for k, v in resp.getheaders():
            if k.lower() not in HOP:
                self.send_header(k, v)
        self.send_header('Transfer-Encoding', 'chunked')
        self.end_headers()
        while True:
            chunk = resp.read1(65536) if hasattr(resp, 'read1') else resp.read(65536)
            if not chunk:
                break
            self.wfile.write(b'%x\r\n%s\r\n' % (len(chunk), chunk))
            self.wfile.flush()
        self.wfile.write(b'0\r\n\r\n')
        conn.close()
        return resp

    def _reply(self, payload):
        self.send_response(200)
        self.send_header('Content-Type', 'application/json')
        self.send_header('Content-Length', str(len(payload)))
        self.end_headers()
        self.wfile.write(payload)

    def do_GET(self):
        self._forward('GET', None)

    def do_DELETE(self):
        self._forward('DELETE', None)

    def do_POST(self):
        body = self.rfile.read(int(self.headers.get('Content-Length') or 0))
        try:
            msg = json.loads(body or b'{}')
        except ValueError:
            msg = {}
        if isinstance(msg, dict) and msg.get('method') == 'initialize':
            resp = self._forward('POST', body)
            sid = resp.getheader('Mcp-Session-Id')
            if sid:
                clients[sid] = msg.get('params', {}).get('clientInfo', {}).get('name', '?')
            return
        if not (isinstance(msg, dict) and msg.get('method') == 'tools/call'):
            return self._forward('POST', body)

        rid = msg.get('id')
        tool = msg.get('params', {}).get('name', '')
        who = clients.get(self.headers.get('Mcp-Session-Id'), '?')
        if tool in WRITE_TOOLS and self.headers.get('X-XHS-Allow-Write', '').lower() != 'yes':
            log(f'DENY write {tool} from {who}')
            return self._reply(rpc_error(
                rid, f'网关拦截：{tool} 是写操作，需要人工确认后带 X-XHS-Allow-Write: yes 请求头再调用'))

        t0 = time.time()
        if not lock.acquire(timeout=QUEUE_TIMEOUT):
            return self._reply(rpc_error(rid, '网关排队超时，小红书服务正忙，请稍后重试'))
        try:
            if tool != 'get_login_qrcode' and login_active():
                return self._reply(rpc_error(rid, '网关：正在等扫码登录，暂停其他操作，请几分钟后重试'))
            wait = state['next_ok'] - time.time()
            if wait > 0:
                time.sleep(wait)
            log(f'RUN {tool} from {who} (queued {time.time() - t0:.1f}s)')
            if tool == 'get_login_qrcode':
                try:
                    state['login_log_pos'] = os.path.getsize(UPSTREAM_LOG)
                except OSError:
                    state['login_log_pos'] = 0
            self._forward('POST', body)
            if tool == 'get_login_qrcode':
                state['login_until'] = time.time() + LOGIN_WINDOW
                log('login window opened')
        finally:
            state['next_ok'] = time.time() + random.uniform(MIN_GAP, MAX_GAP)
            lock.release()


if __name__ == '__main__':
    log(f'xhs gateway listening {LISTEN} -> upstream {UPSTREAM}')
    ThreadingHTTPServer(LISTEN, H).serve_forever()
