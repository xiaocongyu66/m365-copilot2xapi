package turnstile

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// solveTurnstileViaAPI 调用外部 HTTP solver API(兼容 theyka/d3vin/solver-gateway 接口):
//
//	GET /turnstile?url=&sitekey=&action=&cdata=  → {"task_id":"...","id":"..."}
//	GET /result?id=                               → {"status":"success","value":"<token>"}
//
// 环境变量:
//
//	turnstileAPIURL              base URL (如 http://127.0.0.1:5080)
//	turnstileAPIToken            可选 bearer/X-API-Key token
//	turnstileAPITimeout          总求解预算秒数(默认 120)
//	turnstileAPIPollIntervalMs  轮询间隔毫秒(默认 500)
//	turnstileAPIAction           可选 action 参数
//	turnstileAPICData            可选 cdata 参数
func solveTurnstileViaAPI(siteKey, targetURL string) (string, error) {
	base := strings.TrimRight(strings.TrimSpace(envFirst("TURNSTILE_API_URL")), "/")
	if base == "" {
		return "", fmt.Errorf("TURNSTILE_API_URL not set")
	}
	if targetURL == "" {
		targetURL = "https://office.965007.xyz/"
	}
	timeoutSec := envInt("TURNSTILE_API_TIMEOUT", 120)
	pollMs := envInt("TURNSTILE_API_POLL_INTERVAL_MS", 500)
	if pollMs < 100 {
		pollMs = 100
	}
	token := envFirst("TURNSTILE_API_TOKEN", "TURNSTILE_TOKEN", "SOLVER_API_TOKEN")
	action := envFirst("TURNSTILE_API_ACTION")
	cdata := envFirst("TURNSTILE_API_CDATA")

	hc := &http.Client{Timeout: time.Duration(timeoutSec) * time.Second}

	q := url.Values{}
	q.Set("url", targetURL)
	q.Set("sitekey", siteKey)
	if action != "" {
		q.Set("action", action)
	}
	if cdata != "" {
		q.Set("cdata", cdata)
	}
	submitURL := base + "/turnstile?" + q.Encode()

	jobID, err := apiGet(hc, submitURL, token, "task_id", "id")
	if err != nil {
		return "", fmt.Errorf("submit: %w", err)
	}
	if jobID == "" {
		return "", fmt.Errorf("submit: empty job id")
	}

	deadline := time.Now().Add(time.Duration(timeoutSec) * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(time.Duration(pollMs) * time.Millisecond)
		resultURL := base + "/result?id=" + url.QueryEscape(jobID)
		status, value, err := apiResult(hc, resultURL, token)
		if err != nil {
			continue
		}
		switch status {
		case "success":
			if value != "" {
				return value, nil
			}
		case "fail", "error", "expired":
			return "", fmt.Errorf("solver: %s %s", status, value)
		}
	}
	return "", fmt.Errorf("solver: timeout after %ds", timeoutSec)
}

func apiGet(hc *http.Client, urlStr, token string, idKeys ...string) (string, error) {
	req, err := http.NewRequest("GET", urlStr, nil)
	if err != nil {
		return "", err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("X-API-Key", token)
	}
	resp, err := hc.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("http %d: %s", resp.StatusCode, truncStr(string(body), 120))
	}
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		s := strings.TrimSpace(string(body))
		if s != "" && len(s) < 200 {
			return s, nil
		}
		return "", fmt.Errorf("json: %w", err)
	}
	for _, k := range idKeys {
		if v, ok := m[k].(string); ok && v != "" {
			return v, nil
		}
	}
	if data, ok := m["data"].(map[string]any); ok {
		for _, k := range idKeys {
			if v, ok := data[k].(string); ok && v != "" {
				return v, nil
			}
		}
	}
	return "", nil
}

func apiResult(hc *http.Client, urlStr, token string) (string, string, error) {
	req, err := http.NewRequest("GET", urlStr, nil)
	if err != nil {
		return "", "", err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("X-API-Key", token)
	}
	resp, err := hc.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode >= 400 {
		return "error", "", fmt.Errorf("http %d", resp.StatusCode)
	}
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		return "", "", err
	}
	status, _ := m["status"].(string)
	value, _ := m["value"].(string)
	if status == "" {
		status = "pending"
	}
	return status, value, nil
}

func truncStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
