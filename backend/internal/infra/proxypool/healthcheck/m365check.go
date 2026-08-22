package healthcheck

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"time"

	"M365Copilot2ApiX/backend/internal/infra/proxypool/log"
	"M365Copilot2ApiX/backend/internal/infra/proxypool/proxy"
)

// netDialTimeout 封装 net.DialTimeout(方便测试 mock)
func netDialTimeout(network, addr string, timeout time.Duration) (net.Conn, error) {
	return net.DialTimeout(network, addr, timeout)
}

// M365 健康检查配置:
//   - 只测能访问微软(login.microsoftonline.com 和 substrate.office.com)
//   - 持续发包 10MB 测稳定性
//   - 不筛节点质量(延迟/带宽不排序),只筛持续可用能力
//   - 多个代理同时测试

const (
	// m365AccessibleURL 测试微软登录端点可达性(轻量 HEAD 请求)
	m365AccessibleURL = "https://login.microsoftonline.com/common/oauth2/v2.0/authorize"
	// m365ContinuousDownloadURL 持续下载测试用的微软 CDN 文件(Windows 更新大文件,稳定可用)
	m365ContinuousDownloadURL = "https://download.microsoft.com/download/1/4/9/149D5352-159B-4C70-8104-6BD2D965F0B7/MicrosoftEdgeEnterpriseX64.msi"
	// m365ContinuousTestBytes 持续测试下载字节数(10MB)
	m365ContinuousTestBytes = 10 * 1024 * 1024
	// m365ContinuousTestDuration 持续测试最短时长(10秒,期间不能断流)
	m365ContinuousTestDuration = 10 * time.Second
	// m365AccessibleTimeout 可达性测试超时
	m365AccessibleTimeout = 8 * time.Second
)

// M365CheckResult 是单个代理的 M365 测活结果
type M365CheckResult struct {
	Proxy      proxy.Proxy
	Accessible bool   // 能否访问微软登录端点
	Stable     bool   // 持续 10MB 下载是否稳定(不断流)
	Bytes      int64  // 实际下载字节数
	Duration   time.Duration
	Error      string // 失败原因
}

// M365CheckAll 对所有代理执行三层测活:
//   - 第 1 层:快速 TCP 连通性测试(排除死节点)
//   - 第 2 层:微软可达性测试(能访问 login.microsoftonline.com)
//   - 第 3 层:持续 10MB 下载稳定性测试(持续发包,不断流)
// 只有通过三层测试的代理才算可用。多个代理同时测试。
func M365CheckAll(proxies []proxy.Proxy) []M365CheckResult {
	if len(proxies) == 0 {
		return nil
	}
	numWorker := SpeedConn
	if numWorker <= 0 {
		numWorker = 50
	}
	if numWorker > 200 {
		numWorker = 200
	}

	results := make([]M365CheckResult, len(proxies))
	pool := newSimplePool(numWorker)
	var wg sync.WaitGroup

	for i, p := range proxies {
		wg.Add(1)
		idx, pp := i, p
		pool.submit(func() {
			defer pool.jobDone()
			defer wg.Done()
			results[idx] = m365CheckOne(pp)
		})
	}
	wg.Wait()
	return results
}

// M365CheckUsable 返回通过 M365 测活的代理列表(可达 + 稳定)
func M365CheckUsable(proxies []proxy.Proxy) proxy.ProxyList {
	results := M365CheckAll(proxies)
	usable := make(proxy.ProxyList, 0, len(results))
	for _, r := range results {
		if r.Accessible && r.Stable {
			usable = append(usable, r.Proxy)
		}
	}
	return usable
}

// m365CheckOne 对单个代理执行三层测活:
//   - 第 1 层:快速 TCP 连通性(失败直接返回,不浪费后续测试)
//   - 第 2 层:微软可达性
//   - 第 3 层:持续 10MB 下载稳定性
func m365CheckOne(p proxy.Proxy) M365CheckResult {
	result := M365CheckResult{Proxy: p}

	// 第 1 层:快速 TCP 连通性测试
	base := p.BaseInfo()
	if base.Server == "" || base.Port == 0 {
		result.Error = "missing server/port"
		return result
	}
	tcpAddr := fmt.Sprintf("%s:%d", base.Server, base.Port)
	// hysteria2 用 UDP,其他用 TCP
	network := "tcp"
	if p.TypeName() == "hysteria2" {
		network = "udp"
	}
	conn, err := netDialTimeout(network, tcpAddr, 3*time.Second)
	if err != nil {
		result.Error = fmt.Sprintf("layer1 tcp: %v", err)
		return result
	}
	conn.Close()

	// 第 2 层:微软可达性测试(轻量 HEAD 请求)
	accessible, accessErr := m365AccessibleTest(p)
	result.Accessible = accessible
	if !accessible {
		result.Error = fmt.Sprintf("layer2 accessible: %v", accessErr)
		return result
	}

	// 第 3 层:持续 10MB 下载稳定性测试
	bytes, duration, stable, stableErr := m365ContinuousDownload(p)
	result.Bytes = bytes
	result.Duration = duration
	result.Stable = stable
	if !stable {
		errMsg := "layer3 continuous download unstable"
		if stableErr != nil {
			errMsg = fmt.Sprintf("layer3 continuous: %v", stableErr)
		}
		result.Error = errMsg
	}
	return result
}

// m365AccessibleTest 测试代理能否访问微软登录端点
func m365AccessibleTest(p proxy.Proxy) (bool, error) {
	pmap, err := parseProxyMap(p)
	if err != nil {
		return false, fmt.Errorf("parse proxy map: %w", err)
	}
	clashProxy, err := parseClashProxy(pmap, p)
	if err != nil {
		return false, fmt.Errorf("parse clash proxy: %w", err)
	}
	addr, err := urlToMetadata(m365AccessibleURL)
	if err != nil {
		return false, fmt.Errorf("url metadata: %w", err)
	}
	transport := newProxyTransport(clashProxy, addr, m365AccessibleTimeout)
	client := &http.Client{Transport: transport, Timeout: m365AccessibleTimeout}
	req, err := http.NewRequest(http.MethodHead, m365AccessibleURL, nil)
	if err != nil {
		return false, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	// 微软登录端点返回 200/302/400 都算可达(说明能连上微软)
	return resp.StatusCode < 500, nil
}

// m365ContinuousDownload 持续下载 10MB 测稳定性:
// 要求在 m365ContinuousTestDuration 期间连接不断流,
// 且下载字节数达到 m365ContinuousTestBytes(或持续时间内没断流)。
func m365ContinuousDownload(p proxy.Proxy) (bytes int64, duration time.Duration, stable bool, err error) {
	pmap, parseErr := parseProxyMap(p)
	if parseErr != nil {
		return 0, 0, false, fmt.Errorf("parse proxy map: %w", parseErr)
	}
	clashProxy, parseErr := parseClashProxy(pmap, p)
	if parseErr != nil {
		return 0, 0, false, fmt.Errorf("parse clash proxy: %w", parseErr)
	}
	addr, parseErr := urlToMetadata(m365ContinuousDownloadURL)
	if parseErr != nil {
		return 0, 0, false, fmt.Errorf("url metadata: %w", parseErr)
	}
	transport := newProxyTransport(clashProxy, addr, m365ContinuousTestDuration+10*time.Second)
	client := &http.Client{
		Transport: transport,
		Timeout:   m365ContinuousTestDuration + 10*time.Second,
	}

	// 用 Range 请求只下载 10MB(避免下载整个 100MB+ 文件)
	req, err := http.NewRequest(http.MethodGet, m365ContinuousDownloadURL, nil)
	if err != nil {
		return 0, 0, false, err
	}
	req.Header.Set("Range", fmt.Sprintf("bytes=0-%d", m365ContinuousTestBytes-1))

	ctx, cancel := context.WithTimeout(context.Background(), m365ContinuousTestDuration+5*time.Second)
	defer cancel()
	req = req.WithContext(ctx)

	start := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		return 0, 0, false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusPartialContent && resp.StatusCode != http.StatusOK {
		return 0, 0, false, fmt.Errorf("unexpected status: %d", resp.StatusCode)
	}

	// 持续读取响应体,记录下载字节数和时长
	// 稳定性判定:在 m365ContinuousTestDuration 期间持续收到数据(不中断)
	buf := make([]byte, 32*1024)
	deadline := start.Add(m365ContinuousTestDuration)
	lastProgress := start
	for time.Now().Before(deadline) {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			bytes += int64(n)
			lastProgress = time.Now()
		}
		if readErr != nil {
			if readErr == io.EOF {
				break
			}
			// 读取错误,检查是否在持续时间内断流
			if time.Since(lastProgress) > 3*time.Second {
				duration = time.Since(start)
				return bytes, duration, false, readErr
			}
		}
	}
	duration = time.Since(start)

	// 稳定性判定:
	// 1. 下载字节数达到阈值(说明带宽足够)
	// 2. 没有在测试期间断流(lastProgress 接近当前时间)
	stable = bytes >= m365ContinuousTestBytes/2 && time.Since(lastProgress) < 3*time.Second
	if !stable {
		log.Debugf("m365 continuous test unstable: proxy=%s bytes=%d duration=%s", p.BaseInfo().Name, bytes, duration)
	}
	return bytes, duration, stable, nil
}
