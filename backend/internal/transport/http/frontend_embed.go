package httpserver

import "embed"

// frontendDist 内置前端构建产物。
// 构建时 GitHub Actions 把 frontend/dist 下载到 backend/frontend_dist/,
// go:embed 把它编译进二进制,运行时不需要外部文件。
//
//go:embed all:frontend_dist
var frontendDist embed.FS
