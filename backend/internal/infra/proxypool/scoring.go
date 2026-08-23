package proxypool

import (
	"sync"
	"time"
)

// Score 管理代理节点的动态分数。
// 规则:
//   - 报错多 → 扣分(每次错误 -2)
//   - 持续可用时间长 → 加分(每持续可用 1 分钟 +1,上限 100)
//   - 测活失败 → 大幅扣分(-20)
//   - 测活成功 → 加分(+5)
//   - 分数低于阈值(0)时节点被禁用
//
// 分数动态更新,影响节点选择权重:负载均衡时,分数高的节点多分配请求。

const (
	scoreMin             = -50   // 最低分(低于此值节点被禁用)
	scoreMax             = 100   // 最高分
	scoreErrorPenalty    = -2    // 每次报错扣分
	scoreTestFailPenalty = -20   // 测活失败扣分
	scoreTestPassBonus   = 5     // 测活通过加分
	scoreUptimeBonus     = 1     // 持续可用加分(每分钟)
	scoreDisableThreshold = 0    // 低于此分禁用
)

// NodeScore 记录单个节点的分数和运行时状态
type NodeScore struct {
	mu sync.RWMutex

	Identifier  string    // 节点唯一标识
	Name        string    // 节点名称
	Country     string    // 节点国家代码(CN/HK/US 等)
	Score       int       // 当前分数
	Enabled     bool      // 是否启用(用户可手动禁用)
	AutoDisabled bool     // 因分数过低被自动禁用

	// 打分依据
	ErrorCount    int       // 累计报错数
	LastError     string    // 最近一次报错信息
	LastErrorAt   time.Time // 最近报错时间
	SuccessCount  int       // 累计成功请求数
	LastSuccessAt time.Time // 最近成功时间
	UpSince       time.Time // 节点持续可用起始时间(用于算持续可用时长)

	// 测活结果
	LastCheckAt     time.Time // 最近测活时间
	LastCheckStable bool      // 最近测活是否稳定
	LastCheckBytes  int64     // 最近测活下载字节数
	LastLatency     int64     // 最近测活微软可达往返延迟(纳秒)
	LastPurityScore int       // 最近测活 IP 纯净度评分 0-100(0 表示未测)
	LastIPType      string    // 最近测活 IP 类型 residential/mobile/datacenter
	LastExitIP      string    // 最近测活出口 IP
	LastISP         string    // 最近测活 ISP

	// 负载均衡
	ActiveRequests int // 当前正在处理的请求数
}

// ScoreStore 管理所有节点的分数
type ScoreStore struct {
	mu     sync.RWMutex
	scores map[string]*NodeScore // key: Identifier
}

func NewScoreStore() *ScoreStore {
	return &ScoreStore{scores: make(map[string]*NodeScore)}
}

// Register 注册一个节点(如果已存在则返回已存在的)
func (s *ScoreStore) Register(identifier, name string) *NodeScore {
	s.mu.Lock()
	defer s.mu.Unlock()
	if ns, ok := s.scores[identifier]; ok {
		return ns
	}
	ns := &NodeScore{
		Identifier: identifier,
		Name:      name,
		Score:     50, // 初始分 50
		Enabled:   true,
		UpSince:    time.Now(),
	}
	s.scores[identifier] = ns
	return ns
}

// Get 获取节点分数
func (s *ScoreStore) Get(identifier string) *NodeScore {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.scores[identifier]
}

// NodeScoreSnapshot 是 NodeScore 的快照(不含锁,用于返回给调用方)
type NodeScoreSnapshot struct {
	Identifier       string
	Name             string
	Country          string
	Score            int
	Enabled          bool
	AutoDisabled     bool
	ErrorCount       int
	LastError         string
	LastErrorAt      time.Time
	SuccessCount     int
	LastSuccessAt    time.Time
	UpSince          time.Time
	LastCheckAt      time.Time
	LastCheckStable  bool
	LastCheckBytes   int64
	LastLatency      int64
	LastPurityScore  int
	LastIPType       string
	LastExitIP       string
	LastISP          string
	ActiveRequests   int
}

// List 返回所有节点分数(快照)
func (s *ScoreStore) List() []NodeScoreSnapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]NodeScoreSnapshot, 0, len(s.scores))
	for _, ns := range s.scores {
		ns.mu.RLock()
		result = append(result, NodeScoreSnapshot{
			Identifier:      ns.Identifier,
			Name:            ns.Name,
			Country:         ns.Country,
			Score:           ns.Score,
			Enabled:         ns.Enabled,
			AutoDisabled:    ns.AutoDisabled,
			ErrorCount:      ns.ErrorCount,
			LastError:        ns.LastError,
			LastErrorAt:     ns.LastErrorAt,
			SuccessCount:    ns.SuccessCount,
			LastSuccessAt:   ns.LastSuccessAt,
			UpSince:         ns.UpSince,
			LastCheckAt:     ns.LastCheckAt,
			LastCheckStable: ns.LastCheckStable,
			LastCheckBytes:  ns.LastCheckBytes,
			LastLatency:     ns.LastLatency,
			LastPurityScore: ns.LastPurityScore,
			LastIPType:      ns.LastIPType,
			LastExitIP:      ns.LastExitIP,
			LastISP:         ns.LastISP,
			ActiveRequests:  ns.ActiveRequests,
		})
		ns.mu.RUnlock()
	}
	return result
}

// RecordError 记录一次请求报错(扣分)
func (s *ScoreStore) RecordError(identifier, errMsg string) {
	ns := s.Get(identifier)
	if ns == nil {
		return
	}
	ns.mu.Lock()
	defer ns.mu.Unlock()
	ns.ErrorCount++
	ns.LastError = errMsg
	ns.LastErrorAt = time.Now()
	ns.Score += scoreErrorPenalty
	if ns.Score < scoreMin {
		ns.Score = scoreMin
	}
	// 分数过低自动禁用
	if ns.Score < scoreDisableThreshold {
		ns.AutoDisabled = true
	}
}

// RecordSuccess 记录一次请求成功(如果持续可用,加分)
func (s *ScoreStore) RecordSuccess(identifier string) {
	ns := s.Get(identifier)
	if ns == nil {
		return
	}
	ns.mu.Lock()
	defer ns.mu.Unlock()
	ns.SuccessCount++
	ns.LastSuccessAt = time.Now()

	// 节点恢复可用时自动清除报错状态
	if ns.AutoDisabled && ns.Score >= scoreDisableThreshold {
		ns.AutoDisabled = false
		ns.UpSince = time.Now() // 重新计算持续可用时长
	}

	// 持续可用加分:每持续可用 1 分钟 +1
	if !ns.UpSince.IsZero() {
		uptime := time.Since(ns.UpSince)
		expectedBonus := int(uptime.Minutes())
		// 只加"新增"的持续可用时长(简化:每次成功 +1,上限 100)
		if ns.Score < scoreMax {
			ns.Score++
		}
		_ = expectedBonus
	}
}

// RecordCheckResult 记录测活结果
func (s *ScoreStore) RecordCheckResult(identifier string, stable bool, bytes int64) {
	s.RecordCheckFull(identifier, stable, bytes, 0, 0, "", "", "", "")
}

// RecordCheckFull 记录完整测活结果(含延迟、纯净度、IP 类型、国家)
func (s *ScoreStore) RecordCheckFull(identifier string, stable bool, bytes int64, latencyNs int64, purityScore int, ipType, exitIP, isp, country string) {
	ns := s.Get(identifier)
	if ns == nil {
		return
	}
	ns.mu.Lock()
	defer ns.mu.Unlock()
	ns.LastCheckAt = time.Now()
	ns.LastCheckStable = stable
	ns.LastCheckBytes = bytes
	ns.LastLatency = latencyNs
	ns.LastPurityScore = purityScore
	ns.LastIPType = ipType
	ns.LastExitIP = exitIP
	ns.LastISP = isp
	// 用出口 IP 的国家代码更新(比查服务器地址准确)
	if country != "" {
		ns.Country = country
	}

	if stable {
		ns.Score += scoreTestPassBonus
		if ns.Score > scoreMax {
			ns.Score = scoreMax
		}
		// 测活通过,如果之前被自动禁用,恢复
		if ns.AutoDisabled {
			ns.AutoDisabled = false
			ns.UpSince = time.Now()
		}
	} else {
		ns.Score += scoreTestFailPenalty
		if ns.Score < scoreMin {
			ns.Score = scoreMin
		}
		if ns.Score < scoreDisableThreshold {
			ns.AutoDisabled = true
		}
	}
}

// SetEnabled 手动启用/禁用节点
func (s *ScoreStore) SetEnabled(identifier string, enabled bool) {
	ns := s.Get(identifier)
	if ns == nil {
		return
	}
	ns.mu.Lock()
	defer ns.mu.Unlock()
	ns.Enabled = enabled
	if enabled {
		ns.UpSince = time.Now()
	}
}

// ClearErrors 清除节点的报错记录(用户手动清除,或节点恢复可用时自动清除)
func (s *ScoreStore) ClearErrors(identifier string) {
	ns := s.Get(identifier)
	if ns == nil {
		return
	}
	ns.mu.Lock()
	defer ns.mu.Unlock()
	ns.ErrorCount = 0
	ns.LastError = ""
	ns.LastErrorAt = time.Time{}
	// 清除报错时恢复分数到合理水平
	if ns.Score < 30 {
		ns.Score = 30
	}
	ns.AutoDisabled = false
	ns.UpSince = time.Now()
}

// UsableNodes 返回当前可用的节点(已启用 + 未被自动禁用),按分数降序排列
func (s *ScoreStore) UsableNodes() []*NodeScore {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]*NodeScore, 0, len(s.scores))
	for _, ns := range s.scores {
		ns.mu.RLock()
		if ns.Enabled && !ns.AutoDisabled {
			result = append(result, ns)
		}
		ns.mu.RUnlock()
	}
	// 按分数降序排序(分数高的优先)
	for i := 0; i < len(result); i++ {
		for j := i + 1; j < len(result); j++ {
			if result[j].Score > result[i].Score {
				result[i], result[j] = result[j], result[i]
			}
		}
	}
	return result
}

// SetCountry 设置节点国家代码
func (ns *NodeScore) SetCountry(country string) {
	ns.mu.Lock()
	defer ns.mu.Unlock()
	ns.Country = country
}

// GetCountry 获取节点国家代码
func (ns *NodeScore) GetCountry() string {
	ns.mu.RLock()
	defer ns.mu.RUnlock()
	return ns.Country
}
