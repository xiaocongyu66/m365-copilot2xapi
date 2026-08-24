package healthcheck

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"M365Copilot2ApiX/backend/internal/infra/proxypool/geoip"
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

	// 2. 查询 ip-api.com 获取 IP 信誉
	info, err := fetchIPInfo(exitIP)
	if err != nil {
		// ip-api.com 限流(45次/分钟)或查询失败,用本地 GeoIP 查国家作为 fallback
		log.Debugf("[ipcheck] ip-api.com failed for %s: %v, fallback to local GeoIP", exitIP, err)
		country := geoip.Get().LookupCountry(exitIP)
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
