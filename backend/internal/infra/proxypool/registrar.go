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
	"time"

	"M365Copilot2ApiX/backend/internal/infra/proxypool/log"
)

// Registrar 从 office.965007.xyz 自动注册 Office 365 E3 账号
// 流程:
//   1. 通过代理访问注册 API(每 IP 每天只能注册 1 个,需要代理轮换)
//   2. 求解 Cloudflare Turnstile(需要外部求解服务或浏览器自动化)
//   3. 提交注册:随机用户名/密码/显示名
//   4. 注册成功后用 ROPC(用户名+密码)换 refresh token
//   5. 入库到 M365 账号池

const (
	registerURL    = "https://office.965007.xyz/api/register"
	siteConfigURL  = "https://office.965007.xyz/api/public/site-config"
	plansURL       = "https://office.965007.xyz/api/public/plans"
	turnstileSiteKey = "0x4AAAAAACIQH0jzb6zLNH8t"
	e3PlanID       = "1" // Office 365 E3
	e3Domain       = "office.bo.edu.kg"
)

// RegisteredAccount 是注册成功后返回的账号信息
type RegisteredAccount struct {
	UPN       string `json:"upn"`       // user@domain.com
	Password  string `json:"password"`  // 密码
	LoginURL  string `json:"loginUrl"`  // 登录 URL
	Message   string `json:"message"`   // 注册消息
}

// RegisterResult 是完整的注册结果(含 ROPC 换取的 token)
type RegisterResult struct {
	Account      RegisteredAccount `json:"account"`
	RefreshToken string            `json:"refreshToken"`  // ROPC 换取的 refresh token
	AccessToken  string            `json:"accessToken"`   // ROPC 换取的 access token
	ProxyUsed    string            `json:"proxyUsed"`     // 使用的代理
}

// Registrar 管理账号注册
type Registrar struct {
	svc *Service
}

func NewRegistrar(svc *Service) *Registrar {
	return &Registrar{svc: svc}
}

// Register 注册一个新账号:
//   - 通过代理池选择一个可用代理(必须是 CN/HK/MO/TW 的 IP)
//   - 随机生成用户名/密码/显示名
//   - 自动求解 Turnstile(用 Chrome 无头浏览器)
//   - 提交注册
//   - ROPC 换 token
func (r *Registrar) Register(ctx context.Context, turnstileToken string) (*RegisterResult, error) {
	// 1. 选择代理(CN/HK/MO/TW 地区)
	nodeID, release := r.svc.PickNodeForRegister()
	if release != nil {
		defer release()
	}
	if nodeID == "" {
		return nil, fmt.Errorf("没有 CN/HK/MO/TW 地区的可用代理节点")
	}

	// 2. 如果没有 turnstileToken,自动求解
	if turnstileToken == "" {
		log.Infof("自动求解 Turnstile token...")
		solver := NewTurnstileSolver("https://office.965007.xyz", turnstileSiteKey, nil)
		token, err := solver.Solve()
		if err != nil {
			return nil, fmt.Errorf("Turnstile 求解失败: %w", err)
		}
		turnstileToken = token
		log.Infof("Turnstile 求解成功,token 长度: %d", len(token))
	}

	// 3. 随机生成账号信息
	username := randomUsername()
	password := randomPassword()
	displayName := randomDisplayName()

	// 4. 提交注册
	acc, err := r.submitRegister(ctx, nodeID, username, password, displayName, turnstileToken)
	if err != nil {
		r.svc.RecordRequestError(nodeID, nodeID, registerURL, err.Error())
		return nil, fmt.Errorf("注册失败: %w", err)
	}

	r.svc.RecordRequestSuccess(nodeID)

	// 5. ROPC 换 token(refresh token)
	rt, at, err := r.rocpExchange(ctx, nodeID, acc.UPN, password)
	if err != nil {
		// 注册成功但换 token 失败,仍然返回账号信息(用户可以手动换 token)
		log.Warnf("ROPC 换 token 失败(账号已注册): %v", err)
		return &RegisterResult{
			Account:   *acc,
			ProxyUsed: nodeID,
		}, nil
	}

	return &RegisterResult{
		Account:      *acc,
		RefreshToken: rt,
		AccessToken:  at,
		ProxyUsed:    nodeID,
	}, nil
}

// submitRegister 提交注册请求
func (r *Registrar) submitRegister(ctx context.Context, proxyNodeID, username, password, displayName, turnstileToken string) (*RegisteredAccount, error) {
	body := map[string]string{
		"planId":          e3PlanID,
		"username":        username,
		"password":        password,
		"displayName":     displayName,
		"turnstileToken":  turnstileToken,
	}
	bodyBytes, _ := json.Marshal(body)

	// 通过代理发送请求
	client := r.svc.HTTPClientWithNode(proxyNodeID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, registerURL, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, err
	}
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
		return nil, fmt.Errorf("解析注册响应失败: %w, body: %s", err, string(respBody))
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
	// M365 OAuth ROPC:用用户名+密码换 token
	// client_id 用 M365 默认的 OAuth 客户端
	form := url.Values{}
	form.Set("client_id", "c0ab8ce9-e9a0-42e7-b064-33d422df41f1") // M365 默认 client_id
	form.Set("grant_type", "password")
	form.Set("username", upn)
	form.Set("password", password)
	form.Set("scope", "openid profile offline_access https://substrate.office.com/sydney/M365Chat.Read https://substrate.office.com/sydney/sydney.readwrite")

	client := r.svc.HTTPClientWithNode(proxyNodeID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://login.microsoftonline.com/common/oauth2/v2.0/token", strings.NewReader(form.Encode()))
	if err != nil {
		return "", "", err
	}
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

// randomUsername 生成随机用户名(字母数字,3-20 位)
func randomUsername() string {
	const chars = "abcdefghijklmnopqrstuvwxyz0123456789"
	r := rand.New(rand.NewSource(time.Now().UnixNano()))
	b := make([]byte, 10)
	for i := range b {
		b[i] = chars[r.Intn(len(chars))]
	}
	return "u" + string(b) // 以字母开头
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
	// 打乱
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
