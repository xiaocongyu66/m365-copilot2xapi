// Package turnstile 提供 Cloudflare Turnstile token 求解能力。
// 支持两种模式:
//   - 外部 API solver(设置 TURNSTILE_API_URL 时启用)
//   - 内置 playwright + CloakBrowser 浏览器 solver(默认)
//
// 浏览器 solver 策略:加载真实页面 → 注入 turnstile api.js → 渲染 widget →
// 轮询获取 token(期间点击 checkbox + 伪造 screen 坐标过反检测)。
package turnstile

import (
	"fmt"
	"math/rand"
	"strings"
	"time"

	playwright "github.com/mxschmitt/playwright-go"
)

// SolveTurnstile 求解 Cloudflare Turnstile token。
// siteKey 是目标站点的 Turnstile sitekey。
// proxy 是浏览器要走的代理 URL(socks5/http,可带认证,可为空)。
// 设置环境变量 TURNSTILE_API_URL 时优先用外部 API solver,
// 失败后若 TURNSTILE_BROWSER_FALLBACK=1 则回退到浏览器 solver。
func SolveTurnstile(siteKey, proxy, targetURL string) (string, error) {
	if apiURL := strings.TrimSpace(envFirst("TURNSTILE_API_URL")); apiURL != "" {
		tok, err := solveTurnstileViaAPI(siteKey, targetURL)
		if err == nil {
			return tok, nil
		}
		fmt.Printf("[ts] API solver failed: %v\n", err)
		if !envBool("TURNSTILE_BROWSER_FALLBACK", false) {
			return "", fmt.Errorf("turnstile api: %w (browser fallback disabled)", err)
		}
		fmt.Println("[ts] falling back to browser solver")
	}
	return solveTurnstileBrowser(siteKey, proxy, targetURL)
}

// solveTurnstileBrowser 用 playwright + CloakBrowser 获取 Turnstile token。
func solveTurnstileBrowser(siteKey, proxy, targetURL string) (string, error) {
	chromePath := findChromePath()
	if chromePath == "" {
		return "", fmt.Errorf("chrome/chromium not found")
	}

	// Chrome 不支持带认证的 socks5 代理,需要先转成无认证的本地中继
	browserProxy := maybeRelayProxy(proxy)

	ensureXvfb()

	browser, err := sharedBrowserPool.getBrowser(browserProxy, chromePath)
	if err != nil {
		return "", err
	}

	context, err := browser.NewContext()
	if err != nil {
		return "", fmt.Errorf("context: %w", err)
	}
	defer context.Close()
	// 反检测脚本:隐藏 webdriver 标记,伪造浏览器指纹
	context.AddInitScript(playwright.Script{
		Content: playwright.String(`
			Object.defineProperty(navigator,'webdriver',{get:()=>undefined});
			Object.defineProperty(navigator,'plugins',{get:()=>[1,2,3,4,5]});
			Object.defineProperty(navigator,'languages',{get:()=>['zh-CN','zh','en']});
			window.chrome = { runtime: {} };
			Object.defineProperty(navigator,'permissions',{get:()=>({query:()=>Promise.resolve({state:'granted'})})});
		`),
	})

	page, err := context.NewPage()
	if err != nil {
		return "", fmt.Errorf("page: %w", err)
	}
	defer page.Close()
	page.SetViewportSize(800, 600)

	if targetURL == "" {
		targetURL = "https://office.965007.xyz/"
	}
	_, err = page.Goto(targetURL, playwright.PageGotoOptions{
		WaitUntil: playwright.WaitUntilStateDomcontentloaded,
		Timeout:  playwright.Float(45000),
	})
	if err != nil {
		return "", fmt.Errorf("navigate: %w", err)
	}

	// 随机延迟模拟人类行为
	time.Sleep(time.Duration(3+rand.Intn(3)) * time.Second)

	// 模拟鼠标移动(人类行为)
	mouse := page.Mouse()
	for i := 0; i < 3; i++ {
		mouse.Move(float64(100+rand.Intn(600)), float64(100+rand.Intn(400)))
		time.Sleep(time.Duration(500+rand.Intn(500)) * time.Millisecond)
	}

	// 注入 turnstile api.js(如果页面没有)
	page.Evaluate(`() => {
		if (typeof turnstile === 'undefined') {
			var s = document.createElement('script');
			s.src = 'https://challenges.cloudflare.com/turnstile/v0/api.js';
			s.async = true;
			document.head.appendChild(s);
		}
	}`)

	// 等待 turnstile 全局对象就绪(最多 30 秒)
	ready := false
	for i := 0; i < 60; i++ {
		v, _ := page.Evaluate("() => typeof turnstile !== 'undefined' ? 'yes' : 'no'")
		if s, ok := v.(string); ok && s == "yes" {
			ready = true
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if !ready {
		return "", fmt.Errorf("turnstile: api.js failed to load after 30s")
	}
	fmt.Println("[ts] turnstile API ready")

	// 渲染 turnstile widget
	renderResult, err := page.Evaluate(fmt.Sprintf(`() => {
		if (typeof turnstile === 'undefined') return 'no-turnstile';
		var existing = document.getElementById('cf-ts');
		if (existing) return 'already';
		var d = document.createElement('div');
		d.id = 'cf-ts';
		d.style.cssText = 'position:fixed;top:10px;left:10px;z-index:99999;width:300px;height:70px';
		document.body.appendChild(d);
		window.__ts_token = '';
		window.__ts_err = '';
		try {
			turnstile.render(d, {
				sitekey: '%s',
				callback: function(t) { window.__ts_token = t; },
				'error-callback': function(e) { window.__ts_err = String(e); }
			});
			return 'rendered';
		} catch(e) { window.__ts_err = e.message; return 'error:' + e.message; }
	}`, siteKey))
	if err != nil {
		fmt.Printf("[ts] render eval err: %v\n", err)
	} else {
		fmt.Printf("[ts] render result: %v\n", renderResult)
	}

	// 重置 widget 清除陈旧状态
	page.Evaluate(`() => { try { if (window.turnstile && typeof turnstile.reset === 'function') turnstile.reset(); } catch(e) {} }`)
	time.Sleep(1 * time.Second)

	// 轮询获取 token(最多 50 秒,期间持续点击 checkbox)
	for i := 0; i < 90; i++ {
		tokenVal, _ := page.Evaluate(`() => {
			try {
				var byInput = String((document.querySelector('input[name="cf-turnstile-response"]') || {}).value || '').trim();
				if (byInput) return byInput;
				if (window.turnstile && typeof turnstile.getResponse === 'function') {
					var r = String(turnstile.getResponse() || '').trim();
					if (r) return r;
				}
				return window.__ts_token || '';
			} catch(e) { return ''; }
		}`)
		if t, ok := tokenVal.(string); ok && len(t) >= 80 {
			fmt.Printf("[ts] token acquired (len=%d)\n", len(t))
			return t, nil
		}

		// 检查致命错误(110200/300010/300030/300031/600010 是可重试错误,不中断)
		errVal, _ := page.Evaluate("() => window.__ts_err || ''")
		if e, ok := errVal.(string); ok && e != "" {
			if !strings.Contains(e, "110200") && !strings.Contains(e, "300010") &&
				!strings.Contains(e, "300030") && !strings.Contains(e, "300031") &&
				!strings.Contains(e, "600010") {
				return "", fmt.Errorf("turnstile error: %s", e)
			}
		}

		clickTurnstileCheckbox(page)

		if i%10 == 0 {
			widget, _ := page.Evaluate("() => { var w=document.getElementById('cf-ts'); return w ? w.innerHTML.substring(0,80) : 'none'; }")
			ts, _ := page.Evaluate("() => typeof turnstile !== 'undefined' ? 'yes' : 'no'")
			fmt.Printf("[ts-poll %d] ts=%v widget=%v\n", i, ts, widget)
		}
		time.Sleep(1 * time.Second)
	}

	return "", fmt.Errorf("turnstile: timeout")
}

// clickTurnstileCheckbox 伪造 screen 坐标并点击 Turnstile challenge iframe 内的 checkbox。
func clickTurnstileCheckbox(page playwright.Page) {
	// 主页面伪造 screenX/screenY 规避 headless 检测
	page.Evaluate(`() => {
		try {
			window.dtp = 1;
			function getRandomInt(min, max) { return Math.floor(Math.random() * (max - min + 1)) + min; }
			var sx = getRandomInt(800, 1200);
			var sy = getRandomInt(400, 700);
			Object.defineProperty(MouseEvent.prototype, 'screenX', { value: sx, configurable: true });
			Object.defineProperty(MouseEvent.prototype, 'screenY', { value: sy, configurable: true });
		} catch(e) {}
	}`)

	clickScript := `() => {
		try {
			var cb = document.querySelector('input[type=checkbox]');
			if (cb) { cb.click(); return 'checkbox'; }
			var body = document.querySelector('body');
			if (body && body.shadowRoot) {
				var input = body.shadowRoot.querySelector('input');
				if (input) { input.click(); return 'body-shadow-input'; }
			}
			var inputs = document.querySelectorAll('input, button');
			for (var i = 0; i < inputs.length; i++) {
				var el = inputs[i];
				if (el.type === 'checkbox' || el.type === 'button' || el.tagName === 'BUTTON') {
					el.click();
					return 'generic-' + el.tagName;
				}
			}
			return 'no-target';
		} catch(e) { return 'err:' + e.message; }
	}`

	for _, frame := range page.Frames() {
		url := frame.URL()
		if !strings.Contains(url, "challenges.cloudflare.com") {
			continue
		}
		frame.Evaluate(`() => {
			try {
				function getRandomInt(min, max) { return Math.floor(Math.random() * (max - min + 1)) + min; }
				var sx = getRandomInt(800, 1200);
				var sy = getRandomInt(400, 700);
				Object.defineProperty(MouseEvent.prototype, 'screenX', { value: sx, configurable: true });
				Object.defineProperty(MouseEvent.prototype, 'screenY', { value: sy, configurable: true });
			} catch(e) {}
		}`)
		frame.Evaluate(clickScript)
	}

	// 兜底:点击 widget checkbox 位置(widget 在 top:10,left:10,300x70,checkbox 约 x=40,y=45)
	mouse := page.Mouse()
	mouse.Click(40, 45)
}
