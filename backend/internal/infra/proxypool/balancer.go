package proxypool

import (
	"strings"
	"sync/atomic"
)

// Balancer 实现基于分数的负载均衡:
//   - 启用节点后,请求走节点 IP(不走原始 IP)
//   - 多节点平均分配(如 12 请求 / 2 节点 = 各 6 个)
//   - 不可靠的节点少分配,可靠的节点多分配(按分数加权)
//
// 加权策略:分数高的节点权重高,分到更多请求。
// 如果某节点当前活跃请求多,适当降低权重(避免过载)。

type Balancer struct {
	store   *ScoreStore
	counter int64 // 用于加权轮询的计数器(atomic 操作)
}

func NewBalancer(store *ScoreStore) *Balancer {
	return &Balancer{store: store}
}

// 中国地区代码(注册 M365 时只允许这些地区的代理)
var allowedRegisterCountries = map[string]bool{"CN": true, "HK": true, "MO": true, "TW": true}

// PickNode 为一个新请求选择一个节点(排除中国代理)。
// M365 请求不走中国代理(直连即可),但注册时可以走中国代理。
func (b *Balancer) PickNode() (identifier string, release func()) {
	return b.pickNodeWithFilter(func(country string) bool {
		// 排除中国代理(M365 请求不走中国代理)
		return !isChineseProxy(country)
	})
}

// PickNodeForRegister 为 M365 注册选择一个节点(只选 CN/HK/MO/TW 地区)。
// 注册网站 GeoIP 限制只允许这些地区。
func (b *Balancer) PickNodeForRegister() (identifier string, release func()) {
	return b.pickNodeWithFilter(func(country string) bool {
		// 只选 CN/HK/MO/TW
		return isAllowedRegisterCountry(country)
	})
}

func isChineseProxy(country string) bool {
	c := strings.ToUpper(strings.TrimSpace(country))
	return c == "CN" || c == "CHINA" || strings.Contains(c, "CN")
}

func isAllowedRegisterCountry(country string) bool {
	c := strings.ToUpper(strings.TrimSpace(country))
	return allowedRegisterCountries[c] || c == "CHINA" || c == "HONG KONG" || c == "HONGKONG" || c == "MACAO" || c == "MACAU" || c == "TAIWAN"
}

// pickNodeWithFilter 按过滤条件选择节点
func (b *Balancer) pickNodeWithFilter(countryFilter func(country string) bool) (identifier string, release func()) {
	allNodes := b.store.UsableNodes()
	if len(allNodes) == 0 {
		return "", nil
	}
	// 按国家过滤
	nodes := make([]*NodeScore, 0, len(allNodes))
	for _, ns := range allNodes {
		if countryFilter(ns.Country) {
			nodes = append(nodes, ns)
		}
	}
	if len(nodes) == 0 {
		return "", nil
	}

	// 加权随机选择:
	//   weight = max(score, 1) / (activeRequests + 1)
	// 分数高且当前负载低的节点权重最高。
	totalWeight := 0
	weights := make([]int, len(nodes))
	for i, ns := range nodes {
		ns.mu.RLock()
		score := ns.Score
		if score < 1 {
			score = 1
		}
		active := ns.ActiveRequests
		ns.mu.RUnlock()
		weight := score * 100 / (active + 1)
		if weight < 1 {
			weight = 1
		}
		weights[i] = weight
		totalWeight += weight
	}

	if totalWeight == 0 {
		// 全部权重为 0,平均分配
		return nodes[0].Identifier, b.makeRelease(nodes[0].Identifier)
	}

	// 加权随机:按权重随机选一个
	pick := atomic.AddInt64(&b.counter, 1)
	pos := int(pick % int64(totalWeight))
	if pos < 0 {
		pos = -pos
	}
	for i, w := range weights {
		pos -= w
		if pos < 0 {
			nodes[i].mu.Lock()
			nodes[i].ActiveRequests++
			nodes[i].mu.Unlock()
			return nodes[i].Identifier, b.makeRelease(nodes[i].Identifier)
		}
	}

	// 兜底
	nodes[0].mu.Lock()
	nodes[0].ActiveRequests++
	nodes[0].mu.Unlock()
	return nodes[0].Identifier, b.makeRelease(nodes[0].Identifier)
}

func (b *Balancer) makeRelease(identifier string) func() {
	return func() {
		ns := b.store.Get(identifier)
		if ns == nil {
			return
		}
		ns.mu.Lock()
		if ns.ActiveRequests > 0 {
			ns.ActiveRequests--
		}
		// 请求完成算一次成功(持续可用加分)
		ns.mu.Unlock()
		b.store.RecordSuccess(identifier)
	}
}

// 修正:counter 需要是 Balancer 的字段
