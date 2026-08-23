package geoip

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/oschwald/geoip2-golang"
)

// GeoIPDB 封装 MaxMind GeoLite2-City 数据库,自动下载和查询
// 启动时自动下载数据库到 assets 目录,后续查询用本地数据库(无 API 调用)
type GeoIPDB struct {
	mu   sync.RWMutex
	db   *geoip2.Reader
	path string
}

var (
	instance *GeoIPDB
	once     sync.Once
)

// DefaultDBPath 返回默认的 GeoIP 数据库路径
func DefaultDBPath() string {
	return filepath.Join("assets", "GeoLite2-City.mmdb")
}

// Get 返回 GeoIP 单例(启动时初始化)
func Get() *GeoIPDB {
	once.Do(func() {
		instance = &GeoIPDB{path: DefaultDBPath()}
		instance.init()
	})
	return instance
}

// init 初始化 GeoIP 数据库:
//   1. 如果本地文件存在,直接加载
//   2. 如果不存在,自动下载(GeoLite2 免费版)
func (g *GeoIPDB) init() {
	// 用 fmt 确保输出到 stdout
	fmt.Printf("[GeoIP] init: path=%s\n", g.path)
	// 尝试加载本地数据库
	if g.loadLocal() {
		fmt.Printf("[GeoIP] database loaded from %s\n", g.path)
		return
	}

	// 本地不存在,自动下载
	fmt.Printf("[GeoIP] database not found, downloading...\n")
	if err := g.download(); err != nil {
		fmt.Printf("[GeoIP] download failed: %v (geo features disabled)\n", err)
		return
	}

	// 加载下载的数据库
	if g.loadLocal() {
		fmt.Printf("[GeoIP] database downloaded and loaded successfully\n")
	}
}

// loadLocal 加载本地 GeoIP 数据库
func (g *GeoIPDB) loadLocal() bool {
	db, err := geoip2.Open(g.path)
	if err != nil {
		return false
	}
	g.mu.Lock()
	g.db = db
	g.mu.Unlock()
	return true
}

// download 自动下载 GeoLite2-City 数据库
// 使用 GitHub 上的免费镜像(无需 MaxMind 许可证)
func (g *GeoIPDB) download() error {
	// 确保目录存在
	dir := filepath.Dir(g.path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("create dir: %w", err)
	}

	// 使用免费的 GeoLite2 数据库镜像(P3TERX 维护,定期更新)
	urls := []string{
		"https://git.io/GeoLite2-City.mmdb",
		"https://github.com/P3TERX/GeoLite.mmdb/releases/latest/download/GeoLite2-City.mmdb",
		"https://cdn.jsdelivr.net/gh/P3TERX/GeoLite.mmdb@latest/GeoLite2-City.mmdb",
	}

	client := &http.Client{Timeout: 5 * time.Minute}
	for _, url := range urls {
		fmt.Printf("[GeoIP] downloading from %s\n", url)
		resp, err := client.Get(url)
		if err != nil {
			fmt.Printf("[GeoIP] download from %s failed: %v\n", url, err)
			continue
		}
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			fmt.Printf("[GeoIP] download from %s returned %d\n", url, resp.StatusCode)
			continue
		}

		// 写入文件
		out, err := os.Create(g.path)
		if err != nil {
			resp.Body.Close()
			return fmt.Errorf("create file: %w", err)
		}
		if _, err := copy(out, resp.Body); err != nil {
			resp.Body.Close()
			out.Close()
			os.Remove(g.path)
			return fmt.Errorf("write file: %w", err)
		}
		resp.Body.Close()
		out.Close()

		// 验证文件大小(至少 1MB)
		info, err := os.Stat(g.path)
		if err != nil || info.Size() < 1024*1024 {
			os.Remove(g.path)
			fmt.Printf("[GeoIP] downloaded file too small or invalid from %s\n", url)
			continue
		}

		fmt.Printf("[GeoIP] database downloaded: %d bytes from %s\n", info.Size(), url)
		return nil
	}
	return fmt.Errorf("all download sources failed")
}

// copy 是 io.Copy 的简单封装(避免额外 import)
func copy(dst *os.File, src interface{ Read([]byte) (int, error) }) (int64, error) {
	buf := make([]byte, 32*1024)
	var total int64
	for {
		n, err := src.Read(buf)
		if n > 0 {
			w, werr := dst.Write(buf[:n])
			total += int64(w)
			if werr != nil {
				return total, werr
			}
		}
		if err != nil {
			if err.Error() == "EOF" {
				return total, nil
			}
			return total, err
		}
	}
}

// LookupCountry 查询 IP 的国家代码(如 CN、HK、US)
// 如果查询失败返回空字符串
func (g *GeoIPDB) LookupCountry(ipStr string) string {
	g.mu.RLock()
	db := g.db
	g.mu.RUnlock()
	if db == nil {
		return ""
	}
	ip := net.ParseIP(ipStr)
	if ip == nil {
		return ""
	}
	country, err := db.Country(ip)
	if err != nil {
		return ""
	}
	return country.Country.IsoCode
}

// LookupCountryName 查询 IP 的国家名称(如 China、Hong Kong)
func (g *GeoIPDB) LookupCountryName(ipStr string) string {
	g.mu.RLock()
	db := g.db
	g.mu.RUnlock()
	if db == nil {
		return ""
	}
	ip := net.ParseIP(ipStr)
	if ip == nil {
		return ""
	}
	country, err := db.Country(ip)
	if err != nil {
		return ""
	}
	return country.Country.Names["en"]
}

// IsAvailable 返回 GeoIP 数据库是否可用
func (g *GeoIPDB) IsAvailable() bool {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.db != nil
}

// Refresh 重新加载 GeoIP 数据库(如果文件更新了)
func (g *GeoIPDB) Refresh() error {
	_, err := os.Stat(g.path)
	if err != nil {
		// 文件不存在,重新下载
		if err := g.download(); err != nil {
			return err
		}
	}
	if g.loadLocal() {
		return nil
	}
	return fmt.Errorf("load failed")
}
