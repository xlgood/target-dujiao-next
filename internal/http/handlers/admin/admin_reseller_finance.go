package admin

import (
	"errors"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/dujiao-next/internal/http/handlers/shared"
	"github.com/dujiao-next/internal/http/response"
	"github.com/dujiao-next/internal/service"
)

// ResellerReviewWithdrawRequest 分销提现审核请求。
type ResellerReviewWithdrawRequest struct {
	Reason string `json:"reason"`
}

// ListResellerLedgerEntries 管理端分销账务流水列表。
func (h *Handler) ListResellerLedgerEntries(c *gin.Context) {
	if h.ResellerAccountingService == nil {
		shared.RespondError(c, response.CodeInternal, "error.user_fetch_failed", nil)
		return
	}
	page, pageSize := shared.ParsePagination(c)
	resellerID, _ := shared.ParseQueryUint(c.Query("reseller_id"), false)
	userID, _ := shared.ParseQueryUint(c.Query("user_id"), false)
	orderID, _ := shared.ParseQueryUint(c.Query("order_id"), false)
	createdFrom := parseAdminResellerTimePointer(c.Query("created_from"))
	createdTo := parseAdminResellerTimePointer(c.Query("created_to"))

	rows, total, err := h.ResellerAccountingService.ListAdminLedgerEntries(service.ResellerAdminLedgerListFilter{
		Page:        page,
		PageSize:    pageSize,
		ResellerID:  resellerID,
		UserID:      userID,
		Keyword:     strings.TrimSpace(c.Query("keyword")),
		Type:        strings.TrimSpace(c.Query("type")),
		Status:      strings.TrimSpace(c.Query("status")),
		OrderID:     orderID,
		OrderNo:     strings.TrimSpace(c.Query("order_no")),
		CreatedFrom: createdFrom,
		CreatedTo:   createdTo,
	})
	if err != nil {
		shared.RespondError(c, response.CodeInternal, "error.user_fetch_failed", err)
		return
	}
	response.SuccessWithPage(c, rows, response.BuildPagination(page, pageSize, total))
}

// ListResellerBalanceAccounts 管理端分销余额账户列表。
func (h *Handler) ListResellerBalanceAccounts(c *gin.Context) {
	if h.ResellerAccountingService == nil {
		shared.RespondError(c, response.CodeInternal, "error.user_fetch_failed", nil)
		return
	}
	page, pageSize := shared.ParsePagination(c)
	resellerID, _ := shared.ParseQueryUint(c.Query("reseller_id"), false)
	userID, _ := shared.ParseQueryUint(c.Query("user_id"), false)

	rows, total, err := h.ResellerAccountingService.ListAdminBalanceAccounts(service.ResellerAdminBalanceAccountListFilter{
		Page:       page,
		PageSize:   pageSize,
		ResellerID: resellerID,
		UserID:     userID,
		Keyword:    strings.TrimSpace(c.Query("keyword")),
		Status:     strings.TrimSpace(c.Query("status")),
	})
	if err != nil {
		shared.RespondError(c, response.CodeInternal, "error.user_fetch_failed", err)
		return
	}
	response.SuccessWithPage(c, rows, response.BuildPagination(page, pageSize, total))
}

// ListResellerWithdraws 管理端分销提现申请列表。
func (h *Handler) ListResellerWithdraws(c *gin.Context) {
	if h.ResellerAccountingService == nil {
		shared.RespondError(c, response.CodeInternal, "error.user_fetch_failed", nil)
		return
	}
	page, pageSize := shared.ParsePagination(c)
	resellerID, _ := shared.ParseQueryUint(c.Query("reseller_id"), false)
	userID, _ := shared.ParseQueryUint(c.Query("user_id"), false)
	createdFrom := parseAdminResellerTimePointer(c.Query("created_from"))
	createdTo := parseAdminResellerTimePointer(c.Query("created_to"))

	rows, total, err := h.ResellerAccountingService.ListAdminWithdrawRequests(service.ResellerAdminWithdrawListFilter{
		Page:        page,
		PageSize:    pageSize,
		ResellerID:  resellerID,
		UserID:      userID,
		Keyword:     strings.TrimSpace(c.Query("keyword")),
		Status:      strings.TrimSpace(c.Query("status")),
		CreatedFrom: createdFrom,
		CreatedTo:   createdTo,
	})
	if err != nil {
		shared.RespondError(c, response.CodeInternal, "error.user_fetch_failed", err)
		return
	}
	response.SuccessWithPage(c, rows, response.BuildPagination(page, pageSize, total))
}

// RejectResellerWithdraw 拒绝分销提现申请。
func (h *Handler) RejectResellerWithdraw(c *gin.Context) {
	adminID, ok := shared.GetAdminID(c)
	if !ok {
		return
	}
	if h.ResellerAccountingService == nil {
		shared.RespondError(c, response.CodeInternal, "error.save_failed", nil)
		return
	}
	id, err := shared.ParseParamUint(c, "id")
	if err != nil {
		shared.RespondError(c, response.CodeBadRequest, "error.bad_request", nil)
		return
	}

	var req ResellerReviewWithdrawRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		shared.RespondBindError(c, err)
		return
	}
	row, err := h.ResellerAccountingService.ReviewWithdraw(adminID, id, "reject", req.Reason)
	if err != nil {
		respondResellerWithdrawReviewError(c, err)
		return
	}
	response.Success(c, row)
}

// PayResellerWithdraw 标记分销提现已打款。
func (h *Handler) PayResellerWithdraw(c *gin.Context) {
	adminID, ok := shared.GetAdminID(c)
	if !ok {
		return
	}
	if h.ResellerAccountingService == nil {
		shared.RespondError(c, response.CodeInternal, "error.save_failed", nil)
		return
	}
	id, err := shared.ParseParamUint(c, "id")
	if err != nil {
		shared.RespondError(c, response.CodeBadRequest, "error.bad_request", nil)
		return
	}
	row, err := h.ResellerAccountingService.ReviewWithdraw(adminID, id, "pay", "")
	if err != nil {
		respondResellerWithdrawReviewError(c, err)
		return
	}
	response.Success(c, row)
}

func respondResellerWithdrawReviewError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, service.ErrNotFound):
		shared.RespondError(c, response.CodeNotFound, "error.bad_request", nil)
	case errors.Is(err, service.ErrResellerWithdrawStatusInvalid):
		shared.RespondError(c, response.CodeBadRequest, "error.bad_request", nil)
	default:
		shared.RespondError(c, response.CodeInternal, "error.save_failed", err)
	}
}

func parseAdminResellerTimePointer(raw string) *time.Time {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	if t, err := time.Parse(time.RFC3339, raw); err == nil {
		return &t
	}
	return nil
}
