package service

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/dujiao-next/internal/constants"
	"github.com/dujiao-next/internal/models"
	"github.com/dujiao-next/internal/repository"
	"github.com/glebarez/sqlite"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

func openResellerOrderServiceTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:reseller_order_service_%d?mode=memory&cache=shared", time.Now().UnixNano())), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite failed: %v", err)
	}
	if err := db.AutoMigrate(
		&models.User{},
		&models.Order{},
		&models.OrderItem{},
		&models.ResellerProfile{},
		&models.ResellerOrderSnapshot{},
		&models.ResellerLedgerEntry{},
	); err != nil {
		t.Fatalf("migrate failed: %v", err)
	}
	return db
}

func seedResellerOrderFixture(t *testing.T, db *gorm.DB, email string) (models.ResellerProfile, models.Order, models.ResellerOrderSnapshot) {
	t.Helper()
	user := models.User{Email: email, PasswordHash: "hash", Status: constants.UserStatusActive}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user failed: %v", err)
	}
	profile := models.ResellerProfile{
		UserID:           user.ID,
		Status:           models.ResellerProfileStatusActive,
		SettlementStatus: models.ResellerSettlementStatusNormal,
	}
	if err := db.Create(&profile).Error; err != nil {
		t.Fatalf("create profile failed: %v", err)
	}
	paidAt := time.Now().Add(-time.Hour)
	order := models.Order{
		OrderNo:              fmt.Sprintf("DJ-RES-%d", time.Now().UnixNano()),
		UserID:               999,
		Status:               constants.OrderStatusPaid,
		Currency:             "USD",
		TotalAmount:          models.NewMoneyFromDecimal(decimal.RequireFromString("130.00")),
		ResellerID:           &profile.ID,
		ResellerDomain:       "shop.example.test",
		ResellerProfitAmount: models.NewMoneyFromDecimal(decimal.RequireFromString("30.00")),
		PaidAt:               &paidAt,
	}
	if err := db.Create(&order).Error; err != nil {
		t.Fatalf("create order failed: %v", err)
	}
	item := models.OrderItem{
		OrderID:         order.ID,
		ProductID:       10,
		SKUID:           20,
		TitleJSON:       models.JSON{"zh-CN": "测试商品"},
		SKUSnapshotJSON: models.JSON{"规格": "A"},
		Quantity:        2,
		UnitPrice:       models.NewMoneyFromDecimal(decimal.RequireFromString("65.00")),
		TotalPrice:      models.NewMoneyFromDecimal(decimal.RequireFromString("130.00")),
		CostPrice:       models.NewMoneyFromDecimal(decimal.RequireFromString("1.00")),
	}
	if err := db.Create(&item).Error; err != nil {
		t.Fatalf("create order item failed: %v", err)
	}
	snapshot := models.ResellerOrderSnapshot{
		OrderID:           order.ID,
		ResellerID:        profile.ID,
		Domain:            order.ResellerDomain,
		Currency:          order.Currency,
		ResellerUserID:    profile.UserID,
		BuyerUserID:       order.UserID,
		BaseAmount:        models.NewMoneyFromDecimal(decimal.RequireFromString("100.00")),
		ResellerAmount:    models.NewMoneyFromDecimal(decimal.RequireFromString("130.00")),
		ProfitAmount:      models.NewMoneyFromDecimal(decimal.RequireFromString("30.00")),
		ProfitEligible:    false,
		ProfitBlockReason: "self_dealing_owner",
		PricingSnapshotJSON: models.JSON{"items": []interface{}{
			map[string]interface{}{
				"order_item_id":          item.ID,
				"base_unit_amount":       "50.00",
				"reseller_unit_amount":   "65.00",
				"base_total_amount":      "100.00",
				"reseller_total_amount":  "130.00",
				"profit_amount":          "30.00",
				"profit_block_reason":    "self_dealing_owner",
				"internal_risk_decision": "blocked",
			},
		}},
	}
	if err := db.Create(&snapshot).Error; err != nil {
		t.Fatalf("create snapshot failed: %v", err)
	}
	if err := db.Model(&models.ResellerOrderSnapshot{}).
		Where("id = ?", snapshot.ID).
		Update("profit_eligible", false).Error; err != nil {
		t.Fatalf("force ineligible snapshot failed: %v", err)
	}
	snapshot.ProfitEligible = false
	return profile, order, snapshot
}

func seedResellerOrderWithChildItemsFixture(t *testing.T, db *gorm.DB, email string) (models.ResellerProfile, models.Order, []models.OrderItem) {
	t.Helper()
	user := models.User{Email: email, PasswordHash: "hash", Status: constants.UserStatusActive}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user failed: %v", err)
	}
	profile := models.ResellerProfile{
		UserID:           user.ID,
		Status:           models.ResellerProfileStatusActive,
		SettlementStatus: models.ResellerSettlementStatusNormal,
	}
	if err := db.Create(&profile).Error; err != nil {
		t.Fatalf("create profile failed: %v", err)
	}
	paidAt := time.Now().Add(-time.Hour)
	parent := models.Order{
		OrderNo:              fmt.Sprintf("DJ-RES-PARENT-%d", time.Now().UnixNano()),
		UserID:               user.ID,
		Status:               constants.OrderStatusPaid,
		Currency:             "USD",
		TotalAmount:          models.NewMoneyFromDecimal(decimal.RequireFromString("150.00")),
		ResellerID:           &profile.ID,
		ResellerDomain:       "child-items.example.test",
		ResellerProfitAmount: models.NewMoneyFromDecimal(decimal.RequireFromString("30.00")),
		PaidAt:               &paidAt,
	}
	if err := db.Create(&parent).Error; err != nil {
		t.Fatalf("create parent order failed: %v", err)
	}
	var items []models.OrderItem
	childAmounts := []string{"70.00", "80.00"}
	for idx, amount := range childAmounts {
		child := models.Order{
			OrderNo:              fmt.Sprintf("%s-%d", parent.OrderNo, idx+1),
			ParentID:             &parent.ID,
			UserID:               user.ID,
			Status:               constants.OrderStatusPaid,
			Currency:             "USD",
			TotalAmount:          models.NewMoneyFromDecimal(decimal.RequireFromString(amount)),
			ResellerID:           &profile.ID,
			ResellerDomain:       parent.ResellerDomain,
			ResellerProfitAmount: models.NewMoneyFromDecimal(decimal.RequireFromString("15.00")),
			PaidAt:               &paidAt,
		}
		if err := db.Create(&child).Error; err != nil {
			t.Fatalf("create child order failed: %v", err)
		}
		item := models.OrderItem{
			OrderID:         child.ID,
			ProductID:       uint(100 + idx),
			SKUID:           uint(200 + idx),
			TitleJSON:       models.JSON{"zh-CN": fmt.Sprintf("子订单商品 %d", idx+1)},
			SKUSnapshotJSON: models.JSON{"规格": fmt.Sprintf("S%d", idx+1)},
			Quantity:        1,
			UnitPrice:       models.NewMoneyFromDecimal(decimal.RequireFromString(amount)),
			TotalPrice:      models.NewMoneyFromDecimal(decimal.RequireFromString(amount)),
			CostPrice:       models.NewMoneyFromDecimal(decimal.RequireFromString("2.00")),
		}
		if err := db.Create(&item).Error; err != nil {
			t.Fatalf("create child item failed: %v", err)
		}
		items = append(items, item)
	}
	snapshot := models.ResellerOrderSnapshot{
		OrderID:        parent.ID,
		ResellerID:     profile.ID,
		Domain:         parent.ResellerDomain,
		Currency:       parent.Currency,
		ResellerUserID: profile.UserID,
		BuyerUserID:    parent.UserID,
		BaseAmount:     models.NewMoneyFromDecimal(decimal.RequireFromString("120.00")),
		ResellerAmount: models.NewMoneyFromDecimal(decimal.RequireFromString("150.00")),
		ProfitAmount:   models.NewMoneyFromDecimal(decimal.RequireFromString("30.00")),
		ProfitEligible: true,
		PricingSnapshotJSON: models.JSON{"items": []interface{}{
			map[string]interface{}{
				"order_item_id":         items[0].ID,
				"base_unit_amount":      "55.00",
				"reseller_unit_amount":  "70.00",
				"base_total_amount":     "55.00",
				"reseller_total_amount": "70.00",
				"profit_amount":         "15.00",
			},
			map[string]interface{}{
				"order_item_id":         items[1].ID,
				"base_unit_amount":      "65.00",
				"reseller_unit_amount":  "80.00",
				"base_total_amount":     "65.00",
				"reseller_total_amount": "80.00",
				"profit_amount":         "15.00",
			},
		}},
	}
	if err := db.Create(&snapshot).Error; err != nil {
		t.Fatalf("create snapshot failed: %v", err)
	}
	return profile, parent, items
}

func TestResellerOrderServiceListUsesSnapshotAndHidesRiskFields(t *testing.T) {
	db := openResellerOrderServiceTestDB(t)
	profile, order, _ := seedResellerOrderFixture(t, db, "reseller-orders@example.test")
	_, otherOrder, _ := seedResellerOrderFixture(t, db, "other-reseller-orders@example.test")
	svc := NewResellerOrderService(repository.NewResellerRepository(db))

	rows, total, err := svc.ListUserOrders(profile.UserID, ResellerOrderListInput{Page: 1, PageSize: 20})
	if err != nil {
		t.Fatalf("ListUserOrders failed: %v", err)
	}
	if total != 1 || len(rows) != 1 {
		t.Fatalf("expected one row, total=%d rows=%d", total, len(rows))
	}
	if rows[0].OrderNo != order.OrderNo || rows[0].OrderNo == otherOrder.OrderNo {
		t.Fatalf("unexpected order isolation: %+v", rows[0])
	}
	if rows[0].BaseAmount.StringFixed(2) != "100.00" || rows[0].ProfitAmount.StringFixed(2) != "30.00" {
		t.Fatalf("expected snapshot amounts, got base=%s profit=%s", rows[0].BaseAmount.StringFixed(2), rows[0].ProfitAmount.StringFixed(2))
	}
	if rows[0].ProfitStatus != ResellerProfitStatusUnavailable {
		t.Fatalf("blocked or ineligible profit must stay neutral unavailable, got %+v", rows[0])
	}
}

func TestResellerOrderServiceBuyerLabelMasksMemberEmail(t *testing.T) {
	db := openResellerOrderServiceTestDB(t)
	profile, order, _ := seedResellerOrderFixture(t, db, "reseller-buyer-label@example.test")
	buyer := models.User{
		Email:        "buyer-label@example.test",
		PasswordHash: "hash",
		Status:       constants.UserStatusActive,
	}
	if err := db.Create(&buyer).Error; err != nil {
		t.Fatalf("create buyer user failed: %v", err)
	}
	if err := db.Model(&models.Order{}).Where("id = ?", order.ID).Update("user_id", buyer.ID).Error; err != nil {
		t.Fatalf("update order buyer failed: %v", err)
	}
	if err := db.Model(&models.ResellerOrderSnapshot{}).Where("order_id = ?", order.ID).Update("buyer_user_id", buyer.ID).Error; err != nil {
		t.Fatalf("update snapshot buyer failed: %v", err)
	}
	svc := NewResellerOrderService(repository.NewResellerRepository(db))

	rows, _, err := svc.ListUserOrders(profile.UserID, ResellerOrderListInput{Page: 1, PageSize: 20})
	if err != nil {
		t.Fatalf("ListUserOrders failed: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected one row, got %d", len(rows))
	}
	if rows[0].BuyerLabel != "b***@example.test" {
		t.Fatalf("member buyer label should mask email instead of hardcoded user, got %q", rows[0].BuyerLabel)
	}

	detail, err := svc.GetUserOrderDetail(profile.UserID, order.OrderNo)
	if err != nil {
		t.Fatalf("GetUserOrderDetail failed: %v", err)
	}
	if detail.BuyerLabel != "b***@example.test" {
		t.Fatalf("member detail buyer label should mask email instead of hardcoded user, got %q", detail.BuyerLabel)
	}
}

func TestResellerOrderServiceProfitStatusRequiresAvailableLedger(t *testing.T) {
	db := openResellerOrderServiceTestDB(t)
	profile, order, snapshot := seedResellerOrderFixture(t, db, "reseller-ledger-status@example.test")
	if err := db.Model(&models.ResellerOrderSnapshot{}).Where("id = ?", snapshot.ID).Updates(map[string]interface{}{
		"profit_eligible":     true,
		"profit_block_reason": "",
	}).Error; err != nil {
		t.Fatalf("update snapshot failed: %v", err)
	}
	svc := NewResellerOrderService(repository.NewResellerRepository(db))

	rows, _, err := svc.ListUserOrders(profile.UserID, ResellerOrderListInput{Page: 1, PageSize: 20})
	if err != nil {
		t.Fatalf("ListUserOrders failed: %v", err)
	}
	if rows[0].ProfitStatus == ResellerProfitStatusCredited {
		t.Fatalf("paid order without ledger must not be credited: %+v", rows[0])
	}

	orderID := order.ID
	if err := db.Create(&models.ResellerLedgerEntry{
		ResellerID:     profile.ID,
		OrderID:        &orderID,
		Type:           models.ResellerLedgerTypeOrderProfit,
		Amount:         models.NewMoneyFromDecimal(decimal.RequireFromString("30.00")),
		Currency:       "USD",
		IdempotencyKey: "order-profit-status-pending",
		Status:         models.ResellerLedgerStatusPendingConfirm,
	}).Error; err != nil {
		t.Fatalf("create pending ledger failed: %v", err)
	}
	rows, _, err = svc.ListUserOrders(profile.UserID, ResellerOrderListInput{Page: 1, PageSize: 20})
	if err != nil {
		t.Fatalf("ListUserOrders failed after pending ledger: %v", err)
	}
	if rows[0].ProfitStatus != ResellerProfitStatusPending {
		t.Fatalf("pending_confirm ledger must map to pending, got %+v", rows[0])
	}

	if err := db.Model(&models.ResellerLedgerEntry{}).Where("idempotency_key = ?", "order-profit-status-pending").Update("status", models.ResellerLedgerStatusAvailable).Error; err != nil {
		t.Fatalf("update ledger failed: %v", err)
	}
	rows, _, err = svc.ListUserOrders(profile.UserID, ResellerOrderListInput{Page: 1, PageSize: 20})
	if err != nil {
		t.Fatalf("ListUserOrders failed after available ledger: %v", err)
	}
	if rows[0].ProfitStatus != ResellerProfitStatusCredited {
		t.Fatalf("available ledger must map to credited, got %+v", rows[0])
	}
}

func TestResellerOrderServicePartiallyRefundedOrderIsNeutralUnavailable(t *testing.T) {
	db := openResellerOrderServiceTestDB(t)
	profile, order, snapshot := seedResellerOrderFixture(t, db, "reseller-partial-refund@example.test")
	if err := db.Model(&models.ResellerOrderSnapshot{}).Where("id = ?", snapshot.ID).Updates(map[string]interface{}{
		"profit_eligible":     true,
		"profit_block_reason": "",
	}).Error; err != nil {
		t.Fatalf("update snapshot failed: %v", err)
	}
	if err := db.Model(&models.Order{}).Where("id = ?", order.ID).Update("status", constants.OrderStatusPartiallyRefunded).Error; err != nil {
		t.Fatalf("update order failed: %v", err)
	}
	orderID := order.ID
	if err := db.Create(&models.ResellerLedgerEntry{
		ResellerID:     profile.ID,
		OrderID:        &orderID,
		Type:           models.ResellerLedgerTypeOrderProfit,
		Amount:         models.NewMoneyFromDecimal(decimal.RequireFromString("30.00")),
		Currency:       "USD",
		IdempotencyKey: "order-profit-status-partially-refunded",
		Status:         models.ResellerLedgerStatusAvailable,
	}).Error; err != nil {
		t.Fatalf("create available ledger failed: %v", err)
	}
	svc := NewResellerOrderService(repository.NewResellerRepository(db))

	rows, _, err := svc.ListUserOrders(profile.UserID, ResellerOrderListInput{Page: 1, PageSize: 20})
	if err != nil {
		t.Fatalf("ListUserOrders failed: %v", err)
	}
	if rows[0].ProfitStatus != ResellerProfitStatusUnavailable {
		t.Fatalf("partially refunded order must not show gross snapshot profit as credited, got %+v", rows[0])
	}
}

func TestResellerOrderServiceDetailUsesItemSnapshot(t *testing.T) {
	db := openResellerOrderServiceTestDB(t)
	profile, order, _ := seedResellerOrderFixture(t, db, "reseller-order-detail@example.test")
	svc := NewResellerOrderService(repository.NewResellerRepository(db))

	detail, err := svc.GetUserOrderDetail(profile.UserID, order.OrderNo)
	if err != nil {
		t.Fatalf("GetUserOrderDetail failed: %v", err)
	}
	if len(detail.Items) != 1 {
		t.Fatalf("expected one item, got %d", len(detail.Items))
	}
	if detail.Items[0].BaseUnitAmount != "50.00" || detail.Items[0].ResellerUnitAmount != "65.00" || detail.Items[0].ProfitAmount != "30.00" {
		t.Fatalf("expected item pricing snapshot, got %+v", detail.Items[0])
	}
}

func TestResellerOrderServiceDetailAggregatesItemsFromChildOrders(t *testing.T) {
	db := openResellerOrderServiceTestDB(t)
	profile, parent, childItems := seedResellerOrderWithChildItemsFixture(t, db, "reseller-child-items@example.test")
	svc := NewResellerOrderService(repository.NewResellerRepository(db))

	rows, _, err := svc.ListUserOrders(profile.UserID, ResellerOrderListInput{Page: 1, PageSize: 20})
	if err != nil {
		t.Fatalf("ListUserOrders failed: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected one order row, got %d", len(rows))
	}
	if rows[0].ItemsCount != len(childItems) {
		t.Fatalf("expected list items_count from child orders=%d, got %d", len(childItems), rows[0].ItemsCount)
	}

	detail, err := svc.GetUserOrderDetail(profile.UserID, parent.OrderNo)
	if err != nil {
		t.Fatalf("GetUserOrderDetail failed: %v", err)
	}
	if len(detail.Items) != len(childItems) {
		t.Fatalf("expected detail items from child orders=%d, got %d", len(childItems), len(detail.Items))
	}
	if detail.Items[0].BaseUnitAmount != "55.00" || detail.Items[0].ResellerUnitAmount != "70.00" || detail.Items[0].ProfitAmount != "15.00" {
		t.Fatalf("expected first child item pricing snapshot, got %+v", detail.Items[0])
	}
	if detail.Items[1].BaseUnitAmount != "65.00" || detail.Items[1].ResellerUnitAmount != "80.00" || detail.Items[1].ProfitAmount != "15.00" {
		t.Fatalf("expected second child item pricing snapshot, got %+v", detail.Items[1])
	}
}

func TestResellerOrderServiceRejectsInactiveProfile(t *testing.T) {
	db := openResellerOrderServiceTestDB(t)
	profile, _, _ := seedResellerOrderFixture(t, db, "inactive-reseller-orders@example.test")
	if err := db.Model(&models.ResellerProfile{}).Where("id = ?", profile.ID).Update("status", models.ResellerProfileStatusPendingReview).Error; err != nil {
		t.Fatalf("update profile failed: %v", err)
	}
	svc := NewResellerOrderService(repository.NewResellerRepository(db))
	_, _, err := svc.ListUserOrders(profile.UserID, ResellerOrderListInput{Page: 1, PageSize: 20})
	if !errors.Is(err, ErrResellerProfileInactive) {
		t.Fatalf("expected ErrResellerProfileInactive, got %v", err)
	}
}
