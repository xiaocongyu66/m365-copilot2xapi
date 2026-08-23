package proxypool

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	playwright "github.com/mxschmitt/playwright-go"
)

// TurnstileSolver 移植自 grok-register_for-go
// 用 playwright-go 控制浏览器,支持 browser pool 复用

const SignupURL = "https://office.965007.xyz/"

type browserPool struct {
	mu      sync.Mutex
	entries map[string]*browserEntry
	pw      *playwright.Playwright
	pwOnce  sync.Once
	pwErr   error
}

type browserEntry struct {
	browser  playwright.Browser
	proxy    string
	lastUsed time.Time
}

var sharedBrowserPool = &browserPool{
	entries: make(map[string]*browserEntry),
}

func CloseAllBrowsers() {
	sharedBrowserPool.mu.Lock()
	defer sharedBrowserPool.mu.Unlock()
	for _, entry := range sharedBrowserPool.entries {
		entry.browser.Close()
	}
	sharedBrowserPool.entries = make(map[string]*browserEntry)
	if sharedBrowserPool.pw != nil {
		sharedBrowserPool.pw.Stop()
		sharedBrowserPool.pw = nil
	}
}

func (p *browserPool) getBrowser(proxy, chromePath string) (playwright.Browser, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if entry, ok := p.entries[proxy]; ok {
		if entry.browser.IsConnected() {
			entry.lastUsed = time.Now()
			return entry.browser, nil
		}
		entry.browser.Close()
		delete(p.entries, proxy)
	}

	if err := p.ensurePlaywright(); err != nil {
		return nil, fmt.Errorf("playwright: %w", err)
	}

	chromeIsHeadlessShell := strings.Contains(chromePath, "headless_shell")
	useHeadless := chromeIsHeadlessShell

	launchArgs := []string{
		"--no-sandbox",
		"--disable-dev-shm-usage",
		"--disable-blink-features=AutomationControlled",
		"--ignore-certificate-errors",
	}
	if !useHeadless {
		launchArgs = append(launchArgs, "--window-position=-32000,-32000", "--window-size=800,600")
	}

	var proxyOpts *playwright.Proxy
	if proxy != "" {
		proxyOpts = &playwright.Proxy{Server: proxy}
	}

	browser, err := p.pw.Chromium.Launch(playwright.BrowserTypeLaunchOptions{
		Headless:       playwright.Bool(useHeadless),
		ExecutablePath: playwright.String(chromePath),
		Args:           launchArgs,
		Proxy:          proxyOpts,
	})
	if err != nil {
		return nil, fmt.Errorf("launch: %w", err)
	}

	p.entries[proxy] = &browserEntry{
		browser:  browser,
		proxy:    proxy,
		lastUsed: time.Now(),
	}
	fmt.Printf("[bpool] launched browser for proxy=%s headless=%v (total=%d)\n", proxy, useHeadless, len(p.entries))
	return browser, nil
}

func (p *browserPool) ensurePlaywright() error {
	p.pwOnce.Do(func() {
		p.pw, p.pwErr = playwright.Run()
	})
	return p.pwErr
}

// SolveTurnstile 用 playwright 浏览器求解 Cloudflare Turnstile token
func SolveTurnstile(siteKey, proxy string) (string, error) {
	chromePath := findChromePath()
	if chromePath == "" {
		return "", fmt.Errorf("chrome/chromium not found")
	}

	ensureXvfb()

	browser, err := sharedBrowserPool.getBrowser(proxy, chromePath)
	if err != nil {
		return "", err
	}

	context, err := browser.NewContext()
	if err != nil {
		return "", fmt.Errorf("context: %w", err)
	}
	defer context.Close()
	context.AddInitScript(playwright.Script{
		Content: playwright.String("Object.defineProperty(navigator,'webdriver',{get:()=>undefined});"),
	})

	page, err := context.NewPage()
	if err != nil {
		return "", fmt.Errorf("page: %w", err)
	}
	defer page.Close()
	page.SetViewportSize(800, 600)

	_, err = page.Goto(SignupURL, playwright.PageGotoOptions{
		WaitUntil: playwright.WaitUntilStateDomcontentloaded,
		Timeout:   playwright.Float(45000),
	})
	if err != nil {
		return "", fmt.Errorf("navigate: %w", err)
	}

	time.Sleep(3 * time.Second)

	// 注入 Turnstile JS
	page.Evaluate(`() => {
		if (typeof turnstile === 'undefined') {
			var s = document.createElement('script');
			s.src = 'https://challenges.cloudflare.com/turnstile/v0/api.js';
			s.async = true;
			document.head.appendChild(s);
		}
	}`)

	// 等待 turnstile 加载
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
		return "", fmt.Errorf("turnstile api.js failed to load after 30s")
	}
	fmt.Println("[ts] turnstile API ready")

	// 渲染 Turnstile widget
	page.Evaluate(fmt.Sprintf(`() => {
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

	// 重置 widget
	page.Evaluate(`() => { try { if (window.turnstile && typeof turnstile.reset === 'function') turnstile.reset(); } catch(e) {} }`)
	time.Sleep(1 * time.Second)

	// 轮询获取 token(最多 50 秒)
	for i := 0; i < 50; i++ {
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

		// 检查错误
		errVal, _ := page.Evaluate("() => window.__ts_err || ''")
		if e, ok := errVal.(string); ok && e != "" {
			if !strings.Contains(e, "110200") && !strings.Contains(e, "300010") &&
				!strings.Contains(e, "300030") && !strings.Contains(e, "300031") &&
				!strings.Contains(e, "600010") {
				return "", fmt.Errorf("turnstile error: %s", e)
			}
		}

		// 点击 checkbox
		clickTurnstileCheckbox(page)

		if i%10 == 0 {
			fmt.Printf("[ts-poll %d] waiting...\n", i)
		}
		time.Sleep(1 * time.Second)
	}

	return "", fmt.Errorf("turnstile: timeout")
}

func clickTurnstileCheckbox(page playwright.Page) {
	page.Evaluate(`() => {
		try {
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

	mouse := page.Mouse()
	mouse.Click(40, 45)
}

func ensureXvfb() {
	if runtime.GOOS != "linux" {
		return
	}
	if os.Getenv("DISPLAY") == "" {
		os.Setenv("DISPLAY", ":2")
	}
	cmd := exec.Command("xdpyinfo", "-display", ":2")
	if err := cmd.Run(); err == nil {
		return
	}
	tmpDir := os.TempDir()
	os.Remove(filepath.Join(tmpDir, ".X2-lock"))
	os.Remove(filepath.Join(tmpDir, ".X11-unix", "X2"))
	exec.Command("setsid", "Xvfb", ":2", "-screen", "0", "1280x720x24").Start()
	time.Sleep(2 * time.Second)
}

func findChromePath() string {
	for _, key := range []string{"SOLVER_CHROME_PATH", "CHROME_BIN", "CHROME_PATH"} {
		if p := os.Getenv(key); p != "" {
			if _, err := os.Stat(p); err == nil {
				return p
			}
		}
	}
	home, _ := os.UserHomeDir()

	cloakDir := filepath.Join(home, ".cloakbrowser")
	if entries, err := os.ReadDir(cloakDir); err == nil {
		for _, e := range entries {
			if strings.Contains(strings.ToLower(e.Name()), "chrom") {
				for _, p := range []string{
					filepath.Join(cloakDir, e.Name(), "chrome"),
					filepath.Join(cloakDir, e.Name(), "chrome.exe"),
				} {
					if _, err := os.Stat(p); err == nil {
						return p
					}
				}
			}
		}
	}

	if runtime.GOOS == "linux" {
		for _, name := range []string{"chromium", "chromium-browser", "google-chrome", "google-chrome-stable", "chrome"} {
			if p, err := exec.LookPath(name); err == nil {
				return p
			}
		}
	}
	return ""
}
