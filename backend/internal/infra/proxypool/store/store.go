package store

import (
	"sync"

	"M365Copilot2ApiX/backend/internal/infra/proxypool/proxy"
)

// Store 管理已导入的代理节点。支持:
//   - 按行导入(一行一个节点链接)
//   - 列出所有节点
//   - 启用/禁用节点
//   - 删除节点
//   - 按标识查找

type Store struct {
	mu      sync.RWMutex
	proxies map[string]proxy.Proxy // key: Identifier
}

func New() *Store {
	return &Store{proxies: make(map[string]proxy.Proxy)}
}

// ImportFromText 从文本导入节点(一行一个),返回成功导入的数量和跳过的数量。
// 支持的格式:ss:// vmess:// vless:// trojan:// hysteria2:// http:// socks5:// 等
func (s *Store) ImportFromText(text string) (imported, skipped int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, line := range splitLines(text) {
		line = trimSpace(line)
		if line == "" || hasPrefix(line, "#") || hasPrefix(line, "//") {
			skipped++
			continue
		}
		p, err := proxy.ParseProxyFromLink(line)
		if err != nil || p == nil {
			skipped++
			continue
		}
		id := p.Identifier()
		if _, exists := s.proxies[id]; exists {
			skipped++
			continue
		}
		s.proxies[id] = p
		imported++
	}
	return
}

// List 返回所有节点(快照)
func (s *Store) List() []proxy.Proxy {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]proxy.Proxy, 0, len(s.proxies))
	for _, p := range s.proxies {
		result = append(result, p)
	}
	return result
}

// Get 按标识查找节点
func (s *Store) Get(identifier string) (proxy.Proxy, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	p, ok := s.proxies[identifier]
	return p, ok
}

// Delete 删除节点
func (s *Store) Delete(identifier string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.proxies[identifier]; !ok {
		return false
	}
	delete(s.proxies, identifier)
	return true
}

// Count 返回节点总数
func (s *Store) Count() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.proxies)
}

// splitLines 分行(兼容 \n 和 \r\n)
func splitLines(text string) []string {
	var lines []string
	current := ""
	for _, ch := range text {
		if ch == '\n' {
			lines = append(lines, current)
			current = ""
		} else if ch == '\r' {
			continue
		} else {
			current += string(ch)
		}
	}
	if current != "" {
		lines = append(lines, current)
	}
	return lines
}

func trimSpace(s string) string {
	start, end := 0, len(s)
	for start < end && (s[start] == ' ' || s[start] == '\t') {
		start++
	}
	for end > start && (s[end-1] == ' ' || s[end-1] == '\t') {
		end--
	}
	return s[start:end]
}

func hasPrefix(s, prefix string) bool {
	if len(s) < len(prefix) {
		return false
	}
	return s[:len(prefix)] == prefix
}
