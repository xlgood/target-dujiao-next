package admin

import (
	"fmt"

	"github.com/dujiao-next/internal/constants"
	"github.com/dujiao-next/internal/http/handlers/shared"
	"github.com/dujiao-next/internal/http/response"
	"github.com/dujiao-next/internal/logger"
	"github.com/dujiao-next/internal/models"
	"github.com/dujiao-next/internal/queue"
	"github.com/dujiao-next/internal/service"
	"github.com/dujiao-next/internal/upstream"

	"github.com/gin-gonic/gin"
)

type ProviderCatalogClientFactory func(fansConn, tgxConn *models.SiteConnection, decryptSecret func(string) (string, error)) (service.FansGurusCatalogClient, service.TGXCatalogClient, error)

type syncProviderCatalogRequest struct {
	FansGurusConnectionID uint `json:"fansgurus_connection_id" binding:"required"`
	TGXConnectionID       uint `json:"tgx_connection_id" binding:"required"`
}

func defaultProviderCatalogClientFactory(fansConn, tgxConn *models.SiteConnection, decryptSecret func(string) (string, error)) (service.FansGurusCatalogClient, service.TGXCatalogClient, error) {
	if tgxConn == nil {
		return nil, nil, fmt.Errorf("tgx connection is required")
	}
	tgxAppKey, err := decryptSecret(tgxConn.ApiSecret)
	if err != nil {
		return nil, nil, err
	}

	return upstream.NewFansGurusClient(fansConn.BaseURL, fansConn.ApiKey),
		upstream.NewTGXClient(tgxConn.BaseURL, tgxConn.ApiKey, tgxAppKey),
		nil
}

// SyncProviderCatalog 手动触发 FansGurus + TGX 商品目录同步。
func (h *Handler) SyncProviderCatalog(c *gin.Context) {
	var req syncProviderCatalogRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		shared.RespondBindError(c, err)
		return
	}
	if req.FansGurusConnectionID == 0 || req.TGXConnectionID == 0 {
		shared.RespondError(c, response.CodeBadRequest, "error.bad_request", nil)
		return
	}

	fansConn, ok := h.loadProviderCatalogConnection(c, req.FansGurusConnectionID, constants.ConnectionProtocolFansGurus)
	if !ok {
		return
	}
	tgxConn, ok := h.loadProviderCatalogConnection(c, req.TGXConnectionID, constants.ConnectionProtocolTGXAccount)
	if !ok {
		return
	}

	factory := h.ProviderCatalogClientFactory
	if factory == nil {
		factory = defaultProviderCatalogClientFactory
	}
	fansClient, tgxClient, err := factory(fansConn, tgxConn, h.SiteConnectionService.DecryptSecret)
	if err != nil {
		shared.RespondError(c, response.CodeInternal, "error.connection_invalid", err)
		return
	}

	result, err := h.ProductMappingService.SyncProviderCatalogWithClients(c.Request.Context(), service.ProviderCatalogSyncInput{
		FansGurusConnectionID: req.FansGurusConnectionID,
		TGXConnectionID:       req.TGXConnectionID,
	}, fansClient, tgxClient)
	if err != nil {
		shared.RespondError(c, response.CodeInternal, "error.bad_request", err)
		return
	}
	result.InventoryRefreshStatus = "queue_disabled"
	if h.ProviderCatalogInventoryEnqueue != nil {
		if err := h.ProviderCatalogInventoryEnqueue(); err != nil {
			result.InventoryRefreshStatus = "enqueue_failed"
			logger.Warnw("provider_catalog_inventory_enqueue_failed", "error", err)
		} else {
			result.InventoryRefreshQueued = true
			result.InventoryRefreshStatus = "queued"
		}
	} else if h.QueueClient != nil && h.QueueClient.Enabled() {
		if err := h.QueueClient.EnqueueUpstreamSyncStock(); err != nil {
			result.InventoryRefreshStatus = "enqueue_failed"
			logger.Warnw("provider_catalog_inventory_enqueue_failed", "error", err)
		} else {
			result.InventoryRefreshQueued = true
			result.InventoryRefreshStatus = "queued"
		}
	}

	response.Success(c, result)
}

// SyncProviderCatalogContent refreshes safe storefront descriptions and
// per-item TGX purchase profiles. It deliberately leaves prices, stock,
// reviews, and publication state untouched.
func (h *Handler) SyncProviderCatalogContent(c *gin.Context) {
	var req syncProviderCatalogRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		shared.RespondBindError(c, err)
		return
	}
	fansConn, ok := h.loadProviderCatalogConnection(c, req.FansGurusConnectionID, constants.ConnectionProtocolFansGurus)
	if !ok {
		return
	}
	tgxConn, ok := h.loadProviderCatalogConnection(c, req.TGXConnectionID, constants.ConnectionProtocolTGXAccount)
	if !ok {
		return
	}
	payload := queue.ProviderCatalogContentSyncPayload{FansGurusConnectionID: fansConn.ID, TGXConnectionID: tgxConn.ID}
	if h.ProviderCatalogContentEnqueue != nil {
		if err := h.ProviderCatalogContentEnqueue(payload); err != nil {
			shared.RespondError(c, response.CodeInternal, "error.bad_request", err)
			return
		}
		response.Success(c, gin.H{"status": "queued"})
		return
	}
	if h.QueueClient != nil && h.QueueClient.Enabled() {
		if err := h.QueueClient.EnqueueProviderCatalogContentSync(payload); err != nil {
			shared.RespondError(c, response.CodeInternal, "error.bad_request", err)
			return
		}
		response.Success(c, gin.H{"status": "queued"})
		return
	}
	response.Success(c, gin.H{"status": "queue_disabled"})
}

func (h *Handler) loadProviderCatalogConnection(c *gin.Context, id uint, protocol string) (*models.SiteConnection, bool) {
	conn, err := h.SiteConnectionService.GetByID(id)
	if err != nil {
		shared.RespondError(c, response.CodeInternal, "error.connection_fetch_failed", err)
		return nil, false
	}
	if conn == nil {
		shared.RespondError(c, response.CodeNotFound, "error.connection_not_found", nil)
		return nil, false
	}
	if conn.Protocol != protocol {
		shared.RespondError(c, response.CodeBadRequest, "error.connection_invalid", nil)
		return nil, false
	}
	return conn, true
}
