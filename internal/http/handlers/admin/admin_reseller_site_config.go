package admin

import (
	"strings"

	"github.com/dujiao-next/internal/dto"
	"github.com/dujiao-next/internal/http/handlers/shared"
	"github.com/dujiao-next/internal/http/response"
	"github.com/dujiao-next/internal/models"
	"github.com/dujiao-next/internal/repository"
	"github.com/dujiao-next/internal/service"

	"github.com/gin-gonic/gin"
)

type AdminResellerSiteConfigRequest struct {
	SiteName     string                            `json:"site_name"`
	Logo         string                            `json:"logo"`
	Favicon      string                            `json:"favicon"`
	Announcement service.ResellerAnnouncementInput `json:"announcement"`
	Support      service.ResellerSupportInput      `json:"support"`
	SEO          service.ResellerSEOInput          `json:"seo"`
	FooterLinks  []service.ResellerFooterLinkInput `json:"footer_links"`
	NavConfig    service.ResellerNavConfigInput    `json:"nav_config"`
}

func (req AdminResellerSiteConfigRequest) toServiceInput() service.ResellerSiteConfigInput {
	return service.ResellerSiteConfigInput{
		SiteName:     req.SiteName,
		Logo:         req.Logo,
		Favicon:      req.Favicon,
		Announcement: req.Announcement,
		Support:      req.Support,
		SEO:          req.SEO,
		FooterLinks:  req.FooterLinks,
		NavConfig:    req.NavConfig,
	}
}

// ListResellerSiteConfigs 管理端分销站点配置列表。
func (h *Handler) ListResellerSiteConfigs(c *gin.Context) {
	if h.ResellerRepo == nil {
		shared.RespondError(c, response.CodeInternal, "error.user_fetch_failed", nil)
		return
	}
	page, pageSize := shared.ParsePagination(c)
	resellerID, _ := shared.ParseQueryUint(c.Query("reseller_id"), false)
	rows, total, err := h.ResellerRepo.ListSiteConfigs(repository.ResellerSiteConfigListFilter{
		Page:        page,
		PageSize:    pageSize,
		ResellerID:  resellerID,
		Keyword:     strings.TrimSpace(c.Query("keyword")),
		CreatedFrom: parseAdminResellerTimePointer(c.Query("created_from")),
		CreatedTo:   parseAdminResellerTimePointer(c.Query("created_to")),
	})
	if err != nil {
		shared.RespondError(c, response.CodeInternal, "error.user_fetch_failed", err)
		return
	}
	response.SuccessWithPage(c, dto.NewAdminResellerSiteConfigRespList(rows), response.BuildPagination(page, pageSize, total))
}

// GetResellerSiteConfig 管理端获取单个分销站点配置。
func (h *Handler) GetResellerSiteConfig(c *gin.Context) {
	if h.ResellerRepo == nil {
		shared.RespondError(c, response.CodeInternal, "error.user_fetch_failed", nil)
		return
	}
	resellerID, err := shared.ParseParamUint(c, "reseller_id")
	if err != nil {
		shared.RespondError(c, response.CodeBadRequest, "error.bad_request", nil)
		return
	}
	row, err := h.ResellerRepo.GetSiteConfigByResellerID(resellerID)
	if err != nil {
		shared.RespondError(c, response.CodeInternal, "error.user_fetch_failed", err)
		return
	}
	if row == nil {
		profile, profileErr := h.ResellerRepo.GetProfileByID(resellerID)
		if profileErr != nil {
			shared.RespondError(c, response.CodeInternal, "error.user_fetch_failed", profileErr)
			return
		}
		if profile == nil {
			shared.RespondError(c, response.CodeNotFound, "error.bad_request", nil)
			return
		}
		row = &models.ResellerSiteConfig{ResellerID: resellerID, Profile: profile}
	}
	response.Success(c, dto.NewAdminResellerSiteConfigResp(row))
}

// UpdateResellerSiteConfig 管理端更新分销站点配置。
func (h *Handler) UpdateResellerSiteConfig(c *gin.Context) {
	if h.ResellerSiteConfigService == nil || h.ResellerRepo == nil {
		shared.RespondError(c, response.CodeInternal, "error.save_failed", nil)
		return
	}
	resellerID, err := shared.ParseParamUint(c, "reseller_id")
	if err != nil {
		shared.RespondError(c, response.CodeBadRequest, "error.bad_request", nil)
		return
	}
	var req AdminResellerSiteConfigRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		shared.RespondBindError(c, err)
		return
	}
	row, err := h.ResellerSiteConfigService.UpdateAdminSiteConfig(c.Request.Context(), resellerID, req.toServiceInput())
	if err != nil {
		respondAdminResellerManagementError(c, err)
		return
	}
	reloaded, reloadErr := h.ResellerRepo.GetSiteConfigByResellerID(resellerID)
	if reloadErr == nil && reloaded != nil {
		row = reloaded
	}
	h.recordAuthzAudit(c, service.AuthzAuditRecordInput{
		OperatorAdminID:  c.GetUint("admin_id"),
		OperatorUsername: c.GetString("username"),
		Action:           "reseller_site_config_update",
		Object:           "/admin/resellers/site-configs/:reseller_id",
		Method:           "PUT",
		RequestID:        strings.TrimSpace(c.GetString("request_id")),
		Detail: models.JSON{
			"reseller_id":    resellerID,
			"config_id":      row.ID,
			"site_name":      row.SiteName,
			"changed_fields": []string{"site_name", "logo", "favicon", "announcement", "support", "seo", "footer_links", "nav_config"},
			"source":         "admin",
		},
	})
	response.Success(c, dto.NewAdminResellerSiteConfigResp(row))
}

// ResetResellerSiteConfig 管理端重置分销站点配置。
func (h *Handler) ResetResellerSiteConfig(c *gin.Context) {
	if h.ResellerSiteConfigService == nil {
		shared.RespondError(c, response.CodeInternal, "error.save_failed", nil)
		return
	}
	resellerID, err := shared.ParseParamUint(c, "reseller_id")
	if err != nil {
		shared.RespondError(c, response.CodeBadRequest, "error.bad_request", nil)
		return
	}
	if err := h.ResellerSiteConfigService.ResetAdminSiteConfig(c.Request.Context(), resellerID); err != nil {
		respondAdminResellerManagementError(c, err)
		return
	}
	h.recordAuthzAudit(c, service.AuthzAuditRecordInput{
		OperatorAdminID:  c.GetUint("admin_id"),
		OperatorUsername: c.GetString("username"),
		Action:           "reseller_site_config_reset",
		Object:           "/admin/resellers/site-configs/:reseller_id/reset",
		Method:           "POST",
		RequestID:        strings.TrimSpace(c.GetString("request_id")),
		Detail: models.JSON{
			"reseller_id": resellerID,
			"source":      "admin",
		},
	})
	response.Success(c, gin.H{"ok": true})
}
