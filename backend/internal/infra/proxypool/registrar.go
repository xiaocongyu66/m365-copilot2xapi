package proxypool

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"M365Copilot2ApiX/backend/internal/infra/proxypool/log"
)

// Registrar 全自动 M365 账号注册器
// 功能:
//   - 自动求解 Cloudflare Turnstile(Chrome 无头浏览器)
//   - 自动选择 CN/HK/MO/TW 代理(每 IP 每天限 1 次)
//   - 随机生成用户名/密码/显示名
//   - 提交注册到 office.965007.xyz
//   - ROPC 换 refresh token 入库
//   - 支持设置注册目标数量或持续注册
//   - 代理标记:用过的代理当天标记,第二天自动取消,失败不标记

const (
	e3PlanID        = "1"
	e3Domain        = "office.bo.edu.kg"
	turnstileSiteKey = "0x4AAAAAACIQH0jzb6zLNH8t"
	registerURL     = "https://office.965007.xyz/api/register"
)

// RegisteredAccount 注册成功后的账号信息
type RegisteredAccount struct {
	UPN      string `json:"upn"`
	Password string `json:"password"`
	LoginURL string `json:"loginUrl"`
}

// RegisterResult 完整注册结果
type RegisterResult struct {
	Account      RegisteredAccount `json:"account"`
	RefreshToken string            `json:"refreshToken"`
	AccessToken  string            `json:"accessToken"`
	ProxyUsed    string            `json:"proxyUsed"`
	ProxyCountry string            `json:"proxyCountry"`
	Success      bool              `json:"success"`
	Error        string            `json:"error,omitempty"`
}

// RegistrarConfig 注册器配置
type RegistrarConfig struct {
	Enabled       bool           `json:"enabled"`
	TargetCount   int            `json:"targetCount"`   // 注册目标数量(0 = 一直注册)
	Concurrency   int            `json:"concurrency"`   // 并发注册数(默认 1)
}

// Registrar 注册器
type Registrar struct {
	svc    *Service
	config RegistrarConfig

	// 代理标记:记录当天已使用的代理(第二天自动清除)
	mu         sync.Mutex
	usedToday  map[string]time.Time // key: nodeID, value: 使用时间

	// 注册统计
	statsMu    sync.Mutex
	total      int // 总尝试次数
	succeeded  int // 成功次数
	failed     int // 失败次数
	results    []RegisterResult

	// 运行控制
	cancel context.CancelFunc
	running bool
}

func NewRegistrar(svc *Service) *Registrar {
	return &Registrar{
		svc:       svc,
		usedToday: make(map[string]time.Time),
	}
}

// Start 启动自动注册
func (r *Registrar) Start(cfg RegistrarConfig) {
	r.mu.Lock()
	if r.running {
		r.config = cfg
		r.mu.Unlock()
		return
	}
	r.config = cfg
	r.running = true
	r.mu.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	r.cancel = cancel

	go r.runLoop(ctx)
	log.Infof("M365 registrar started: target=%d concurrency=%d", cfg.TargetCount, cfg.Concurrency)
}

// Stop 停止注册
func (r *Registrar) Stop() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.running {
		return
	}
	if r.cancel != nil {
		r.cancel()
	}
	r.running = false
	log.Infof("M365 registrar stopped: total=%d success=%d failed=%d", r.total, r.succeeded, r.failed)
}

// IsRunning 返回是否正在运行
func (r *Registrar) IsRunning() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.running
}

// Stats 返回注册统计
func (r *Registrar) Stats() (total, succeeded, failed int, results []RegisterResult) {
	r.statsMu.Lock()
	defer r.statsMu.Unlock()
	return r.total, r.succeeded, r.failed, r.results
}

// Config 返回当前配置
func (r *Registrar) Config() RegistrarConfig {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.config
}

// runLoop 注册主循环
func (r *Registrar) runLoop(ctx context.Context) {
	concurrency := r.config.Concurrency
	if concurrency < 1 {
		concurrency = 1
	}
	if concurrency > 5 {
		concurrency = 5
	}

	for {
		// 检查是否达到目标
		r.statsMu.Lock()
		current := r.succeeded
		r.statsMu.Unlock()

		if r.config.TargetCount > 0 && current >= r.config.TargetCount {
			log.Infof("M365 registrar reached target: %d/%d", current, r.config.TargetCount)
			break
		}

		// 检查 context
		if ctx.Err() != nil {
			break
		}

		// 并发注册
		var wg sync.WaitGroup
		for i := 0; i < concurrency; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				r.registerOne(ctx)
			}()
		}
		wg.Wait()
	}

	r.mu.Lock()
	r.running = false
	r.mu.Unlock()
}

// registerOne 注册单个账号
func (r *Registrar) registerOne(ctx context.Context) {
	r.statsMu.Lock()
	r.total++
	r.statsMu.Unlock()

	result := RegisterResult{}

	// 1. 清理过期的代理标记(第二天自动取消)
	r.cleanUsedProxies()

	// 2. 选择一个未使用的 CN/HK/MO/TW 代理
	nodeID, release := r.pickUnusedProxy()
	if release != nil {
		defer release()
	}
	if nodeID == "" {
		result.Error = "没有可用的 CN/HK/MO/TW 代理(当天已用完或无可用节点)"
		r.recordResult(result)
		return
	}
	result.ProxyUsed = nodeID

	// 3. 自动求解 Turnstile
	solver := NewTurnstileSolver("https://office.965007.xyz", turnstileSiteKey, nil)
	token, err := solver.Solve()
	if err != nil {
		result.Error = fmt.Sprintf("Turnstile 求解失败: %v", err)
		r.recordResult(result)
		return
	}

	// 4. 随机生成账号信息
	username := randomUsername()
	password := randomPassword()
	displayName := randomDisplayName()

	// 5. 提交注册
	acc, err := r.submitRegister(ctx, nodeID, username, password, displayName, token)
	if err != nil {
		result.Error = fmt.Sprintf("注册失败: %v", err)
		r.svc.RecordRequestError(nodeID, nodeID, registerURL, err.Error())
		r.recordResult(result)
		return
	}

	// 6. 注册成功,标记代理已用
	r.markProxyUsed(nodeID)
	r.svc.RecordRequestSuccess(nodeID)
	result.Account = *acc

	// 7. ROPC 换 token
	rt, at, err := r.rocpExchange(ctx, nodeID, acc.UPN, password)
	if err != nil {
		log.Warnf("ROPC 换 token 失败(账号已注册): %v", err)
		result.Success = true
		result.Error = fmt.Sprintf("注册成功但 ROPC 换 token 失败: %v", err)
	} else {
		result.Success = true
		result.RefreshToken = rt
		result.AccessToken = at
	}

	// 8. 如果有 refresh token,导入到 M365 账号池
	if rt != "" {
		r.importToAccountPool(rt, acc.UPN)
	}

	r.recordResult(result)
	log.Infof("M365 注册成功: %s 代理=%s", acc.UPN, nodeID)
}

// pickUnusedProxy 选择一个当天未使用的 CN/HK/MO/TW 代理
func (r *Registrar) pickUnusedProxy() (string, func()) {
	// 用 Balancer 选一个 CN/HK/MO/TW 代理
	nodeID, release := r.svc.Balancer().PickNodeForRegister()
	if nodeID == "" {
		return "", nil
	}
	// 检查是否当天已用
	r.mu.Lock()
	if _, used := r.usedToday[nodeID]; used {
		r.mu.Unlock()
		// 已用,递归找下一个(简化:直接返回空)
		return "", nil
	}
	r.mu.Unlock()
	return nodeID, release
}

// markProxyUsed 标记代理当天已用
func (r *Registrar) markProxyUsed(nodeID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.usedToday[nodeID] = time.Now()
}

// cleanUsedProxies 清理过期的代理标记(超过 24 小时自动取消)
func (r *Registrar) cleanUsedProxies() {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	for id, usedAt := range r.usedToday {
		if now.Sub(usedAt) > 24*time.Hour {
			delete(r.usedToday, id)
		}
	}
}

// recordResult 记录注册结果
func (r *Registrar) recordResult(result RegisterResult) {
	r.statsMu.Lock()
	defer r.statsMu.Unlock()
	r.results = append(r.results, result)
	if result.Success {
		r.succeeded++
	} else {
		r.failed++
	}
	// 只保留最近 100 条
	if len(r.results) > 100 {
		r.results = r.results[len(r.results)-100:]
	}
}

// UsedProxies 返回当天已用的代理列表
func (r *Registrar) UsedProxies() map[string]time.Time {
	r.mu.Lock()
	defer r.mu.Unlock()
	result := make(map[string]time.Time, len(r.usedToday))
	for k, v := range r.usedToday {
		result[k] = v
	}
	return result
}

// submitRegister 提交注册请求
func (r *Registrar) submitRegister(ctx context.Context, proxyNodeID, username, password, displayName, turnstileToken string) (*RegisteredAccount, error) {
	body := map[string]string{
		"planId":         e3PlanID,
		"username":       username,
		"password":       password,
		"displayName":    displayName,
		"turnstileToken": turnstileToken,
	}
	bodyBytes, _ := json.Marshal(body)

	client := r.svc.HTTPClientWithNode(proxyNodeID)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, registerURL, bytes.NewReader(bodyBytes))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "https://office.965007.xyz")
	req.Header.Set("Referer", "https://office.965007.xyz/")

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)

	var result struct {
		OK      bool              `json:"ok"`
		Message string            `json:"message"`
		Data    *RegisteredAccount `json:"data"`
	}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("解析注册响应失败: %w", err)
	}
	if !result.OK {
		return nil, fmt.Errorf("注册失败: %s", result.Message)
	}
	if result.Data == nil {
		return nil, fmt.Errorf("注册响应无数据")
	}
	return result.Data, nil
}

// rocpExchange 用用户名+密码通过 ROPC 换取 refresh token
func (r *Registrar) rocpExchange(ctx context.Context, proxyNodeID, upn, password string) (refreshToken, accessToken string, err error) {
	form := url.Values{}
	form.Set("client_id", "c0ab8ce9-e9a0-42e7-b064-33d422df41f1")
	form.Set("grant_type", "password")
	form.Set("username", upn)
	form.Set("password", password)
	form.Set("scope", "openid profile offline_access https://substrate.office.com/sydney/M365Chat.Read https://substrate.office.com/sydney/sydney.readwrite")

	client := r.svc.HTTPClientWithNode(proxyNodeID)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, "https://login.microsoftonline.com/common/oauth2/v2.0/token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := client.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	var tokenResp struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		Error        string `json:"error"`
		ErrorDesc    string `json:"error_description"`
	}
	if err := json.Unmarshal(body, &tokenResp); err != nil {
		return "", "", fmt.Errorf("解析 token 响应失败: %w", err)
	}
	if tokenResp.Error != "" {
		return "", "", fmt.Errorf("ROPC %s: %s", tokenResp.Error, tokenResp.ErrorDesc)
	}
	return tokenResp.RefreshToken, tokenResp.AccessToken, nil
}

// importToAccountPool 把 refresh token 导入到 M365 账号池
func (r *Registrar) importToAccountPool(refreshToken, upn string) {
	// 通过 store 导入 refresh token(一行一个)
	text := refreshToken + "\n"
	imported, skipped := r.svc.ImportNodes(text)
	log.Infof("账号 %s 导入账号池: imported=%d skipped=%d", upn, imported, skipped)
}

// randomUsername 生成随机用户名(字母数字,3-20 位)
func randomUsername() string {
	const chars = "abcdefghijklmnopqrstuvwxyz0123456789"
	r := rand.New(rand.NewSource(time.Now().UnixNano()))
	b := make([]byte, 10)
	for i := range b {
		b[i] = chars[r.Intn(len(chars))]
	}
	return "u" + string(b)
}

// randomPassword 生成随机密码(至少 8 位,包含大小写字母+数字+特殊字符)
func randomPassword() string {
	const (
		upper   = "ABCDEFGHJKLMNPQRSTUVWXYZ"
		lower   = "abcdefghjkmnpqrstuvwxyz"
		digits  = "23456789"
		special = "!@#$%&*"
	)
	r := rand.New(rand.NewSource(time.Now().UnixNano()))
	all := upper + lower + digits + special
	b := make([]byte, 16)
	b[0] = upper[r.Intn(len(upper))]
	b[1] = lower[r.Intn(len(lower))]
	b[2] = digits[r.Intn(len(digits))]
	b[3] = special[r.Intn(len(special))]
	for i := 4; i < 16; i++ {
		b[i] = all[r.Intn(len(all))]
	}
	r.Shuffle(len(b), func(i, j int) { b[i], b[j] = b[j], b[i] })
	return string(b)
}

// randomDisplayName 生成随机显示名
func randomDisplayName() string {
	adjectives := []string{"Happy", "Sunny", "Lucky", "Bright", "Calm", "Swift", "Smart", "Brave"}
	nouns := []string{"Fox", "Eagle", "Wolf", "Tiger", "Bear", "Hawk", "Lion", "Cat"}
	r := rand.New(rand.NewSource(time.Now().UnixNano()))
	return adjectives[r.Intn(len(adjectives))] + nouns[r.Intn(len(nouns))] + fmt.Sprintf("%d", r.Intn(100))
}
