package httpserver

import (
	"embed"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/gin-gonic/gin"
)

// registerFrontend 在构建产物存在时托管静态文件,并为前端路由提供 SPA 回退。
// 优先使用 go:embed 内置的 frontendDist;若内置不存在则 fallback 到 staticPath。
func registerFrontend(router *gin.Engine, staticPath string) {
	root, indexPath, fsRoot, ok := frontendRoot(staticPath)
	if !ok {
		return
	}
	var handler http.Handler
	if fsRoot != nil {
		handler = http.FileServer(http.FS(fsRoot))
	} else {
		handler = http.FileServer(http.Dir(root))
	}
	router.NoRoute(func(c *gin.Context) {
		requestPath := c.Request.URL.Path
		if (c.Request.Method != http.MethodGet && c.Request.Method != http.MethodHead) || isBackendPath(requestPath) {
			c.Status(http.StatusNotFound)
			return
		}
		if filePath, exists := frontendFile(root, requestPath, fsRoot); exists {
			if strings.HasPrefix(path.Clean(requestPath), "/assets/") {
				c.Header("Cache-Control", "public, max-age=31536000, immutable")
			} else {
				c.Header("Cache-Control", "no-cache")
			}
			c.Request.URL.Path = "/" + filepath.ToSlash(filePath)
			handler.ServeHTTP(c.Writer, c.Request)
			return
		}
		if path.Ext(path.Clean(requestPath)) != "" {
			c.Status(http.StatusNotFound)
			return
		}
		c.Header("Cache-Control", "no-cache")
		if fsRoot != nil {
			// embed 模式:从 embed.FS 读 index.html
			if data, err := fs.ReadFile(fsRoot, "index.html"); err == nil {
				c.Data(http.StatusOK, "text/html; charset=utf-8", data)
				return
			}
		}
		http.ServeFile(c.Writer, c.Request, indexPath)
	})
}

// frontendRoot 返回 (root, indexPath, embeddedFS, ok)。
// 优先用 go:embed 内置的前端;若没有则用 staticPath 目录。
func frontendRoot(staticPath string) (string, string, fs.FS, bool) {
	// 1. 优先用内置 embed
	if sub, err := fs.Sub(frontendDist, "frontend_dist"); err == nil {
		if idx, err := sub.Open("index.html"); err == nil {
			idx.Close()
			return "frontend_dist", "", sub, true
		}
	}
	// 2. fallback 到 staticPath
	staticPath = strings.TrimSpace(staticPath)
	if staticPath == "" {
		return "", "", nil, false
	}
	root, err := filepath.Abs(staticPath)
	if err != nil {
		return "", "", nil, false
	}
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		return "", "", nil, false
	}
	indexPath := filepath.Join(root, "index.html")
	indexInfo, err := os.Stat(indexPath)
	if err != nil || !indexInfo.Mode().IsRegular() {
		return "", "", nil, false
	}
	return filepath.Clean(root), indexPath, nil, true
}

func frontendFile(root, requestPath string, fsys fs.FS) (string, bool) {
	cleanPath := strings.TrimPrefix(path.Clean("/"+requestPath), "/")
	if cleanPath == "" || cleanPath == "." {
		return "", false
	}
	if fsys != nil {
		info, err := fs.Stat(fsys, cleanPath)
		if err != nil || info.IsDir() {
			return "", false
		}
		return cleanPath, true
	}
	fullPath := filepath.Join(root, filepath.FromSlash(cleanPath))
	relative, err := filepath.Rel(root, fullPath)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", false
	}
	info, err := os.Stat(fullPath)
	if err != nil || !info.Mode().IsRegular() {
		return "", false
	}
	return relative, true
}

func isBackendPath(value string) bool {
	cleanPath := path.Clean("/" + value)
	for _, prefix := range []string{"/api", "/v1", "/swagger"} {
		if cleanPath == prefix || strings.HasPrefix(cleanPath, prefix+"/") {
			return true
		}
	}
	return cleanPath == "/healthz" || cleanPath == "/readyz"
}

// 确保 embed.FS 被引用(避免 unused import)
var _ = embed.FS{}
