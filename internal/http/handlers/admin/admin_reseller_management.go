package admin

import (
	"context"
	"errors"
	"strings"

	"github.com/dujiao-next/internal/dto"
	"github.com/dujiao-next/internal/http/handlers/shared"
	"github.com/dujiao-next/internal/http/response"
	"github.com/dujiao-next/internal/models"
	"github.com/dujiao-next/internal/repository"
	"github.com/dujiao-next/internal/service"

	"github.com/gin-gonic/gin"
	"github.com/shopspring/decimal"
)

type ResellerProfileApproveRequest struct {
	DefaultMarkupPercent string `json:"default_markup_percent"`
	MaxMarkupPercent     string `json:"max_markup_percent"`
}

type ResellerProfileReasonRequest struct {
	Reason string `json:"reason"`
}

type ResellerProfileUpdateRequest struct {
	DefaultMarkupPercent string `json:"default_markup_percent"`
	MaxMarkupPercent     string `json:"max_markup_percent"`
	SettlementStatus     string `json:"settlement_status"`
	Reason               string `json:"reason"`
}

type ResellerSystemDomainRequest struct {
	Subdomain string `json:"subdomain"`
	Domain    string `json:"domain"`
}

// ListResellerProfiles 管理端分销商资料列表。
func (h *Handler) ListResellerProfiles(c *gin.Context) {
	if h.ResellerRepo == nil {
		shared.RespondError(c, response.CodeInternal, "error.user_fetch_failed", nil)
		return
	}
	page, pageSize := shared.ParsePagination(c)
	userID, _ := shared.ParseQueryUint(c.Query("user_id"), false)
	rows, total, err := h.ResellerRepo.ListProfiles(repository.ResellerProfileListFilter{
		Page:             page,
		PageSize:         pageSize,
		UserID:           userID,
		Status:           strings.TrimSpace(c.Query("status")),
		SettlementStatus: strings.TrimSpace(c.Query("settlement_status")),
		Keyword:          strings.TrimSpace(c.Query("keyword")),
		CreatedFrom:      parseAdminResellerTimePointer(c.Query("created_from")),
		CreatedTo:        parseAdminResellerTimePointer(c.Query("created_to")),
	})
	if err != nil {
		shared.RespondError(c, response.CodeInternal, "error.user_fetch_failed", err)
		return
	}
	response.SuccessWithPage(c, rows, response.BuildPagination(page, pageSize, total))
}

// GetResellerProfileDetail 管理端分销商运营详情。
func (h *Handler) GetResellerProfileDetail(c *gin.Context) {
	if h.ResellerRepo == nil {
		shared.RespondError(c, response.CodeInternal, "error.user_fetch_failed", nil)
		return
	}
	id, err := shared.ParseParamUint(c, "id")
	if err != nil {
		shared.RespondError(c, response.CodeBadRequest, "error.bad_request", nil)
		return
	}
	profile, err := h.ResellerRepo.GetProfileByID(id)
	if err != nil {
		shared.RespondError(c, response.CodeInternal, "error.user_fetch_failed", err)
		return
	}
	if profile == nil {
		shared.RespondError(c, response.CodeNotFound, "error.bad_request", nil)
		return
	}
	domains, err := h.ResellerRepo.ListDomainsByResellerID(id)
	if err != nil {
		shared.RespondError(c, response.CodeInternal, "error.user_fetch_failed", err)
		return
	}
	siteConfig, err := h.ResellerRepo.GetSiteConfigByResellerID(id)
	if err != nil {
		shared.RespondError(c, response.CodeInternal, "error.user_fetch_failed", err)
		return
	}
	productSummary := service.ResellerProductSettingSummary{}
	if h.ResellerProductSettingService != nil {
		productSummary, err = h.ResellerProductSettingService.SummarizeAdminSettings(id)
		if err != nil {
			shared.RespondError(c, response.CodeInternal, "error.user_fetch_failed", err)
			return
		}
	}
	balances := make([]models.ResellerBalanceAccount, 0)
	recentLedgerEntries := make([]models.ResellerLedgerEntry, 0)
	recentWithdraws := make([]models.ResellerWithdrawRequest, 0)
	if h.ResellerAccountingService != nil {
		balances, _, err = h.ResellerAccountingService.ListAdminBalanceAccounts(service.ResellerAdminBalanceAccountListFilter{
			Page:       1,
			PageSize:   20,
			ResellerID: id,
		})
		if err != nil {
			shared.RespondError(c, response.CodeInternal, "error.user_fetch_failed", err)
			return
		}
		recentLedgerEntries, _, err = h.ResellerAccountingService.ListAdminLedgerEntries(service.ResellerAdminLedgerListFilter{
			Page:       1,
			PageSize:   10,
			ResellerID: id,
		})
		if err != nil {
			shared.RespondError(c, response.CodeInternal, "error.user_fetch_failed", err)
			return
		}
		recentWithdraws, _, err = h.ResellerAccountingService.ListAdminWithdrawRequests(service.ResellerAdminWithdrawListFilter{
			Page:       1,
			PageSize:   10,
			ResellerID: id,
		})
		if err != nil {
			shared.RespondError(c, response.CodeInternal, "error.user_fetch_failed", err)
			return
		}
	}
	recentOrders := make([]service.ResellerOrderListItem, 0)
	if h.ResellerOrderService != nil {
		recentOrders, _, err = h.ResellerOrderService.ListAdminOrders(id, service.ResellerOrderListInput{
			Page:     1,
			PageSize: 10,
		})
		if err != nil {
			respondAdminResellerManagementError(c, err)
			return
		}
	}
	response.Success(c, dto.NewAdminResellerProfileDetailResp(profile, domains, siteConfig, productSummary, balances, recentOrders, recentLedgerEntries, recentWithdraws))
}

// ListResellerDomains 管理端分销域名列表。
func (h *Handler) ListResellerDomains(c *gin.Context) {
	if h.ResellerRepo == nil {
		shared.RespondError(c, response.CodeInternal, "error.user_fetch_failed", nil)
		return
	}
	page, pageSize := shared.ParsePagination(c)
	resellerID, _ := shared.ParseQueryUint(c.Query("reseller_id"), false)
	userID, _ := shared.ParseQueryUint(c.Query("user_id"), false)
	rows, total, err := h.ResellerRepo.ListDomains(repository.ResellerDomainListFilter{
		Page:               page,
		PageSize:           pageSize,
		ResellerID:         resellerID,
		UserID:             userID,
		Domain:             strings.TrimSpace(c.Query("domain")),
		Type:               strings.TrimSpace(c.Query("type")),
		Status:             strings.TrimSpace(c.Query("status")),
		VerificationStatus: strings.TrimSpace(c.Query("verification_status")),
		Keyword:            strings.TrimSpace(c.Query("keyword")),
		CreatedFrom:        parseAdminResellerTimePointer(c.Query("created_from")),
		CreatedTo:          parseAdminResellerTimePointer(c.Query("created_to")),
	})
	if err != nil {
		shared.RespondError(c, response.CodeInternal, "error.user_fetch_failed", err)
		return
	}
	response.SuccessWithPage(c, rows, response.BuildPagination(page, pageSize, total))
}

// ApproveResellerProfile 审核通过分销商资料。
func (h *Handler) ApproveResellerProfile(c *gin.Context) {
	adminID, ok := shared.GetAdminID(c)
	if !ok {
		return
	}
	if h.ResellerManagementService == nil {
		shared.RespondError(c, response.CodeInternal, "error.save_failed", nil)
		return
	}
	id, err := shared.ParseParamUint(c, "id")
	if err != nil {
		shared.RespondError(c, response.CodeBadRequest, "error.bad_request", nil)
		return
	}
	var req ResellerProfileApproveRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		shared.RespondBindError(c, err)
		return
	}
	defaultMarkup, err := parseOptionalDecimal(req.DefaultMarkupPercent)
	if err != nil {
		shared.RespondError(c, response.CodeBadRequest, "error.bad_request", nil)
		return
	}
	maxMarkup, err := parseOptionalDecimal(req.MaxMarkupPercent)
	if err != nil {
		shared.RespondError(c, response.CodeBadRequest, "error.bad_request", nil)
		return
	}
	result, err := h.ResellerManagementService.ApproveProfile(c.Request.Context(), adminID, id, service.ResellerApproveInput{
		DefaultMarkupPercent: defaultMarkup,
		MaxMarkupPercent:     maxMarkup,
	})
	if err != nil {
		respondAdminResellerManagementError(c, err)
		return
	}
	h.recordResellerAudit(c, "reseller_profile_approve", "/admin/resellers/profiles/:id/approve", gin.H{
		"profile_id":  id,
		"reseller_id": id,
		"next_status": models.ResellerProfileStatusActive,
	})
	var systemDomain any
	if result.SystemDomain != nil {
		row := dto.NewResellerDomainResp(result.SystemDomain)
		systemDomain = row
	}
	response.Success(c, gin.H{"profile": result.Profile, "system_domain": systemDomain})
}

// UpdateResellerProfile 更新分销商运营配置。
func (h *Handler) UpdateResellerProfile(c *gin.Context) {
	adminID, ok := shared.GetAdminID(c)
	if !ok {
		return
	}
	if h.ResellerManagementService == nil {
		shared.RespondError(c, response.CodeInternal, "error.save_failed", nil)
		return
	}
	id, err := shared.ParseParamUint(c, "id")
	if err != nil {
		shared.RespondError(c, response.CodeBadRequest, "error.bad_request", nil)
		return
	}
	var req ResellerProfileUpdateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		shared.RespondBindError(c, err)
		return
	}
	defaultMarkup, err := parseOptionalDecimal(req.DefaultMarkupPercent)
	if err != nil {
		shared.RespondError(c, response.CodeBadRequest, "error.bad_request", nil)
		return
	}
	maxMarkup, err := parseOptionalDecimal(req.MaxMarkupPercent)
	if err != nil {
		shared.RespondError(c, response.CodeBadRequest, "error.bad_request", nil)
		return
	}
	row, err := h.ResellerManagementService.UpdateProfileOperationalConfig(adminID, id, service.ResellerProfileUpdateInput{
		DefaultMarkupPercent: defaultMarkup,
		MaxMarkupPercent:     maxMarkup,
		SettlementStatus:     req.SettlementStatus,
		Reason:               req.Reason,
	})
	if err != nil {
		respondAdminResellerManagementError(c, err)
		return
	}
	h.recordResellerAudit(c, "reseller_profile_update", "/admin/resellers/profiles/:id", gin.H{
		"profile_id":             id,
		"reseller_id":            id,
		"default_markup_percent": row.DefaultMarkupPercent.String(),
		"max_markup_percent":     row.MaxMarkupPercent.String(),
		"settlement_status":      row.SettlementStatus,
		"reason":                 strings.TrimSpace(req.Reason),
	})
	response.Success(c, dto.NewAdminResellerProfileResp(row))
}

// AssignResellerSystemDomain 为分销商分配或编辑系统二级域名。
func (h *Handler) AssignResellerSystemDomain(c *gin.Context) {
	adminID, ok := shared.GetAdminID(c)
	if !ok {
		return
	}
	if h.ResellerManagementService == nil {
		shared.RespondError(c, response.CodeInternal, "error.save_failed", nil)
		return
	}
	id, err := shared.ParseParamUint(c, "id")
	if err != nil {
		shared.RespondError(c, response.CodeBadRequest, "error.bad_request", nil)
		return
	}
	var req ResellerSystemDomainRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		shared.RespondBindError(c, err)
		return
	}
	rawSubdomain := strings.TrimSpace(req.Subdomain)
	if rawSubdomain == "" {
		rawSubdomain = strings.TrimSpace(req.Domain)
	}
	row, err := h.ResellerManagementService.AssignSystemSubdomain(c.Request.Context(), adminID, id, service.ResellerSystemDomainInput{
		Subdomain: rawSubdomain,
	})
	if err != nil {
		respondAdminResellerManagementError(c, err)
		return
	}
	h.recordResellerAudit(c, "reseller_profile_system_domain_update", "/admin/resellers/profiles/:id/system-domain", gin.H{
		"profile_id":  id,
		"reseller_id": id,
		"domain_id":   row.ID,
		"domain":      row.Domain,
	})
	response.Success(c, dto.NewResellerDomainResp(row))
}

// RejectResellerProfile 拒绝分销商申请。
func (h *Handler) RejectResellerProfile(c *gin.Context) {
	h.handleResellerProfileReasonAction(c, "reseller_profile_reject", "/admin/resellers/profiles/:id/reject", h.ResellerManagementService.RejectProfile)
}

// DisableResellerProfile 禁用分销商资料。
func (h *Handler) DisableResellerProfile(c *gin.Context) {
	h.handleResellerProfileReasonAction(c, "reseller_profile_disable", "/admin/resellers/profiles/:id/disable", h.ResellerManagementService.DisableProfile)
}

// RestoreResellerProfile 恢复分销商资料。
func (h *Handler) RestoreResellerProfile(c *gin.Context) {
	adminID, ok := shared.GetAdminID(c)
	if !ok {
		return
	}
	if h.ResellerManagementService == nil {
		shared.RespondError(c, response.CodeInternal, "error.save_failed", nil)
		return
	}
	id, err := shared.ParseParamUint(c, "id")
	if err != nil {
		shared.RespondError(c, response.CodeBadRequest, "error.bad_request", nil)
		return
	}
	row, err := h.ResellerManagementService.RestoreProfile(adminID, id)
	if err != nil {
		respondAdminResellerManagementError(c, err)
		return
	}
	h.recordResellerAudit(c, "reseller_profile_restore", "/admin/resellers/profiles/:id/restore", gin.H{
		"profile_id":  id,
		"reseller_id": id,
		"next_status": row.Status,
	})
	response.Success(c, row)
}

// ApproveResellerDomain 审核通过自定义域名。
func (h *Handler) ApproveResellerDomain(c *gin.Context) {
	h.handleResellerDomainAction(c, "reseller_domain_approve", "/admin/resellers/domains/:id/approve", h.ResellerManagementService.ApproveDomain)
}

// DisableResellerDomain 禁用域名。
func (h *Handler) DisableResellerDomain(c *gin.Context) {
	h.handleResellerDomainAction(c, "reseller_domain_disable", "/admin/resellers/domains/:id/disable", h.ResellerManagementService.DisableDomain)
}

// SetPrimaryResellerDomain 将已启用且已验证域名设为主域名。
func (h *Handler) SetPrimaryResellerDomain(c *gin.Context) {
	h.handleResellerDomainAction(c, "reseller_domain_set_primary", "/admin/resellers/domains/:id/set-primary", h.ResellerManagementService.SetPrimaryDomain)
}

func (h *Handler) handleResellerProfileReasonAction(
	c *gin.Context,
	action string,
	object string,
	fn func(adminID, profileID uint, reason string) (*models.ResellerProfile, error),
) {
	adminID, ok := shared.GetAdminID(c)
	if !ok {
		return
	}
	if h.ResellerManagementService == nil || fn == nil {
		shared.RespondError(c, response.CodeInternal, "error.save_failed", nil)
		return
	}
	id, err := shared.ParseParamUint(c, "id")
	if err != nil {
		shared.RespondError(c, response.CodeBadRequest, "error.bad_request", nil)
		return
	}
	var req ResellerProfileReasonRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		shared.RespondBindError(c, err)
		return
	}
	row, err := fn(adminID, id, req.Reason)
	if err != nil {
		respondAdminResellerManagementError(c, err)
		return
	}
	h.recordResellerAudit(c, action, object, gin.H{
		"profile_id":  id,
		"reseller_id": id,
		"next_status": row.Status,
	})
	response.Success(c, row)
}

func (h *Handler) handleResellerDomainAction(
	c *gin.Context,
	action string,
	object string,
	fn func(ctx context.Context, adminID, domainID uint) (*models.ResellerDomain, error),
) {
	adminID, ok := shared.GetAdminID(c)
	if !ok {
		return
	}
	if h.ResellerManagementService == nil || fn == nil {
		shared.RespondError(c, response.CodeInternal, "error.save_failed", nil)
		return
	}
	id, err := shared.ParseParamUint(c, "id")
	if err != nil {
		shared.RespondError(c, response.CodeBadRequest, "error.bad_request", nil)
		return
	}
	row, err := fn(c.Request.Context(), adminID, id)
	if err != nil {
		respondAdminResellerManagementError(c, err)
		return
	}
	h.recordResellerAudit(c, action, object, gin.H{
		"domain_id":   id,
		"reseller_id": row.ResellerID,
		"domain":      row.Domain,
		"next_status": row.Status,
	})
	response.Success(c, dto.NewResellerDomainResp(row))
}

func (h *Handler) recordResellerAudit(c *gin.Context, action string, object string, detail gin.H) {
	method := "POST"
	if c.Request != nil && c.Request.Method != "" {
		method = c.Request.Method
	}
	h.recordAuthzAudit(c, service.AuthzAuditRecordInput{
		OperatorAdminID:  c.GetUint("admin_id"),
		OperatorUsername: c.GetString("username"),
		Action:           action,
		Object:           object,
		Method:           method,
		RequestID:        strings.TrimSpace(c.GetString("request_id")),
		Detail:           models.JSON(detail),
	})
}

func respondAdminResellerManagementError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, service.ErrNotFound):
		shared.RespondError(c, response.CodeNotFound, "error.bad_request", nil)
	case errors.Is(err, service.ErrResellerProfileStatusInvalid),
		errors.Is(err, service.ErrResellerDomainStatusInvalid),
		errors.Is(err, service.ErrResellerDomainInvalid),
		errors.Is(err, service.ErrResellerSiteConfigInvalid),
		errors.Is(err, service.ErrResellerDomainMainHostNotAllowed),
		errors.Is(err, service.ErrResellerDomainConflict):
		shared.RespondError(c, response.CodeBadRequest, "error.bad_request", nil)
	case errors.Is(err, service.ErrResellerSubdomainBaseMissing):
		shared.RespondError(c, response.CodeBadRequest, "error.reseller_subdomain_base_missing", nil)
	default:
		shared.RespondError(c, response.CodeInternal, "error.save_failed", err)
	}
}

func parseOptionalDecimal(raw string) (decimal.Decimal, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return decimal.Zero, nil
	}
	return decimal.NewFromString(raw)
}
