package healthcheck

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"M365Copilot2ApiX/backend/internal/infra/proxypool/log"
	"M365Copilot2ApiX/backend/internal/infra/proxypool/proxy"
)

// IPInfo 是 ip-api.com 返回的 IP 信誉信息
type IPInfo struct {
	Status      string `json:"status"`
	Query       string `json:"query"`
	ISP         string `json:"isp"`
	Org         string `json:"org"`
	AS          string `json:"as"`
	Mobile      bool   `json:"mobile"`
	Proxy       bool   `json:"proxy"`
	Hosting     bool   `json:"hosting"`
	CountryCode string `json:"countryCode"`
}

// IPCheckResult 是纯净度测试结果
type IPCheckResult struct {
	ExitIP      string // 出口 IP
	IPType      string // residential / mobile / datacenter
	IPScore     int    // 0-100 纯净度评分
	ISP         string
	CountryCode string // 出口 IP 的国家代码(从 ip-api.com 获取,比查服务器地址准确)
}

// CheckIPCleanliness 通过代理获取出口 IP 并查询纯净度
// 评分:residential=100, mobile=90, datacenter(非proxy)=60, datacenter+proxy=30
// 低于 40 分的节点会被丢弃
func CheckIPCleanliness(p proxy.Proxy) *IPCheckResult {
	// 1. 通过代理访问 ipify 获取出口 IP
	exitIP, err := probeExitIP(p)
	if err != nil || exitIP == "" {
		return nil
	}

	// 2. 查询 ip-api.com 获取 IP 信誉(含纯净度评分)
	info, err := fetchIPInfo(exitIP)
	if err != nil {
		// ip-api.com 失败,三源并发查国家(负载均衡,哪个快用哪个)
		log.Debugf("[ipcheck] ip-api.com failed for %s: %v, concurrent country lookup", exitIP, err)
		country := fetchCountryConcurrent(exitIP)
		return &IPCheckResult{ExitIP: exitIP, IPScore: 50, CountryCode: country}
	}

	ipType := classifyIPType(info)
	score := computeIPScore(info, ipType)

	return &IPCheckResult{
		ExitIP:      exitIP,
		IPType:      ipType,
		IPScore:     score,
		ISP:         info.ISP,
		CountryCode: info.CountryCode,
	}
}

// probeExitIP 通过代理访问 ipify 获取出口 IP
// ProbeExitIP 通过代理访问 ipify 获取出口 IP(导出给 checker 用)
func ProbeExitIP(p proxy.Proxy) (string, error) {
	return probeExitIP(p)
}

func probeExitIP(p proxy.Proxy) (string, error) {
	pmap, err := parseProxyMap(p)
	if err != nil {
		return "", err
	}
	clashProxy, err := parseClashProxy(pmap, p)
	if err != nil {
		return "", err
	}
	addr, err := urlToMetadata("https://api.ipify.org?format=json")
	if err != nil {
		return "", err
	}
	transport := newProxyTransport(clashProxy, addr, 8*time.Second)
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second}

	req, _ := http.NewRequest(http.MethodGet, "https://api.ipify.org?format=json", nil)
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	var result struct {
		IP string `json:"ip"`
	}
	if json.Unmarshal(body, &result) == nil {
		return result.IP, nil
	}
	return "", fmt.Errorf("no IP in response")
}

// fetchIPInfo 查询 ip-api.com(免费,无需 key,45 次/分钟)
func fetchIPInfo(ip string) (*IPInfo, error) {
	client := &http.Client{Timeout: 10 * time.Second}
	url := "http://ip-api.com/json/" + ip + "?fields=status,query,isp,org,as,mobile,proxy,hosting,countryCode"
	resp, err := client.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var info IPInfo
	if err := json.Unmarshal(body, &info); err != nil {
		return nil, err
	}
	if info.Status != "success" {
		return nil, fmt.Errorf("ip-api returned: %s", info.Status)
	}
	return &info, nil
}

// fetchCountryFromIPInfo 用 ipinfo.io 查询国家代码(fallback,免费 50000/月)
func fetchCountryFromIPInfo(ip string) string {
	client := &http.Client{Timeout: 8 * time.Second}
	resp, err := client.Get("https://ipinfo.io/" + ip + "/json")
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	var result struct {
		Country string `json:"country"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return ""
	}
	return result.Country
}

// fetchCountryFromIPWhois 用 ipwhois.app 查询国家代码(第三 fallback)
func fetchCountryFromIPWhois(ip string) string {
	client := &http.Client{Timeout: 8 * time.Second}
	resp, err := client.Get("https://ipwhois.app/json/" + ip)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	var result struct {
		Success     bool   `json:"success"`
		CountryCode string `json:"country_code"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return ""
	}
	if !result.Success {
		return ""
	}
	return result.CountryCode
}

// FetchCountryBalanced 负载均衡查询国家代码(导出给 checker 用)。
// 按 IP 哈希分配到三个源(ip-api.com / ipinfo.io / ipwhois.app),
// 每个 IP 只查一个源,避免三源同时查询浪费带宽。
// 如果分配的源失败,fallback 到下一个源。
func FetchCountryBalanced(ip string) string {
	// 按 IP 哈希分配源(0/1/2)
	h := uint32(0)
	for _, c := range ip {
		h = h*31 + uint32(c)
	}
	sourceIdx := int(h % 3)
	// 按顺序尝试:分配的源 → 下一个 → 下一个
	for i := 0; i < 3; i++ {
		idx := (sourceIdx + i) % 3
		var country string
		switch idx {
		case 0:
			country = fetchCountryFromIPAPI(ip)
		case 1:
			country = fetchCountryFromIPInfo(ip)
		case 2:
			country = fetchCountryFromIPWhois(ip)
		}
		if country != "" {
			return country
		}
	}
	return ""
}

// fetchCountryFromIPAPI 用 ip-api.com 只查国家代码(轻量版,不含纯净度)
func fetchCountryFromIPAPI(ip string) string {
	client := &http.Client{Timeout: 8 * time.Second}
	resp, err := client.Get("http://ip-api.com/json/" + ip + "?fields=countryCode")
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	var result struct {
		CountryCode string `json:"countryCode"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return ""
	}
	return result.CountryCode
}

// classifyIPType 判断 IP 类型
func classifyIPType(info *IPInfo) string {
	if info.Mobile {
		return "mobile"
	}
	if info.Hosting {
		return "datacenter"
	}
	if info.Proxy {
		return "datacenter"
	}
	return "residential"
}

// computeIPScore 计算 0-100 纯净度评分
// residential=100, mobile=90, datacenter(非proxy)=60, datacenter+proxy=30
func computeIPScore(info *IPInfo, ipType string) int {
	switch ipType {
	case "residential":
		return 100
	case "mobile":
		return 90
	case "datacenter":
		if info.Proxy {
			return 30
		}
		return 60
	default:
		return 50
	}
}
