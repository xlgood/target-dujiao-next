package service

import (
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

func openResellerAccountingServiceTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:reseller_accounting_service_%d?mode=memory&cache=shared", time.Now().UnixNano())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite failed: %v", err)
	}
	if err := db.AutoMigrate(
		&models.User{},
		&models.Order{},
		&models.Payment{},
		&models.PaymentChannel{},
		&models.OrderRefundRecord{},
		&models.ResellerProfile{},
		&models.ResellerOrderSnapshot{},
		&models.ResellerLedgerEntry{},
		&models.ResellerWithdrawRequest{},
		&models.ResellerBalanceAccount{},
	); err != nil {
		t.Fatalf("migrate failed: %v", err)
	}
	return db
}

func seedResellerAccountingProfile(t *testing.T, db *gorm.DB) models.ResellerProfile {
	t.Helper()
	user := models.User{Email: fmt.Sprintf("reseller-%d@example.test", time.Now().UnixNano()), PasswordHash: "x"}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create reseller user failed: %v", err)
	}
	profile := models.ResellerProfile{
		UserID:           user.ID,
		Status:           models.ResellerProfileStatusActive,
		SettlementStatus: models.ResellerSettlementStatusNormal,
	}
	if err := db.Create(&profile).Error; err != nil {
		t.Fatalf("create reseller profile failed: %v", err)
	}
	return profile
}

func TestResellerAccountingServiceListAdminWithdrawRequests(t *testing.T) {
	db := openResellerAccountingServiceTestDB(t)
	repo := repository.NewResellerRepository(db)
	svc := NewResellerAccountingService(repo, ResellerAccountingOptions{ConfirmDays: 7})
	profile := seedResellerAccountingProfile(t, db)
	req := models.ResellerWithdrawRequest{
		ResellerID: profile.ID,
		Amount:     models.NewMoneyFromDecimal(decimal.NewFromInt(25)),
		Currency:   "USD",
		Channel:    "USDT",
		Account:    "TserviceWithdraw",
		Status:     models.ResellerWithdrawStatusPending,
	}
	if err := db.Create(&req).Error; err != nil {
		t.Fatalf("create withdraw failed: %v", err)
	}

	rows, total, err := svc.ListAdminWithdrawRequests(ResellerAdminWithdrawListFilter{
		Page:       1,
		PageSize:   20,
		ResellerID: profile.ID,
		Currency:   " USD ",
		Status:     " pending ",
	})
	if err != nil {
		t.Fatalf("ListAdminWithdrawRequests failed: %v", err)
	}
	if total != 1 || len(rows) != 1 || rows[0].ID != req.ID {
		t.Fatalf("expected created withdraw, total=%d rows=%+v", total, rows)
	}
}

func seedPaidResellerOrderSnapshot(t *testing.T, db *gorm.DB, eligible bool) (models.Order, models.Payment, models.ResellerOrderSnapshot) {
	t.Helper()
	user := models.User{Email: fmt.Sprintf("buyer-%d@example.test", time.Now().UnixNano()), PasswordHash: "x"}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user failed: %v", err)
	}
	profile := models.ResellerProfile{UserID: user.ID, Status: models.ResellerProfileStatusActive, SettlementStatus: models.ResellerSettlementStatusNormal}
	if err := db.Create(&profile).Error; err != nil {
		t.Fatalf("create profile failed: %v", err)
	}
	resellerID := profile.ID
	now := time.Now()
	order := models.Order{
		OrderNo:              fmt.Sprintf("DJ-RES-%d", now.UnixNano()),
		UserID:               user.ID,
		Status:               constants.OrderStatusPaid,
		TotalAmount:          models.NewMoneyFromDecimal(decimal.NewFromInt(130)),
		OriginalAmount:       models.NewMoneyFromDecimal(decimal.NewFromInt(130)),
		Currency:             "USD",
		WalletPaidAmount:     models.NewMoneyFromDecimal(decimal.NewFromInt(30)),
		OnlinePaidAmount:     models.NewMoneyFromDecimal(decimal.NewFromInt(100)),
		ResellerID:           &resellerID,
		ResellerDomain:       "shop.example.test",
		ResellerProfitAmount: models.NewMoneyFromDecimal(decimal.NewFromInt(30)),
		PaidAt:               &now,
		CreatedAt:            now,
		UpdatedAt:            now,
	}
	if err := db.Create(&order).Error; err != nil {
		t.Fatalf("create order failed: %v", err)
	}
	channel := models.PaymentChannel{
		Name:         "Stripe",
		ProviderType: constants.PaymentProviderOfficial,
		ChannelType:  constants.PaymentChannelTypeStripe,
		IsActive:     true,
	}
	if err := db.Create(&channel).Error; err != nil {
		t.Fatalf("create payment channel failed: %v", err)
	}
	payment := models.Payment{
		OrderID:   order.ID,
		ChannelID: channel.ID,
		Status:    constants.PaymentStatusSuccess,
		Amount:    models.NewMoneyFromDecimal(decimal.NewFromInt(100)),
		Currency:  "USD",
		PaidAt:    &now,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := db.Create(&payment).Error; err != nil {
		t.Fatalf("create payment failed: %v", err)
	}
	snapshot := models.ResellerOrderSnapshot{
		OrderID:           order.ID,
		ResellerID:        profile.ID,
		Domain:            "shop.example.test",
		Currency:          "USD",
		ResellerUserID:    profile.UserID,
		BuyerUserID:       user.ID,
		BaseAmount:        models.NewMoneyFromDecimal(decimal.NewFromInt(100)),
		ResellerAmount:    models.NewMoneyFromDecimal(decimal.NewFromInt(130)),
		ProfitAmount:      models.NewMoneyFromDecimal(decimal.NewFromInt(30)),
		ProfitEligible:    eligible,
		ProfitBlockReason: "",
		PricingSnapshotJSON: models.JSON{
			"base_amount":     "100.00",
			"reseller_amount": "130.00",
			"profit_amount":   "30.00",
			"items": []interface{}{
				map[string]interface{}{
					"order_item_id":         "1",
					"product_id":            "10",
					"sku_id":                "100",
					"quantity":              "2",
					"base_total_amount":     "100.00",
					"reseller_total_amount": "130.00",
					"profit_amount":         "30.00",
				},
			},
		},
		RiskSnapshotJSON: models.JSON{"profit_eligible": eligible},
		CreatedAt:        now,
		UpdatedAt:        now,
	}
	if !eligible {
		snapshot.ProfitBlockReason = "self_dealing_owner"
	}
	if err := db.Create(&snapshot).Error; err != nil {
		t.Fatalf("create snapshot failed: %v", err)
	}
	if !eligible {
		if err := db.Model(&snapshot).Update("profit_eligible", false).Error; err != nil {
			t.Fatalf("force snapshot profit_eligible=false failed: %v", err)
		}
		snapshot.ProfitEligible = false
	}
	return order, payment, snapshot
}

func TestResellerAccountingPostOrderProfitIdempotent(t *testing.T) {
	db := openResellerAccountingServiceTestDB(t)
	order, payment, snapshot := seedPaidResellerOrderSnapshot(t, db, true)
	repo := repository.NewResellerRepository(db)
	svc := NewResellerAccountingService(repo, ResellerAccountingOptions{ConfirmDays: 7})
	err := repo.Transaction(func(tx *gorm.DB) error {
		return svc.PostOrderProfitTx(tx, &order, &payment)
	})
	if err != nil {
		t.Fatalf("first post failed: %v", err)
	}
	err = repo.Transaction(func(tx *gorm.DB) error {
		return svc.PostOrderProfitTx(tx, &order, &payment)
	})
	if err != nil {
		t.Fatalf("second post failed: %v", err)
	}
	var rows []models.ResellerLedgerEntry
	if err := db.Find(&rows).Error; err != nil {
		t.Fatalf("list ledger failed: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected one ledger row, got %d", len(rows))
	}
	if rows[0].ResellerID != snapshot.ResellerID || rows[0].Amount.String() != "30.00" || rows[0].Currency != "USD" {
		t.Fatalf("unexpected ledger row: %+v", rows[0])
	}
	if rows[0].Status != models.ResellerLedgerStatusPendingConfirm {
		t.Fatalf("expected pending_confirm, got %s", rows[0].Status)
	}
	if rows[0].AvailableAt == nil || rows[0].AvailableAt.Before(time.Now().Add(6*24*time.Hour)) {
		t.Fatalf("expected available_at roughly 7 days later, got %v", rows[0].AvailableAt)
	}
}

func TestResellerAccountingSkipsSelfDealingSnapshot(t *testing.T) {
	db := openResellerAccountingServiceTestDB(t)
	order, payment, _ := seedPaidResellerOrderSnapshot(t, db, false)
	repo := repository.NewResellerRepository(db)
	svc := NewResellerAccountingService(repo, ResellerAccountingOptions{ConfirmDays: 7})
	if err := repo.Transaction(func(tx *gorm.DB) error {
		return svc.PostOrderProfitTx(tx, &order, &payment)
	}); err != nil {
		t.Fatalf("post self-dealing order failed: %v", err)
	}
	var count int64
	if err := db.Model(&models.ResellerLedgerEntry{}).Count(&count).Error; err != nil {
		t.Fatalf("count ledger failed: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected no ledger row for self-dealing, got %d", count)
	}
}

func TestResellerAccountingMissingSnapshotSkipsWithoutRollingBack(t *testing.T) {
	db := openResellerAccountingServiceTestDB(t)
	order, payment, snapshot := seedPaidResellerOrderSnapshot(t, db, true)
	if err := db.Delete(&snapshot).Error; err != nil {
		t.Fatalf("delete snapshot failed: %v", err)
	}
	repo := repository.NewResellerRepository(db)
	svc := NewResellerAccountingService(repo, ResellerAccountingOptions{ConfirmDays: 7})
	if err := repo.Transaction(func(tx *gorm.DB) error {
		return svc.PostOrderProfitTx(tx, &order, &payment)
	}); err != nil {
		t.Fatalf("post order profit with missing snapshot should skip, got %v", err)
	}
	var count int64
	if err := db.Model(&models.ResellerLedgerEntry{}).Count(&count).Error; err != nil {
		t.Fatalf("count ledger failed: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected no ledger row for missing snapshot, got %d", count)
	}
}

func TestResellerAccountingConfirmDueLedgerEntries(t *testing.T) {
	db := openResellerAccountingServiceTestDB(t)
	order, payment, _ := seedPaidResellerOrderSnapshot(t, db, true)
	repo := repository.NewResellerRepository(db)
	svc := NewResellerAccountingService(repo, ResellerAccountingOptions{ConfirmDays: 0})
	if err := repo.Transaction(func(tx *gorm.DB) error {
		return svc.PostOrderProfitTx(tx, &order, &payment)
	}); err != nil {
		t.Fatalf("post order profit failed: %v", err)
	}
	affected, err := svc.ConfirmDueLedgerEntries(time.Now().Add(time.Second))
	if err != nil {
		t.Fatalf("confirm due failed: %v", err)
	}
	if affected != 1 {
		t.Fatalf("expected affected=1, got %d", affected)
	}
	var row models.ResellerLedgerEntry
	if err := db.First(&row).Error; err != nil {
		t.Fatalf("load ledger failed: %v", err)
	}
	if row.Status != models.ResellerLedgerStatusAvailable {
		t.Fatalf("expected available, got %s", row.Status)
	}
}

func TestPaymentSuccessTransactionPostsResellerLedger(t *testing.T) {
	db := openResellerAccountingServiceTestDB(t)
	order, payment, _ := seedPaidResellerOrderSnapshot(t, db, true)
	order.Status = constants.OrderStatusPendingPayment
	order.PaidAt = nil
	if err := db.Save(&order).Error; err != nil {
		t.Fatalf("reset order failed: %v", err)
	}
	repo := repository.NewResellerRepository(db)
	accounting := NewResellerAccountingService(repo, ResellerAccountingOptions{ConfirmDays: 0})
	orderRepo := repository.NewOrderRepository(db)
	paymentRepo := repository.NewPaymentRepository(db)
	productRepo := repository.NewProductRepository(db)
	productSKURepo := repository.NewProductSKURepository(db)
	paymentSvc := NewPaymentService(PaymentServiceOptions{
		OrderRepo:                 orderRepo,
		PaymentRepo:               paymentRepo,
		ProductRepo:               productRepo,
		ProductSKURepo:            productSKURepo,
		ResellerAccountingService: accounting,
	})
	_, orderPaid, err := paymentSvc.applyPaymentUpdate(&payment, &order, constants.PaymentStatusSuccess, PaymentCallbackInput{}, time.Now())
	if err != nil {
		t.Fatalf("apply payment update failed: %v", err)
	}
	if !orderPaid {
		t.Fatal("expected orderPaid=true")
	}
	var count int64
	if err := db.Model(&models.ResellerLedgerEntry{}).Where("idempotency_key = ?", fmt.Sprintf("order_profit:%d", order.ID)).Count(&count).Error; err != nil {
		t.Fatalf("count reseller ledger failed: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected reseller ledger created, got %d", count)
	}
}

func TestResellerAccountingRefundDeductUsesSnapshotItems(t *testing.T) {
	db := openResellerAccountingServiceTestDB(t)
	order, payment, snapshot := seedPaidResellerOrderSnapshot(t, db, true)
	repo := repository.NewResellerRepository(db)
	svc := NewResellerAccountingService(repo, ResellerAccountingOptions{ConfirmDays: 0})
	if err := repo.Transaction(func(tx *gorm.DB) error {
		return svc.PostOrderProfitTx(tx, &order, &payment)
	}); err != nil {
		t.Fatalf("post profit failed: %v", err)
	}
	refundRecord := models.OrderRefundRecord{
		UserID:    order.UserID,
		OrderID:   order.ID,
		Type:      constants.OrderRefundTypeManual,
		Amount:    models.NewMoneyFromDecimal(decimal.NewFromInt(65)),
		Currency:  "USD",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	if err := db.Create(&refundRecord).Error; err != nil {
		t.Fatalf("create refund record failed: %v", err)
	}
	if err := repo.Transaction(func(tx *gorm.DB) error {
		return svc.HandleRefundDeductTx(tx, &order, &refundRecord, decimal.Zero)
	}); err != nil {
		t.Fatalf("refund deduct failed: %v", err)
	}
	var deduct models.ResellerLedgerEntry
	if err := db.Where("idempotency_key = ?", fmt.Sprintf("refund_deduct:%d", refundRecord.ID)).First(&deduct).Error; err != nil {
		t.Fatalf("load deduct ledger failed: %v", err)
	}
	if deduct.ResellerID != snapshot.ResellerID || deduct.Type != models.ResellerLedgerTypeRefundDeduct || deduct.Currency != "USD" {
		t.Fatalf("unexpected deduct row: %+v", deduct)
	}
	if deduct.Amount.String() != "-15.00" {
		t.Fatalf("expected half profit deduction -15.00, got %s", deduct.Amount.String())
	}
	if _, ok := deduct.MetadataJSON["refund_allocation_json"]; !ok {
		t.Fatalf("expected refund_allocation_json metadata, got %+v", deduct.MetadataJSON)
	}
}

func TestResellerAccountingRefundDeductIsIdempotent(t *testing.T) {
	db := openResellerAccountingServiceTestDB(t)
	order, payment, _ := seedPaidResellerOrderSnapshot(t, db, true)
	repo := repository.NewResellerRepository(db)
	svc := NewResellerAccountingService(repo, ResellerAccountingOptions{ConfirmDays: 0})
	if err := repo.Transaction(func(tx *gorm.DB) error {
		return svc.PostOrderProfitTx(tx, &order, &payment)
	}); err != nil {
		t.Fatalf("post profit failed: %v", err)
	}
	refundRecord := models.OrderRefundRecord{UserID: order.UserID, OrderID: order.ID, Type: constants.OrderRefundTypeManual, Amount: models.NewMoneyFromDecimal(decimal.NewFromInt(65)), Currency: "USD"}
	if err := db.Create(&refundRecord).Error; err != nil {
		t.Fatalf("create refund record failed: %v", err)
	}
	for i := 0; i < 2; i++ {
		if err := repo.Transaction(func(tx *gorm.DB) error {
			return svc.HandleRefundDeductTx(tx, &order, &refundRecord, decimal.Zero)
		}); err != nil {
			t.Fatalf("refund deduct attempt %d failed: %v", i+1, err)
		}
	}
	var count int64
	if err := db.Model(&models.ResellerLedgerEntry{}).Where("type = ?", models.ResellerLedgerTypeRefundDeduct).Count(&count).Error; err != nil {
		t.Fatalf("count deduct failed: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected one refund deduct row, got %d", count)
	}
}

func TestResellerAccountingRefundDeductSkipsIneligibleSnapshot(t *testing.T) {
	db := openResellerAccountingServiceTestDB(t)
	order, _, _ := seedPaidResellerOrderSnapshot(t, db, false)
	repo := repository.NewResellerRepository(db)
	svc := NewResellerAccountingService(repo, ResellerAccountingOptions{ConfirmDays: 0})
	refundRecord := models.OrderRefundRecord{UserID: order.UserID, OrderID: order.ID, Type: constants.OrderRefundTypeManual, Amount: models.NewMoneyFromDecimal(decimal.NewFromInt(65)), Currency: "USD"}
	if err := db.Create(&refundRecord).Error; err != nil {
		t.Fatalf("create refund record failed: %v", err)
	}
	if err := repo.Transaction(func(tx *gorm.DB) error {
		return svc.HandleRefundDeductTx(tx, &order, &refundRecord, decimal.Zero)
	}); err != nil {
		t.Fatalf("refund deduct for ineligible snapshot failed: %v", err)
	}
	var count int64
	if err := db.Model(&models.ResellerLedgerEntry{}).Where("type = ?", models.ResellerLedgerTypeRefundDeduct).Count(&count).Error; err != nil {
		t.Fatalf("count refund deduct failed: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected no refund deduct row for ineligible snapshot, got %d", count)
	}
}

func TestResellerAccountingRefundDeductMissingSnapshotSkipsWithoutRollingBack(t *testing.T) {
	db := openResellerAccountingServiceTestDB(t)
	order, _, snapshot := seedPaidResellerOrderSnapshot(t, db, true)
	if err := db.Delete(&snapshot).Error; err != nil {
		t.Fatalf("delete snapshot failed: %v", err)
	}
	repo := repository.NewResellerRepository(db)
	svc := NewResellerAccountingService(repo, ResellerAccountingOptions{ConfirmDays: 0})
	refundRecord := models.OrderRefundRecord{UserID: order.UserID, OrderID: order.ID, Type: constants.OrderRefundTypeManual, Amount: models.NewMoneyFromDecimal(decimal.NewFromInt(65)), Currency: "USD"}
	if err := db.Create(&refundRecord).Error; err != nil {
		t.Fatalf("create refund record failed: %v", err)
	}
	if err := repo.Transaction(func(tx *gorm.DB) error {
		return svc.HandleRefundDeductTx(tx, &order, &refundRecord, decimal.Zero)
	}); err != nil {
		t.Fatalf("refund deduct with missing snapshot should skip, got %v", err)
	}
	var count int64
	if err := db.Model(&models.ResellerLedgerEntry{}).Where("type = ?", models.ResellerLedgerTypeRefundDeduct).Count(&count).Error; err != nil {
		t.Fatalf("count refund deduct failed: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected no refund deduct row for missing snapshot, got %d", count)
	}
}

func TestResellerAccountingApplyWithdrawLocksSameCurrencyLedgers(t *testing.T) {
	db := openResellerAccountingServiceTestDB(t)
	profile := seedResellerAccountingProfile(t, db)
	now := time.Now()
	rows := []models.ResellerLedgerEntry{
		{ResellerID: profile.ID, Type: models.ResellerLedgerTypeOrderProfit, Amount: models.NewMoneyFromDecimal(decimal.NewFromInt(10)), Currency: "USD", IdempotencyKey: "order_profit:w-usd-1", Status: models.ResellerLedgerStatusAvailable, AvailableAt: &now},
		{ResellerID: profile.ID, Type: models.ResellerLedgerTypeOrderProfit, Amount: models.NewMoneyFromDecimal(decimal.NewFromInt(15)), Currency: "USD", IdempotencyKey: "order_profit:w-usd-2", Status: models.ResellerLedgerStatusAvailable, AvailableAt: &now},
		{ResellerID: profile.ID, Type: models.ResellerLedgerTypeOrderProfit, Amount: models.NewMoneyFromDecimal(decimal.NewFromInt(20)), Currency: "CNY", IdempotencyKey: "order_profit:w-cny-1", Status: models.ResellerLedgerStatusAvailable, AvailableAt: &now},
	}
	if err := db.Create(&rows).Error; err != nil {
		t.Fatalf("seed ledger rows failed: %v", err)
	}
	repo := repository.NewResellerRepository(db)
	svc := NewResellerAccountingService(repo, ResellerAccountingOptions{ConfirmDays: 0})
	req, err := svc.ApplyWithdraw(profile.ID, ResellerWithdrawApplyInput{
		Amount:   decimal.NewFromInt(12),
		Currency: "USD",
		Channel:  "usdt",
		Account:  "T-address",
	})
	if err != nil {
		t.Fatalf("apply withdraw failed: %v", err)
	}
	if req.Status != models.ResellerWithdrawStatusPending || req.Currency != "USD" || req.Amount.String() != "12.00" {
		t.Fatalf("unexpected withdraw request: %+v", req)
	}
	var locked []models.ResellerLedgerEntry
	if err := db.Where("withdraw_request_id = ?", req.ID).Find(&locked).Error; err != nil {
		t.Fatalf("load locked ledgers failed: %v", err)
	}
	if len(locked) != 2 {
		t.Fatalf("expected split and locked two USD rows, got %+v", locked)
	}
	for _, row := range locked {
		if row.Currency != "USD" || row.Status != models.ResellerLedgerStatusLocked {
			t.Fatalf("unexpected locked row: %+v", row)
		}
	}
	var cnyCount int64
	if err := db.Model(&models.ResellerLedgerEntry{}).Where("currency = ? AND status = ?", "CNY", models.ResellerLedgerStatusAvailable).Count(&cnyCount).Error; err != nil {
		t.Fatalf("count CNY available failed: %v", err)
	}
	if cnyCount != 1 {
		t.Fatalf("CNY ledger should remain available, got %d", cnyCount)
	}
}

func TestResellerAccountingRejectWithdrawUnlocksLedgers(t *testing.T) {
	db := openResellerAccountingServiceTestDB(t)
	profile := seedResellerAccountingProfile(t, db)
	now := time.Now()
	row := models.ResellerLedgerEntry{ResellerID: profile.ID, Type: models.ResellerLedgerTypeOrderProfit, Amount: models.NewMoneyFromDecimal(decimal.NewFromInt(10)), Currency: "USD", IdempotencyKey: "order_profit:reject", Status: models.ResellerLedgerStatusAvailable, AvailableAt: &now}
	if err := db.Create(&row).Error; err != nil {
		t.Fatalf("seed ledger failed: %v", err)
	}
	repo := repository.NewResellerRepository(db)
	svc := NewResellerAccountingService(repo, ResellerAccountingOptions{ConfirmDays: 0})
	req, err := svc.ApplyWithdraw(profile.ID, ResellerWithdrawApplyInput{Amount: decimal.NewFromInt(10), Currency: "USD", Channel: "usdt", Account: "T-address"})
	if err != nil {
		t.Fatalf("apply withdraw failed: %v", err)
	}
	reviewed, err := svc.ReviewWithdraw(99, req.ID, resellerWithdrawActionReject, "bad account")
	if err != nil {
		t.Fatalf("reject withdraw failed: %v", err)
	}
	if reviewed.Status != models.ResellerWithdrawStatusRejected {
		t.Fatalf("expected rejected, got %s", reviewed.Status)
	}
	var unlocked models.ResellerLedgerEntry
	if err := db.First(&unlocked, row.ID).Error; err != nil {
		t.Fatalf("load ledger failed: %v", err)
	}
	if unlocked.Status != models.ResellerLedgerStatusAvailable || unlocked.WithdrawRequestID != nil {
		t.Fatalf("expected unlocked available ledger, got %+v", unlocked)
	}
}

func TestResellerAccountingPayWithdrawMarksLedgersWithdrawn(t *testing.T) {
	db := openResellerAccountingServiceTestDB(t)
	profile := seedResellerAccountingProfile(t, db)
	now := time.Now()
	row := models.ResellerLedgerEntry{ResellerID: profile.ID, Type: models.ResellerLedgerTypeOrderProfit, Amount: models.NewMoneyFromDecimal(decimal.NewFromInt(10)), Currency: "USD", IdempotencyKey: "order_profit:pay", Status: models.ResellerLedgerStatusAvailable, AvailableAt: &now}
	if err := db.Create(&row).Error; err != nil {
		t.Fatalf("seed ledger failed: %v", err)
	}
	repo := repository.NewResellerRepository(db)
	svc := NewResellerAccountingService(repo, ResellerAccountingOptions{ConfirmDays: 0})
	req, err := svc.ApplyWithdraw(profile.ID, ResellerWithdrawApplyInput{Amount: decimal.NewFromInt(10), Currency: "USD", Channel: "usdt", Account: "T-address"})
	if err != nil {
		t.Fatalf("apply withdraw failed: %v", err)
	}
	reviewed, err := svc.ReviewWithdraw(99, req.ID, resellerWithdrawActionPay, "")
	if err != nil {
		t.Fatalf("pay withdraw failed: %v", err)
	}
	if reviewed.Status != models.ResellerWithdrawStatusPaid {
		t.Fatalf("expected paid, got %s", reviewed.Status)
	}
	var withdrawn models.ResellerLedgerEntry
	if err := db.First(&withdrawn, row.ID).Error; err != nil {
		t.Fatalf("load ledger failed: %v", err)
	}
	if withdrawn.Status != models.ResellerLedgerStatusWithdrawn || withdrawn.WithdrawRequestID == nil || *withdrawn.WithdrawRequestID != req.ID {
		t.Fatalf("expected withdrawn ledger, got %+v", withdrawn)
	}
}
