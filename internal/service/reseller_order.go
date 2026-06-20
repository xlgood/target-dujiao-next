package service

import (
	"fmt"
	"strings"
	"time"

	"github.com/dujiao-next/internal/constants"
	"github.com/dujiao-next/internal/models"
	"github.com/dujiao-next/internal/repository"
	"github.com/shopspring/decimal"
)

const (
	ResellerProfitStatusCredited    = "credited"
	ResellerProfitStatusPending     = "pending"
	ResellerProfitStatusUnavailable = "unavailable"
)

type ResellerOrderService struct {
	repo repository.ResellerRepository
}

type ResellerOrderListInput struct {
	Page        int
	PageSize    int
	Status      string
	OrderNo     string
	CreatedFrom *time.Time
	CreatedTo   *time.Time
	PaidFrom    *time.Time
	PaidTo      *time.Time
}

type ResellerOrderListItem struct {
	OrderNo      string
	Status       string
	Currency     string
	TotalAmount  models.Money
	BaseAmount   models.Money
	ProfitAmount models.Money
	ProfitStatus string
	Domain       string
	BuyerLabel   string
	ItemsCount   int
	CreatedAt    time.Time
	PaidAt       *time.Time
}

type ResellerOrderItemDetail struct {
	Title               models.JSON
	SKUSnapshot         models.JSON
	Quantity            int
	UnitPrice           models.Money
	TotalPrice          models.Money
	BaseUnitAmount      string
	ResellerUnitAmount  string
	BaseTotalAmount     string
	ResellerTotalAmount string
	ProfitAmount        string
}

type ResellerOrderDetail struct {
	ResellerOrderListItem
	Items []ResellerOrderItemDetail
}

type ResellerOrderStats struct {
	Total      int64
	ByStatus   map[string]int64
	ByCurrency map[string]int64
}

type resellerPricingItemSnapshot struct {
	BaseUnitAmount      string
	ResellerUnitAmount  string
	BaseTotalAmount     string
	ResellerTotalAmount string
	ProfitAmount        string
}

func NewResellerOrderService(repo repository.ResellerRepository) *ResellerOrderService {
	return &ResellerOrderService{repo: repo}
}

func (s *ResellerOrderService) requireActiveProfileByUser(userID uint) (*models.ResellerProfile, error) {
	if s == nil || s.repo == nil || userID == 0 {
		return nil, ErrResellerNotOpened
	}
	profile, err := s.repo.GetProfileByUserID(userID)
	if err != nil {
		return nil, err
	}
	if profile == nil {
		return nil, ErrResellerNotOpened
	}
	if profile.Status != models.ResellerProfileStatusActive {
		return nil, ErrResellerProfileInactive
	}
	return profile, nil
}

func (s *ResellerOrderService) ListUserOrders(userID uint, input ResellerOrderListInput) ([]ResellerOrderListItem, int64, error) {
	profile, err := s.requireActiveProfileByUser(userID)
	if err != nil {
		return nil, 0, err
	}
	rows, total, err := s.repo.ListOrderSnapshotsByReseller(resellerOrderFilter(profile.ID, input))
	if err != nil {
		return nil, 0, err
	}
	out := make([]ResellerOrderListItem, 0, len(rows))
	for _, row := range rows {
		out = append(out, buildResellerOrderListItem(row))
	}
	return out, total, nil
}

func (s *ResellerOrderService) ListAdminOrders(resellerID uint, input ResellerOrderListInput) ([]ResellerOrderListItem, int64, error) {
	if s == nil || s.repo == nil || resellerID == 0 {
		return nil, 0, ErrNotFound
	}
	profile, err := s.repo.GetProfileByID(resellerID)
	if err != nil {
		return nil, 0, err
	}
	if profile == nil {
		return nil, 0, ErrNotFound
	}
	rows, total, err := s.repo.ListOrderSnapshotsByReseller(resellerOrderFilter(resellerID, input))
	if err != nil {
		return nil, 0, err
	}
	out := make([]ResellerOrderListItem, 0, len(rows))
	for _, row := range rows {
		out = append(out, buildResellerOrderListItem(row))
	}
	return out, total, nil
}

func (s *ResellerOrderService) GetUserOrderDetail(userID uint, orderNo string) (*ResellerOrderDetail, error) {
	profile, err := s.requireActiveProfileByUser(userID)
	if err != nil {
		return nil, err
	}
	row, err := s.repo.GetOrderSnapshotByResellerOrderNo(profile.ID, orderNo)
	if err != nil {
		return nil, err
	}
	if row == nil {
		return nil, ErrOrderNotFound
	}
	detail := &ResellerOrderDetail{ResellerOrderListItem: buildResellerOrderListItem(*row)}
	detail.Items = buildResellerOrderItemDetails(*row)
	return detail, nil
}

func (s *ResellerOrderService) StatsUserOrders(userID uint, input ResellerOrderListInput) (ResellerOrderStats, error) {
	profile, err := s.requireActiveProfileByUser(userID)
	if err != nil {
		return ResellerOrderStats{}, err
	}
	row, err := s.repo.StatsOrderSnapshotsByReseller(resellerOrderFilter(profile.ID, input))
	if err != nil {
		return ResellerOrderStats{}, err
	}
	return ResellerOrderStats{Total: row.Total, ByStatus: row.ByStatus, ByCurrency: row.ByCurrency}, nil
}

func resellerOrderFilter(resellerID uint, input ResellerOrderListInput) repository.ResellerOrderListFilter {
	return repository.ResellerOrderListFilter{
		ResellerID:  resellerID,
		Page:        input.Page,
		PageSize:    input.PageSize,
		Status:      strings.TrimSpace(input.Status),
		OrderNo:     strings.TrimSpace(input.OrderNo),
		CreatedFrom: input.CreatedFrom,
		CreatedTo:   input.CreatedTo,
		PaidFrom:    input.PaidFrom,
		PaidTo:      input.PaidTo,
	}
}

func buildResellerOrderListItem(row repository.ResellerOrderSnapshotRow) ResellerOrderListItem {
	order := row.Order
	snapshot := row.Snapshot
	return ResellerOrderListItem{
		OrderNo:      order.OrderNo,
		Status:       order.Status,
		Currency:     snapshot.Currency,
		TotalAmount:  order.TotalAmount,
		BaseAmount:   snapshot.BaseAmount,
		ProfitAmount: snapshot.ProfitAmount,
		ProfitStatus: neutralResellerProfitStatus(snapshot, order, row.LedgerEntries),
		Domain:       snapshot.Domain,
		BuyerLabel:   maskResellerBuyerLabel(order, row.BuyerEmail),
		ItemsCount:   len(row.Items),
		CreatedAt:    order.CreatedAt,
		PaidAt:       order.PaidAt,
	}
}

func neutralResellerProfitStatus(snapshot models.ResellerOrderSnapshot, order models.Order, ledgerEntries []models.ResellerLedgerEntry) string {
	if !snapshot.ProfitEligible || snapshot.ProfitAmount.Decimal.LessThanOrEqual(decimal.Zero) {
		return ResellerProfitStatusUnavailable
	}
	switch order.Status {
	case constants.OrderStatusCanceled, constants.OrderStatusRefunded, constants.OrderStatusPartiallyRefunded:
		return ResellerProfitStatusUnavailable
	}
	if order.PaidAt == nil || order.Status == constants.OrderStatusPendingPayment {
		return ResellerProfitStatusPending
	}
	for _, entry := range ledgerEntries {
		if entry.Type != models.ResellerLedgerTypeOrderProfit {
			continue
		}
		switch entry.Status {
		case models.ResellerLedgerStatusAvailable, models.ResellerLedgerStatusLocked, models.ResellerLedgerStatusWithdrawn:
			return ResellerProfitStatusCredited
		case models.ResellerLedgerStatusPendingConfirm:
			return ResellerProfitStatusPending
		case models.ResellerLedgerStatusCanceled:
			return ResellerProfitStatusUnavailable
		}
	}
	return ResellerProfitStatusPending
}

func maskResellerBuyerLabel(order models.Order, buyerEmail string) string {
	if order.UserID > 0 {
		if label := maskResellerBuyerEmail(buyerEmail); label != "" {
			return label
		}
		return fmt.Sprintf("user#%d", order.UserID)
	}
	if label := maskResellerBuyerEmail(order.GuestEmail); label != "" {
		return label
	}
	return "guest"
}

func maskResellerBuyerEmail(email string) string {
	email = strings.TrimSpace(email)
	if email == "" {
		return ""
	}
	parts := strings.SplitN(email, "@", 2)
	if len(parts) != 2 {
		return ""
	}
	prefix := parts[0]
	if len(prefix) > 1 {
		prefix = prefix[:1]
	}
	return prefix + "***@" + parts[1]
}

func buildResellerOrderItemDetails(row repository.ResellerOrderSnapshotRow) []ResellerOrderItemDetail {
	pricingByItemID := pricingSnapshotByOrderItemID(row.Snapshot.PricingSnapshotJSON)
	out := make([]ResellerOrderItemDetail, 0, len(row.Items))
	for i := range row.Items {
		item := row.Items[i]
		itemPricing := pricingByItemID[item.ID]
		out = append(out, ResellerOrderItemDetail{
			Title:               item.TitleJSON,
			SKUSnapshot:         item.SKUSnapshotJSON,
			Quantity:            item.Quantity,
			UnitPrice:           item.UnitPrice,
			TotalPrice:          item.TotalPrice,
			BaseUnitAmount:      itemPricing.BaseUnitAmount,
			ResellerUnitAmount:  itemPricing.ResellerUnitAmount,
			BaseTotalAmount:     itemPricing.BaseTotalAmount,
			ResellerTotalAmount: itemPricing.ResellerTotalAmount,
			ProfitAmount:        itemPricing.ProfitAmount,
		})
	}
	return out
}

func pricingSnapshotByOrderItemID(snapshot models.JSON) map[uint]resellerPricingItemSnapshot {
	out := map[uint]resellerPricingItemSnapshot{}
	rawItems, ok := snapshot["items"].([]interface{})
	if !ok {
		return out
	}
	for _, raw := range rawItems {
		item, ok := raw.(map[string]interface{})
		if !ok {
			continue
		}
		itemID := uintFromSnapshotValue(item["order_item_id"])
		if itemID == 0 {
			continue
		}
		out[itemID] = resellerPricingItemSnapshot{
			BaseUnitAmount:      resellerOrderSnapshotStringValue(item["base_unit_amount"]),
			ResellerUnitAmount:  resellerOrderSnapshotStringValue(item["reseller_unit_amount"]),
			BaseTotalAmount:     resellerOrderSnapshotStringValue(item["base_total_amount"]),
			ResellerTotalAmount: resellerOrderSnapshotStringValue(item["reseller_total_amount"]),
			ProfitAmount:        resellerOrderSnapshotStringValue(item["profit_amount"]),
		}
	}
	return out
}

func uintFromSnapshotValue(value interface{}) uint {
	switch v := value.(type) {
	case uint:
		return v
	case int:
		if v > 0 {
			return uint(v)
		}
	case int64:
		if v > 0 {
			return uint(v)
		}
	case float64:
		if v > 0 {
			return uint(v)
		}
	case string:
		parsed, err := decimal.NewFromString(strings.TrimSpace(v))
		if err == nil && parsed.GreaterThan(decimal.Zero) {
			return uint(parsed.IntPart())
		}
	}
	return 0
}

func resellerOrderSnapshotStringValue(value interface{}) string {
	switch v := value.(type) {
	case string:
		return v
	case fmt.Stringer:
		return v.String()
	case float64:
		return decimal.NewFromFloat(v).Round(2).StringFixed(2)
	case int:
		return decimal.NewFromInt(int64(v)).Round(2).StringFixed(2)
	case int64:
		return decimal.NewFromInt(v).Round(2).StringFixed(2)
	case uint:
		return decimal.NewFromInt(int64(v)).Round(2).StringFixed(2)
	}
	return ""
}
