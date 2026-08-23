package turnstile

import (
	"fmt"
	"net"
	"net/url"
	"strings"
	"sync"
	"time"

	"M365Copilot2ApiX/backend/internal/infra/proxypool/minirelay"
)

// proxyRelayManager 管理为带认证 socks5 代理启动的本地中继。
// Chrome 不支持 socks5://user:pass@host:port,需要先转成无认证的本地 socks5。
type proxyRelayManager struct {
	mu        sync.Mutex
	relays    map[string]*relayEntry
	startPort int
}

type relayEntry struct {
	localURL string
	relay    *minirelay.Relay
}

var proxyRelay = &proxyRelayManager{
	relays:    make(map[string]*relayEntry),
	startPort: 19200,
}

// maybeRelayProxy 检测代理是否为带认证的 socks5,如果是则启动本地中继
// 并返回无认证的本地 socks5 URL;否则原样返回。
// 对 http 代理带认证的情况,Chrome 原生支持,不需要中继。
func maybeRelayProxy(proxy string) string {
	proxy = strings.TrimSpace(proxy)
	if proxy == "" {
		return ""
	}
	u, err := url.Parse(proxy)
	if err != nil {
		return proxy
	}
	scheme := strings.ToLower(u.Scheme)
	if !strings.HasPrefix(scheme, "socks") {
		return proxy
	}
	if u.User == nil {
		return proxy
	}

	proxyRelay.mu.Lock()
	defer proxyRelay.mu.Unlock()

	if existing, ok := proxyRelay.relays[proxy]; ok && existing.relay != nil {
		return existing.localURL
	}

	port := proxyRelay.allocatePort()
	listen := fmt.Sprintf("127.0.0.1:%d", port)
	localURL := fmt.Sprintf("socks5://%s", listen)

	relay, err := minirelay.New(listen, proxy)
	if err != nil {
		fmt.Printf("[relay] failed to create minirelay: %v\n", err)
		return proxy
	}
	if err := relay.Start(); err != nil {
		fmt.Printf("[relay] failed to start: %v\n", err)
		return proxy
	}
	if !waitPortReady("127.0.0.1", port, 3*time.Second) {
		fmt.Printf("[relay] port not ready on %s\n", listen)
		relay.Close()
		return proxy
	}

	fmt.Printf("[relay] %s → %s (auth stripped)\n", listen, proxy)
	proxyRelay.relays[proxy] = &relayEntry{
		localURL: localURL,
		relay:    relay,
	}
	return localURL
}

func (m *proxyRelayManager) allocatePort() int {
	for port := m.startPort; port < m.startPort+500; port++ {
		ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err == nil {
			ln.Close()
			return port
		}
	}
	return m.startPort
}

func waitPortReady(host string, port int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", fmt.Sprintf("%s:%d", host, port), 200*time.Millisecond)
		if err == nil {
			conn.Close()
			return true
		}
		time.Sleep(100 * time.Millisecond)
	}
	return false
}

// StopAllRelays 清理所有中继(进程退出时调用)。
func StopAllRelays() {
	proxyRelay.mu.Lock()
	defer proxyRelay.mu.Unlock()
	for _, e := range proxyRelay.relays {
		if e.relay != nil {
			e.relay.Close()
		}
	}
	proxyRelay.relays = make(map[string]*relayEntry)
}
