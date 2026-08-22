package proxypool

import (
	"context"
	"strings"
	"sync"
	"time"

	"M365Copilot2ApiX/backend/internal/infra/proxypool/getter"
	"M365Copilot2ApiX/backend/internal/infra/proxypool/log"
	"M365Copilot2ApiX/backend/internal/infra/proxypool/proxy"
	"M365Copilot2ApiX/backend/internal/infra/proxypool/store"
)

// FetcherConfig 是代理抓取器的配置
type FetcherConfig struct {
	Enabled  bool          // 是否启用抓取
	Interval time.Duration // 抓取间隔
	Sources  []FetchSource // 抓取源列表
}

// FetchSource 是单个抓取源
type FetchSource struct {
	URL      string // 抓取链接
	SourceID string // 源标识(用于去重和显示)
}

// Fetcher 管理代理节点抓取:
//   - 支持自定义链接(订阅 URL / 网页 URL)
//   - 自动检测格式(txt / html / 含 js 的网页)
//   - 自定义抓取间隔
//   - 定时自动抓取
type Fetcher struct {
	mu       sync.RWMutex
	config   FetcherConfig
	store    *store.Store
	score    *ScoreStore
	cancel   context.CancelFunc
	running  bool
	lastRun  time.Time
	lastResult FetchResult
}

// FetchResult 是单次抓取的结果
type FetchResult struct {
	Time     time.Time
	Total    int // 抓取到的总数
	Imported int // 成功导入数(去重后)
	Skipped  int // 跳过数(无效或重复)
	Errors   []string // 各源的抓取错误
}

func NewFetcher(s *store.Store, score *ScoreStore) *Fetcher {
	return &Fetcher{store: s, score: score}
}

// UpdateConfig 更新抓取配置(会重启抓取循环)
func (f *Fetcher) UpdateConfig(cfg FetcherConfig) {
	f.mu.Lock()
	f.config = cfg
	f.mu.Unlock()

	// 如果启用且间隔有效,重启抓取循环
	if cfg.Enabled && cfg.Interval > 0 {
		f.Start()
	} else {
		f.Stop()
	}
}

// Start 启动定时抓取循环
func (f *Fetcher) Start() {
	f.mu.Lock()
	if f.running {
		f.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	f.cancel = cancel
	f.running = true
	f.mu.Unlock()

	go f.runLoop(ctx)
	log.Infof("proxy fetcher started, interval=%s", f.config.Interval)
}

// Stop 停止抓取循环
func (f *Fetcher) Stop() {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.running {
		return
	}
	if f.cancel != nil {
		f.cancel()
	}
	f.running = false
}

// RunOnce 立即执行一次抓取(不等待间隔)
func (f *Fetcher) RunOnce() FetchResult {
	f.mu.RLock()
	cfg := f.config
	f.mu.RUnlock()

	result := FetchResult{Time: time.Now()}
	if len(cfg.Sources) == 0 {
		return result
	}

	allProxies := make([]proxy.Proxy, 0)
	for _, src := range cfg.Sources {
		proxies, err := fetchFromSource(src.URL)
		if err != nil {
			result.Errors = append(result.Errors, src.SourceID+": "+err.Error())
			continue
		}
		allProxies = append(allProxies, proxies...)
	}
	result.Total = len(allProxies)

	// 导入到 store
	imported, skipped := f.importProxies(allProxies)
	result.Imported = imported
	result.Skipped = skipped

	f.mu.Lock()
	f.lastRun = time.Now()
	f.lastResult = result
	f.mu.Unlock()

	// 给新导入的节点注册分数
	for _, p := range allProxies {
		f.score.Register(p.Identifier(), p.BaseInfo().Name)
	}

	log.Infof("proxy fetch done: total=%d imported=%d skipped=%d errors=%d",
		result.Total, result.Imported, result.Skipped, len(result.Errors))
	return result
}

// importProxies 导入代理到 store(线程安全)
func (f *Fetcher) importProxies(proxies []proxy.Proxy) (imported, skipped int) {
	// 把 proxies 转成文本(每行一个 link)一次性导入 store
	var text strings.Builder
	for _, p := range proxies {
		if p == nil {
			skipped++
			continue
		}
		text.WriteString(p.Link())
		text.WriteString("\n")
	}
	imp, skp := f.store.ImportFromText(text.String())
	return imp, skp
}

// runLoop 定时抓取循环
func (f *Fetcher) runLoop(ctx context.Context) {
	// 启动时先抓一次
	f.RunOnce()
	ticker := time.NewTicker(f.config.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			f.RunOnce()
		}
	}
}

// Status 返回当前抓取状态
func (f *Fetcher) Status() (running bool, lastRun time.Time, lastResult FetchResult) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.running, f.lastRun, f.lastResult
}

// fetchFromSource 从单个源抓取代理,自动检测格式
func fetchFromSource(url string) ([]proxy.Proxy, error) {
	// proxypool 的 subscribe getter 能处理订阅 URL 和网页 URL
	// 自动检测:txt(直接解析)、html(模糊抓取)、含 js 的网页
	return getter.FetchSubscribeURL(url)
}
