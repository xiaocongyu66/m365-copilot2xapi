package proxies

import (
	"net/http"
	"time"

	"M365Copilot2ApiX/backend/internal/infra/proxypool"
	"M365Copilot2ApiX/backend/internal/shared/response"

	"github.com/gin-gonic/gin"
)

// Handler 代理节点池的 HTTP handler
type Handler struct {
	svc *proxypool.Service
}

func NewHandler(svc *proxypool.Service) *Handler {
	return &Handler{svc: svc}
}

// Register 注册代理池管理 API(需要管理员认证,由上层 middleware 保证)
func (h *Handler) Register(router *gin.RouterGroup) {
	router.GET("/proxies/nodes", h.listNodes)
	router.POST("/proxies/import", h.importNodes)
	router.POST("/proxies/enabled", h.setEnabled)
	router.POST("/proxies/delete", h.deleteNodes)
	router.POST("/proxies/clear-errors", h.clearErrors)
	router.POST("/proxies/check", h.checkNow)
	router.GET("/proxies/check/sync", h.checkSync)
	router.GET("/proxies/errors", h.recentErrors)
	router.POST("/proxies/fetch", h.fetchNow)
	router.GET("/proxies/fetcher/status", h.fetcherStatus)
	router.GET("/proxies/fetcher/progress", h.fetcherProgress)
	router.GET("/proxies/fetcher/config", h.getFetcherConfig)
	router.POST("/proxies/register/start", h.registrarStart)
	router.POST("/proxies/register/stop", h.registrarStop)
	router.GET("/proxies/register/status", h.registrarStatus)
	router.POST("/proxies/fetcher/config", h.updateFetcherConfig)
	router.POST("/proxies/restart", h.restartService)
	router.POST("/proxies/migrate-cn", h.migrateCN)
	router.POST("/proxies/reset-countries", h.resetCountries)
}

func (h *Handler) listNodes(c *gin.Context) {
	nodes := h.svc.ListNodes()
	response.Success(c, http.StatusOK, nodes)
}

type importRequest struct {
	Text string `json:"text"`
}

func (h *Handler) importNodes(c *gin.Context) {
	var req importRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, http.StatusBadRequest, "invalidRequest", "请求参数无效")
		
	}
	imported, skipped := h.svc.ImportNodes(req.Text)
	response.Success(c, http.StatusOK, gin.H{"imported": imported, "skipped": skipped})
}

type setEnabledRequest struct {
	IDs     []string `json:"ids"`
	Enabled bool     `json:"enabled"`
}

func (h *Handler) setEnabled(c *gin.Context) {
	var req setEnabledRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, http.StatusBadRequest, "invalidRequest", "请求参数无效")
		
	}
	h.svc.SetEnabled(req.IDs, req.Enabled)
	response.Success(c, http.StatusOK, gin.H{"status": "updated"})
}

type deleteRequest struct {
	IDs []string `json:"ids"`
}

func (h *Handler) deleteNodes(c *gin.Context) {
	var req deleteRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, http.StatusBadRequest, "invalidRequest", "请求参数无效")
		
	}
	h.svc.DeleteNodes(req.IDs)
	response.Success(c, http.StatusOK, gin.H{"status": "deleted"})
}

func (h *Handler) clearErrors(c *gin.Context) {
	var req deleteRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, http.StatusBadRequest, "invalidRequest", "请求参数无效")
		
	}
	h.svc.ClearErrors(req.IDs)
	response.Success(c, http.StatusOK, gin.H{"status": "cleared"})
}

func (h *Handler) checkNow(c *gin.Context) {
	// 支持对单个节点测试:POST /proxies/check?identifier=xxx
	identifier := c.Query("identifier")
	if identifier != "" {
		go h.svc.CheckOne(identifier)
		response.Success(c, http.StatusOK, gin.H{"status": "check_started", "identifier": identifier})
		return
	}
	go h.svc.Checker().RunOnce()
	response.Success(c, http.StatusOK, gin.H{"status": "check_started"})
}

// restartService 软重启 proxypool 子系统:重新加载持久化文件,不退出进程。
func (h *Handler) restartService(c *gin.Context) {
	// 先保存当前状态
	h.svc.Fetcher().Save()
	h.svc.Score().Save()
	// 软重启(重新加载持久化文件 + 重启后台任务)
	h.svc.Reload()
	response.Success(c, http.StatusOK, gin.H{"status": "reloaded"})
}

// migrateCN 把主池里的中国节点移到注册专用池。
func (h *Handler) migrateCN(c *gin.Context) {
	migrated := h.svc.MigrateCNToRegister()
	response.Success(c, http.StatusOK, gin.H{"migrated": migrated})
}

// resetCountries 清空所有节点的国家记录,把注册池节点移回主池。
// 用于修复之前用服务器地址查国家的不准记录,测活后重新填充。
func (h *Handler) resetCountries(c *gin.Context) {
	count := h.svc.ResetCountries()
	response.Success(c, http.StatusOK, gin.H{"reset": count})
}
func (h *Handler) checkSync(c *gin.Context) {
	identifier := c.Query("identifier")
	if identifier == "" {
		response.Error(c, http.StatusBadRequest, "invalidRequest", "缺少 identifier 参数")
		return
	}
	result, found := h.svc.CheckOneSync(identifier)
	if !found {
		response.Error(c, http.StatusNotFound, "nodeNotFound", "节点不存在")
		return
	}
	response.Success(c, http.StatusOK, gin.H{
		"accessible":  result.Accessible,
		"stable":      result.Stable,
		"latencyMs":   result.Latency.Milliseconds(),
		"purityScore": result.PurityScore,
		"ipType":      result.IPType,
		"exitIP":      result.ExitIP,
		"isp":         result.ISP,
		"error":       result.Error,
	})
}

func (h *Handler) recentErrors(c *gin.Context) {
	errors := h.svc.RecentErrors()
	response.Success(c, http.StatusOK, errors)
}

type fetchRequest struct {
	URL string `json:"url"`
}

func (h *Handler) fetchNow(c *gin.Context) {
	var req fetchRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, http.StatusBadRequest, "invalidRequest", "请求参数无效")
		
	}
	// 立即抓取一次
	result := h.svc.Fetcher().RunOnceWithSource(req.URL)
	response.Success(c, http.StatusOK, gin.H{
		"imported": result.Imported,
		"skipped":  result.Skipped,
		"total":    result.Total,
		"errors":   result.Errors,
	})
}

func (h *Handler) fetcherStatus(c *gin.Context) {
	running, lastRun, lastResult := h.svc.Fetcher().Status()
	response.Success(c, http.StatusOK, gin.H{
		"running":   running,
		"lastRun":   lastRun.Format(time.RFC3339),
		"lastResult": lastResult,
	})
}

// getFetcherConfig 返回当前抓取配置
func (h *Handler) getFetcherConfig(c *gin.Context) {
	running, _, _ := h.svc.Fetcher().Status()
	config := h.svc.Fetcher().GetConfig()
	response.Success(c, http.StatusOK, gin.H{
		"enabled":  running,
		"interval": config.Interval.String(),
		"sources":  sourceURLs(config.Sources),
	})
}

// updateFetcherConfig 更新抓取配置(启用/禁用、间隔、源列表)
func (h *Handler) updateFetcherConfig(c *gin.Context) {
	var req struct {
		Enabled  bool     `json:"enabled"`
		Interval  string   `json:"interval"`
		Sources  []string `json:"sources"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, http.StatusBadRequest, "invalidRequest", "请求参数无效")
		
	}
	// 解析间隔
	interval := proxypool.DefaultFetchInterval()
	if req.Interval != "" {
		if d, err := time.ParseDuration(req.Interval + "m"); err == nil {
			interval = d
		}
	}
	// 构造 FetchSource 列表
	sources := make([]proxypool.FetchSource, 0, len(req.Sources))
	for _, url := range req.Sources {
		sources = append(sources, proxypool.FetchSource{URL: url, SourceID: url})
	}
	h.svc.Fetcher().UpdateConfig(proxypool.FetcherConfig{
		Enabled:  req.Enabled,
		Interval: interval,
		Sources:  sources,
	})
	response.Success(c, http.StatusOK, gin.H{"status": "updated"})
}

// sourceURLs 从 FetchSource 列表提取 URL
func sourceURLs(sources []proxypool.FetchSource) []string {
	urls := make([]string, 0, len(sources))
	for _, s := range sources {
		urls = append(urls, s.URL)
	}
	return urls
}

// ===== M365 账号注册 =====

// ===== M365 自动注册管理 =====

// registrarStart 启动自动注册
func (h *Handler) registrarStart(c *gin.Context) {
	var req struct {
		TargetCount int    `json:"targetCount"` // 注册目标数量(0=一直注册)
		Concurrency int    `json:"concurrency"` // 并发数(默认1)
		Username    string `json:"username"`    // 邮箱前缀(留空=随机)
		Password    string `json:"password"`    // 密码(留空=随机)
		DisplayName string `json:"displayName"` // 显示名(留空=随机)
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, http.StatusBadRequest, "invalidRequest", "请求参数无效")
		return
	}
	h.svc.Registrar().Start(proxypool.RegistrarConfig{
		Enabled:     true,
		TargetCount: req.TargetCount,
		Concurrency: req.Concurrency,
		Username:    req.Username,
		Password:    req.Password,
		DisplayName: req.DisplayName,
	})
	response.Success(c, http.StatusOK, gin.H{"status": "started"})
}

// registrarStop 停止自动注册
func (h *Handler) registrarStop(c *gin.Context) {
	h.svc.Registrar().Stop()
	response.Success(c, http.StatusOK, gin.H{"status": "stopped"})
}

// registrarStatus 返回注册状态和统计
func (h *Handler) registrarStatus(c *gin.Context) {
	total, succeeded, failed, results := h.svc.Registrar().Stats()
	response.Success(c, http.StatusOK, gin.H{
		"running":     h.svc.Registrar().IsRunning(),
		"config":      h.svc.Registrar().Config(),
		"total":       total,
		"succeeded":   succeeded,
		"failed":      failed,
		"results":     results,
		"usedProxies": h.svc.Registrar().UsedProxies(),
	})
}

// fetcherProgress 返回当前抓取进度
func (h *Handler) fetcherProgress(c *gin.Context) {
	stage, total, done, usable := h.svc.Fetcher().Progress()
	response.Success(c, http.StatusOK, gin.H{
		"stage":   stage,
		"total":   total,
		"done":    done,
		"usable":  usable,
		"percent": func() float64 { if total == 0 { return 0 }; return float64(done) / float64(total) * 100 }(),
	})
}
