package public

import (
	"errors"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/shopspring/decimal"

	"github.com/dujiao-next/internal/dto"
	"github.com/dujiao-next/internal/http/handlers/shared"
	"github.com/dujiao-next/internal/http/response"
	"github.com/dujiao-next/internal/service"
)

// resellerSiteConfigFieldErrorKey 将站点配置字段级校验错误映射为可读的 i18n key，
// 让分销商在保存失败时能明确知道是哪个字段、应如何修正。
func resellerSiteConfigFieldErrorKey(field string) string {
	switch field {
	case "support_telegram":
		return "error.reseller_support_telegram_invalid"
	case "support_whatsapp":
		return "error.reseller_support_whatsapp_invalid"
	case "support_email":
		return "error.reseller_support_email_invalid"
	case "support_url":
		return "error.reseller_support_url_invalid"
	case "image":
		return "error.reseller_image_invalid"
	case "link":
		return "error.reseller_link_invalid"
	default:
		return "error.bad_request"
	}
}

var userResellerFinanceErrorRules = []mappedHandlerError{
	{target: service.ErrResellerNotOpened, code: response.CodeBadRequest, key: "error.bad_request"},
	{target: service.ErrResellerProfileInactive, code: response.CodeBadRequest, key: "error.reseller_profile_inactive"},
	{target: service.ErrResellerSettlementUnavailable, code: response.CodeBadRequest, key: "error.reseller_settlement_unavailable"},
	{target: service.ErrResellerWithdrawAmountInvalid, code: response.CodeBadRequest, key: "error.reseller_withdraw_amount_invalid"},
	{target: service.ErrResellerWithdrawCurrencyUnavailable, code: response.CodeBadRequest, key: "error.reseller_withdraw_currency_unavailable"},
	{target: service.ErrResellerWithdrawInsufficient, code: response.CodeBadRequest, key: "error.reseller_withdraw_insufficient"},
	{target: service.ErrResellerBalanceAccountFrozen, code: response.CodeBadRequest, key: "error.reseller_balance_frozen"},
}

var userResellerManagementErrorRules = []mappedHandlerError{
	{target: service.ErrResellerNotOpened, code: response.CodeBadRequest, key: "error.bad_request"},
	{target: service.ErrResellerApplyDisabled, code: response.CodeForbidden, key: "error.forbidden"},
	{target: service.ErrResellerProfileInactive, code: response.CodeBadRequest, key: "error.forbidden"},
	{target: service.ErrResellerDomainInvalid, code: response.CodeBadRequest, key: "error.bad_request"},
	{target: service.ErrResellerDomainMainHostNotAllowed, code: response.CodeBadRequest, key: "error.bad_request"},
	{target: service.ErrResellerDomainConflict, code: response.CodeBadRequest, key: "error.bad_request"},
	{target: service.ErrResellerSiteConfigInvalid, code: response.CodeBadRequest, key: "error.bad_request"},
	{target: service.ErrProductSKUInvalid, code: response.CodeBadRequest, key: "error.order_item_invalid"},
	{target: service.ErrResellerPriceBelowBase, code: response.CodeBadRequest, key: "error.reseller_price_invalid"},
	{target: service.ErrResellerMarkupExceeded, code: response.CodeBadRequest, key: "error.reseller_markup_exceeded"},
	{target: service.ErrResellerPricingModeInvalid, code: response.CodeBadRequest, key: "error.reseller_price_invalid"},
}

var userResellerOrderErrorRules = []mappedHandlerError{
	{target: service.ErrResellerNotOpened, code: response.CodeBadRequest, key: "error.bad_request"},
	{target: service.ErrResellerProfileInactive, code: response.CodeBadRequest, key: "error.forbidden"},
	{target: service.ErrOrderNotFound, code: response.CodeNotFound, key: "error.order_not_found"},
}

func respondUserResellerFinanceError(c *gin.Context, err error, fallbackKey string) {
	respondWithMappedError(c, err, userResellerFinanceErrorRules, response.CodeInternal, fallbackKey)
}

func respondUserResellerManagementError(c *gin.Context, err error, fallbackKey string) {
	respondWithMappedError(c, err, userResellerManagementErrorRules, response.CodeInternal, fallbackKey)
}

func respondUserResellerOrderError(c *gin.Context, err error, fallbackKey string) {
	respondWithMappedError(c, err, userResellerOrderErrorRules, response.CodeInternal, fallbackKey)
}

type ResellerApplyRequest struct {
	Reason string `json:"reason"`
}

type ResellerCustomDomainRequest struct {
	Domain string `json:"domain" binding:"required"`
}

type ResellerSiteConfigRequest struct {
	SiteName     string                            `json:"site_name"`
	Logo         string                            `json:"logo"`
	Favicon      string                            `json:"favicon"`
	Announcement service.ResellerAnnouncementInput `json:"announcement"`
	Support      service.ResellerSupportInput      `json:"support"`
	SEO          service.ResellerSEOInput          `json:"seo"`
	FooterLinks  []service.ResellerFooterLinkInput `json:"footer_links"`
	NavConfig    service.ResellerNavConfigInput    `json:"nav_config"`
}

type ResellerProductSettingRequest struct {
	SKUID             uint   `json:"sku_id"`
	IsListed          bool   `json:"is_listed"`
	PricingMode       string `json:"pricing_mode"`
	MarkupPercent     string `json:"markup_percent"`
	FixedMarkupAmount string `json:"fixed_markup_amount"`
	FixedPriceAmount  string `json:"fixed_price_amount"`
	SortOrder         int    `json:"sort_order"`
}

type ResellerProductSettingsUpdateRequest struct {
	Settings []ResellerProductSettingRequest `json:"settings"`
}

func (req ResellerSiteConfigRequest) toServiceInput() service.ResellerSiteConfigInput {
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

func (req ResellerProductSettingsUpdateRequest) toServiceInput() (service.ResellerProductSettingSaveInput, error) {
	input := service.ResellerProductSettingSaveInput{Settings: make([]service.ResellerProductSettingInput, 0, len(req.Settings))}
	for _, item := range req.Settings {
		markup, err := parseResellerDecimalField(item.MarkupPercent)
		if err != nil {
			return input, err
		}
		fixedMarkup, err := parseResellerDecimalField(item.FixedMarkupAmount)
		if err != nil {
			return input, err
		}
		fixedPrice, err := parseResellerDecimalField(item.FixedPriceAmount)
		if err != nil {
			return input, err
		}
		input.Settings = append(input.Settings, service.ResellerProductSettingInput{
			SKUID:             item.SKUID,
			IsListed:          item.IsListed,
			PricingMode:       strings.TrimSpace(item.PricingMode),
			MarkupPercent:     markup,
			FixedMarkupAmount: fixedMarkup,
			FixedPriceAmount:  fixedPrice,
			SortOrder:         item.SortOrder,
		})
	}
	return input, nil
}

func parseResellerDecimalField(raw string) (decimal.Decimal, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return decimal.Zero, nil
	}
	return decimal.NewFromString(value)
}

// GetResellerManagementSnapshot 获取当前用户的分销商准入与域名状态。
func (h *Handler) GetResellerManagementSnapshot(c *gin.Context) {
	uid, ok := shared.GetUserID(c)
	if !ok {
		return
	}
	if h.ResellerManagementService == nil {
		shared.RespondError(c, response.CodeInternal, "error.user_fetch_failed", nil)
		return
	}
	profile, domains, canApply, err := h.ResellerManagementService.GetUserManagementSnapshot(uid)
	if err != nil {
		respondUserResellerManagementError(c, err, "error.user_fetch_failed")
		return
	}
	response.Success(c, dto.NewResellerManagementSnapshotResp(profile, domains, canApply))
}

// ApplyResellerProfile 提交当前用户的分销商申请。
func (h *Handler) ApplyResellerProfile(c *gin.Context) {
	uid, ok := shared.GetUserID(c)
	if !ok {
		return
	}
	if h.ResellerManagementService == nil {
		shared.RespondError(c, response.CodeInternal, "error.save_failed", nil)
		return
	}
	var req ResellerApplyRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		shared.RespondBindError(c, err)
		return
	}
	profile, err := h.ResellerManagementService.ApplyUserReseller(uid, service.ResellerApplyInput{Reason: req.Reason})
	if err != nil {
		respondUserResellerManagementError(c, err, "error.save_failed")
		return
	}
	response.Success(c, dto.NewResellerManagementProfileResp(profile))
}

// ListResellerDomains 查询当前用户的分销域名。
func (h *Handler) ListResellerDomains(c *gin.Context) {
	uid, ok := shared.GetUserID(c)
	if !ok {
		return
	}
	if h.ResellerManagementService == nil {
		shared.RespondError(c, response.CodeInternal, "error.user_fetch_failed", nil)
		return
	}
	profile, domains, _, err := h.ResellerManagementService.GetUserManagementSnapshot(uid)
	if err != nil {
		respondUserResellerManagementError(c, err, "error.user_fetch_failed")
		return
	}
	if profile == nil {
		respondUserResellerManagementError(c, service.ErrResellerNotOpened, "error.user_fetch_failed")
		return
	}
	response.Success(c, dto.NewResellerDomainRespList(domains))
}

// SubmitResellerCustomDomain 提交当前用户的自定义分销域名。
func (h *Handler) SubmitResellerCustomDomain(c *gin.Context) {
	uid, ok := shared.GetUserID(c)
	if !ok {
		return
	}
	if h.ResellerManagementService == nil {
		shared.RespondError(c, response.CodeInternal, "error.save_failed", nil)
		return
	}
	var req ResellerCustomDomainRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		shared.RespondBindError(c, err)
		return
	}
	row, err := h.ResellerManagementService.SubmitUserCustomDomain(uid, req.Domain)
	if err != nil {
		respondUserResellerManagementError(c, err, "error.save_failed")
		return
	}
	response.Success(c, dto.NewResellerDomainResp(row))
}

// GetResellerSiteConfig 获取当前用户的分销站点配置。
func (h *Handler) GetResellerSiteConfig(c *gin.Context) {
	uid, ok := shared.GetUserID(c)
	if !ok {
		return
	}
	if h.ResellerSiteConfigService == nil {
		shared.RespondError(c, response.CodeInternal, "error.user_fetch_failed", nil)
		return
	}
	profile, row, canEdit, err := h.ResellerSiteConfigService.GetUserSiteConfig(uid)
	if err != nil {
		respondUserResellerManagementError(c, err, "error.user_fetch_failed")
		return
	}
	response.Success(c, dto.NewResellerSiteConfigSnapshotResp(profile, row, canEdit))
}

// UpdateResellerSiteConfig 更新当前用户的分销站点配置。
func (h *Handler) UpdateResellerSiteConfig(c *gin.Context) {
	uid, ok := shared.GetUserID(c)
	if !ok {
		return
	}
	if h.ResellerSiteConfigService == nil {
		shared.RespondError(c, response.CodeInternal, "error.save_failed", nil)
		return
	}
	var req ResellerSiteConfigRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		shared.RespondBindError(c, err)
		return
	}
	row, err := h.ResellerSiteConfigService.UpdateUserSiteConfig(c.Request.Context(), uid, req.toServiceInput())
	if err != nil {
		var fieldErr *service.ResellerSiteConfigFieldError
		if errors.As(err, &fieldErr) {
			shared.RespondError(c, response.CodeBadRequest, resellerSiteConfigFieldErrorKey(fieldErr.Field), nil)
			return
		}
		respondUserResellerManagementError(c, err, "error.save_failed")
		return
	}
	response.Success(c, dto.NewResellerSiteConfigResp(row))
}

// UploadResellerImage 分销商上传站点图片（Logo / 图标 / 分享图 / 公告插图）。
// 仅允许已开通且可编辑站点配置的分销商上传，复用统一的上传校验与存储逻辑。
func (h *Handler) UploadResellerImage(c *gin.Context) {
	uid, ok := shared.GetUserID(c)
	if !ok {
		return
	}
	if h.UploadService == nil || h.ResellerSiteConfigService == nil {
		shared.RespondError(c, response.CodeInternal, "error.upload_failed", nil)
		return
	}
	// 鉴权：必须是已开通且站点配置可编辑的分销商
	_, _, canEdit, err := h.ResellerSiteConfigService.GetUserSiteConfig(uid)
	if err != nil {
		respondUserResellerManagementError(c, err, "error.forbidden")
		return
	}
	if !canEdit {
		shared.RespondError(c, response.CodeForbidden, "error.forbidden", nil)
		return
	}

	file, err := c.FormFile("file")
	if err != nil {
		shared.RespondError(c, response.CodeBadRequest, "error.file_missing", nil)
		return
	}
	result, err := h.UploadService.SaveFileWithMeta(file, "reseller")
	if err != nil {
		if service.IsUploadValidationError(err) {
			shared.RespondErrorWithMsg(c, response.CodeBadRequest, err.Error(), nil)
			return
		}
		shared.RespondError(c, response.CodeInternal, "error.upload_failed", err)
		return
	}
	response.Success(c, gin.H{
		"url":      result.URL,
		"filename": result.Filename,
		"size":     result.Size,
	})
}

// ListResellerProductSettings 查询当前用户可配置的分销商品。
func (h *Handler) ListResellerProductSettings(c *gin.Context) {
	uid, ok := shared.GetUserID(c)
	if !ok {
		return
	}
	if h.ResellerProductSettingService == nil {
		shared.RespondError(c, response.CodeInternal, "error.user_fetch_failed", nil)
		return
	}
	page, pageSize := shared.ParsePagination(c)
	categoryID, _ := shared.ParseQueryUint(c.Query("category_id"), false)
	rows, total, err := h.ResellerProductSettingService.ListUserProductSettings(uid, service.ResellerProductSettingUserListInput{
		Page:       page,
		PageSize:   pageSize,
		Keyword:    strings.TrimSpace(c.Query("keyword")),
		CategoryID: categoryID,
		Configured: strings.TrimSpace(c.Query("configured")),
		Listed:     strings.TrimSpace(c.Query("listed")),
	})
	if err != nil {
		respondUserResellerManagementError(c, err, "error.user_fetch_failed")
		return
	}
	response.SuccessWithPage(c, dto.NewResellerProductSettingListResp(resellerProductSettingDTOInputList(rows)), response.BuildPagination(page, pageSize, total))
}

// GetResellerProductSetting 获取当前用户的单个商品分销配置详情。
func (h *Handler) GetResellerProductSetting(c *gin.Context) {
	uid, ok := shared.GetUserID(c)
	if !ok {
		return
	}
	if h.ResellerProductSettingService == nil {
		shared.RespondError(c, response.CodeInternal, "error.user_fetch_failed", nil)
		return
	}
	productID, err := shared.ParseParamUint(c, "product_id")
	if err != nil {
		shared.RespondError(c, response.CodeBadRequest, "error.bad_request", err)
		return
	}
	detail, err := h.ResellerProductSettingService.GetUserProductSetting(uid, productID)
	if err != nil {
		respondUserResellerManagementError(c, err, "error.user_fetch_failed")
		return
	}
	response.Success(c, dto.NewResellerProductSettingDetailResp(resellerProductSettingDTOInputFromDetail(detail)))
}

// UpdateResellerProductSettings 保存当前用户的商品级或 SKU 级分销配置。
func (h *Handler) UpdateResellerProductSettings(c *gin.Context) {
	uid, ok := shared.GetUserID(c)
	if !ok {
		return
	}
	if h.ResellerProductSettingService == nil {
		shared.RespondError(c, response.CodeInternal, "error.save_failed", nil)
		return
	}
	productID, err := shared.ParseParamUint(c, "product_id")
	if err != nil {
		shared.RespondError(c, response.CodeBadRequest, "error.bad_request", err)
		return
	}
	var req ResellerProductSettingsUpdateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		shared.RespondBindError(c, err)
		return
	}
	input, err := req.toServiceInput()
	if err != nil {
		shared.RespondError(c, response.CodeBadRequest, "error.bad_request", err)
		return
	}
	detail, err := h.ResellerProductSettingService.SaveUserProductSettings(uid, productID, input)
	if err != nil {
		respondUserResellerManagementError(c, err, "error.save_failed")
		return
	}
	response.Success(c, dto.NewResellerProductSettingDetailResp(resellerProductSettingDTOInputFromDetail(detail)))
}

// ResetResellerProductSetting 删除当前用户的商品级或 SKU 级分销配置。
func (h *Handler) ResetResellerProductSetting(c *gin.Context) {
	uid, ok := shared.GetUserID(c)
	if !ok {
		return
	}
	if h.ResellerProductSettingService == nil {
		shared.RespondError(c, response.CodeInternal, "error.save_failed", nil)
		return
	}
	productID, err := shared.ParseParamUint(c, "product_id")
	if err != nil {
		shared.RespondError(c, response.CodeBadRequest, "error.bad_request", err)
		return
	}
	skuID, err := shared.ParseQueryUint(c.Query("sku_id"), false)
	if err != nil {
		shared.RespondError(c, response.CodeBadRequest, "error.bad_request", err)
		return
	}
	if err := h.ResellerProductSettingService.ResetUserProductSetting(uid, productID, skuID); err != nil {
		respondUserResellerManagementError(c, err, "error.save_failed")
		return
	}
	response.Success(c, gin.H{"deleted": true})
}

// ListResellerOrders 查询当前分销商视角的销售订单。
func (h *Handler) ListResellerOrders(c *gin.Context) {
	uid, ok := shared.GetUserID(c)
	if !ok {
		return
	}
	if h.ResellerOrderService == nil {
		shared.RespondError(c, response.CodeInternal, "error.order_fetch_failed", nil)
		return
	}
	page, pageSize := shared.ParsePagination(c)
	input, err := resellerOrderListInputFromQuery(c, page, pageSize)
	if err != nil {
		shared.RespondError(c, response.CodeBadRequest, "error.bad_request", err)
		return
	}
	rows, total, err := h.ResellerOrderService.ListUserOrders(uid, input)
	if err != nil {
		respondUserResellerOrderError(c, err, "error.order_fetch_failed")
		return
	}
	response.SuccessWithPage(c, dto.NewResellerOrderRespList(rows), response.BuildPagination(page, pageSize, total))
}

// GetResellerOrderDetail 获取当前分销商视角的销售订单详情。
func (h *Handler) GetResellerOrderDetail(c *gin.Context) {
	uid, ok := shared.GetUserID(c)
	if !ok {
		return
	}
	if h.ResellerOrderService == nil {
		shared.RespondError(c, response.CodeInternal, "error.order_fetch_failed", nil)
		return
	}
	orderNo := strings.TrimSpace(c.Param("order_no"))
	if orderNo == "" {
		shared.RespondError(c, response.CodeBadRequest, "error.bad_request", nil)
		return
	}
	detail, err := h.ResellerOrderService.GetUserOrderDetail(uid, orderNo)
	if err != nil {
		respondUserResellerOrderError(c, err, "error.order_fetch_failed")
		return
	}
	response.Success(c, dto.NewResellerOrderDetailResp(detail))
}

// GetResellerOrderStats 获取当前分销商视角的销售订单统计。
func (h *Handler) GetResellerOrderStats(c *gin.Context) {
	uid, ok := shared.GetUserID(c)
	if !ok {
		return
	}
	if h.ResellerOrderService == nil {
		shared.RespondError(c, response.CodeInternal, "error.order_fetch_failed", nil)
		return
	}
	input, err := resellerOrderListInputFromQuery(c, 1, 0)
	if err != nil {
		shared.RespondError(c, response.CodeBadRequest, "error.bad_request", err)
		return
	}
	stats, err := h.ResellerOrderService.StatsUserOrders(uid, input)
	if err != nil {
		respondUserResellerOrderError(c, err, "error.order_fetch_failed")
		return
	}
	response.Success(c, dto.NewResellerOrderStatsResp(stats))
}

func resellerOrderListInputFromQuery(c *gin.Context, page, pageSize int) (service.ResellerOrderListInput, error) {
	createdFrom, err := parseResellerOrderTimeQuery(c.Query("created_from"), false)
	if err != nil {
		return service.ResellerOrderListInput{}, err
	}
	createdTo, err := parseResellerOrderTimeQuery(c.Query("created_to"), true)
	if err != nil {
		return service.ResellerOrderListInput{}, err
	}
	paidFrom, err := parseResellerOrderTimeQuery(c.Query("paid_from"), false)
	if err != nil {
		return service.ResellerOrderListInput{}, err
	}
	paidTo, err := parseResellerOrderTimeQuery(c.Query("paid_to"), true)
	if err != nil {
		return service.ResellerOrderListInput{}, err
	}
	return service.ResellerOrderListInput{
		Page:        page,
		PageSize:    pageSize,
		Status:      strings.TrimSpace(c.Query("status")),
		OrderNo:     strings.TrimSpace(c.Query("order_no")),
		CreatedFrom: createdFrom,
		CreatedTo:   createdTo,
		PaidFrom:    paidFrom,
		PaidTo:      paidTo,
	}, nil
}

func parseResellerOrderTimeQuery(raw string, endOfDay bool) (*time.Time, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return nil, nil
	}
	if parsed, err := time.Parse(time.RFC3339, value); err == nil {
		return &parsed, nil
	}
	parsed, err := time.ParseInLocation("2006-01-02", value, time.Local)
	if err != nil {
		return nil, err
	}
	if endOfDay {
		parsed = parsed.Add(24*time.Hour - time.Nanosecond)
	}
	return &parsed, nil
}

func resellerProductSettingDTOInputFromDetail(detail *service.ResellerProductSettingDetail) dto.ResellerProductSettingDTOInput {
	if detail == nil {
		return dto.ResellerProductSettingDTOInput{}
	}
	return dto.ResellerProductSettingDTOInput{
		Product:          detail.Product,
		Settings:         detail.Settings,
		EffectiveBySKUID: resellerDecimalMapToStringMap(detail.EffectiveBySKUID),
		RuleBySKUID:      detail.RuleBySKUID,
	}
}

func resellerProductSettingDTOInputList(rows []service.ResellerProductSettingListRow) []dto.ResellerProductSettingDTOInput {
	out := make([]dto.ResellerProductSettingDTOInput, 0, len(rows))
	for i := range rows {
		out = append(out, dto.ResellerProductSettingDTOInput{
			Product:          rows[i].Product,
			Settings:         rows[i].Settings,
			EffectiveBySKUID: resellerDecimalMapToStringMap(rows[i].EffectiveBySKUID),
			RuleBySKUID:      rows[i].RuleBySKUID,
		})
	}
	return out
}

func resellerDecimalMapToStringMap(input map[uint]decimal.Decimal) map[uint]string {
	out := make(map[uint]string, len(input))
	for key, value := range input {
		out[key] = value.StringFixed(2)
	}
	return out
}

// GetResellerDashboard 获取当前用户的分销商财务看板。
func (h *Handler) GetResellerDashboard(c *gin.Context) {
	uid, ok := shared.GetUserID(c)
	if !ok {
		return
	}
	if h.ResellerAccountingService == nil {
		shared.RespondError(c, response.CodeInternal, "error.user_fetch_failed", nil)
		return
	}
	data, err := h.ResellerAccountingService.GetUserFinanceDashboard(uid)
	if err != nil {
		shared.RespondError(c, response.CodeInternal, "error.user_fetch_failed", err)
		return
	}
	response.Success(c, dto.NewResellerDashboardResp(data.Opened, data.Profile, data.Balances, data.WithdrawEnabled, data.WithdrawDisabledReason))
}

// ListResellerBalanceAccounts 查询当前用户的分销余额账户。
func (h *Handler) ListResellerBalanceAccounts(c *gin.Context) {
	uid, ok := shared.GetUserID(c)
	if !ok {
		return
	}
	if h.ResellerAccountingService == nil {
		shared.RespondError(c, response.CodeInternal, "error.user_fetch_failed", nil)
		return
	}
	page, pageSize := shared.ParsePagination(c)
	rows, total, err := h.ResellerAccountingService.ListUserBalanceAccounts(uid, service.ResellerUserBalanceAccountListFilter{
		Page:     page,
		PageSize: pageSize,
		Status:   strings.TrimSpace(c.Query("status")),
	})
	if err != nil {
		respondUserResellerFinanceError(c, err, "error.user_fetch_failed")
		return
	}
	response.SuccessWithPage(c, dto.NewResellerBalanceRespList(rows), response.BuildPagination(page, pageSize, total))
}

// ListResellerLedgerEntries 查询当前用户的分销账务流水。
func (h *Handler) ListResellerLedgerEntries(c *gin.Context) {
	uid, ok := shared.GetUserID(c)
	if !ok {
		return
	}
	if h.ResellerAccountingService == nil {
		shared.RespondError(c, response.CodeInternal, "error.user_fetch_failed", nil)
		return
	}
	page, pageSize := shared.ParsePagination(c)
	orderID, err := shared.ParseQueryUint(c.Query("order_id"), false)
	if err != nil {
		shared.RespondError(c, response.CodeBadRequest, "error.bad_request", nil)
		return
	}
	rows, total, err := h.ResellerAccountingService.ListUserLedgerEntries(uid, service.ResellerUserLedgerListFilter{
		Page:     page,
		PageSize: pageSize,
		Type:     strings.TrimSpace(c.Query("type")),
		Status:   strings.TrimSpace(c.Query("status")),
		OrderID:  orderID,
	})
	if err != nil {
		respondUserResellerFinanceError(c, err, "error.user_fetch_failed")
		return
	}
	response.SuccessWithPage(c, dto.NewResellerLedgerRespList(rows), response.BuildPagination(page, pageSize, total))
}

// ListResellerWithdraws 查询当前用户的分销提现申请。
func (h *Handler) ListResellerWithdraws(c *gin.Context) {
	uid, ok := shared.GetUserID(c)
	if !ok {
		return
	}
	if h.ResellerAccountingService == nil {
		shared.RespondError(c, response.CodeInternal, "error.user_fetch_failed", nil)
		return
	}
	page, pageSize := shared.ParsePagination(c)
	rows, total, err := h.ResellerAccountingService.ListUserWithdrawRequests(uid, service.ResellerUserWithdrawListFilter{
		Page:     page,
		PageSize: pageSize,
		Status:   strings.TrimSpace(c.Query("status")),
	})
	if err != nil {
		respondUserResellerFinanceError(c, err, "error.user_fetch_failed")
		return
	}
	response.SuccessWithPage(c, dto.NewResellerWithdrawRespList(rows), response.BuildPagination(page, pageSize, total))
}

// ResellerWithdrawApplyRequest 分销商提现申请请求。
type ResellerWithdrawApplyRequest struct {
	Amount   string `json:"amount" binding:"required"`
	Currency string `json:"currency" binding:"required"`
	Channel  string `json:"channel" binding:"required"`
	Account  string `json:"account" binding:"required"`
}

// ApplyResellerWithdraw 提交当前用户的分销提现申请。
func (h *Handler) ApplyResellerWithdraw(c *gin.Context) {
	uid, ok := shared.GetUserID(c)
	if !ok {
		return
	}
	if h.ResellerAccountingService == nil {
		shared.RespondError(c, response.CodeInternal, "error.save_failed", nil)
		return
	}

	var req ResellerWithdrawApplyRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		shared.RespondBindError(c, err)
		return
	}
	amount, err := decimal.NewFromString(strings.TrimSpace(req.Amount))
	if err != nil {
		shared.RespondError(c, response.CodeBadRequest, "error.bad_request", nil)
		return
	}
	row, err := h.ResellerAccountingService.ApplyUserWithdraw(uid, service.ResellerWithdrawApplyInput{
		Amount:   amount,
		Currency: strings.TrimSpace(req.Currency),
		Channel:  strings.TrimSpace(req.Channel),
		Account:  strings.TrimSpace(req.Account),
	})
	if err != nil {
		respondUserResellerFinanceError(c, err, "error.save_failed")
		return
	}
	response.Success(c, dto.NewResellerWithdrawResp(row))
}
