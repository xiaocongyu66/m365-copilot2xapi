package proxy

import (
	"encoding/json"
	"errors"

	"M365Copilot2ApiX/backend/internal/infra/proxypool/tool"
)

var ErrorTypeCanNotConvert = errors.New("type not support")

// Convert2SS convert proxy to ShadowsocksR if possible
func Convert2SSR(p Proxy) (ssr *ShadowsocksR, err error) {
	if p.TypeName() == "ss" {
		ss := p.(*Shadowsocks)
		if ss == nil {
			return nil, errors.New("ss is nil")
		}
		if !tool.CheckInList(SSRCipherList, ss.Cipher) {
			return nil, errors.New("cipher not support")
		}
		base := ss.Base
		base.Type = "ssr"
		return &ShadowsocksR{
			Base:     base,
			Password: ss.Password,
			Cipher:   ss.Cipher,
			Protocol: "origin",
			Obfs:     "plain",
		}, nil
	}
	return nil, ErrorTypeCanNotConvert
}

// Convert2SS convert proxy to Shadowsocks if possible
func Convert2SS(p Proxy) (ss *Shadowsocks, err error) {
	if p.TypeName() == "ssr" {
		ssr := p.(*ShadowsocksR)
		if ssr == nil {
			return nil, errors.New("ssr is nil")
		}
		if !tool.CheckInList(SSCipherList, ssr.Cipher) {
			return nil, errors.New("cipher not support")
		}
		if ssr.Protocol != "origin" || ssr.Obfs != "plain" || ssr.Ot_enable != 0 {
			return nil, errors.New("protocol or obfs not allowed")
		}
		base := ssr.Base
		base.Type = "ss"
		return &Shadowsocks{
			Base:       base,
			Password:   ssr.Password,
			Cipher:     ssr.Cipher,
			Plugin:     "",
			PluginOpts: nil,
		}, nil
	}
	return nil, ErrorTypeCanNotConvert
}

var SSRCipherList = []string{
	"none",
	"aes-128-cfb",
	"aes-192-cfb",
	"aes-256-cfb",
	"aes-128-ctr",
	"aes-192-ctr",
	"aes-256-ctr",
	"aes-128-ofb",
	"aes-192-ofb",
	"aes-256-ofb",
	"des-cfb",
	"bf-cfb",
	"cast5-cfb",
	"rc4-md5",
	"chacha20-ietf",
	"salsa20",
	"camellia-128-cfb",
	"camellia-192-cfb",
	"camellia-256-cfb",
	"idea-cfb",
	"rc2-cfb",
	"seed-cfb",
}

var SSCipherList = []string{
	"aes-128-gcm",
	"aes-192-gcm",
	"aes-256-gcm",
	"aes-128-cfb",
	"aes-192-cfb",
	"aes-256-cfb",
	"aes-128-ctr",
	"aes-192-ctr",
	"aes-256-ctr",
	"rc4-md5",
	"chacha20-ietf",
	"xchacha20",
	"chacha20-ietf-poly1305",
	"xchacha20-ietf-poly1305",
}

// ClashJSONToProxy 把 Clash 格式的 proxy JSON 转成 Proxy 对象。
// 用于解析 clash.yml 订阅和 sing-box(已转成 Clash 格式)。
// 输入是单个 proxy 的 JSON,如 {"type":"ss","name":"...","server":"...","port":...}
func ClashJSONToProxy(data []byte) Proxy {
	var m map[string]interface{}
	if err := json.Unmarshal(data, &m); err != nil {
		return nil
	}
	proxyType, _ := m["type"].(string)
	name, _ := m["name"].(string)
	server, _ := m["server"].(string)
	port := toIntProxy(m["port"])

	switch proxyType {
	case "ss":
		cipher, _ := m["cipher"].(string)
		password, _ := m["password"].(string)
		ss := &Shadowsocks{
			Cipher:   cipher,
			Password: password,
		}
		ss.Base = Base{Name: name, Server: server, Port: port, Type: "ss"}
		if plugin, ok := m["plugin"].(string); ok {
			ss.Plugin = plugin
		}
		if pluginOpts, ok := m["plugin-opts"].(map[string]interface{}); ok {
			ss.PluginOpts = pluginOpts
		}
		return ss
	case "vmess":
		uuid, _ := m["uuid"].(string)
		cipher, _ := m["cipher"].(string)
		network, _ := m["network"].(string)
		serverName, _ := m["servername"].(string)
		tls, _ := m["tls"].(bool)
		skipCert, _ := m["skip-cert-verify"].(bool)
		alterID := toIntProxy(m["alterId"])
		v := &Vmess{
			UUID:           uuid,
			AlterID:        alterID,
			Cipher:         cipher,
			Network:        network,
			ServerName:      serverName,
			TLS:            tls,
			SkipCertVerify: skipCert,
		}
		v.Base = Base{Name: name, Server: server, Port: port, Type: "vmess"}
		if wsOpts, ok := m["ws-opts"].(map[string]interface{}); ok {
			ws := &WSOptions{}
			if path, ok := wsOpts["path"].(string); ok {
				ws.Path = path
			}
			if headers, ok := wsOpts["headers"].(map[string]interface{}); ok {
				ws.Headers = make(map[string]string)
				for k, v := range headers {
					if vs, ok := v.(string); ok {
						ws.Headers[k] = vs
					}
				}
			}
			v.WSOpts = ws
		}
		return v
	case "trojan":
		password, _ := m["password"].(string)
		sni, _ := m["sni"].(string)
		t := &Trojan{Password: password, Sni: sni}
		t.Base = Base{Name: name, Server: server, Port: port, Type: "trojan"}
		return t
	case "vless":
		uuid, _ := m["uuid"].(string)
		network, _ := m["network"].(string)
		flow, _ := m["flow"].(string)
		tls, _ := m["tls"].(bool)
		serverName, _ := m["servername"].(string)
		skipCert, _ := m["skip-cert-verify"].(bool)
		v := &Vless{
			UUID:           uuid,
			Network:        network,
			Flow:           flow,
			TLS:            tls,
			ServerName:     serverName,
			SkipCertVerify: skipCert,
		}
		v.Base = Base{Name: name, Server: server, Port: port, Type: "vless"}
		return v
	case "hysteria2":
		password, _ := m["password"].(string)
		sni, _ := m["sni"].(string)
		h := &Hysteria2{Password: password, Sni: sni}
		h.Base = Base{Name: name, Server: server, Port: port, Type: "hysteria2"}
		return h
	case "http":
		username, _ := m["username"].(string)
		password, _ := m["password"].(string)
		h := &Http{Username: username, Password: password}
		h.Base = Base{Name: name, Server: server, Port: port, Type: "http"}
		return h
	case "socks5":
		username, _ := m["username"].(string)
		password, _ := m["password"].(string)
		s := &Socks5{Username: username, Password: password}
		s.Base = Base{Name: name, Server: server, Port: port, Type: "socks5"}
		return s
	}
	return nil
}

func toIntProxy(v interface{}) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case json.Number:
		i, _ := n.Int64()
		return int(i)
	default:
		return 0
	}
}
