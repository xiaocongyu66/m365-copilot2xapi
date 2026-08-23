package minirelay

import (
	"context"
	"encoding/base64"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"

	"github.com/sagernet/sing-shadowsocks/shadowimpl"
	M "github.com/sagernet/sing/common/metadata"
)

// dialShadowsocks 建立 shadowsocks 连接。
// 支持的 ss URL 格式:
//   - ss://base64(method:password@host:port)#name  (整个认证+地址 base64 编码)
//   - ss://base64(method:password)@host:port#name  (只认证 base64 编码)
//   - ss://method:password@host:port#name          (明文)
func (d *outboundDialer) dialShadowsocks(ctx context.Context, target string) (net.Conn, error) {
	u := d.u
	host := u.Hostname()
	port := u.Port()
	userInfo := ""
	if u.User != nil {
		userInfo = u.User.Username()
	}

	// 如果 u.User 为空,说明 base64 编码了完整的 method:password@host:port
	// url.Parse 把整个 base64 当成了 host。需要从 host 里解析。
	if userInfo == "" && host != "" {
		// host 可能是 base64(method:password@host:port) 或包含其他编码
		// 尝试 base64 解码 host
		decoded, decErr := base64.RawStdEncoding.DecodeString(host)
		if decErr != nil {
			decoded, decErr = base64.StdEncoding.DecodeString(host)
		}
		if decErr == nil {
			// 解码后应该是 method:password@host:port
			decodedStr := string(decoded)
			if atIdx := strings.LastIndex(decodedStr, "@"); atIdx >= 0 {
				userInfo = decodedStr[:atIdx]
				addrPart := decodedStr[atIdx+1:]
				if h, p, ok := splitHostPort(addrPart); ok {
					host = h
					port = p
				}
			}
		}
	}
	if port == "" {
		port = "8388"
	}

	// 解析 method:password
	method, password, err := parseSSCredentials(userInfo, u.User)
	if err != nil {
		return nil, err
	}
	if method == "" || password == "" {
		return nil, fmt.Errorf("ss: empty method or password")
	}

	// 创建 ss method（加密方法实现）
	ssMethod, err := shadowimpl.FetchMethod(method, password, time.Now)
	if err != nil {
		return nil, fmt.Errorf("ss: %w", err)
	}

	// 建立 TCP 连接到 ss 服务器
	rawConn, err := (&net.Dialer{Timeout: 15 * time.Second}).DialContext(ctx, "tcp", net.JoinHostPort(host, port))
	if err != nil {
		return nil, fmt.Errorf("ss tcp: %w", err)
	}

	// ss 握手（写入目标地址）
	destination := M.ParseSocksaddr(target)
	if !destination.IsValid() {
		rawConn.Close()
		return nil, fmt.Errorf("ss: invalid target %s", target)
	}
	ssConn, err := ssMethod.DialConn(rawConn, destination)
	if err != nil {
		rawConn.Close()
		return nil, fmt.Errorf("ss handshake: %w", err)
	}
	return ssConn, nil
}

// splitHostPort 分割 host:port,处理 IPv6 地址。
func splitHostPort(addr string) (string, string, bool) {
	if i := strings.LastIndex(addr, ":"); i >= 0 {
		return addr[:i], addr[i+1:], true
	}
	return "", "", false
}

// parseSSCredentials 解析 ss URL 的 method:password。
// 用户信息可能是 base64 编码的 "method:password"，也可能是明文。
func parseSSCredentials(userInfo string, user *url.Userinfo) (method, password string, err error) {
	if userInfo == "" {
		return "", "", fmt.Errorf("ss: empty user info")
	}
	// 先尝试 base64 解码
	if !strings.Contains(userInfo, ":") {
		decoded, decErr := base64.RawURLEncoding.DecodeString(userInfo)
		if decErr != nil {
			decoded, decErr = base64.StdEncoding.DecodeString(userInfo)
			if decErr != nil {
				return "", "", fmt.Errorf("ss: base64 decode: %w", decErr)
			}
		}
		userInfo = string(decoded)
	}
	// 现在 userInfo 应该是 "method:password"
	parts := strings.SplitN(userInfo, ":", 2)
	if len(parts) != 2 {
		return "", "", fmt.Errorf("ss: invalid credentials format")
	}
	return parts[0], parts[1], nil
}
