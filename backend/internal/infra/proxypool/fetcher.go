package proxypool

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"M365Copilot2ApiX/backend/internal/infra/proxypool/getter"
	"M365Copilot2ApiX/backend/internal/infra/proxypool/healthcheck"
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
	URL      string // 抓取链接(可用 #scheme=http 或 #scheme=socks5 标注协议)
	SourceID string // 源标识(用于去重和显示)
}

// DefaultFetchSources 返回内置的默认抓取源列表(免费公开代理源)
// 这些源提供 http/socks5/v2ray/clash 等多种格式的代理列表
func DefaultFetchSources() []FetchSource {
	return []FetchSource{
		// proxifly 免费 HTTP/SOCKS 代理列表(全量)
		{URL: "https://cdn.jsdelivr.net/gh/proxifly/free-proxy-list@main/proxies/all/data.txt", SourceID: "proxifly-all"},
		{URL: "https://raw.githubusercontent.com/proxifly/free-proxy-list/main/proxies/all/data.txt", SourceID: "proxifly-all-raw"},
		// proxifly 按协议分类
		{URL: "https://cdn.jsdelivr.net/gh/proxifly/free-proxy-list@main/proxies/protocols/http/data.txt#scheme=http", SourceID: "proxifly-http"},
		{URL: "https://cdn.jsdelivr.net/gh/proxifly/free-proxy-list@main/proxies/protocols/socks5/data.txt#scheme=socks5", SourceID: "proxifly-socks5"},
		// snakem982 proxypool(v2ray 订阅 + clash 配置)
		{URL: "https://raw.githubusercontent.com/snakem982/proxypool/main/source/v2ray-2.txt", SourceID: "snakem982-v2ray"},
		{URL: "https://cdn.jsdelivr.net/gh/snakem982/proxypool@main/source/clash-meta.yaml", SourceID: "snakem982-clash"},
		// NoMoreWalls 代理列表
		{URL: "https://raw.githubusercontent.com/peasoft/NoMoreWalls/master/list.txt", SourceID: "nomorewalls"},
		// mfuu v2ray 订阅
		{URL: "https://raw.githubusercontent.com/mfuu/v2ray/master/v2ray", SourceID: "mfuu-v2ray"},
		// V2RayAggregator 合并订阅
		{URL: "https://raw.githubusercontent.com/mahdibland/V2RayAggregator/master/sub/sub_merge.txt", SourceID: "v2rayaggregator"},
		// TheSpeedX HTTP/SOCKS5 代理列表
		{URL: "https://raw.githubusercontent.com/TheSpeedX/PROXY-List/master/http.txt#scheme=http", SourceID: "thespeedx-http"},
		{URL: "https://raw.githubusercontent.com/TheSpeedX/PROXY-List/master/socks5.txt#scheme=socks5", SourceID: "thespeedx-socks5"},
		// ProxyScrape API
		{URL: "https://api.proxyscrape.com/v2/?request=displayproxies&protocol=http&timeout=10000&country=all&ssl=all&anonymity=all#scheme=http", SourceID: "proxyscrape-http"},
		{URL: "https://api.proxyscrape.com/v2/?request=displayproxies&protocol=socks5&timeout=10000&country=all&ssl=all&anonymity=all#scheme=socks5", SourceID: "proxyscrape-socks5"},
		// GeoNode 代理列表 API
		{URL: "https://proxylist.geonode.com/api/proxy-list?limit=500&page=1&sort_by=lastChecked&sort_type=desc", SourceID: "geonode"},
		// HankNovic/ProxyClean — 纯中国境内 SOCKS5(已测试,直接用于 M365 注册)
		{URL: "https://raw.githubusercontent.com/HankNovic/ProxyClean/main/SOCKS5.txt#scheme=socks5", SourceID: "hanknovic-cn-socks5"},
		// proxy.scdn.io 纯文本接口(抓取后测活,CN 出口自动分流到注册池)
		{URL: "https://proxy.scdn.io/text.php?protocol=socks5#scheme=socks5", SourceID: "scdn-socks5"},
		{URL: "https://proxy.scdn.io/text.php?protocol=http#scheme=http", SourceID: "scdn-http"},
	}
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
	statePath string // 持久化文件路径
	// 抓取进度
	progressTotal    int64 // 本次抓取总数
	progressDone     int64 // 已完成测试数
	progressUsable   int64 // 通过测试数
	progressStage    string // 当前阶段: "fetching"/"testing"/"done"
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

// UpdateConfig 更新抓取配置(会重启抓取循环)并持久化
func (f *Fetcher) UpdateConfig(cfg FetcherConfig) {
	f.mu.Lock()
	f.config = cfg
	f.mu.Unlock()
	f.Save()

	// 如果启用且间隔有效,重启抓取循环
	if cfg.Enabled && cfg.Interval > 0 {
		f.Start()
	} else {
		f.Stop()
	}
}

// SetStatePath 设置持久化文件路径并加载已保存的配置。
func (f *Fetcher) SetStatePath(path string) {
	f.mu.Lock()
	f.statePath = path
	f.mu.Unlock()
	f.Load()
}

// fetcherConfigRecord 是持久化到 JSON 的格式(Interval 用字符串,JSON 友好)
type fetcherConfigRecord struct {
	Enabled  bool          `json:"enabled"`
	Interval time.Duration `json:"interval"`
	Sources  []FetchSource `json:"sources"`
}

// Save 把抓取配置持久化到文件。
func (f *Fetcher) Save() {
	f.mu.RLock()
	path := f.statePath
	cfg := f.config
	f.mu.RUnlock()
	if path == "" {
		return
	}
	rec := fetcherConfigRecord{
		Enabled:  cfg.Enabled,
		Interval: cfg.Interval,
		Sources:  cfg.Sources,
	}
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return
	}
	_ = os.MkdirAll(filepath.Dir(path), 0755)
	_ = os.WriteFile(path, data, 0644)
}

// Load 从文件加载抓取配置。
func (f *Fetcher) Load() {
	f.mu.RLock()
	path := f.statePath
	f.mu.RUnlock()
	if path == "" {
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var rec fetcherConfigRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		return
	}
	f.mu.Lock()
	f.config.Enabled = rec.Enabled
	if rec.Interval > 0 {
		f.config.Interval = rec.Interval
	}
	if len(rec.Sources) > 0 {
		f.config.Sources = rec.Sources
	}
	f.mu.Unlock()
}

// GetConfig 返回当前抓取配置
func (f *Fetcher) GetConfig() FetcherConfig {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.config
}

// DefaultFetchInterval 返回默认抓取间隔(30 分钟)
func DefaultFetchInterval() time.Duration {
	return 30 * time.Minute
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

	// 设置进度:正在抓取
	f.mu.Lock()
	f.progressStage = "fetching"
	f.progressTotal = 0
	f.progressDone = 0
	f.progressUsable = 0
	f.mu.Unlock()

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

	// 不用服务器地址查 country(中转节点服务器在中国但出口在其他国家,会误判)
	// country 只在 m365CheckOneOpt 纯净度测试时用出口 IP 查询填充


	// 设置进度:正在测试
	f.mu.Lock()
	f.progressStage = "testing"
	f.progressTotal = int64(len(allProxies))
	f.progressDone = 0
	f.progressUsable = 0
	f.mu.Unlock()

	// 入库前两层测试:TCP/UDP 连通 → 微软可达
	// 流式入库:测试通过一个就立刻入库,不等全部测完!
	log.Infof("fetcher: 2-layer test on %d new proxies (TCP/UDP → Microsoft, streaming import)...", len(allProxies))
	importedCount := int64(0)
	healthcheck.M365CheckAndImport(allProxies,
		// 进度回调
		func(done, total, usable int64) {
			f.mu.Lock()
			f.progressDone = done
			f.progressUsable = usable
			f.mu.Unlock()
		},
		// 立刻入库回调:每通过一个就导入
		func(p proxy.Proxy) {
			f.importProxies([]proxy.Proxy{p})
			atomic.AddInt64(&importedCount, 1)
		},
	)
	result.Imported = int(atomic.LoadInt64(&importedCount))
	result.Skipped = result.Total - result.Imported
	log.Infof("fetcher: done: total=%d imported=%d dropped=%d", len(allProxies), result.Imported, result.Skipped)

	// 设置进度:完成
	f.mu.Lock()
	f.progressDone = int64(len(allProxies))
	f.progressUsable = importedCount
	f.progressStage = "done"
	f.mu.Unlock()

	f.mu.Lock()
	f.lastRun = time.Now()
	f.lastResult = result
	f.mu.Unlock()

	// 给新导入的节点注册分数(不查 country,country 只在纯净度测试时填充)
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

// Progress 返回当前抓取进度
func (f *Fetcher) Progress() (stage string, total, done, usable int64) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.progressStage, f.progressTotal, f.progressDone, f.progressUsable
}

// RunOnceWithSource 立即从指定 URL 抓取一次(不入配置源列表,只抓一次)
func (f *Fetcher) RunOnceWithSource(url string) FetchResult {
	proxies, err := fetchFromSource(url)
	result := FetchResult{Time: time.Now()}
	if err != nil {
		result.Errors = append(result.Errors, err.Error())
		return result
	}
	result.Total = len(proxies)
	imported, skipped := f.importProxies(proxies)
	result.Imported = imported
	result.Skipped = skipped
	// 注册分数
	for _, p := range proxies {
		f.score.Register(p.Identifier(), p.BaseInfo().Name)
	}
	return result
}

// fetchFromSource 从单个源抓取代理,自动检测格式
func fetchFromSource(url string) ([]proxy.Proxy, error) {
	// proxypool 的 subscribe getter 能处理订阅 URL 和网页 URL
	// 自动检测:txt(直接解析)、html(模糊抓取)、含 js 的网页
	return getter.FetchSubscribeURL(url)
}
