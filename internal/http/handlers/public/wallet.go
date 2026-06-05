package public

import (
	"errors"
	"strings"

	"github.com/dujiao-next/internal/constants"
	"github.com/dujiao-next/internal/dto"
	"github.com/dujiao-next/internal/http/handlers/shared"
	"github.com/dujiao-next/internal/http/response"
	"github.com/dujiao-next/internal/models"
	"github.com/dujiao-next/internal/repository"
	"github.com/dujiao-next/internal/service"

	"github.com/gin-gonic/gin"
	"github.com/shopspring/decimal"
)

// WalletRechargeRequest 用户充值请求
type WalletRechargeRequest struct {
	Amount    string `json:"amount" binding:"required"`
	ChannelID uint   `json:"channel_id" binding:"required"`
	Currency  string `json:"currency"`
	Remark    string `json:"remark"`
}

// WalletPaymentChannelsRequest 查询钱包充值可用支付渠道请求
type WalletPaymentChannelsRequest struct {
	Amount string `json:"amount" binding:"required"`
}

// GetMyWalletPaymentChannels 获取当前用户钱包充值可用支付渠道（按金额过滤）
func (h *Handler) GetMyWalletPaymentChannels(c *gin.Context) {
	uid, ok := shared.GetUserID(c)
	if !ok {
		return
	}

	var req WalletPaymentChannelsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		shared.RespondBindError(c, err)
		return
	}

	amount, err := decimal.NewFromString(strings.TrimSpace(req.Amount))
	if err != nil {
		shared.RespondError(c, response.CodeBadRequest, "error.bad_request", err)
		return
	}
	if amount.LessThanOrEqual(decimal.Zero) {
		shared.RespondError(c, response.CodeBadRequest, "error.bad_request", nil)
		return
	}

	user, _ := h.UserRepo.GetByID(uid)
	channels, err := h.PaymentService.GetAvailableChannels(service.AvailablePaymentChannelFilter{
		TargetAmount: &models.Money{Decimal: amount},
		User:         user,
		PaymentType:  constants.PaymentTypeWallet,
	})
	if err != nil {
		shared.RespondError(c, response.CodeInternal, "error.payment_fetch_failed", err)
		return
	}
	response.Success(c, channels)
}

// GetMyWallet 获取当前用户钱包信息
func (h *Handler) GetMyWallet(c *gin.Context) {
	uid, ok := shared.GetUserID(c)
	if !ok {
		return
	}
	account, err := h.WalletService.GetAccount(uid)
	if err != nil {
		shared.RespondError(c, response.CodeInternal, "error.user_fetch_failed", err)
		return
	}
	response.Success(c, dto.NewWalletAccountResp(account))
}

// GetMyWalletTransactions 获取当前用户钱包流水
func (h *Handler) GetMyWalletTransactions(c *gin.Context) {
	uid, ok := shared.GetUserID(c)
	if !ok {
		return
	}
	page, pageSize := shared.ParsePagination(c)

	transactions, total, err := h.WalletService.ListTransactions(repository.WalletTransactionListFilter{
		Page:     page,
		PageSize: pageSize,
		UserID:   uid,
	})
	if err != nil {
		shared.RespondError(c, response.CodeInternal, "error.user_fetch_failed", err)
		return
	}

	pagination := response.BuildPagination(page, pageSize, total)
	response.SuccessWithPage(c, dto.NewWalletTransactionRespList(transactions), pagination)
}

// RechargeWallet 用户充值钱包余额
func (h *Handler) RechargeWallet(c *gin.Context) {
	uid, ok := shared.GetUserID(c)
	if !ok {
		return
	}
	var req WalletRechargeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		shared.RespondBindError(c, err)
		return
	}
	amount, err := decimal.NewFromString(strings.TrimSpace(req.Amount))
	if err != nil {
		shared.RespondError(c, response.CodeBadRequest, "error.bad_request", err)
		return
	}
	currency := strings.TrimSpace(req.Currency)
	if currency == "" && h.SettingService != nil {
		siteCurrency, currencyErr := h.SettingService.GetSiteCurrency(constants.SiteCurrencyDefault)
		if currencyErr == nil {
			currency = siteCurrency
		}
	}
	result, err := h.PaymentService.CreateWalletRechargePayment(service.CreateWalletRechargePaymentInput{
		UserID:    uid,
		ChannelID: req.ChannelID,
		Amount:    models.NewMoneyFromDecimal(amount),
		Currency:  currency,
		Remark:    strings.TrimSpace(req.Remark),
		ClientIP:  c.ClientIP(),
		Context:   c.Request.Context(),
	})
	if err != nil {
		switch {
		case errors.Is(err, service.ErrWalletInvalidAmount):
			shared.RespondError(c, response.CodeBadRequest, "error.bad_request", nil)
		case errors.Is(err, service.ErrWalletNotSupportedForGuest):
			shared.RespondError(c, response.CodeBadRequest, "error.payment_invalid", nil)
		default:
			respondPaymentCreateError(c, err)
		}
		return
	}
	account, err := h.WalletService.GetAccount(uid)
	if err != nil {
		shared.RespondError(c, response.CodeInternal, "error.user_fetch_failed", err)
		return
	}
	response.Success(c, dto.NewWalletRechargePaymentPayload(result.Recharge, result.Payment, account))
}

// GetMyWalletRecharge 获取当前用户充值单详情
func (h *Handler) GetMyWalletRecharge(c *gin.Context) {
	uid, ok := shared.GetUserID(c)
	if !ok {
		return
	}
	rechargeNo := strings.TrimSpace(c.Param("recharge_no"))
	if rechargeNo == "" {
		shared.RespondError(c, response.CodeBadRequest, "error.bad_request", nil)
		return
	}
	recharge, err := h.WalletService.GetRechargeOrderByRechargeNo(uid, rechargeNo)
	if err != nil {
		switch {
		case errors.Is(err, service.ErrWalletRechargeNotFound):
			shared.RespondError(c, response.CodeNotFound, "error.payment_not_found", nil)
		default:
			shared.RespondError(c, response.CodeInternal, "error.payment_fetch_failed", err)
		}
		return
	}
	payment, err := h.PaymentService.GetPayment(recharge.PaymentID)
	if err != nil {
		respondPaymentCaptureError(c, err)
		return
	}
	account, err := h.WalletService.GetAccount(uid)
	if err != nil {
		shared.RespondError(c, response.CodeInternal, "error.user_fetch_failed", err)
		return
	}
	response.Success(c, dto.NewWalletRechargePaymentPayload(recharge, payment, account))
}

// ListMyWalletRecharges 获取当前用户充值订单列表
func (h *Handler) ListMyWalletRecharges(c *gin.Context) {
	uid, ok := shared.GetUserID(c)
	if !ok {
		return
	}
	page, pageSize := shared.ParsePagination(c)
	status := strings.TrimSpace(c.Query("status"))
	rechargeNo := strings.TrimSpace(c.Query("recharge_no"))

	orders, total, err := h.WalletService.ListUserRechargeOrders(uid, page, pageSize, status, rechargeNo)
	if err != nil {
		shared.RespondError(c, response.CodeInternal, "error.user_fetch_failed", err)
		return
	}
	pagination := response.BuildPagination(page, pageSize, total)
	response.SuccessWithPage(c, dto.NewWalletRechargeRespList(orders), pagination)
}

// MyWalletRechargeStats 按状态聚合当前用户充值单数量（基于全量数据，仅复用关键词筛选）
func (h *Handler) MyWalletRechargeStats(c *gin.Context) {
	uid, ok := shared.GetUserID(c)
	if !ok {
		return
	}
	rechargeNo := strings.TrimSpace(c.Query("recharge_no"))

	stats, err := h.WalletService.StatsUserRechargeOrders(uid, rechargeNo)
	if err != nil {
		shared.RespondError(c, response.CodeInternal, "error.user_fetch_failed", err)
		return
	}

	var total int64
	for _, v := range stats {
		total += v
	}
	response.Success(c, gin.H{
		"total":     total,
		"by_status": stats,
	})
}

// CaptureMyWalletRechargePayment 主动检查当前用户充值支付状态
func (h *Handler) CaptureMyWalletRechargePayment(c *gin.Context) {
	uid, ok := shared.GetUserID(c)
	if !ok {
		return
	}
	paymentID, err := shared.ParseParamUint(c, "id")
	if err != nil {
		shared.RespondError(c, response.CodeBadRequest, "error.payment_invalid", nil)
		return
	}
	recharge, err := h.WalletService.GetRechargeOrderByPaymentIDAndUser(paymentID, uid)
	if err != nil {
		switch {
		case errors.Is(err, service.ErrWalletRechargeNotFound):
			shared.RespondError(c, response.CodeNotFound, "error.payment_not_found", nil)
		default:
			shared.RespondError(c, response.CodeInternal, "error.payment_fetch_failed", err)
		}
		return
	}
	updatedPayment, err := h.PaymentService.CapturePayment(service.CapturePaymentInput{
		PaymentID: paymentID,
		Context:   c.Request.Context(),
	})
	if err != nil {
		// 部分渠道不支持主动捕获时，回退为返回当前支付状态，避免前端“检查支付状态”直接报错。
		if !errors.Is(err, service.ErrPaymentProviderNotSupported) {
			respondPaymentCaptureError(c, err)
			return
		}
		updatedPayment, err = h.PaymentService.GetPayment(paymentID)
		if err != nil {
			respondPaymentCaptureError(c, err)
			return
		}
	}
	updatedRecharge, err := h.WalletService.GetRechargeOrderByRechargeNo(uid, recharge.RechargeNo)
	if err != nil {
		shared.RespondError(c, response.CodeInternal, "error.payment_fetch_failed", err)
		return
	}
	account, err := h.WalletService.GetAccount(uid)
	if err != nil {
		shared.RespondError(c, response.CodeInternal, "error.user_fetch_failed", err)
		return
	}
	response.Success(c, dto.NewWalletRechargePaymentPayload(updatedRecharge, updatedPayment, account))
}
