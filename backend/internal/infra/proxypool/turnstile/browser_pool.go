package turnstile

import (
	"fmt"
	"strings"
	"sync"
	"time"

	playwright "github.com/mxschmitt/playwright-go"
)

// browserPool 按代理缓存 playwright + browser 实例,避免每次求解都启动/关闭 Chrome。
// 同一代理的多次注册复用同一个 browser,只在进程退出时清理。
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

// getBrowser 返回指定代理的复用 browser 实例(不存在则创建)。
// 调用方负责创建/关闭 context 和 page,但不关闭 browser。
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
	useHeadless := chromeIsHeadlessShell || envBool("M365_HEADLESS", false)

	launchArgs := []string{
		"--no-sandbox",
		"--disable-dev-shm-usage",
		"--disable-blink-features=AutomationControlled",
		"--ignore-certificate-errors",
	}
	if !useHeadless {
		launchArgs = append(launchArgs, "--window-position=-32000,-32000", "--window-size=800,600")
	}

	launchOpts := playwright.BrowserTypeLaunchOptions{
		Headless:       playwright.Bool(useHeadless),
		ExecutablePath: playwright.String(chromePath),
		Args:           launchArgs,
	}
	if proxy != "" {
		launchOpts.Proxy = &playwright.Proxy{Server: proxy}
	}

	browser, err := p.pw.Chromium.Launch(launchOpts)
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

// CloseAllBrowsers 清理所有 browser 和 playwright(进程退出时调用)。
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
