package getter

import (
	"encoding/json"
	"io"
	"strings"
	"sync"

	"M365Copilot2ApiX/backend/internal/infra/proxypool/log"

	"M365Copilot2ApiX/backend/internal/infra/proxypool/proxy"
	"M365Copilot2ApiX/backend/internal/infra/proxypool/tool"

	"github.com/ghodss/yaml"
)

// Add key value pair to creatorMap(string → creator) in base.go
func init() {
	Register("subscribe", NewSubscribe)
}

// Subscribe is A Getter with an additional property
type Subscribe struct {
	Url string
}

// Get() of Subscribe is to implement Getter interface
// 自动检测订阅格式:
//   - base64 编码的 txt(v2ray 订阅,每行一个 ss/vmess/trojan/vless 链接)
//   - 纯文本(每行一个链接)
//   - clash.yml(Clash 配置文件,proxies 字段)
//   - singbox.json(sing-box 配置,outbounds 字段)
//   - HTML 网页(模糊抓取页面里的节点链接)
func (s *Subscribe) Get() proxy.ProxyList {
	resp, err := tool.GetHttpClient().Get(s.Url)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil
	}
	bodyStr := string(body)

	// 按格式依次尝试解析
	if proxies := parseClashYAML(bodyStr); len(proxies) > 0 {
		log.Infoln("subscribe parsed as clash.yml: count=%d url=%s", len(proxies), s.Url)
		return proxies
	}
	if proxies := parseSingboxJSON(bodyStr); len(proxies) > 0 {
		log.Infoln("subscribe parsed as singbox.json: count=%d url=%s", len(proxies), s.Url)
		return proxies
	}

	// 尝试 base64 解码(v2ray 订阅格式)
	decoded, err := tool.Base64DecodeString(bodyStr)
	if err == nil {
		decoded = strings.ReplaceAll(decoded, "\t", "")
		nodes := strings.Split(decoded, "\n")
		proxies := StringArray2ProxyArray(nodes)
		if len(proxies) > 0 {
			log.Infoln("subscribe parsed as base64 txt: count=%d url=%s", len(proxies), s.Url)
			return proxies
		}
	}

	// 原始文本当纯文本解析(每行一个链接)
	bodyStr = strings.ReplaceAll(bodyStr, "\t", "")
	nodes := strings.Split(bodyStr, "\n")
	proxies := StringArray2ProxyArray(nodes)
	if len(proxies) > 0 {
		log.Infoln("subscribe parsed as plain text: count=%d url=%s", len(proxies), s.Url)
	}
	return proxies
}

// parseClashYAML 解析 Clash 配置文件(proxies 字段)
func parseClashYAML(body string) proxy.ProxyList {
	// 快速检测:Clash 配置一定有 proxies: 字段
	if !strings.Contains(body, "proxies:") && !strings.Contains(body, "proxies :") {
		return nil
	}
	// 解析 YAML
	var clashConfig struct {
		Proxies []map[string]interface{} `yaml:"proxies" json:"proxies"`
	}
	if err := yaml.Unmarshal([]byte(body), &clashConfig); err != nil {
		return nil
	}
	if len(clashConfig.Proxies) == 0 {
		return nil
	}
	// 把每个 proxy map 转成 JSON,再用 proxy 包解析
	result := make(proxy.ProxyList, 0, len(clashConfig.Proxies))
	for _, p := range clashConfig.Proxies {
		if proxyNode := clashProxyMapToProxy(p); proxyNode != nil {
			result = append(result, proxyNode)
		}
	}
	return result
}

// parseSingboxJSON 解析 sing-box 配置文件(outbounds 字段)
func parseSingboxJSON(body string) proxy.ProxyList {
	// 快速检测:sing-box 配置是 JSON 且有 outbounds 字段
	trimmed := strings.TrimSpace(body)
	if !strings.HasPrefix(trimmed, "{") || !strings.Contains(body, "outbounds") {
		return nil
	}
	var singboxConfig struct {
		Outbounds []map[string]interface{} `json:"outbounds"`
	}
	if err := json.Unmarshal([]byte(body), &singboxConfig); err != nil {
		return nil
	}
	if len(singboxConfig.Outbounds) == 0 {
		return nil
	}
	// sing-box 的 outbound 类型映射到 proxypool 的 proxy 类型
	result := make(proxy.ProxyList, 0, len(singboxConfig.Outbounds))
	for _, ob := range singboxConfig.Outbounds {
		if proxyNode := singboxOutboundToProxy(ob); proxyNode != nil {
			result = append(result, proxyNode)
		}
	}
	return result
}

// clashProxyMapToProxy 把 Clash proxy map 转成 proxypool 的 Proxy 对象
func clashProxyMapToProxy(m map[string]interface{}) proxy.Proxy {
	// proxypool 的 proxy 包有 ClashJSONToProxy 函数,接受 JSON 字符串
	data, err := json.Marshal(m)
	if err != nil {
		return nil
	}
	return proxy.ClashJSONToProxy(data)
}

// singboxOutboundToProxy 把 sing-box outbound 转成 proxypool 的 Proxy 对象
func singboxOutboundToProxy(m map[string]interface{}) proxy.Proxy {
	// sing-box 的 outbound 类型:shadowsocks/vmess/trojan/hysteria2/socks/http
	// 转成 Clash 格式 JSON 再解析
	clashMap := singboxToClashMap(m)
	if clashMap == nil {
		return nil
	}
	data, err := json.Marshal(clashMap)
	if err != nil {
		return nil
	}
	return proxy.ClashJSONToProxy(data)
}

// singboxToClashMap 把 sing-box outbound 转成 Clash proxy 格式
func singboxToClashMap(m map[string]interface{}) map[string]interface{} {
	outType, _ := m["type"].(string)
	server, _ := m["server"].(string)
	serverPort := toInt(m["server_port"])

	base := map[string]interface{}{
		"name":   m["tag"],
		"server": server,
		"port":   serverPort,
	}

	switch outType {
	case "shadowsocks":
		base["type"] = "ss"
		base["cipher"] = m["method"]
		base["password"] = m["password"]
	case "vmess":
		base["type"] = "vmess"
		base["uuid"] = m["uuid"]
		if sec, ok := m["security"]; ok {
			base["cipher"] = sec
		}
		if tls, ok := m["tls"].(map[string]interface{}); ok {
			if enabled, _ := tls["enabled"].(bool); enabled {
				base["tls"] = true
			}
			if sni, _ := tls["server_name"].(string); sni != "" {
				base["servername"] = sni
			}
		}
		if transport, ok := m["transport"].(map[string]interface{}); ok {
			tType, _ := transport["type"].(string)
			if tType == "ws" {
				wsOpts := map[string]interface{}{}
				if path, _ := transport["path"].(string); path != "" {
					wsOpts["path"] = path
				}
				if headers, ok := transport["headers"].(map[string]interface{}); ok {
					wsOpts["headers"] = headers
				}
				base["ws-opts"] = wsOpts
				base["network"] = "ws"
			}
		}
	case "trojan":
		base["type"] = "trojan"
		base["password"] = m["password"]
		if tls, ok := m["tls"].(map[string]interface{}); ok {
			if sni, _ := tls["server_name"].(string); sni != "" {
				base["sni"] = sni
			}
		}
	case "hysteria2":
		base["type"] = "hysteria2"
		base["password"] = m["password"]
		if tls, ok := m["tls"].(map[string]interface{}); ok {
			if sni, _ := tls["server_name"].(string); sni != "" {
				base["sni"] = sni
			}
		}
	case "socks":
		base["type"] = "socks5"
		if username, _ := m["username"].(string); username != "" {
			base["username"] = username
		}
		if password, _ := m["password"].(string); password != "" {
			base["password"] = password
		}
	case "http":
		base["type"] = "http"
		if username, _ := m["username"].(string); username != "" {
			base["username"] = username
		}
		if password, _ := m["password"].(string); password != "" {
			base["password"] = password
		}
	default:
		return nil
	}
	return base
}

func toInt(v interface{}) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case json.Number:
		i, _ := n.Int64()
		return int(i)
	default:
		return 0
	}
}

// Subscribe is to implement Getter interface. It gets proxies and send proxy to channel one by one
func (s *Subscribe) Get2ChanWG(pc chan proxy.Proxy, wg *sync.WaitGroup) {
	defer wg.Done()
	nodes := s.Get()
	log.Infoln("STATISTIC: Subscribe\tcount=%d\turl=%s", len(nodes), s.Url)
	for _, node := range nodes {
		pc <- node
	}
}

func NewSubscribe(options tool.Options) (getter Getter, err error) {
	urlInterface, found := options["url"]
	if found {
		url, err := AssertTypeStringNotNull(urlInterface)
		if err != nil {
			return nil, err
		}
		return &Subscribe{
			Url: url,
		}, nil
	}
	return nil, ErrorUrlNotFound
}

// FetchSubscribeURL 从订阅 URL 抓取代理列表(便捷函数)。
// 自动检测格式:base64 编码的 txt、纯文本、clash.yml、singbox.json、HTML 网页。
func FetchSubscribeURL(url string) ([]proxy.Proxy, error) {
	sub := &Subscribe{Url: url}
	return sub.Get(), nil
}
