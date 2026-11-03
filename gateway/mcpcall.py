#!/usr/bin/env python3
"""最小 MCP HTTP 客户端：mcpcall.py <tool> '<json args>' [url]

默认连网关 http://127.0.0.1:18060/mcp，打印工具返回的 JSON。
- 写操作：设环境变量 XHS_ALLOW_WRITE=yes，客户端会带上 X-XHS-Allow-Write: yes，网关据此放行。
- 调用方名字（网关日志里的 from）：用 XHS_CLIENT_NAME 指定，默认 mcpcall。

例：
  python3 mcpcall.py check_login_status '{}'
  python3 mcpcall.py search_feeds '{"keyword": "咖啡"}'
"""
import json
import os
import sys
import urllib.request

url = sys.argv[3] if len(sys.argv) > 3 else 'http://127.0.0.1:18060/mcp'
client_name = os.environ.get('XHS_CLIENT_NAME', 'mcpcall')


def post(body, sid=None):
    h = {'Content-Type': 'application/json', 'Accept': 'application/json, text/event-stream'}
    if sid:
        h['Mcp-Session-Id'] = sid
    if os.environ.get('XHS_ALLOW_WRITE') == 'yes':
        h['X-XHS-Allow-Write'] = 'yes'
    r = urllib.request.urlopen(urllib.request.Request(url, json.dumps(body).encode(), h), timeout=600)
    raw = r.read().decode()
    if raw.startswith('event:') or raw.startswith('data:'):
        raw = '\n'.join(line[5:] for line in raw.splitlines() if line.startswith('data:'))
    return r.headers.get('Mcp-Session-Id'), (json.loads(raw) if raw.strip() else None)


if __name__ == '__main__':
    if len(sys.argv) < 2:
        sys.exit(__doc__)
    sid, _ = post({'jsonrpc': '2.0', 'id': 1, 'method': 'initialize',
                   'params': {'protocolVersion': '2025-03-26', 'capabilities': {},
                              'clientInfo': {'name': client_name, 'version': '1'}}})
    post({'jsonrpc': '2.0', 'method': 'notifications/initialized'}, sid)
    args = json.loads(sys.argv[2]) if len(sys.argv) > 2 else {}
    _, res = post({'jsonrpc': '2.0', 'id': 2, 'method': 'tools/call',
                   'params': {'name': sys.argv[1], 'arguments': args}}, sid)
    print(json.dumps(res, ensure_ascii=False))
