package m365

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/Dreamacro/clash/adapter"
	C "github.com/Dreamacro/clash/constant"
	"github.com/gorilla/websocket"

	"M365Copilot2ApiX/backend/internal/infra/proxypool"
	"M365Copilot2ApiX/backend/internal/infra/proxypool/proxy"
)

// nodeResolver 是全局的节点解析器(由 application.go 注入 proxypool.Service)
// 如果 proxypool 未启用或没有可用节点,HTTP/WebSocket 请求走原始 IP
var (
	nodeResolverMu sync.RWMutex
	nodeResolver   *proxypool.Service
)

// SetNodeResolver 注入 proxypool Service(由 application.go 在启动时调用)
func SetNodeResolver(svc *proxypool.Service) {
	nodeResolverMu.Lock()
	defer nodeResolverMu.Unlock()
	nodeResolver = svc
}

// getNodeResolver 获取当前的 proxypool Service(可能为 nil)
func getNodeResolver() *proxypool.Service {
	nodeResolverMu.RLock()
	defer nodeResolverMu.RUnlock()
	return nodeResolver
}

// DefaultHTTPClient 返回 HTTP 客户端。
// 如果 proxypool 启用且有可用节点,请求走节点 IP(通过 clash adapter 代理);
// 否则走原始 IP(直连)。
func DefaultHTTPClient() *http.Client {
	svc := getNodeResolver()
	if svc == nil {
		return &http.Client{Timeout: 120 * time.Second}
	}
	// 返回一个客户端,每次请求都动态选择节点
	return &http.Client{
		Timeout: 120 * time.Second,
		Transport: &nodeRoutedTransport{
			svc: svc,
		},
	}
}

// nodeRoutedTransport 是一个 HTTP Transport,根据 proxypool 负载均衡选择节点路由请求
type nodeRoutedTransport struct {
	svc *proxypool.Service
}

func (t *nodeRoutedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// 选择一个节点
	nodeID, release := t.svc.PickNodeForRequest()
	if release != nil {
		defer release()
	}
	if nodeID == "" {
		// 没有可用节点,直连
		return http.DefaultTransport.RoundTrip(req)
	}
	// 用 clash adapter 通过节点代理请求
	transport := t.buildNodeTransport(nodeID)
	if transport == nil {
		// 节点不可用,回退直连
		t.svc.RecordRequestError(nodeID, nodeID, req.URL.String(), "build transport failed")
		return http.DefaultTransport.RoundTrip(req)
	}
	resp, err := transport.RoundTrip(req)
	if err != nil {
		t.svc.RecordRequestError(nodeID, nodeID, req.URL.String(), err.Error())
		return nil, err
	}
	t.svc.RecordRequestSuccess(nodeID)
	return resp, nil
}

// buildNodeTransport 为指定节点构造 HTTP Transport(通过 clash adapter 代理)
func (t *nodeRoutedTransport) buildNodeTransport(nodeID string) http.RoundTripper {
	p, ok := t.svc.Store().Get(nodeID)
	if !ok {
		return nil
	}
	// 把 proxy.Proxy 转成 clash adapter
	pmap, err := proxyMapFromProxy(p)
	if err != nil {
		return nil
	}
	clashProxy, err := adapter.ParseProxy(pmap)
	if err != nil {
		return nil
	}
	return &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			// 构造 clash Metadata
			host, port, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, err
			}
			portNum, err := parsePort(port)
			if err != nil {
				return nil, err
			}
			metadata := &C.Metadata{
				Host:    host,
				DstPort: C.Port(portNum),
				NetWork: C.TCP,
			}
			return clashProxy.DialContext(ctx, metadata)
		},
		TLSClientConfig:    &tls.Config{InsecureSkipVerify: false},
		MaxIdleConns:       10,
		IdleConnTimeout:    30 * time.Second,
		TLSHandshakeTimeout: 10 * time.Second,
	}
}

// proxyMapFromProxy 把 proxy.Proxy 转成 clash adapter 需要的 map[string]interface{}
func proxyMapFromProxy(p proxy.Proxy) (map[string]interface{}, error) {
	// proxy.Proxy 的 String() 方法返回 JSON,解析成 map
	data := p.String()
	var m map[string]interface{}
	if err := json.Unmarshal([]byte(data), &m); err != nil {
		return nil, err
	}
	return m, nil
}

func parsePort(s string) (uint16, error) {
	var p int
	if _, err := fmt.Sscanf(s, "%d", &p); err != nil {
		return 0, err
	}
	return uint16(p), nil
}

// DefaultWebSocketDialer 返回 WebSocket 拨号器。
// 如果 proxypool 启用且有可用节点,WebSocket 走节点 IP;否则直连。
func DefaultWebSocketDialer() *websocket.Dialer {
	svc := getNodeResolver()
	if svc == nil {
		return &websocket.Dialer{HandshakeTimeout: 30 * time.Second}
	}
	return &websocket.Dialer{
		HandshakeTimeout: 30 * time.Second,
		NetDialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			// 选择节点
			nodeID, release := svc.PickNodeForRequest()
			if release != nil {
				defer release()
			}
			if nodeID == "" {
				// 直连
				var d net.Dialer
				return d.DialContext(ctx, network, addr)
			}
			// 通过节点代理
			p, ok := svc.Store().Get(nodeID)
			if !ok {
				var d net.Dialer
				return d.DialContext(ctx, network, addr)
			}
			pmap, err := proxyMapFromProxy(p)
			if err != nil {
				svc.RecordRequestError(nodeID, nodeID, addr, err.Error())
				var d net.Dialer
				return d.DialContext(ctx, network, addr)
			}
			clashProxy, err := adapter.ParseProxy(pmap)
			if err != nil {
				svc.RecordRequestError(nodeID, nodeID, addr, err.Error())
				var d net.Dialer
				return d.DialContext(ctx, network, addr)
			}
			host, port, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, err
			}
			portNum, _ := parsePort(port)
			metadata := &C.Metadata{
				Host:    host,
				DstPort: C.Port(portNum),
				NetWork: C.TCP,
			}
			conn, err := clashProxy.DialContext(ctx, metadata)
			if err != nil {
				svc.RecordRequestError(nodeID, nodeID, addr, err.Error())
				return nil, err
			}
			svc.RecordRequestSuccess(nodeID)
			return conn, nil
		},
	}
}

// CurlcffiHTTPClient 用 go-curlcffi 创建带 TLS 指纹模拟的 HTTP 客户端(反检测更强)
// 暂未集成,后续用于替换 proxypool 抓取器和 M365 OAuth 的 HTTP 客户端
func CurlcffiHTTPClient(proxyURL string) *http.Client {
	// TODO: 用 go-curlcffi 创建带 TLS 指纹模拟的客户端
	_ = url.Parse // placeholder to keep import
	return &http.Client{Timeout: 120 * time.Second}
}
