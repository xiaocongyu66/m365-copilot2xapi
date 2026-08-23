package minirelay

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"time"

	tls "github.com/sagernet/sing-box/common/tls"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing-quic/hysteria2"
	M "github.com/sagernet/sing/common/metadata"
)

// dialHysteria2 建立 hysteria2 连接（QUIC 协议）。
// hy2://password@host:port?sni=...&insecure=1
// 或 hysteria2://...
func (d *outboundDialer) dialHysteria2(ctx context.Context, target string) (net.Conn, error) {
	u := d.u
	host := u.Hostname()
	port := u.Port()
	if port == "" {
		port = "443"
	}
	password := u.User.Username()

	serverAddr := M.ParseSocksaddr(net.JoinHostPort(host, port))

	// TLS 配置（hysteria2 用 utls + ALPN h3）
	tlsOpts := parseTLSOptions(u)
	if tlsOpts == nil {
		tlsOpts = &option.OutboundTLSOptions{
			Enabled:    true,
			ServerName: host,
			Insecure:   true,
		}
	}
	// hysteria2 用 h3 ALPN
	tlsOpts.ALPN = []string{"h3"}
	tlsConfig, err := tls.NewClient(ctx, net.JoinHostPort(host, port), *tlsOpts)
	if err != nil {
		return nil, fmt.Errorf("hy2 tls: %w", err)
	}

	// 解析带宽参数
	q := u.Query()
	sendBPS := uint64(0)
	recvBPS := uint64(0)
	if up := q.Get("up"); up != "" {
		if bps, err := parseBandwidth(up); err == nil {
			sendBPS = bps
		}
	}
	if down := q.Get("down"); down != "" {
		if bps, err := parseBandwidth(down); err == nil {
			recvBPS = bps
		}
	}

	client, err := hysteria2.NewClient(hysteria2.ClientOptions{
		Context:       ctx,
		Dialer:        newStdDialer(net.JoinHostPort(host, port)),
		Logger:        nopLogger{},
		ServerAddress: serverAddr,
		SendBPS:       sendBPS,
		ReceiveBPS:    recvBPS,
		Password:      password,
		TLSConfig:     tlsConfig,
		UDPDisabled:    true,
	})
	if err != nil {
		return nil, fmt.Errorf("hy2 client: %w", err)
	}

	destination := M.ParseSocksaddr(target)
	if !destination.IsValid() {
		return nil, fmt.Errorf("hy2: invalid target %s", target)
	}
	return client.DialConn(ctx, destination)
}

// parseBandwidth 解析带宽参数（如 "100 mbps" → 字节/秒）。
func parseBandwidth(s string) (uint64, error) {
	// 支持 "100 mbps", "1 gbps", "100000000" 等
	var num float64
	var unit string
	fmt.Sscanf(s, "%f %s", &num, &unit)
	if num == 0 {
		return strconv.ParseUint(s, 10, 64)
	}
	switch unit {
	case "gbps", "Gbps", "g":
		return uint64(num * 1e9 / 8), nil
	case "mbps", "Mbps", "m":
		return uint64(num * 1e6 / 8), nil
	case "kbps", "Kbps", "k":
		return uint64(num * 1e3 / 8), nil
	default:
		return uint64(num), nil
	}
}

// _ 防止 import 被清理
var (
	_ = url.Parse
	_ = time.Second
)
