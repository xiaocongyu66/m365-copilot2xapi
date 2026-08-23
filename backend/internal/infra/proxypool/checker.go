package proxypool

import (
	"context"
	"time"

	"M365Copilot2ApiX/backend/internal/infra/proxypool/geoip"
	"M365Copilot2ApiX/backend/internal/infra/proxypool/healthcheck"
	"M365Copilot2ApiX/backend/internal/infra/proxypool/log"
	"M365Copilot2ApiX/backend/internal/infra/proxypool/proxy"
	"M365Copilot2ApiX/backend/internal/infra/proxypool/store"
)

// Checker 定时执行 M365 测活:
//   - 每分钟自动测试所有已导入的节点
//   - 只测能访问微软
//   - 持续发包 10MB 测稳定性
//   - 多个代理同时测试
//   - 测活结果更新到 ScoreStore(打分)
type Checker struct {
	store         *store.Store
	registerStore *store.Store
	score         *ScoreStore
	onNodeChecked func(p proxy.Proxy, country string) // 节点测活后回调(Service.routeByCountry)
	cancel        context.CancelFunc
	running       bool
}

func NewChecker(s *store.Store, score *ScoreStore) *Checker {
	return &Checker{store: s, score: score}
}

// SetOnNodeChecked 注入节点测活完成回调(用于按国家分流)。
func (c *Checker) SetOnNodeChecked(fn func(p proxy.Proxy, country string)) {
	c.onNodeChecked = fn
}

// SetRegisterStore 注入注册专用池(测活时也测这个池)
func (c *Checker) SetRegisterStore(s *store.Store) {
	c.registerStore = s
}

// Start 启动定时测活循环(每分钟一次)
func (c *Checker) Start() {
	if c.running {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	c.cancel = cancel
	c.running = true
	go c.runLoop(ctx)
	log.Infof("proxy checker started (interval=1m)")
}

// Stop 停止测活循环
func (c *Checker) Stop() {
	if !c.running {
		return
	}
	if c.cancel != nil {
		c.cancel()
	}
	c.running = false
}

// RunOnce 立即执行一次测活(主池 + 注册专用池)
func (c *Checker) RunOnce() {
	proxies := c.store.List()
	// 合并注册专用池的节点,去重
	if c.registerStore != nil {
		seen := make(map[string]bool, len(proxies))
		for _, p := range proxies {
			seen[p.Identifier()] = true
		}
		for _, p := range c.registerStore.List() {
			if !seen[p.Identifier()] {
				proxies = append(proxies, p)
				seen[p.Identifier()] = true
			}
		}
	}
	if len(proxies) == 0 {
		return
	}

	// 确保 GeoIP 数据库已初始化(首次使用会自动下载)
	geoDB := geoip.Get()
	_ = geoDB

	// 注册节点到 ScoreStore(不设置 country,country 只在纯净度测试时填充)
	for _, p := range proxies {
		c.score.Register(p.Identifier(), p.BaseInfo().Name)
	}

	// 完整测活:走 m365CheckOneOpt(128 并发),会查出口 IP 国家并更新 country
	// 这样 country 准确(出口 IP 国家,不是服务器地址国家)
	log.Infof("proxy check: full M365 check on %d nodes...", len(proxies))
	results := healthcheck.M365CheckAll(proxies)
	for _, r := range results {
		c.score.RecordCheckFull(r.Proxy.Identifier(), r.Stable, r.Bytes, int64(r.Latency), r.PurityScore, r.IPType, r.ExitIP, r.ISP, r.CountryCode)
		// 按国家分流(回调 Service.routeByCountry)
		if c.onNodeChecked != nil {
			c.onNodeChecked(r.Proxy, r.CountryCode)
		}
	}

	usableCount := 0
	for _, r := range results {
		if r.Accessible && r.Stable {
			usableCount++
		}
	}
	log.Infof("proxy check done: total=%d usable=%d", len(proxies), usableCount)
}

// runLoop 定时测活循环(每分钟)
func (c *Checker) runLoop(ctx context.Context) {
	// 启动时先测一次
	c.RunOnce()
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.RunOnce()
		}
	}
}

func countUsable(results []healthcheck.M365CheckResult) int {
	count := 0
	for _, r := range results {
		if r.Accessible && r.Stable {
			count++
		}
	}
	return count
}
