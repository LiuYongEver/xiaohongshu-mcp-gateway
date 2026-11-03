// Modified by LiuYongEver, 2026-10 (Apache-2.0 §4b notice):
// 登录检测改为优先读页面 window.__INITIAL_STATE__（能区分游客和真实登录），旧 DOM 选择器兜底；
// WaitForLogin 增加 web_session 变化、登录弹窗消失、150 秒后刷新确认等判据和排查日志。详见 FORK.md。

package xiaohongshu

import (
	"context"
	"encoding/json"
	"os"
	"time"

	"github.com/go-rod/rod"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
)

type LoginAction struct {
	page *rod.Page
}

func NewLogin(page *rod.Page) *LoginAction {
	return &LoginAction{page: page}
}

func (a *LoginAction) CheckLoginStatus(ctx context.Context) (bool, error) {
	// 加超时保护：只是查登录态的快速检查，不应无限挂（登录扫码的等待在 Login/WaitForLogin 里）
	pp := a.page.Context(ctx).Timeout(30 * time.Second)
	pp.MustNavigate("https://www.xiaohongshu.com/explore").MustWaitLoad()

	time.Sleep(1 * time.Second)

	// 优先：页面 JS 状态（区分游客和真实登录，不依赖 DOM）
	if isReallyLoggedIn(pp) {
		return true, nil
	}

	// 兜底：旧版 DOM 元素
	exists, _, err := pp.Has(`.main-container .user .link-wrapper .channel`)
	if err != nil {
		return false, errors.Wrap(err, "check login status failed")
	}

	if !exists {
		return false, nil
	}

	return true, nil
}

// hasLoginCookie 检查浏览器是否已有 web_session cookie，不依赖页面 DOM.
// 注意：游客也会有 web_session，仅做粗筛，不能单独当登录判据。
func hasLoginCookie(pp *rod.Page) bool {
	cookies, err := pp.Cookies([]string{"https://www.xiaohongshu.com"})
	if err != nil {
		return false
	}
	for _, c := range cookies {
		if c.Name == "web_session" && c.Value != "" {
			return true
		}
	}
	return false
}

// isReallyLoggedIn 通过页面 JS 状态判断是否真正登录（区分游客和真实用户）.
// 不依赖 DOM 结构，改版不会坏；需要页面已加载完（MustWaitLoad 之后）。
func isReallyLoggedIn(pp *rod.Page) bool {
	res, err := pp.Eval(`() => {
		try {
			const u = window.__INITIAL_STATE__ && window.__INITIAL_STATE__.user;
			const info = u && (u.userInfo && u.userInfo.value !== undefined ? u.userInfo.value : u.userInfo);
			return !!(info && !info.guest && (info.userId || info.user_id));
		} catch(e) { return false; }
	}`)
	if err != nil {
		return false
	}
	return res.Value.Bool()
}

// CurrentUser 当前登录用户的基础信息。
type CurrentUser struct {
	Nickname string `json:"nickname"`
	UserID   string `json:"userId"`
}

// CurrentUser 从当前页面的 __INITIAL_STATE__ 读取登录用户信息。
// 需在 CheckLoginStatus 之后调用：复用已加载的 explore 页，不做额外导航。
func (a *LoginAction) CurrentUser(ctx context.Context) (*CurrentUser, error) {
	pp := a.page.Context(ctx).Timeout(10 * time.Second)

	res, err := pp.Eval(`() => {
		const u = window.__INITIAL_STATE__ && window.__INITIAL_STATE__.user;
		const info = u && u.userInfo && u.userInfo.value !== undefined ? u.userInfo.value : (u && u.userInfo);
		if (!info || info.guest) return "";
		return JSON.stringify({nickname: info.nickname, userId: info.userId || info.user_id});
	}`)
	if err != nil {
		return nil, errors.Wrap(err, "read current user state failed")
	}

	raw := res.Value.String()
	if raw == "" {
		return nil, errors.New("current user not found in page state")
	}

	var user CurrentUser
	if err := json.Unmarshal([]byte(raw), &user); err != nil {
		return nil, errors.Wrap(err, "unmarshal current user failed")
	}

	return &user, nil
}

func (a *LoginAction) Login(ctx context.Context) error {
	pp := a.page.Context(ctx)

	// 导航到小红书首页，这会触发二维码弹窗
	pp.MustNavigate("https://www.xiaohongshu.com/explore").MustWaitLoad()

	time.Sleep(2 * time.Second)

	if exists, _, _ := pp.Has(".main-container .user .link-wrapper .channel"); exists || isReallyLoggedIn(pp) {
		return nil
	}

	pp.MustElement(".main-container .user .link-wrapper .channel")

	return nil
}

func (a *LoginAction) FetchQrcodeImage(ctx context.Context) (string, bool, error) {
	pp := a.page.Context(ctx)

	// 导航到小红书首页，这会触发二维码弹窗
	pp.MustNavigate("https://www.xiaohongshu.com/explore").MustWaitLoad()

	time.Sleep(2 * time.Second)

	if exists, _, _ := pp.Has(".main-container .user .link-wrapper .channel"); exists || isReallyLoggedIn(pp) {
		return "", true, nil
	}

	src, err := pp.MustElement(".login-container .qrcode-img").Attribute("src")
	if err != nil {
		return "", false, errors.Wrap(err, "get qrcode src failed")
	}
	if src == nil || len(*src) == 0 {
		return "", false, errors.New("qrcode src is empty")
	}

	return *src, false, nil
}

// getWebSession 返回当前 web_session cookie 的值
func getWebSession(pp *rod.Page) string {
	cookies, err := pp.Cookies([]string{"https://www.xiaohongshu.com"})
	if err != nil {
		return ""
	}
	for _, c := range cookies {
		if c.Name == "web_session" {
			return c.Value
		}
	}
	return ""
}

func (a *LoginAction) WaitForLogin(ctx context.Context) bool {
	pp := a.page.Context(ctx)

	// 记录初始 web_session（游客会话），扫码后值会变
	initialSession := getWebSession(pp)
	logrus.Infof("WaitForLogin: 初始 web_session 长度 %d", len(initialSession))

	// 截图一次，看看浏览器里实际显示什么
	if imgData, err := pp.Screenshot(true, nil); err == nil {
		os.WriteFile("/tmp/xhs-qr-page.png", imgData, 0644)
		logrus.Infof("WaitForLogin: 截图已保存 /tmp/xhs-qr-page.png (%d bytes)", len(imgData))
	}

	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	logTicker := time.NewTicker(10 * time.Second)
	defer logTicker.Stop()

	// v8: 前 150 秒不刷新（让用户扫码），之后刷新一次检查服务端登录态。
	// 如果服务端已认证 web_session，刷新后 isReallyLoggedIn 会 true。
	reloadOnce := time.NewTimer(150 * time.Second)
	defer reloadOnce.Stop()
	reloaded := false

	// 诊断：注入 JS 检查页面的网络活动（XHR/fetch/WebSocket）
	pp.Eval(`() => {
		window.__xhs_net_log = [];
		const origOpen = XMLHttpRequest.prototype.open;
		XMLHttpRequest.prototype.open = function(method, url) {
			window.__xhs_net_log.push({t: Date.now(), type: 'xhr', method, url: url.toString().substring(0, 200)});
			return origOpen.apply(this, arguments);
		};
		const origFetch = window.fetch;
		window.fetch = function(url, opts) {
			window.__xhs_net_log.push({t: Date.now(), type: 'fetch', url: (url||'').toString().substring(0, 200)});
			return origFetch.apply(this, arguments);
		};
		if (window.WebSocket) {
			const origWS = window.WebSocket;
			window.WebSocket = function(url, protocols) {
				window.__xhs_net_log.push({t: Date.now(), type: 'ws', url: url.toString().substring(0, 200)});
				return new origWS(url, protocols);
			};
		}
	}`)
	logrus.Info("WaitForLogin: 已注入网络监控 JS")

	for {
		select {
		case <-ctx.Done():
			return false
		case <-reloadOnce.C:
			// 150 秒后刷新一次：用户应该已经扫码了，检查服务端是否认证了这个 session
			logrus.Info("WaitForLogin: 150秒到，刷新页面检查服务端登录态")
			if err := pp.Navigate("https://www.xiaohongshu.com/explore"); err == nil {
				pp.MustWaitLoad()
				time.Sleep(2 * time.Second)
				if isReallyLoggedIn(pp) {
					logrus.Info("WaitForLogin: 刷新后 isReallyLoggedIn=true, 登录成功！")
					return true
				}
				cur := getWebSession(pp)
				logrus.Infof("WaitForLogin: 刷新后 js=false session 长度 %d", len(cur))
				// 刷新后截图
				if imgData, err := pp.Screenshot(true, nil); err == nil {
					os.WriteFile("/tmp/xhs-post-reload.png", imgData, 0644)
					logrus.Infof("WaitForLogin: 刷新后截图 /tmp/xhs-post-reload.png (%d bytes)", len(imgData))
				}
			}
			reloaded = true
		case <-logTicker.C:
			// 每 10 秒记录一次状态，方便排查
			cur := getWebSession(pp)
			jsOK := isReallyLoggedIn(pp)
			cookieChanged := cur != "" && initialSession != "" && cur != initialSession

			// 额外诊断：页面上有什么
			pageInfo, _ := pp.Eval(`() => {
				try {
					const hasModal = !!document.querySelector('.login-container');
					const hasQR = !!document.querySelector('.login-container .qrcode-img');
					const title = document.title;
					const url = location.href;
					const cookieCount = document.cookie.split(';').length;
					return JSON.stringify({hasModal, hasQR, title, url, cookieCount});
				} catch(e) { return e.message; }
			}`)
			pageStr := ""
			if pageInfo != nil {
				pageStr = pageInfo.Value.String()
			}
			logrus.Infof("WaitForLogin: js=%v cookie_changed=%v session 长度 %d page=%s", jsOK, cookieChanged, len(cur), pageStr)

			// 诊断：页面发了什么网络请求
			if !reloaded {
				netLog, _ := pp.Eval(`() => {
					try {
						const log = window.__xhs_net_log || [];
						window.__xhs_net_log = [];
						return JSON.stringify(log.slice(-10));
					} catch(e) { return "[]"; }
				}`)
				if netLog != nil && netLog.Value.String() != "[]" {
					logrus.Infof("WaitForLogin: 网络请求=%s", netLog.Value.String())
				}
			}
		case <-ticker.C:
			// 检查 1：页面 JS 状态（__INITIAL_STATE__ 实时更新时能检测到）
			if isReallyLoggedIn(pp) {
				logrus.Info("WaitForLogin: isReallyLoggedIn=true, 登录成功")
				return true
			}
			// 检查 2：web_session 值变化（扫码成功后 cookie 会换值）
			if cur := getWebSession(pp); cur != "" && initialSession != "" && cur != initialSession {
				logrus.Info("WaitForLogin: web_session 值变化, 登录成功")
				return true
			}
			// 检查 3：旧版 DOM 元素（用 Has 而非 Element，Has 是非阻塞的）
			if exists, _, _ := pp.Has(".main-container .user .link-wrapper .channel"); exists {
				logrus.Info("WaitForLogin: DOM 元素出现, 登录成功")
				return true
			}
			// 检查 4：QR 弹窗消失（扫码成功后页面 JS 可能关闭弹窗）
			if hasQR, _, _ := pp.Has(".login-container .qrcode-img"); !hasQR {
				if hasModal, _, _ := pp.Has(".login-container"); !hasModal {
					logrus.Info("WaitForLogin: 登录弹窗消失, 可能已登录")
					// 弹窗消失不一定是登录成功，等 1 秒再确认
					time.Sleep(1 * time.Second)
					if isReallyLoggedIn(pp) {
						logrus.Info("WaitForLogin: 弹窗消失 + isReallyLoggedIn=true, 登录成功")
						return true
					}
				}
			}
		}
	}
}
