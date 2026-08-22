package proxypool

import (
	"context"
	"time"

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
	store   *store.Store
	score   *ScoreStore
	cancel  context.CancelFunc
	running bool
}

func NewChecker(s *store.Store, score *ScoreStore) *Checker {
	return &Checker{store: s, score: score}
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

// RunOnce 立即执行一次测活
func (c *Checker) RunOnce() {
	proxies := c.store.List()
	if len(proxies) == 0 {
		return
	}

	// 确保所有节点都注册了分数
	for _, p := range proxies {
		c.score.Register(p.Identifier(), p.BaseInfo().Name)
	}

	// 执行 M365 测活(可达性 + 持续 10MB 下载)
	results := healthcheck.M365CheckAll(proxies)

	// 更新分数
	for _, r := range results {
		c.score.RecordCheckResult(r.Proxy.Identifier(), r.Stable, r.Bytes)
	}

	log.Infof("proxy check done: total=%d usable=%d", len(results), countUsable(results))
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
