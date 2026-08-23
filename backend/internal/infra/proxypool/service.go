package proxypool

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"M365Copilot2ApiX/backend/internal/infra/proxypool/geoip"
	"M365Copilot2ApiX/backend/internal/infra/proxypool/healthcheck"
	"M365Copilot2ApiX/backend/internal/infra/proxypool/log"
	"M365Copilot2ApiX/backend/internal/infra/proxypool/minirelay"
	"M365Copilot2ApiX/backend/internal/infra/proxypool/proxy"
	"M365Copilot2ApiX/backend/internal/infra/proxypool/store"
)

// Service 是 proxypool 的顶层服务,整合:
//   - Store:节点存储和导入
//   - ScoreStore:打分
//   - Balancer:负载均衡
//   - Fetcher:定时抓取
//   - Checker:定时测活
//   - ErrorReporter:请求报错推送
// AccountImporter 把 M365 账号(refresh token + email + password)导入到账号池。
// 由 application 层注入,proxypool 不依赖 account application 包。
type AccountImporter interface {
	ImportM365Account(ctx context.Context, refreshToken, email, password string) error
}

type Service struct {
	mu       sync.RWMutex
	store    *store.Store          // 正常代理池(排除 CN)
	registerStore *store.Store     // 注册专用池(CN/HK/MO/TW)
	score    *ScoreStore
	balancer *Balancer
	fetcher  *Fetcher
	checker  *Checker
	registrar *Registrar
	accountImporter AccountImporter
	scoreCtx    context.Context
	scoreCancel context.CancelFunc

	// 报错缓冲(供前端轮询读取)
	errorMu     sync.Mutex
	recentErrors []NodeError
}

// NodeError 是单个节点的请求报错记录
type NodeError struct {
	NodeID    string    `json:"nodeId"`
	NodeName  string    `json:"nodeName"`
	Error     string    `json:"error"`
	Endpoint  string    `json:"endpoint"`
	Timestamp time.Time `json:"timestamp"`
}

func NewService() *Service {
	s := &Service{
		store:          store.NewWithFile("data/proxies.txt"),
		registerStore:  store.NewWithFile("data/register_proxies.txt"),
	}
	s.score = NewScoreStore()
	s.score.SetStatePath("data/proxies_state.json")
	s.balancer = NewBalancer(s.score)
	// 注入主代理池/注册专用池的 identifier 提供函数
	s.balancer.SetNormalIDs(func() []string { return s.store.IDs() })
	s.balancer.SetRegisterIDs(func() []string { return s.registerStore.IDs() })
	s.fetcher = NewFetcher(s.store, s.score)
	s.fetcher.SetStatePath("data/fetcher_config.json")
	s.checker = NewChecker(s.store, s.score)
	s.checker.SetRegisterStore(s.registerStore)
	s.checker.SetOnNodeChecked(s.routeByCountry)
	s.registrar = NewRegistrar(s)
	return s
}

// SetAccountImporter 注入账号导入器(application 层调用)
func (s *Service) SetAccountImporter(importer AccountImporter) {
	s.accountImporter = importer
}

// Store 返回节点存储
func (s *Service) Store() *store.Store { return s.store }

// Registrar 返回注册器
func (s *Service) Registrar() *Registrar { return s.registrar }

// Score 返回分数存储
func (s *Service) Score() *ScoreStore { return s.score }

// Balancer 返回负载均衡器
func (s *Service) Balancer() *Balancer { return s.balancer }

// Fetcher 返回抓取器
func (s *Service) Fetcher() *Fetcher { return s.fetcher }

// RegisterStore 返回注册专用代理池(CN/HK/MO/TW)
func (s *Service) RegisterStore() *store.Store { return s.registerStore }

// Checker 返回测活器
func (s *Service) Checker() *Checker { return s.checker }

// CheckOne 对单个节点做两层测试(TCP/UDP + 微软可达)
func (s *Service) CheckOne(identifier string) {
	// 节点可能在主 store 或 register store
	p, ok := s.store.Get(identifier)
	if !ok {
		p, ok = s.registerStore.Get(identifier)
		if !ok {
			return
		}
	}
	result := healthcheck.M365CheckOnePublic(p)
	s.score.RecordCheckFull(identifier, result.Stable, result.Bytes, int64(result.Latency), result.PurityScore, result.IPType, result.ExitIP, result.ISP, result.CountryCode)
	// 按国家分流:CN 移到 register store,非 CN 移到主 store
	s.routeByCountry(p, result.CountryCode)
}

// CheckOneSync 同步测试单个节点,返回完整结果(给 API 用)
func (s *Service) CheckOneSync(identifier string) (healthcheck.M365CheckResult, bool) {
	p, ok := s.store.Get(identifier)
	if !ok {
		p, ok = s.registerStore.Get(identifier)
		if !ok {
			return healthcheck.M365CheckResult{}, false
		}
	}
	result := healthcheck.M365CheckOnePublic(p)
	s.score.RecordCheckFull(identifier, result.Stable, result.Bytes, int64(result.Latency), result.PurityScore, result.IPType, result.ExitIP, result.ISP, result.CountryCode)
	s.routeByCountry(p, result.CountryCode)
	return result, true
}

// routeByCountry 按国家把节点路由到正确的 store:
//   - CN → register store(只用于注册)
//   - 非 CN(含 HK/MO/TW) → 主 store(用于正常 M365 请求)
// HK/MO/TW 节点同时在两个 store 里(注册和正常请求都能用)。
func (s *Service) routeByCountry(p proxy.Proxy, country string) {
	if p == nil {
		return
	}
	id := p.Identifier()
	upper := strings.ToUpper(strings.TrimSpace(country))
	isCN := upper == "CN" || upper == "CHINA"
	isRegisterEligible := isCN || upper == "HK" || upper == "MO" || upper == "TW" ||
		upper == "HONG KONG" || upper == "HONGKONG" || upper == "MACAO" || upper == "MACAU" || upper == "TAIWAN"

	if isCN {
		// CN 节点只放 register store,从主 store 删除
		if _, exists := s.store.Get(id); exists {
			s.store.Delete(id)
		}
		if _, exists := s.registerStore.Get(id); !exists {
			s.registerStore.Add(p)
		}
	} else if isRegisterEligible {
		// HK/MO/TW 两边都放(注册和正常请求都能用)
		if _, exists := s.registerStore.Get(id); !exists {
			s.registerStore.Add(p)
		}
	} else {
		// 非 CN/HK/MO/TW 只放主 store,从 register store 删除
		if _, exists := s.registerStore.Get(id); exists {
			s.registerStore.Delete(id)
		}
	}
}

// Start 启动抓取器和测活器
func (s *Service) Start() {
	// 初始化 GeoIP 数据库(自动下载,加载本地数据库)
	geoip.Get()
	s.checker.Start()
	// 启动节点状态定时保存
	s.scoreCtx, s.scoreCancel = context.WithCancel(context.Background())
	go s.score.RunAutoSave(s.scoreCtx)
	// fetcher 由配置驱动启动
}

// Stop 停止所有后台任务
func (s *Service) Stop() {
	s.checker.Stop()
	s.fetcher.Stop()
	if s.scoreCancel != nil {
		s.scoreCancel()
	}
	s.score.StopAutoSave()
}

// Reload 软重启:停止后台任务,重新加载持久化文件,再启动。
// 不退出进程(避免没有进程管理器时进程无法重启)。
func (s *Service) Reload() {
	// 停止后台任务
	s.checker.Stop()
	s.fetcher.Stop()
	if s.scoreCancel != nil {
		s.scoreCancel()
		s.scoreCancel = nil
	}
	// 重新加载持久化状态
	s.score.Load()
	// 重新启动
	s.Start()
	// 应用持久化的抓取配置(会自动启动抓取循环)
	cfg := s.fetcher.GetConfig()
	if cfg.Enabled && cfg.Interval > 0 {
		s.fetcher.UpdateConfig(cfg)
	}
}

// ResetCountries 清空所有节点的国家记录(ScoreStore 的 ns.Country + proxy.Base 的 Country)。
// 用于修复之前用服务器地址查国家导致的不准记录,让测活后重新填充准确的出口 IP 国家。
func (s *Service) ResetCountries() int {
	count := 0
	// 清 ScoreStore 的 Country(通过 NodeScore.SetCountry)
	for _, snap := range s.score.List() {
		if ns := s.score.Get(snap.Identifier); ns != nil {
			ns.SetCountry("")
			count++
		}
	}
	// 清 proxy.Base 的 Country(主池 + 注册池)
	for _, p := range s.store.List() {
		p.SetCountry("")
	}
	for _, p := range s.registerStore.List() {
		p.SetCountry("")
	}
	// 把注册池的节点全部移回主池(等测活后重新分流)
	for _, p := range s.registerStore.List() {
		s.registerStore.Delete(p.Identifier())
		if _, exists := s.store.Get(p.Identifier()); !exists {
			s.store.Add(p)
		}
	}
	return count
}
//   - 出口 IP 是 CN 的节点:从主池移到注册池
//   - 出口 IP 是 HK/MO/TW 的节点:加到注册池(两边都放)
//   - 出口 IP 是其他国家的节点:如果误在注册池,移回主池
//   - 没测活记录(ns.Country 为空)的节点:不迁移,等测活后 routeByCountry 自动分流
//
// 注意:不能用服务器地址国家判断,因为中转节点的服务器可能在中国但出口在其他国家。
func (s *Service) MigrateCNToRegister() int {
	migrated := 0
	// 1. 主池 → 注册池(只迁移有出口 IP 国家记录的)
	for _, p := range s.store.List() {
		ns := s.score.Get(p.Identifier())
		if ns == nil {
			continue
		}
		country := strings.ToUpper(strings.TrimSpace(ns.Country))
		if country == "" {
			continue // 没测活,跳过
		}
		isCN := country == "CN" || country == "CHINA"
		isRegisterEligible := isCN || country == "HK" || country == "MO" || country == "TW" ||
			country == "HONG KONG" || country == "HONGKONG" || country == "MACAO" || country == "MACAU" || country == "TAIWAN"
		if isCN {
			s.store.Delete(p.Identifier())
			if _, exists := s.registerStore.Get(p.Identifier()); !exists {
				s.registerStore.Add(p)
			}
			migrated++
		} else if isRegisterEligible {
			if _, exists := s.registerStore.Get(p.Identifier()); !exists {
				s.registerStore.Add(p)
			}
		}
	}
	// 2. 注册池 → 主池(把误迁移的非 CN 出口节点移回)
	for _, p := range s.registerStore.List() {
		ns := s.score.Get(p.Identifier())
		if ns == nil {
			continue
		}
		country := strings.ToUpper(strings.TrimSpace(ns.Country))
		if country == "" {
			continue // 没测活,保留在注册池
		}
		isCN := country == "CN" || country == "CHINA"
		isRegisterEligible := isCN || country == "HK" || country == "MO" || country == "TW" ||
			country == "HONG KONG" || country == "HONGKONG" || country == "MACAO" || country == "MACAU" || country == "TAIWAN"
		if !isRegisterEligible {
			// 非注册地区,移回主池
			s.registerStore.Delete(p.Identifier())
			if _, exists := s.store.Get(p.Identifier()); !exists {
				s.store.Add(p)
			}
		}
	}
	return migrated
}

// ImportNodes 从文本导入节点(一行一个)
func (s *Service) ImportNodes(text string) (imported, skipped int) {
	return s.store.ImportFromText(text)
}

// ListNodes 列出所有节点(主池 + 注册专用池,带分数)
func (s *Service) ListNodes() []NodeView {
	proxies := s.store.List()
	// 合并注册专用池的节点(CN/HK/MO/TW),用 seen 去重
	seen := make(map[string]bool, len(proxies))
	for _, p := range proxies {
		seen[p.Identifier()] = true
	}
	for _, p := range s.registerStore.List() {
		if !seen[p.Identifier()] {
			proxies = append(proxies, p)
			seen[p.Identifier()] = true
		}
	}
	scores := s.score.List()
	scoreMap := make(map[string]NodeScoreSnapshot, len(scores))
	for _, sc := range scores {
		scoreMap[sc.Identifier] = sc
	}

	views := make([]NodeView, 0, len(proxies))
	for _, p := range proxies {
		sc := scoreMap[p.Identifier()]
		views = append(views, NodeView{
			Identifier:      p.Identifier(),
			Name:            p.BaseInfo().Name,
			Type:            p.TypeName(),
			Server:          p.BaseInfo().Server,
			Port:            p.BaseInfo().Port,
			Country:         firstNonEmpty(sc.Country, p.BaseInfo().Country),
			Score:           sc.Score,
			Enabled:         sc.Enabled,
			AutoDisabled:    sc.AutoDisabled,
			ErrorCount:      sc.ErrorCount,
			LastError:       sc.LastError,
			LastErrorAt:     sc.LastErrorAt,
			SuccessCount:    sc.SuccessCount,
			LastSuccessAt:   sc.LastSuccessAt,
			LastCheckAt:     sc.LastCheckAt,
			LastCheckStable: sc.LastCheckStable,
			LastLatency:     sc.LastLatency / int64(time.Millisecond),
			LastPurityScore: sc.LastPurityScore,
			LastIPType:      sc.LastIPType,
			LastExitIP:      sc.LastExitIP,
			LastISP:         sc.LastISP,
			ActiveRequests:  sc.ActiveRequests,
		})
	}
	return views
}

// NodeView 是节点的完整视图(基本信息 + 分数 + 状态)
type NodeView struct {
	Identifier      string    `json:"identifier"`
	Name            string    `json:"name"`
	Type            string    `json:"type"`
	Server          string    `json:"server"`
	Port            int       `json:"port"`
	Country         string    `json:"country"`
	Score           int       `json:"score"`
	Enabled         bool      `json:"enabled"`
	AutoDisabled    bool      `json:"autoDisabled"`
	ErrorCount      int       `json:"errorCount"`
	LastError       string    `json:"lastError"`
	LastErrorAt     time.Time `json:"lastErrorAt"`
	SuccessCount    int       `json:"successCount"`
	LastSuccessAt   time.Time `json:"lastSuccessAt"`
	LastCheckAt     time.Time `json:"lastCheckAt"`
	LastCheckStable bool      `json:"lastCheckStable"`
	LastLatency     int64     `json:"lastLatencyMs"`
	LastPurityScore int       `json:"lastPurityScore"`
	LastIPType      string    `json:"lastIPType"`
	LastExitIP      string    `json:"lastExitIP"`
	LastISP         string    `json:"lastISP"`
	ActiveRequests  int       `json:"activeRequests"`
}

// SetEnabled 批量启用/禁用节点
func (s *Service) SetEnabled(identifiers []string, enabled bool) {
	for _, id := range identifiers {
		s.score.SetEnabled(id, enabled)
	}
}

// ClearErrors 清除节点的报错记录
func (s *Service) ClearErrors(identifiers []string) {
	for _, id := range identifiers {
		s.score.ClearErrors(id)
	}
}

// DeleteNodes 批量删除节点(两个 store 都删)
func (s *Service) DeleteNodes(identifiers []string) {
	for _, id := range identifiers {
		s.store.Delete(id)
		s.registerStore.Delete(id)
	}
}

// RecordRequestError 记录一次请求报错(供 gateway 层调用)
func (s *Service) RecordRequestError(nodeID, nodeName, endpoint, errMsg string) {
	s.score.RecordError(nodeID, errMsg)
	s.errorMu.Lock()
	defer s.errorMu.Unlock()
	s.recentErrors = append(s.recentErrors, NodeError{
		NodeID:    nodeID,
		NodeName:  nodeName,
		Error:     errMsg,
		Endpoint:  endpoint,
		Timestamp: time.Now(),
	})
	// 只保留最近 100 条
	if len(s.recentErrors) > 100 {
		s.recentErrors = s.recentErrors[len(s.recentErrors)-100:]
	}
}

// RecordRequestSuccess 记录一次请求成功(供 gateway 层调用)
func (s *Service) RecordRequestSuccess(nodeID string) {
	s.score.RecordSuccess(nodeID)
}

// RecentErrors 返回最近的请求报错(供前端轮询)
func (s *Service) RecentErrors() []NodeError {
	s.errorMu.Lock()
	defer s.errorMu.Unlock()
	result := make([]NodeError, len(s.recentErrors))
	copy(result, s.recentErrors)
	return result
}

// ClearRecentErrors 清除报错缓冲
func (s *Service) ClearRecentErrors() {
	s.errorMu.Lock()
	defer s.errorMu.Unlock()
	s.recentErrors = nil
}

// PickNodeForRequest 为一个请求选择节点(负载均衡)
// 返回节点标识和 release 函数。如果没有可用节点,返回空字符串(调用方走原始 IP)
func (s *Service) PickNodeForRequest() (string, func()) {
	return s.balancer.PickNode()
}

// GetProxyURL 获取节点的标准代理 URL(供 playwright/minirelay 使用)。
// 返回节点 Link() 方法生成的标准 URL(socks5:// / http:// / vless:// 等)。
// 没有可用节点返回空字符串(走原始 IP)。
func (s *Service) GetProxyURL(identifier string) string {
	if identifier == "" {
		return ""
	}
	p, ok := s.store.Get(identifier)
	if !ok {
		return ""
	}
	return p.Link()
}

// httpRelayManager 管理为 HTTP 客户端创建的本地代理中继(按节点缓存)。
// 因为 ss/vless/vmess/trojan 等协议 Go 标准库不支持,需要 minirelay 转成本地 socks5。
type httpRelayManager struct {
	mu      sync.Mutex
	relays  map[string]*httpRelayEntry
	startPort int
}
type httpRelayEntry struct {
	localURL string
	relay    *minirelay.Relay
}
var httpRelay = &httpRelayManager{relays: make(map[string]*httpRelayEntry), startPort: 19300}

// HTTPClientWithNode 返回使用指定节点的 HTTP 客户端。
// 通过 minirelay 把节点转成本地 socks5(Chrome 和 Go 标准库都支持 socks5)。
func (s *Service) HTTPClientWithNode(identifier string) *http.Client {
	if identifier == "" {
		return http.DefaultClient
	}
	p, ok := s.store.Get(identifier)
	if !ok {
		if p2, ok2 := s.registerStore.Get(identifier); ok2 {
			p = p2
		} else {
			return http.DefaultClient
		}
	}
	upstreamURL := p.Link()
	if upstreamURL == "" {
		return http.DefaultClient
	}
	localURL := getOrCreateHTTPRelay(upstreamURL)
	if localURL == "" {
		return http.DefaultClient
	}
	// 解析 socks5://127.0.0.1:port
	proxyURL, err := url.Parse(localURL)
	if err != nil {
		return http.DefaultClient
	}
	return &http.Client{
		Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL)},
		Timeout:   60 * time.Second,
	}
}

// getOrCreateHTTPRelay 为上游代理 URL 创建或复用本地 minirelay 中继。
func getOrCreateHTTPRelay(upstreamURL string) string {
	httpRelay.mu.Lock()
	defer httpRelay.mu.Unlock()
	if existing, ok := httpRelay.relays[upstreamURL]; ok && existing.relay != nil {
		return existing.localURL
	}
	// 分配端口
	port := httpRelay.startPort
	for p := httpRelay.startPort; p < httpRelay.startPort+500; p++ {
		ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", p))
		if err == nil {
			ln.Close()
			port = p
			break
		}
	}
	listen := fmt.Sprintf("127.0.0.1:%d", port)
	relay, err := minirelay.New(listen, upstreamURL)
	if err != nil {
		return ""
	}
	if err := relay.Start(); err != nil {
		return ""
	}
	localURL := fmt.Sprintf("socks5://%s", listen)
	httpRelay.relays[upstreamURL] = &httpRelayEntry{localURL: localURL, relay: relay}
	return localURL
}

func init() {
	log.Infof("proxypool service initialized")
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" && v != "🌐" {
			return v
		}
	}
	return ""
}
