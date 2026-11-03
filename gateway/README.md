# 全局单例网关

给 xiaohongshu-mcp 加的一层本地网关，一个 Python 文件，只用标准库。

## 为什么要它

xiaohongshu-mcp 背后只有一个浏览器。只有一个客户端时没问题，一旦有好几个 Agent 或定时任务同时调用（发帖、回评论、搜索、拉数据），就会出现这些问题：

- 两个操作抢同一个浏览器，页面跳走，前一个操作失败。
- 操作一个接一个、没有停顿地连续打出去。
- 扫码登录时，别的任务把二维码页面刷掉，扫不上。
- 某个 Agent 一时判断失误，直接发了帖或评论，撤不回来。

网关把所有调用收拢到一个入口，排队执行。

## 它做什么

```
Agent / 定时任务 ──► 网关 127.0.0.1:18060 ──► xiaohongshu-mcp 127.0.0.1:18061 ──► 浏览器 ──► 小红书
```

1. **串行**：同一时刻只放行一个 `tools/call`，其他的排队。
2. **节奏**：上一个操作结束后，随机等 5～12 秒才放下一个。
3. **扫码保护**：调用 `get_login_qrcode` 后进入登录窗口（最长 250 秒），期间其他操作直接返回错误；上游日志里出现「扫码登录成功」就提前结束。
4. **写操作闸门**：`publish_content`、`publish_with_video`、`post_comment_to_feed`、`reply_comment_in_feed`、`like_feed`、`favorite_feed`、`delete_cookies` 默认拒绝。请求带 `X-XHS-Allow-Write: yes` 才放行。建议只在人工逐条确认后再加这个头。
5. **排队超时**：等 900 秒还没轮到就返回错误，不会无限挂起。

`initialize`、通知、GET 流这类非工具调用直接透传，不排队。

日志每次调用一行，能看出是谁调的、排了多久：

```
2026-11-03 14:20:17 RUN search_feeds from p1 (queued 0.1s)
2026-11-03 14:20:18 RUN list_feeds from p2 (queued 1.3s)
2026-11-03 14:20:16 DENY write publish_content from t2
```

`from` 后面是客户端在 `initialize` 里报的 `clientInfo.name`。

## 怎么用

1. 让 xiaohongshu-mcp 改听上游端口：

   ```bash
   xiaohongshu-mcp -headless=true -port=127.0.0.1:18061
   ```

2. 启动网关（Python 3.8 以上）：

   ```bash
   python3 gateway/xhs_gateway.py
   ```

3. 所有客户端改连 `http://127.0.0.1:18060/mcp`。要做写操作时，在请求头里加 `X-XHS-Allow-Write: yes`。

4. 用自带的小客户端试一下：

   ```bash
   python3 gateway/mcpcall.py check_login_status '{}'
   XHS_ALLOW_WRITE=yes python3 gateway/mcpcall.py like_feed '{"feed_id": "...", "xsec_token": "..."}'
   ```

## 配置

都用环境变量，不设就用默认值。

| 变量 | 默认值 | 说明 |
|---|---|---|
| `XHS_GATEWAY_LISTEN` | `127.0.0.1:18060` | 网关监听地址 |
| `XHS_GATEWAY_UPSTREAM` | `127.0.0.1:18061` | 上游 xiaohongshu-mcp 地址 |
| `XHS_GATEWAY_MIN_GAP` | `5` | 两次操作之间的最小间隔（秒） |
| `XHS_GATEWAY_MAX_GAP` | `12` | 两次操作之间的最大间隔（秒） |
| `XHS_GATEWAY_LOGIN_WINDOW` | `250` | 扫码登录窗口（秒） |
| `XHS_GATEWAY_QUEUE_TIMEOUT` | `900` | 排队超时（秒） |
| `XHS_MCP_STDERR_LOG` | `~/Library/Logs/xiaohongshu-mcp/stderr.log` | 上游的 stderr 日志，用来发现「扫码登录成功」 |

`mcpcall.py` 用两个变量：`XHS_ALLOW_WRITE=yes` 时带上写操作请求头；`XHS_CLIENT_NAME` 是日志里显示的调用方名字，默认 `mcpcall`。

## macOS 开机常驻

`launchd/` 里有两份 plist 模板，一份给 xiaohongshu-mcp，一份给网关。把里面的 `__HOME__`、`__PYTHON__`、`__REPO__` 换成你自己的路径，再装上：

```bash
sed -e "s#__HOME__#$HOME#g" -e "s#__PYTHON__#$(command -v python3)#g" -e "s#__REPO__#$(pwd)#g" \
  gateway/launchd/local.xhs-gateway.plist > ~/Library/LaunchAgents/local.xhs-gateway.plist
mkdir -p ~/Library/Logs/xiaohongshu-mcp
launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/local.xhs-gateway.plist
```

xiaohongshu-mcp 那份同理。如果 `bootstrap` 报 `5: Input/output error`，多半是这个服务以前被 disable 过，先执行 `launchctl enable gui/$(id -u)/<Label>` 再装。

## 实际使用

2026-10 起在一个真实账号的日常运营里使用：发帖、回评论、互动、扫描、日报等多个定时任务共用一个浏览器，全部经这个网关排队。
