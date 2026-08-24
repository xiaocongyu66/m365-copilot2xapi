package store

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"M365Copilot2ApiX/backend/internal/infra/proxypool/log"
	"M365Copilot2ApiX/backend/internal/infra/proxypool/proxy"
)

// Store 管理已导入的代理节点。支持:
//   - 按行导入(一行一个节点链接)
//   - 列出所有节点
//   - 启用/禁用节点
//   - 删除节点
//   - 按标识查找
//   - 持久化到文件(启动时加载,变更时保存)

type Store struct {
	mu      sync.RWMutex
	proxies map[string]proxy.Proxy // key: Identifier
	filePath string                // 持久化文件路径
}

// New 创建内存 Store(不持久化)
func New() *Store {
	return &Store{proxies: make(map[string]proxy.Proxy)}
}

// NewWithFile 创建带文件持久化的 Store,启动时自动加载
func NewWithFile(path string) *Store {
	s := &Store{proxies: make(map[string]proxy.Proxy), filePath: path}
	s.load()
	return s
}

// load 从文件加载节点
func (s *Store) load() {
	if s.filePath == "" {
		return
	}
	data, err := os.ReadFile(s.filePath)
	if err != nil {
		return // 文件不存在或读取失败,正常启动(空 store)
	}
	text := string(data)
	lines := strings.Split(text, "\n")
	loaded := 0
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		p, err := proxy.ParseProxyFromLink(line)
		if err != nil || p == nil {
			continue
		}
		s.proxies[p.Identifier()] = p
		loaded++
	}
	log.Infof("store: loaded %d proxies from %s", loaded, s.filePath)
}

// save 保存所有节点到文件
func (s *Store) save() {
	if s.filePath == "" {
		return
	}
	var b strings.Builder
	for _, p := range s.proxies {
		link := p.Link()
		if link != "" {
			b.WriteString(link)
			b.WriteString("\n")
		}
	}
	dir := filepath.Dir(s.filePath)
	os.MkdirAll(dir, 0755)
	os.WriteFile(s.filePath, []byte(b.String()), 0644)
}

// ImportFromText 从文本导入节点(一行一个),返回成功导入的数量和跳过的数量。
func (s *Store) ImportFromText(text string) (imported, skipped int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, line := range splitLines(text) {
		line = trimSpace(line)
		if line == "" || hasPrefix(line, "#") || hasPrefix(line, "//") {
			skipped++
			continue
		}
		// 提取 #xx 国旗标记(如 "socks5://1.2.3.4:1080 #cn")
		// Go url.Parse 把 # 后面当 fragment,代理链接解析时 fragment 被丢弃
		// 所以先提取国旗,再从链接里去掉空格+#部分
		countryFromFlag := ""
		if idx := indexOf(line, " #"); idx >= 0 {
			flagPart := trimSpace(line[idx+1:]) // #cn
			if hasPrefix(flagPart, "#") {
				countryFromFlag = upper(flagPart[1:]) // "cn" → "CN"
			}
			line = trimSpace(line[:idx]) // 去掉 #cn 部分
		}
		p, err := proxy.ParseProxyFromLink(line)
		if err != nil || p == nil {
			skipped++
			continue
		}
		// 用国旗标记设置国家
		if countryFromFlag != "" {
			p.SetCountry(countryFromFlag)
		}
		// 节点没名字时自动补一个(类型+服务器:端口)
		if p.BaseInfo().Name == "" {
			base := p.BaseInfo()
			autoName := p.TypeName() + "-" + base.Server + ":" + strconv.Itoa(base.Port)
			p.SetName(autoName)
		}
		id := p.Identifier()
		if _, exists := s.proxies[id]; exists {
			skipped++
			continue
		}
		s.proxies[id] = p
		imported++
	}
	s.save()
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
	s.save()
	return true
}

// Add 添加单个节点(已存在则覆盖),并持久化。
func (s *Store) Add(p proxy.Proxy) {
	if p == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.proxies[p.Identifier()] = p
	s.save()
}

// Count 返回节点总数
func (s *Store) Count() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.proxies)
}

// IDs 返回所有节点的 identifier 列表。
func (s *Store) IDs() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	ids := make([]string, 0, len(s.proxies))
	for id := range s.proxies {
		ids = append(ids, id)
	}
	return ids
}

func splitLines(text string) []string {
	return strings.Split(text, "\n")
}

func trimSpace(s string) string {
	return strings.TrimSpace(s)
}

func hasPrefix(s, prefix string) bool {
	return strings.HasPrefix(s, prefix)
}

func indexOf(s, substr string) int {
	return strings.Index(s, substr)
}

func upper(s string) string {
	return strings.ToUpper(s)
}
