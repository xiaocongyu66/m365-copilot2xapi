package proxypool

import (
	"context"
	"time"

	"M365Copilot2ApiX/backend/internal/infra/proxypool/geoip"
	"M365Copilot2ApiX/backend/internal/infra/proxypool/healthcheck"
	"M365Copilot2ApiX/backend/internal/infra/proxypool/log"
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
	cancel        context.CancelFunc
	running       bool
}

func NewChecker(s *store.Store, score *ScoreStore) *Checker {
	return &Checker{store: s, score: score}
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

	// 为所有节点查询 GeoIP 并设置国家代码
	for _, p := range proxies {
		ns := c.score.Register(p.Identifier(), p.BaseInfo().Name)
		if geoDB.IsAvailable() {
			server := p.BaseInfo().Server
			countryCode := geoDB.LookupCountry(server)
			if countryCode != "" {
				ns.SetCountry(countryCode)
				p.SetCountry(countryCode)
			}
		}
	}

	// 已入库节点用简单测试:只测 TCP/UDP 连通性(快速)
	// 新节点入库前已经过了三层测试(fetcher 里),这里只需快速验证是否还活着
	log.Infof("proxy check: simple TCP/UDP test on %d nodes...", len(proxies))
	usable := healthcheck.TCPConnectTestAll(proxies)

	// 更新分数:连通的 +5,不通的 -20(自动禁用)
	usableSet := make(map[string]bool, len(usable))
	for _, p := range usable {
		usableSet[p.Identifier()] = true
	}
	for _, p := range proxies {
		if usableSet[p.Identifier()] {
			c.score.RecordCheckResult(p.Identifier(), true, 0)
		} else {
			c.score.RecordCheckResult(p.Identifier(), false, 0)
		}
	}

	log.Infof("proxy check done: total=%d usable=%d", len(proxies), len(usable))
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
