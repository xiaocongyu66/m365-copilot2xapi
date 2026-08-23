package proxypool

import (
	"net/http"
	"sync"
	"time"

	"M365Copilot2ApiX/backend/internal/infra/proxypool/geoip"
	"M365Copilot2ApiX/backend/internal/infra/proxypool/healthcheck"
	"M365Copilot2ApiX/backend/internal/infra/proxypool/log"
	"M365Copilot2ApiX/backend/internal/infra/proxypool/store"
)

// Service 是 proxypool 的顶层服务,整合:
//   - Store:节点存储和导入
//   - ScoreStore:打分
//   - Balancer:负载均衡
//   - Fetcher:定时抓取
//   - Checker:定时测活
//   - ErrorReporter:请求报错推送
type Service struct {
	mu       sync.RWMutex
	store    *store.Store
	score    *ScoreStore
	balancer *Balancer
	fetcher  *Fetcher
	checker  *Checker
	registrar *Registrar

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
		store: store.New(),
	}
	s.score = NewScoreStore()
	s.balancer = NewBalancer(s.score)
	s.fetcher = NewFetcher(s.store, s.score)
	s.checker = NewChecker(s.store, s.score)
	s.registrar = NewRegistrar(s)
	return s
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

// Checker 返回测活器
func (s *Service) Checker() *Checker { return s.checker }

// CheckOne 对单个节点做两层测试(TCP/UDP + 微软可达)
func (s *Service) CheckOne(identifier string) {
	p, ok := s.store.Get(identifier)
	if !ok {
		return
	}
	// 用 healthcheck 的 m365CheckOne 测试
	result := healthcheck.M365CheckOnePublic(p)
	s.score.RecordCheckResult(identifier, result.Stable, result.Bytes)
}

// CheckOneSync 同步测试单个节点,返回结果(给 API 用)
func (s *Service) CheckOneSync(identifier string) (accessible, stable bool, err string) {
	p, ok := s.store.Get(identifier)
	if !ok {
		return false, false, "node not found"
	}
	result := healthcheck.M365CheckOnePublic(p)
	s.score.RecordCheckResult(identifier, result.Stable, result.Bytes)
	return result.Accessible, result.Stable, result.Error
}

// Start 启动抓取器和测活器
func (s *Service) Start() {
	// 初始化 GeoIP 数据库(自动下载,加载本地数据库)
	geoip.Get()
	s.checker.Start()
	// fetcher 由配置驱动启动
}

// Stop 停止所有后台任务
func (s *Service) Stop() {
	s.checker.Stop()
	s.fetcher.Stop()
}

// ImportNodes 从文本导入节点(一行一个)
func (s *Service) ImportNodes(text string) (imported, skipped int) {
	return s.store.ImportFromText(text)
}

// ListNodes 列出所有节点(带分数)
func (s *Service) ListNodes() []NodeView {
	proxies := s.store.List()
	scores := s.score.List()
	scoreMap := make(map[string]NodeScoreSnapshot, len(scores))
	for _, sc := range scores {
		scoreMap[sc.Identifier] = sc
	}

	views := make([]NodeView, 0, len(proxies))
	for _, p := range proxies {
		sc := scoreMap[p.Identifier()]
		views = append(views, NodeView{
			Identifier:    p.Identifier(),
			Name:          p.BaseInfo().Name,
			Type:          p.TypeName(),
			Server:        p.BaseInfo().Server,
			Port:          p.BaseInfo().Port,
			Country:       firstNonEmpty(sc.Country, p.BaseInfo().Country),
			Score:         sc.Score,
			Enabled:       sc.Enabled,
			AutoDisabled:  sc.AutoDisabled,
			ErrorCount:     sc.ErrorCount,
			LastError:      sc.LastError,
			LastErrorAt:    sc.LastErrorAt,
			SuccessCount:   sc.SuccessCount,
			LastSuccessAt:  sc.LastSuccessAt,
			LastCheckAt:    sc.LastCheckAt,
			LastCheckStable: sc.LastCheckStable,
			ActiveRequests: sc.ActiveRequests,
		})
	}
	return views
}

// NodeView 是节点的完整视图(基本信息 + 分数 + 状态)
type NodeView struct {
	Identifier     string    `json:"identifier"`
	Name           string    `json:"name"`
	Type           string    `json:"type"`
	Server         string    `json:"server"`
	Port           int       `json:"port"`
	Country        string    `json:"country"`
	Score          int       `json:"score"`
	Enabled        bool      `json:"enabled"`
	AutoDisabled   bool      `json:"autoDisabled"`
	ErrorCount     int       `json:"errorCount"`
	LastError      string    `json:"lastError"`
	LastErrorAt    time.Time `json:"lastErrorAt"`
	SuccessCount   int       `json:"successCount"`
	LastSuccessAt  time.Time `json:"lastSuccessAt"`
	LastCheckAt    time.Time `json:"lastCheckAt"`
	LastCheckStable bool    `json:"lastCheckStable"`
	ActiveRequests int       `json:"activeRequests"`
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

// DeleteNodes 批量删除节点
func (s *Service) DeleteNodes(identifiers []string) {
	for _, id := range identifiers {
		s.store.Delete(id)
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

// GetProxyURL 获取节点的代理 URL(供 HTTP 客户端使用)
// 如果没有可用节点,返回空字符串(走原始 IP)
func (s *Service) GetProxyURL(identifier string) string {
	if identifier == "" {
		return ""
	}
	_, ok := s.store.Get(identifier)
	if !ok {
		return ""
	}
	// 构造代理 URL(根据节点类型)——M365 走 clash adapter,这里返回空
	return ""
}

// HTTPClientWithNode 返回使用指定节点的 HTTP 客户端
// 节点通过 clash adapter 代理 HTTP 请求
func (s *Service) HTTPClientWithNode(identifier string) *http.Client {
	if identifier == "" {
		return http.DefaultClient
	}
	// TODO: 用 go-curlcffi 创建带代理的客户端
	return http.DefaultClient
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
