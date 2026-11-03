# 这个修改版改了什么

基于 [xpzouying/xiaohongshu-mcp](https://github.com/xpzouying/xiaohongshu-mcp) 的提交 `a5c8f77`（2026-09-23），许可证沿用上游的 Apache-2.0（见 [LICENSE](LICENSE)）。上游的提交历史和作者信息都完整保留。

除了下面两处代码改动和几份文档，其余文件和上游 `a5c8f77` 完全一致。文档的变化是：`README.md` 换成了我们自己的说明（含实际运营数据），上游原来的 README 移到了 `README.upstream.md`，`README_EN.md` 顶部加了一行说明。

## 1. 登录检测：区分游客和真实登录

文件：`xiaohongshu/login.go`

**问题**
- 小红书改版后，旧的 DOM 选择器 `.main-container .user .link-wrapper .channel` 经常找不到。扫码成功了，也一直判成未登录。
- 游客也会拿到 `web_session` cookie，所以只看 cookie 又会误判成已登录。

**改法**
- 新增 `isReallyLoggedIn`：读页面的 `window.__INITIAL_STATE__.user`，要求不是游客、并且有 `userId`。它不依赖 DOM 结构，也能区分游客。旧的 DOM 选择器留作兜底。
- `CheckLoginStatus` 找不到登录元素时返回 `false`，不再当成错误抛出。
- `WaitForLogin` 增加几种判据：`web_session` 的值发生变化；登录弹窗消失后再复核一次；等 150 秒后刷新页面，确认服务端的登录态。
- 增加排查日志（只记 cookie 的长度，不记值），并把二维码页截图存到 `/tmp/xhs-qr-page.png`。

上游后来也改进了登录用户的读取（`CurrentUser` 改读 `__INITIAL_STATE__`）。本修改版暂时没有跟进上游的最新提交。

## 2. 全局单例网关

目录：`gateway/`

一层本地网关，一个 Python 文件，只用标准库。所有客户端都连网关，网关再转给 xiaohongshu-mcp：

- 串行执行，避免多个 Agent 抢同一个浏览器；
- 两次操作之间随机间隔 5～12 秒；
- 扫码登录期间暂停其他操作；
- 发布、评论、点赞这类写操作默认拦截，请求带 `X-XHS-Allow-Write: yes` 才放行；
- 排队超时直接返回错误，不会无限挂起。

用法和配置见 [gateway/README.md](gateway/README.md)。

## 构建

和上游一样：

```bash
go build -o xiaohongshu-mcp .
```

2026-11-03 用 Go 1.27.2 编译通过。
