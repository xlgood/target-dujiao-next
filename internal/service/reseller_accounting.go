package service

import (
	"fmt"
	"strings"
	"time"

	"github.com/dujiao-next/internal/logger"
	"github.com/dujiao-next/internal/models"
	"github.com/dujiao-next/internal/repository"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

const (
	resellerWithdrawActionReject = "reject"
	resellerWithdrawActionPay    = "pay"
)

type ResellerAccountingOptions struct {
	ConfirmDays int
}

type ResellerAccountingService struct {
	repo        repository.ResellerRepository
	confirmDays int
}

type ResellerAdminLedgerListFilter struct {
	Page        int
	PageSize    int
	ResellerID  uint
	UserID      uint
	Keyword     string
	Currency    string
	Type        string
	Status      string
	OrderID     uint
	OrderNo     string
	CreatedFrom *time.Time
	CreatedTo   *time.Time
}

type ResellerAdminBalanceAccountListFilter struct {
	Page       int
	PageSize   int
	ResellerID uint
	UserID     uint
	Keyword    string
	Currency   string
	Status     string
}

type ResellerAdminWithdrawListFilter struct {
	Page        int
	PageSize    int
	ResellerID  uint
	UserID      uint
	Keyword     string
	Currency    string
	Status      string
	CreatedFrom *time.Time
	CreatedTo   *time.Time
}

func NewResellerAccountingService(repo repository.ResellerRepository, opts ResellerAccountingOptions) *ResellerAccountingService {
	days := opts.ConfirmDays
	if days < 0 {
		days = 0
	}
	return &ResellerAccountingService{repo: repo, confirmDays: days}
}

func (s *ResellerAccountingService) ListAdminLedgerEntries(filter ResellerAdminLedgerListFilter) ([]models.ResellerLedgerEntry, int64, error) {
	if s == nil || s.repo == nil {
		return []models.ResellerLedgerEntry{}, 0, nil
	}
	return s.repo.ListAdminResellerLedgerEntries(repository.ResellerAdminLedgerListFilter{
		Page:        filter.Page,
		PageSize:    filter.PageSize,
		ResellerID:  filter.ResellerID,
		UserID:      filter.UserID,
		Keyword:     strings.TrimSpace(filter.Keyword),
		Currency:    strings.TrimSpace(filter.Currency),
		Type:        strings.TrimSpace(filter.Type),
		Status:      strings.TrimSpace(filter.Status),
		OrderID:     filter.OrderID,
		OrderNo:     strings.TrimSpace(filter.OrderNo),
		CreatedFrom: filter.CreatedFrom,
		CreatedTo:   filter.CreatedTo,
	})
}

func (s *ResellerAccountingService) ListAdminBalanceAccounts(filter ResellerAdminBalanceAccountListFilter) ([]models.ResellerBalanceAccount, int64, error) {
	if s == nil || s.repo == nil {
		return []models.ResellerBalanceAccount{}, 0, nil
	}
	return s.repo.ListAdminResellerBalanceAccounts(repository.ResellerAdminBalanceAccountListFilter{
		Page:       filter.Page,
		PageSize:   filter.PageSize,
		ResellerID: filter.ResellerID,
		UserID:     filter.UserID,
		Keyword:    strings.TrimSpace(filter.Keyword),
		Currency:   strings.TrimSpace(filter.Currency),
		Status:     strings.TrimSpace(filter.Status),
	})
}

func (s *ResellerAccountingService) ListAdminWithdrawRequests(filter ResellerAdminWithdrawListFilter) ([]models.ResellerWithdrawRequest, int64, error) {
	if s == nil || s.repo == nil {
		return []models.ResellerWithdrawRequest{}, 0, nil
	}
	return s.repo.ListAdminResellerWithdrawRequests(repository.ResellerAdminWithdrawListFilter{
		Page:        filter.Page,
		PageSize:    filter.PageSize,
		ResellerID:  filter.ResellerID,
		UserID:      filter.UserID,
		Keyword:     strings.TrimSpace(filter.Keyword),
		Currency:    strings.TrimSpace(filter.Currency),
		Status:      strings.TrimSpace(filter.Status),
		CreatedFrom: filter.CreatedFrom,
		CreatedTo:   filter.CreatedTo,
	})
}

func (s *ResellerAccountingService) PostOrderProfitTx(tx *gorm.DB, order *models.Order, payment *models.Payment) error {
	if s == nil || s.repo == nil || tx == nil || order == nil || order.ID == 0 {
		return nil
	}
	if order.ResellerID == nil || *order.ResellerID == 0 {
		return nil
	}
	repoTx := s.repo.WithTx(tx)
	snapshot, err := repoTx.GetOrderSnapshotByOrderID(order.ID)
	if err != nil {
		return err
	}
	if snapshot == nil {
		logger.Warnw("reseller_accounting_missing_snapshot_skip", "order_id", order.ID, "order_no", order.OrderNo)
		return nil
	}
	if !snapshot.ProfitEligible {
		return nil
	}
	profit := snapshot.ProfitAmount.Decimal.Round(2)
	if profit.LessThanOrEqual(decimal.Zero) {
		return nil
	}
	now := time.Now()
	availableAt := now.AddDate(0, 0, s.confirmDays)
	orderID := order.ID
	metadata := models.JSON{
		"order_no":            order.OrderNo,
		"reseller_domain":     snapshot.Domain,
		"wallet_paid_amount":  order.WalletPaidAmount.String(),
		"online_paid_amount":  order.OnlinePaidAmount.String(),
		"snapshot_id":         snapshot.ID,
		"profit_block_reason": snapshot.ProfitBlockReason,
	}
	if payment != nil {
		metadata["payment_id"] = payment.ID
		metadata["payment_channel_id"] = payment.ChannelID
		metadata["payment_amount"] = payment.Amount.String()
		metadata["payment_status"] = payment.Status
	}
	entry := &models.ResellerLedgerEntry{
		ResellerID:     snapshot.ResellerID,
		OrderID:        &orderID,
		Type:           models.ResellerLedgerTypeOrderProfit,
		Amount:         models.NewMoneyFromDecimal(profit),
		Currency:       strings.TrimSpace(snapshot.Currency),
		IdempotencyKey: fmt.Sprintf("order_profit:%d", order.ID),
		MetadataJSON:   metadata,
		Status:         models.ResellerLedgerStatusPendingConfirm,
		AvailableAt:    &availableAt,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if entry.Currency == "" {
		entry.Currency = strings.TrimSpace(order.Currency)
	}
	if entry.Currency == "" {
		return ErrResellerLedgerInvalidSnapshot
	}
	created, err := repoTx.CreateLedgerEntryIfNotExists(entry)
	if err != nil {
		return err
	}
	if !created {
		return nil
	}
	return s.refreshBalanceAccountTx(repoTx, snapshot.ResellerID, entry.Currency, now)
}

func (s *ResellerAccountingService) ConfirmDueLedgerEntries(now time.Time) (int64, error) {
	if s == nil || s.repo == nil {
		return 0, nil
	}
	return s.repo.MarkDueLedgerEntriesAvailable(now)
}

type resellerRefundAllocationItem struct {
	OrderItemID          string `json:"order_item_id"`
	RefundRatio          string `json:"refund_ratio"`
	OriginalProfitAmount string `json:"original_profit_amount"`
	DeductAmount         string `json:"deduct_amount"`
}

type resellerRefundAllocation struct {
	RefundRecordID uint                           `json:"refund_record_id"`
	OrderID        uint                           `json:"order_id"`
	RefundAmount   string                         `json:"refund_amount"`
	OrderAmount    string                         `json:"order_amount"`
	Items          []resellerRefundAllocationItem `json:"items"`
}

func decimalFromSnapshotValue(v interface{}) decimal.Decimal {
	switch val := v.(type) {
	case string:
		d, err := decimal.NewFromString(strings.TrimSpace(val))
		if err == nil {
			return d.Round(2)
		}
	case float64:
		return decimal.NewFromFloat(val).Round(2)
	case int:
		return decimal.NewFromInt(int64(val)).Round(2)
	case int64:
		return decimal.NewFromInt(val).Round(2)
	case decimal.Decimal:
		return val.Round(2)
	}
	return decimal.Zero
}

func stringFromSnapshotValue(v interface{}) string {
	switch val := v.(type) {
	case string:
		return strings.TrimSpace(val)
	case float64:
		return decimal.NewFromFloat(val).StringFixed(0)
	case int:
		return fmt.Sprintf("%d", val)
	case int64:
		return fmt.Sprintf("%d", val)
	}
	return ""
}

func (s *ResellerAccountingService) HandleRefundDeductTx(tx *gorm.DB, order *models.Order, refundRecord *models.OrderRefundRecord, refundedBefore decimal.Decimal) error {
	if s == nil || s.repo == nil || tx == nil || order == nil || refundRecord == nil || refundRecord.ID == 0 {
		return nil
	}
	if order.ResellerID == nil || *order.ResellerID == 0 {
		return nil
	}
	refundAmount := refundRecord.Amount.Decimal.Round(2)
	if refundAmount.LessThanOrEqual(decimal.Zero) {
		return nil
	}
	repoTx := s.repo.WithTx(tx)
	snapshot, err := repoTx.GetOrderSnapshotByOrderID(order.ID)
	if err != nil {
		return err
	}
	if snapshot == nil {
		logger.Warnw("reseller_refund_missing_snapshot_skip", "order_id", order.ID, "order_no", order.OrderNo, "refund_record_id", refundRecord.ID)
		return nil
	}
	if !snapshot.ProfitEligible {
		return nil
	}
	profit := snapshot.ProfitAmount.Decimal.Round(2)
	if profit.LessThanOrEqual(decimal.Zero) {
		return nil
	}
	orderAmount := snapshot.ResellerAmount.Decimal.Round(2)
	if orderAmount.LessThanOrEqual(decimal.Zero) {
		orderAmount = order.TotalAmount.Decimal.Round(2)
	}
	if orderAmount.LessThanOrEqual(decimal.Zero) {
		return ErrResellerLedgerInvalidSnapshot
	}
	remainingBefore := orderAmount.Sub(refundedBefore.Round(2)).Round(2)
	if remainingBefore.LessThanOrEqual(decimal.Zero) {
		return nil
	}
	if refundAmount.GreaterThan(remainingBefore) {
		refundAmount = remainingBefore
	}
	ratio := refundAmount.Div(remainingBefore)
	deduct := profit.Mul(ratio).Round(2)
	if deduct.LessThanOrEqual(decimal.Zero) {
		return nil
	}
	allocation := resellerRefundAllocation{
		RefundRecordID: refundRecord.ID,
		OrderID:        order.ID,
		RefundAmount:   refundAmount.StringFixed(2),
		OrderAmount:    orderAmount.StringFixed(2),
		Items:          make([]resellerRefundAllocationItem, 0),
	}
	if rawItems, ok := snapshot.PricingSnapshotJSON["items"].([]interface{}); ok {
		for _, raw := range rawItems {
			itemMap, ok := raw.(map[string]interface{})
			if !ok {
				continue
			}
			itemProfit := decimalFromSnapshotValue(itemMap["profit_amount"])
			itemDeduct := itemProfit.Mul(ratio).Round(2)
			if itemDeduct.LessThanOrEqual(decimal.Zero) {
				continue
			}
			allocation.Items = append(allocation.Items, resellerRefundAllocationItem{
				OrderItemID:          stringFromSnapshotValue(itemMap["order_item_id"]),
				RefundRatio:          ratio.StringFixed(8),
				OriginalProfitAmount: itemProfit.StringFixed(2),
				DeductAmount:         itemDeduct.StringFixed(2),
			})
		}
	}
	now := time.Now()
	orderID := order.ID
	entry := &models.ResellerLedgerEntry{
		ResellerID: snapshot.ResellerID,
		OrderID:    &orderID,
		Type:       models.ResellerLedgerTypeRefundDeduct,
		Amount:     models.NewMoneyFromDecimal(deduct.Neg()),
		Currency:   strings.TrimSpace(snapshot.Currency),
		Status:     models.ResellerLedgerStatusAvailable,
		MetadataJSON: models.JSON{
			"refund_record_id":       refundRecord.ID,
			"refund_type":            refundRecord.Type,
			"refund_amount":          refundAmount.StringFixed(2),
			"refunded_before":        refundedBefore.Round(2).StringFixed(2),
			"refund_allocation_json": allocation,
			"snapshot_id":            snapshot.ID,
		},
		IdempotencyKey: fmt.Sprintf("refund_deduct:%d", refundRecord.ID),
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if entry.Currency == "" {
		entry.Currency = strings.TrimSpace(refundRecord.Currency)
	}
	if entry.Currency == "" {
		entry.Currency = strings.TrimSpace(order.Currency)
	}
	if entry.Currency == "" {
		return ErrResellerLedgerInvalidSnapshot
	}
	_, err = repoTx.CreateLedgerEntryIfNotExists(entry)
	if err != nil {
		return err
	}
	return s.refreshBalanceAccountTx(repoTx, snapshot.ResellerID, entry.Currency, now)
}

type ResellerWithdrawApplyInput struct {
	Amount   decimal.Decimal
	Currency string
	Channel  string
	Account  string
}

func (s *ResellerAccountingService) ApplyWithdraw(resellerID uint, input ResellerWithdrawApplyInput) (*models.ResellerWithdrawRequest, error) {
	if s == nil || s.repo == nil || resellerID == 0 {
		return nil, ErrResellerAccountingUnavailable
	}
	amount := input.Amount.Round(2)
	currency := strings.TrimSpace(input.Currency)
	channel := strings.TrimSpace(input.Channel)
	account := strings.TrimSpace(input.Account)
	if amount.LessThanOrEqual(decimal.Zero) {
		return nil, ErrResellerWithdrawAmountInvalid
	}
	if currency == "" {
		return nil, ErrResellerWithdrawCurrencyUnavailable
	}
	if channel == "" || account == "" {
		return nil, ErrResellerWithdrawAmountInvalid
	}
	var createdID uint
	err := s.repo.Transaction(func(tx *gorm.DB) error {
		repoTx := s.repo.WithTx(tx)
		balance, err := repoTx.GetOrCreateBalanceAccountForUpdate(resellerID, currency)
		if err != nil {
			return err
		}
		if balance.Status == models.ResellerBalanceStatusNegativeBalance ||
			balance.Status == models.ResellerBalanceStatusFrozenReview ||
			balance.Status == models.ResellerBalanceStatusDisabled {
			return ErrResellerBalanceAccountFrozen
		}
		ledgers, err := repoTx.ListAvailableLedgerEntriesForUpdate(resellerID, currency)
		if err != nil {
			return err
		}
		remaining := amount
		selectedIDs := make([]uint, 0)
		now := time.Now()
		for i := range ledgers {
			if remaining.LessThanOrEqual(decimal.Zero) {
				break
			}
			row := ledgers[i]
			rowAmount := row.Amount.Decimal.Round(2)
			if rowAmount.LessThanOrEqual(decimal.Zero) {
				continue
			}
			if rowAmount.LessThanOrEqual(remaining) {
				selectedIDs = append(selectedIDs, row.ID)
				remaining = remaining.Sub(rowAmount).Round(2)
				continue
			}
			lockAmount := remaining.Round(2)
			remainAmount := rowAmount.Sub(lockAmount).Round(2)
			row.Amount = models.NewMoneyFromDecimal(lockAmount)
			row.UpdatedAt = now
			if err := repoTx.UpdateLedgerEntry(&row); err != nil {
				return err
			}
			remainRow := row
			remainRow.ID = 0
			remainRow.Amount = models.NewMoneyFromDecimal(remainAmount)
			remainRow.Status = models.ResellerLedgerStatusAvailable
			remainRow.WithdrawRequestID = nil
			remainRow.IdempotencyKey = fmt.Sprintf("%s:split:%d:%d", row.IdempotencyKey, row.ID, now.UnixNano())
			remainRow.CreatedAt = now
			remainRow.UpdatedAt = now
			if _, err := repoTx.CreateLedgerEntryIfNotExists(&remainRow); err != nil {
				return err
			}
			selectedIDs = append(selectedIDs, row.ID)
			remaining = decimal.Zero
			break
		}
		if remaining.GreaterThan(decimal.Zero) {
			return ErrResellerWithdrawInsufficient
		}
		req := &models.ResellerWithdrawRequest{
			ResellerID: resellerID,
			Amount:     models.NewMoneyFromDecimal(amount),
			Currency:   currency,
			Channel:    channel,
			Account:    account,
			Status:     models.ResellerWithdrawStatusPending,
			CreatedAt:  now,
			UpdatedAt:  now,
		}
		if err := repoTx.CreateWithdrawRequest(req); err != nil {
			return err
		}
		if err := repoTx.BatchUpdateLedgerEntries(selectedIDs, map[string]interface{}{
			"status":              models.ResellerLedgerStatusLocked,
			"withdraw_request_id": req.ID,
		}); err != nil {
			return err
		}
		createdID = req.ID
		return s.refreshBalanceAccountTx(repoTx, resellerID, currency, now)
	})
	if err != nil {
		return nil, err
	}
	return s.repo.GetWithdrawRequestByID(createdID)
}

func (s *ResellerAccountingService) ReviewWithdraw(adminID uint, withdrawID uint, action string, rejectReason string) (*models.ResellerWithdrawRequest, error) {
	if s == nil || s.repo == nil || withdrawID == 0 {
		return nil, ErrNotFound
	}
	act := strings.ToLower(strings.TrimSpace(action))
	if act != resellerWithdrawActionReject && act != resellerWithdrawActionPay {
		return nil, ErrResellerWithdrawStatusInvalid
	}
	err := s.repo.Transaction(func(tx *gorm.DB) error {
		repoTx := s.repo.WithTx(tx)
		req, err := repoTx.GetWithdrawRequestByIDForUpdate(withdrawID)
		if err != nil {
			return err
		}
		if req == nil {
			return ErrNotFound
		}
		if req.Status != models.ResellerWithdrawStatusPending {
			return ErrResellerWithdrawStatusInvalid
		}
		now := time.Now()
		req.ProcessedBy = &adminID
		req.ProcessedAt = &now
		req.UpdatedAt = now
		if act == resellerWithdrawActionReject {
			req.Status = models.ResellerWithdrawStatusRejected
			req.RejectReason = strings.TrimSpace(rejectReason)
			if err := repoTx.BatchUpdateLedgerEntriesByWithdrawID(withdrawID, map[string]interface{}{
				"status":              models.ResellerLedgerStatusAvailable,
				"withdraw_request_id": nil,
			}); err != nil {
				return err
			}
		} else {
			req.Status = models.ResellerWithdrawStatusPaid
			req.RejectReason = ""
			if err := repoTx.BatchUpdateLedgerEntriesByWithdrawID(withdrawID, map[string]interface{}{
				"status": models.ResellerLedgerStatusWithdrawn,
			}); err != nil {
				return err
			}
		}
		if err := repoTx.UpdateWithdrawRequest(req); err != nil {
			return err
		}
		return s.refreshBalanceAccountTx(repoTx, req.ResellerID, req.Currency, now)
	})
	if err != nil {
		return nil, err
	}
	return s.repo.GetWithdrawRequestByID(withdrawID)
}

func (s *ResellerAccountingService) refreshBalanceAccountTx(repo repository.ResellerRepository, resellerID uint, currency string, now time.Time) error {
	currency = strings.TrimSpace(currency)
	if repo == nil || resellerID == 0 || currency == "" {
		return nil
	}
	account, err := repo.GetOrCreateBalanceAccountForUpdate(resellerID, currency)
	if err != nil {
		return err
	}
	available, err := repo.SumLedgerAmount(resellerID, currency, []string{models.ResellerLedgerStatusAvailable})
	if err != nil {
		return err
	}
	locked, err := repo.SumLedgerAmount(resellerID, currency, []string{models.ResellerLedgerStatusLocked})
	if err != nil {
		return err
	}
	withdrawn, err := repo.SumLedgerAmount(resellerID, currency, []string{models.ResellerLedgerStatusWithdrawn})
	if err != nil {
		return err
	}
	net := available.Sub(withdrawn).Round(2)
	negative := decimal.Zero
	if net.LessThan(decimal.Zero) {
		negative = net.Abs().Round(2)
		account.Status = models.ResellerBalanceStatusNegativeBalance
	} else if account.Status == models.ResellerBalanceStatusNegativeBalance {
		account.Status = models.ResellerBalanceStatusNormal
	}
	account.AvailableAmountCache = models.NewMoneyFromDecimal(net)
	account.LockedAmountCache = models.NewMoneyFromDecimal(locked.Round(2))
	account.NegativeAmountCache = models.NewMoneyFromDecimal(negative)
	account.UpdatedAt = now
	return repo.UpdateBalanceAccount(account)
}
