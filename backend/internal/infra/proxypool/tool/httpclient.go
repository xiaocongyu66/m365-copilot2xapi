package tool

import (
	"io"
	"net/http"
	"time"

	"github.com/xiaocongyu66/go-curlcffi/pkg/curlcffi"
)

const UserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"

// HttpClient 封装 go-curlcffi Session,提供 TLS 指纹模拟(反检测更强)。
// 如果 go-curlcffi 初始化失败,回退到标准 http.Client。
type HttpClient struct {
	*http.Client
	session *curlcffi.Session
}

var httpClient *HttpClient

func init() {
	// 尝试创建 go-curlcffi Session(Chrome 131 指纹,反检测最强)
	session, err := curlcffi.NewSession(
		curlcffi.WithImpersonate(curlcffi.Chrome131),
	)
	if err != nil || session == nil {
		// 回退到标准 http.Client
		fallback := &http.Client{Timeout: 10 * time.Second}
		httpClient = &HttpClient{Client: fallback}
		return
	}
	httpClient = &HttpClient{
		Client:  &http.Client{Timeout: 30 * time.Second},
		session: session,
	}
}

func GetHttpClient() *HttpClient {
	c := *httpClient
	return &c
}

func (c *HttpClient) Get(url string) (resp *http.Response, err error) {
	// 优先用 go-curlcffi(带 TLS 指纹模拟)
	if c.session != nil {
		return c.session.Get(url, nil, map[string]string{
			"User-Agent":      UserAgent,
			"Accept-Language": "zh-CN,zh;q=0.9,en;q=0.8",
		})
	}
	// 回退到标准 http.Client
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9,en;q=0.8")
	req.Header.Set("User-Agent", UserAgent)
	return c.Client.Do(req)
}

func (c *HttpClient) Post(url string, body io.Reader) (resp *http.Response, err error) {
	// go-curlcffi 的 Post 方法
	if c.session != nil {
		data, _ := io.ReadAll(body)
		return c.session.Post(url, data, map[string]string{
			"User-Agent":      UserAgent,
			"Accept-Language": "zh-CN,zh;q=0.9,en;q=0.8",
		})
	}
	// 回退
	req, err := http.NewRequest(http.MethodPost, url, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9,en;q=0.8")
	req.Header.Set("User-Agent", UserAgent)
	return c.Client.Do(req)
}

func (c *HttpClient) Do(req *http.Request) (resp *http.Response, err error) {
	// go-curlcffi 的 Do 方法
	if c.session != nil {
		headers := map[string]string{
			"User-Agent": UserAgent,
		}
		for k, v := range req.Header {
			if len(v) > 0 {
				headers[k] = v[0]
			}
		}
		return c.session.Get(req.URL.String(), nil, headers)
	}
	return c.Client.Do(req)
}
