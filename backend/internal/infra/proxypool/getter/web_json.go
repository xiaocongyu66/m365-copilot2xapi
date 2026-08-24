package getter

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"

	"M365Copilot2ApiX/backend/internal/infra/proxypool/log"
	"M365Copilot2ApiX/backend/internal/infra/proxypool/proxy"
	"M365Copilot2ApiX/backend/internal/infra/proxypool/tool"
)

// WebJSON 是通用 JSON 代理列表抓取器。
//
// URL fragment 配置(用 & 分隔,全可选):
//   #json=/data&ip=ip&port=port&scheme=socks5&proto=protocols
//
// 参数:
//   json  — 代理数组在 JSON 里的路径(用 / 分隔多层,默认根节点)
//   ip    — IP 字段名(默认 "ip")
//   port  — port 字段名(默认 "port")
//   scheme— 默认 scheme(当 JSON 项没指定协议时用,默认 "socks5")
//   proto — 协议字段名(可选,值可以是数组或字符串;含 socks5→socks5,http→http)
//
// 示例:
//   geonode: https://proxylist.geonode.com/api/proxy-list?limit=500#json=/data&ip=ip&port=port&proto=protocols
//   proxy.scdn.io API: https://proxy.scdn.io/api/get_proxy.php?protocol=socks5&count=100#json=/data/proxies&scheme=socks5

func init() {
	Register("webjson", NewWebJSONGetter)
}

type WebJSON struct {
	Url string
}

func (w *WebJSON) Get() proxy.ProxyList {
	rawURL := w.Url
	// 解析 fragment 里的配置
	jsonPath, ipField, portField, scheme, protoField := parseJSONConfig(rawURL)

	// 去掉 fragment
	if idx := strings.Index(rawURL, "#"); idx >= 0 {
		rawURL = rawURL[:idx]
	}

	resp, err := tool.GetHttpClient().Get(rawURL)
	if err != nil {
		log.Errorf("[webjson] HTTP GET failed: %v (url=%s)", err, rawURL)
		return nil
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Errorf("[webjson] read body failed: %v (url=%s)", err, rawURL)
		return nil
	}
	log.Infof("[webjson] got %d bytes, jsonPath=%s (url=%s)", len(body), jsonPath, rawURL)

	// 解析整个 JSON
	var root interface{}
	if err := json.Unmarshal(body, &root); err != nil {
		log.Errorf("[webjson] parse json failed: %v (url=%s)", err, w.Url)
		return nil
	}

	// 按 jsonPath 找到代理数组
	arr := findJSONArray(root, jsonPath)
	if arr == nil {
		log.Errorf("[webjson] proxy array not found at path %s (url=%s)", jsonPath, w.Url)
		return nil
	}

	links := make([]string, 0, len(arr))
	for _, item := range arr {
		// 元素可能是对象 {"ip":"1.2.3.4","port":"8080"} 或字符串 "1.2.3.4:8080"
		if str, ok := item.(string); ok {
			// 字符串格式:直接当 ip:port,加 scheme 前缀
			if str != "" {
				links = append(links, fmt.Sprintf("%s://%s", scheme, str))
			}
			continue
		}
		m, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		ip, _ := m[ipField].(string)
		port, _ := m[portField].(string)
		// port 可能是数字
		if port == "" {
			if pNum, ok := m[portField].(float64); ok {
				port = fmt.Sprintf("%d", int(pNum))
			}
		}
		if ip == "" || port == "" {
			continue
		}
		// 判断 scheme
		s := scheme
		if protoField != "" {
			if proto, ok := m[protoField]; ok {
				s = schemeFromProto(proto, scheme)
			}
		}
		links = append(links, fmt.Sprintf("%s://%s:%s", s, ip, port))
	}
	return StringArray2ProxyArray(links)
}

// parseJSONConfig 从 URL fragment 解析配置。
func parseJSONConfig(url string) (jsonPath, ipField, portField, scheme, protoField string) {
	ipField = "ip"
	portField = "port"
	scheme = "socks5"
	idx := strings.Index(url, "#")
	if idx < 0 {
		return
	}
	frag := url[idx+1:]
	for _, kv := range strings.Split(frag, "&") {
		parts := strings.SplitN(kv, "=", 2)
		if len(parts) != 2 {
			continue
		}
		k, v := parts[0], parts[1]
		switch k {
		case "json":
			jsonPath = v
		case "ip":
			ipField = v
		case "port":
			portField = v
		case "scheme":
			scheme = v
		case "proto":
			protoField = v
		}
	}
	return
}

// findJSONArray 按 path(用 / 分隔)在 JSON 里找数组。
func findJSONArray(root interface{}, path string) []interface{} {
	if path == "" {
		if arr, ok := root.([]interface{}); ok {
			return arr
		}
		return nil
	}
	current := root
	for _, seg := range strings.Split(strings.Trim(path, "/"), "/") {
		if seg == "" {
			continue
		}
		m, ok := current.(map[string]interface{})
		if !ok {
			return nil
		}
		current, ok = m[seg]
		if !ok {
			return nil
		}
	}
	if arr, ok := current.([]interface{}); ok {
		return arr
	}
	return nil
}

// schemeFromProto 从协议字段值判断 scheme。
// 支持:数组(取第一个含 socks5/http 的)、字符串。
func schemeFromProto(proto interface{}, defaultScheme string) string {
	switch v := proto.(type) {
	case string:
		p := strings.ToLower(v)
		if strings.Contains(p, "socks5") {
			return "socks5"
		}
		if strings.Contains(p, "socks4") {
			return "socks4"
		}
		if strings.Contains(p, "http") {
			return "http"
		}
	case []interface{}:
		for _, item := range v {
			if s, ok := item.(string); ok {
				p := strings.ToLower(s)
				if strings.Contains(p, "socks5") {
					return "socks5"
				}
				if strings.Contains(p, "socks4") {
					return "socks4"
				}
				if strings.Contains(p, "http") {
					return "http"
				}
			}
		}
	}
	return defaultScheme
}

func (w *WebJSON) Get2ChanWG(pc chan proxy.Proxy, wg *sync.WaitGroup) {
	defer wg.Done()
	nodes := w.Get()
	log.Infof("STATISTIC: WebJSON\tcount=%d\turl=%s", len(nodes), w.Url)
	for _, node := range nodes {
		pc <- node
	}
}

func NewWebJSONGetter(options tool.Options) (getter Getter, err error) {
	urlInterface, found := options["url"]
	if found {
		url, err := AssertTypeStringNotNull(urlInterface)
		if err != nil {
			return nil, err
		}
		return &WebJSON{Url: url}, nil
	}
	return nil, ErrorUrlNotFound
}
