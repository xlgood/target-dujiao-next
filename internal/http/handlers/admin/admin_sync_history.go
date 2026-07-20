package admin

import (
	"encoding/csv"
	"fmt"
	"strings"

	"github.com/dujiao-next/internal/http/handlers/shared"
	"github.com/dujiao-next/internal/http/response"
	"github.com/dujiao-next/internal/models"
	"github.com/dujiao-next/internal/repository"

	"github.com/gin-gonic/gin"
)

func (h *Handler) ListTGXInventorySyncRuns(c *gin.Context) {
	page, pageSize := shared.ParsePagination(c)
	connectionID, err := shared.ParseQueryUint(c.Query("connection_id"), false)
	if err != nil {
		shared.RespondError(c, response.CodeBadRequest, "error.bad_request", err)
		return
	}
	runs, total, err := h.ProductMappingService.ListTGXInventorySyncRuns(repository.TGXInventorySyncRunListFilter{
		ConnectionID: connectionID, Status: strings.TrimSpace(c.Query("status")), Pagination: repository.Pagination{Page: page, PageSize: pageSize},
	})
	if err != nil {
		shared.RespondError(c, response.CodeInternal, "error.mapping_fetch_failed", err)
		return
	}
	response.SuccessWithPage(c, runs, response.BuildPagination(page, pageSize, total))
}

func (h *Handler) ExportTGXInventorySyncRunFailures(c *gin.Context) {
	id, err := shared.ParseParamUint(c, "id")
	if err != nil {
		shared.RespondError(c, response.CodeBadRequest, "error.bad_request", err)
		return
	}
	run, err := h.ProductMappingService.GetTGXInventorySyncRun(id)
	if err != nil {
		shared.RespondError(c, response.CodeInternal, "error.mapping_fetch_failed", err)
		return
	}
	if run == nil {
		shared.RespondError(c, response.CodeNotFound, "error.mapping_not_found", nil)
		return
	}
	c.Header("Content-Type", "text/csv; charset=utf-8")
	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=\"tgx-inventory-failures-%d.csv\"", run.ID))
	w := csv.NewWriter(c.Writer)
	_ = w.Write([]string{"sku_mapping_id", "local_sku_id", "upstream_sku_code", "error"})
	for _, item := range jsonItems(run.FailedDetails) {
		_ = w.Write([]string{fmt.Sprint(item["sku_mapping_id"]), fmt.Sprint(item["local_sku_id"]), fmt.Sprint(item["upstream_sku_code"]), fmt.Sprint(item["error"])})
	}
	w.Flush()
}

func (h *Handler) ListProviderBalanceSnapshots(c *gin.Context) {
	page, pageSize := shared.ParsePagination(c)
	connectionID, err := shared.ParseQueryUint(c.Query("connection_id"), false)
	if err != nil {
		shared.RespondError(c, response.CodeBadRequest, "error.bad_request", err)
		return
	}
	snapshots, total, err := h.SiteConnectionService.ListProviderBalanceSnapshots(repository.ProviderBalanceSnapshotListFilter{
		ConnectionID: connectionID, Status: strings.TrimSpace(c.Query("status")), Pagination: repository.Pagination{Page: page, PageSize: pageSize},
	})
	if err != nil {
		shared.RespondError(c, response.CodeInternal, "error.connection_fetch_failed", err)
		return
	}
	response.SuccessWithPage(c, snapshots, response.BuildPagination(page, pageSize, total))
}

func (h *Handler) ListProviderCatalogSyncRuns(c *gin.Context) {
	page, pageSize := shared.ParsePagination(c)
	runs, total, err := h.ProductMappingService.ListProviderCatalogSyncRuns(repository.ProviderCatalogSyncRunListFilter{
		Status: strings.TrimSpace(c.Query("status")), Pagination: repository.Pagination{Page: page, PageSize: pageSize},
	})
	if err != nil {
		shared.RespondError(c, response.CodeInternal, "error.mapping_fetch_failed", err)
		return
	}
	response.SuccessWithPage(c, runs, response.BuildPagination(page, pageSize, total))
}

func (h *Handler) ListProviderCatalogContentSyncRuns(c *gin.Context) {
	page, pageSize := shared.ParsePagination(c)
	runs, total, err := h.ProductMappingService.ListProviderCatalogContentSyncRuns(repository.ProviderCatalogContentSyncRunListFilter{
		Status: strings.TrimSpace(c.Query("status")), Pagination: repository.Pagination{Page: page, PageSize: pageSize},
	})
	if err != nil {
		shared.RespondError(c, response.CodeInternal, "error.mapping_fetch_failed", err)
		return
	}
	response.SuccessWithPage(c, runs, response.BuildPagination(page, pageSize, total))
}

func (h *Handler) ExportProviderCatalogFilterReasons(c *gin.Context) {
	id, err := shared.ParseParamUint(c, "id")
	if err != nil {
		shared.RespondError(c, response.CodeBadRequest, "error.bad_request", err)
		return
	}
	run, err := h.ProductMappingService.GetProviderCatalogSyncRun(id)
	if err != nil {
		shared.RespondError(c, response.CodeInternal, "error.mapping_fetch_failed", err)
		return
	}
	if run == nil {
		shared.RespondError(c, response.CodeNotFound, "error.mapping_not_found", nil)
		return
	}
	c.Header("Content-Type", "text/csv; charset=utf-8")
	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=\"provider-catalog-filter-reasons-%d.csv\"", run.ID))
	w := csv.NewWriter(c.Writer)
	_ = w.Write([]string{"provider", "code", "name", "reason"})
	for _, item := range jsonItems(run.FilterReasonsJSON) {
		_ = w.Write([]string{fmt.Sprint(item["provider"]), fmt.Sprint(item["code"]), fmt.Sprint(item["name"]), fmt.Sprint(item["reason"])})
	}
	w.Flush()
}

func jsonItems(value models.JSON) []map[string]interface{} {
	items, ok := value["items"].([]interface{})
	if !ok {
		return nil
	}
	result := make([]map[string]interface{}, 0, len(items))
	for _, item := range items {
		if row, ok := item.(map[string]interface{}); ok {
			result = append(result, row)
		}
	}
	return result
}
