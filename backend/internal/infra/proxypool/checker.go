package proxypool

import (
	"context"
	"sync"
	"time"

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

	// 注册节点到 ScoreStore
	for _, p := range proxies {
		c.score.Register(p.Identifier(), p.BaseInfo().Name)
	}

	// 快速测活:TCP/UDP 连通性(500 并发,3 秒超时)
	log.Infof("proxy check: TCP/UDP connect test on %d nodes...", len(proxies))
	usable := healthcheck.TCPConnectTestAll(proxies)
	usableSet := make(map[string]bool, len(usable))
	for _, p := range usable {
		usableSet[p.Identifier()] = true
	}

	// 对连通的节点查出口 IP + 负载均衡查国家(3 源:ip-api/ipinfo/ipwhois)
	// 按 IP 哈希分配源,每个 IP 只查一个源,节省带宽
	log.Infof("proxy check: probing exit IP + country on %d reachable nodes...", len(usable))
	sem := make(chan struct{}, 128)
	var wg sync.WaitGroup
	for _, p := range usable {
		wg.Add(1)
		go func(pp proxy.Proxy) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			ip, _ := healthcheck.ProbeExitIP(pp)
			country := ""
			if ip != "" {
				country = healthcheck.FetchCountryBalanced(ip)
			}
			if country != "" {
				if ns := c.score.Get(pp.Identifier()); ns != nil {
					ns.SetCountry(country)
				}
				pp.SetCountry(country)
				if c.onNodeChecked != nil {
					c.onNodeChecked(pp, country)
				}
			}
		}(p)
	}
	wg.Wait()

	// 更新分数
	for _, p := range proxies {
		if usableSet[p.Identifier()] {
			c.score.RecordCheckResult(p.Identifier(), true, 0)
		} else {
			c.score.RecordCheckResult(p.Identifier(), false, 0)
		}
	}

	// 精细化淘汰:分数低于 scoreMin(-50) 的节点从 store 删除
	deadIDs := c.score.DeadNodes()
	if len(deadIDs) > 0 {
		for _, id := range deadIDs {
			c.store.Delete(id)
			if c.registerStore != nil {
				c.registerStore.Delete(id)
			}
			c.score.Remove(id)
		}
		log.Infof("proxy check: removed %d dead nodes (score < %d)", len(deadIDs), -50)
	}

	usableCount := len(usable)
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
