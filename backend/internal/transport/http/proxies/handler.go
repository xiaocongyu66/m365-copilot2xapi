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
	router.GET("/proxies/errors", h.recentErrors)
	router.POST("/proxies/fetch", h.fetchNow)
	router.GET("/proxies/fetcher/status", h.fetcherStatus)
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
		return
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
		return
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
		return
	}
	h.svc.DeleteNodes(req.IDs)
	response.Success(c, http.StatusOK, gin.H{"status": "deleted"})
}

func (h *Handler) clearErrors(c *gin.Context) {
	var req deleteRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, http.StatusBadRequest, "invalidRequest", "请求参数无效")
		return
	}
	h.svc.ClearErrors(req.IDs)
	response.Success(c, http.StatusOK, gin.H{"status": "cleared"})
}

func (h *Handler) checkNow(c *gin.Context) {
	go h.svc.Checker().RunOnce()
	response.Success(c, http.StatusOK, gin.H{"status": "check_started"})
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
		return
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
