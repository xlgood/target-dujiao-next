package repository

import (
	"fmt"
	"testing"
	"time"

	"github.com/dujiao-next/internal/constants"
	"github.com/dujiao-next/internal/models"
	"github.com/glebarez/sqlite"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

func openResellerAccountingRepoTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:reseller_accounting_repo_%d?mode=memory&cache=shared", time.Now().UnixNano())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite failed: %v", err)
	}
	if err := db.AutoMigrate(
		&models.Admin{},
		&models.User{},
		&models.Order{},
		&models.Payment{},
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
		t.Fatalf("create user failed: %v", err)
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

func seedResellerAccountingProfileWithEmail(t *testing.T, db *gorm.DB, email string) models.ResellerProfile {
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
		t.Fatalf("create reseller profile failed: %v", err)
	}
	return profile
}

func seedResellerAccountingOrder(t *testing.T, db *gorm.DB, orderNo string) models.Order {
	t.Helper()
	order := models.Order{
		OrderNo:     orderNo,
		Status:      constants.OrderStatusPaid,
		TotalAmount: models.NewMoneyFromDecimal(decimal.NewFromInt(100)),
		Currency:    "USD",
	}
	if err := db.Create(&order).Error; err != nil {
		t.Fatalf("create order failed: %v", err)
	}
	return order
}

func TestResellerAccountingRepositoryLedgerIdempotency(t *testing.T) {
	db := openResellerAccountingRepoTestDB(t)
	profile := seedResellerAccountingProfile(t, db)
	repo := NewResellerRepository(db)
	orderID := uint(100)
	entry := &models.ResellerLedgerEntry{
		ResellerID:     profile.ID,
		OrderID:        &orderID,
		Type:           models.ResellerLedgerTypeOrderProfit,
		Amount:         models.NewMoneyFromDecimal(decimal.RequireFromString("12.34")),
		Currency:       "USD",
		IdempotencyKey: "order_profit:100",
		Status:         models.ResellerLedgerStatusPendingConfirm,
	}
	created, err := repo.CreateLedgerEntryIfNotExists(entry)
	if err != nil {
		t.Fatalf("first create failed: %v", err)
	}
	if !created {
		t.Fatal("first create should report created=true")
	}
	created, err = repo.CreateLedgerEntryIfNotExists(entry)
	if err != nil {
		t.Fatalf("second create failed: %v", err)
	}
	if created {
		t.Fatal("second create should report created=false")
	}
	var count int64
	if err := db.Model(&models.ResellerLedgerEntry{}).Where("idempotency_key = ?", "order_profit:100").Count(&count).Error; err != nil {
		t.Fatalf("count ledger failed: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected one ledger row, got %d", count)
	}
}

func TestResellerAccountingRepositoryMarkDueLedgersAvailable(t *testing.T) {
	db := openResellerAccountingRepoTestDB(t)
	profile := seedResellerAccountingProfile(t, db)
	repo := NewResellerRepository(db)
	now := time.Now()
	past := now.Add(-time.Minute)
	future := now.Add(time.Minute)
	rows := []models.ResellerLedgerEntry{
		{ResellerID: profile.ID, Type: models.ResellerLedgerTypeOrderProfit, Amount: models.NewMoneyFromDecimal(decimal.NewFromInt(10)), Currency: "USD", IdempotencyKey: "order_profit:1", Status: models.ResellerLedgerStatusPendingConfirm, AvailableAt: &past},
		{ResellerID: profile.ID, Type: models.ResellerLedgerTypeOrderProfit, Amount: models.NewMoneyFromDecimal(decimal.NewFromInt(20)), Currency: "USD", IdempotencyKey: "order_profit:2", Status: models.ResellerLedgerStatusPendingConfirm, AvailableAt: &future},
	}
	if err := db.Create(&rows).Error; err != nil {
		t.Fatalf("seed ledger rows failed: %v", err)
	}
	affected, err := repo.MarkDueLedgerEntriesAvailable(now)
	if err != nil {
		t.Fatalf("mark due failed: %v", err)
	}
	if affected != 1 {
		t.Fatalf("expected affected=1, got %d", affected)
	}
	var due models.ResellerLedgerEntry
	if err := db.First(&due, rows[0].ID).Error; err != nil {
		t.Fatalf("load due row failed: %v", err)
	}
	if due.Status != models.ResellerLedgerStatusAvailable {
		t.Fatalf("expected due row available, got %s", due.Status)
	}
}

func TestResellerAccountingRepositoryWithdrawLocksSameCurrencyOnly(t *testing.T) {
	db := openResellerAccountingRepoTestDB(t)
	profile := seedResellerAccountingProfile(t, db)
	repo := NewResellerRepository(db)
	now := time.Now()
	rows := []models.ResellerLedgerEntry{
		{ResellerID: profile.ID, Type: models.ResellerLedgerTypeOrderProfit, Amount: models.NewMoneyFromDecimal(decimal.NewFromInt(10)), Currency: "USD", IdempotencyKey: "order_profit:usd1", Status: models.ResellerLedgerStatusAvailable, AvailableAt: &now},
		{ResellerID: profile.ID, Type: models.ResellerLedgerTypeOrderProfit, Amount: models.NewMoneyFromDecimal(decimal.NewFromInt(20)), Currency: "CNY", IdempotencyKey: "order_profit:cny1", Status: models.ResellerLedgerStatusAvailable, AvailableAt: &now},
	}
	if err := db.Create(&rows).Error; err != nil {
		t.Fatalf("seed ledger rows failed: %v", err)
	}
	locked, err := repo.ListAvailableLedgerEntriesForUpdate(profile.ID, "USD")
	if err != nil {
		t.Fatalf("list available ledgers failed: %v", err)
	}
	if len(locked) != 1 || locked[0].Currency != "USD" {
		t.Fatalf("expected only USD ledger, got %+v", locked)
	}
}

func TestResellerAccountingRepositoryListAdminLedgerEntriesFiltersByKeywordAndOrderNo(t *testing.T) {
	db := openResellerAccountingRepoTestDB(t)
	repo := NewResellerRepository(db)
	profile := seedResellerAccountingProfileWithEmail(t, db, "ledger-admin@example.com")
	other := seedResellerAccountingProfileWithEmail(t, db, "other-ledger-admin@example.com")
	order := seedResellerAccountingOrder(t, db, "RADMIN-ORDER-001")
	now := time.Now().Add(-time.Hour)

	entry := models.ResellerLedgerEntry{
		ResellerID:     profile.ID,
		OrderID:        &order.ID,
		Type:           models.ResellerLedgerTypeOrderProfit,
		Amount:         models.NewMoneyFromDecimal(decimal.NewFromInt(12)),
		Currency:       "USD",
		IdempotencyKey: "admin-ledger-filter-1",
		Status:         models.ResellerLedgerStatusAvailable,
		AvailableAt:    &now,
	}
	otherEntry := models.ResellerLedgerEntry{
		ResellerID:     other.ID,
		Type:           models.ResellerLedgerTypeOrderProfit,
		Amount:         models.NewMoneyFromDecimal(decimal.NewFromInt(8)),
		Currency:       "USD",
		IdempotencyKey: "admin-ledger-filter-2",
		Status:         models.ResellerLedgerStatusAvailable,
	}
	if err := db.Create(&entry).Error; err != nil {
		t.Fatalf("create ledger failed: %v", err)
	}
	if err := db.Create(&otherEntry).Error; err != nil {
		t.Fatalf("create other ledger failed: %v", err)
	}

	rows, total, err := repo.ListAdminResellerLedgerEntries(ResellerAdminLedgerListFilter{
		Page:     1,
		PageSize: 20,
		Keyword:  "ledger-admin@example.com",
		OrderNo:  "RADMIN-ORDER-001",
		Currency: "USD",
		Status:   models.ResellerLedgerStatusAvailable,
	})
	if err != nil {
		t.Fatalf("ListAdminResellerLedgerEntries failed: %v", err)
	}
	if total != 1 || len(rows) != 1 {
		t.Fatalf("expected one ledger row, total=%d len=%d rows=%+v", total, len(rows), rows)
	}
	if rows[0].Profile == nil || rows[0].Profile.User == nil || rows[0].Profile.User.Email != "ledger-admin@example.com" {
		t.Fatalf("expected profile user preload, got %+v", rows[0].Profile)
	}
	if rows[0].Order == nil || rows[0].Order.OrderNo != "RADMIN-ORDER-001" {
		t.Fatalf("expected order preload, got %+v", rows[0].Order)
	}
}

func TestResellerAccountingRepositoryListAdminBalanceAccountsFiltersAndPreloadsProfile(t *testing.T) {
	db := openResellerAccountingRepoTestDB(t)
	repo := NewResellerRepository(db)
	profile := seedResellerAccountingProfileWithEmail(t, db, "balance-admin@example.com")
	other := seedResellerAccountingProfileWithEmail(t, db, "other-balance-admin@example.com")

	rows := []models.ResellerBalanceAccount{
		{
			ResellerID:           profile.ID,
			Currency:             "USD",
			Status:               models.ResellerBalanceStatusNormal,
			AvailableAmountCache: models.NewMoneyFromDecimal(decimal.NewFromInt(100)),
			LockedAmountCache:    models.NewMoneyFromDecimal(decimal.NewFromInt(10)),
			NegativeAmountCache:  models.NewMoneyFromDecimal(decimal.Zero),
			LastLedgerEntryID:    99,
		},
		{
			ResellerID:           other.ID,
			Currency:             "CNY",
			Status:               models.ResellerBalanceStatusNormal,
			AvailableAmountCache: models.NewMoneyFromDecimal(decimal.NewFromInt(200)),
		},
	}
	if err := db.Create(&rows).Error; err != nil {
		t.Fatalf("create balance accounts failed: %v", err)
	}

	got, total, err := repo.ListAdminResellerBalanceAccounts(ResellerAdminBalanceAccountListFilter{
		Page:     1,
		PageSize: 20,
		Keyword:  "balance-admin@example.com",
		Currency: "USD",
		Status:   models.ResellerBalanceStatusNormal,
	})
	if err != nil {
		t.Fatalf("ListAdminResellerBalanceAccounts failed: %v", err)
	}
	if total != 1 || len(got) != 1 {
		t.Fatalf("expected one balance row, total=%d len=%d rows=%+v", total, len(got), got)
	}
	if got[0].Profile == nil || got[0].Profile.User == nil || got[0].Profile.User.Email != "balance-admin@example.com" {
		t.Fatalf("expected profile user preload, got %+v", got[0].Profile)
	}
}

func TestResellerAccountingRepositoryListAdminWithdrawRequestsFiltersAndPreloadsProcessor(t *testing.T) {
	db := openResellerAccountingRepoTestDB(t)
	repo := NewResellerRepository(db)
	profile := seedResellerAccountingProfileWithEmail(t, db, "withdraw-admin@example.com")
	admin := models.Admin{Username: "reviewer", PasswordHash: "hash"}
	if err := db.Create(&admin).Error; err != nil {
		t.Fatalf("create admin failed: %v", err)
	}
	now := time.Now()
	req := models.ResellerWithdrawRequest{
		ResellerID:  profile.ID,
		Amount:      models.NewMoneyFromDecimal(decimal.NewFromInt(50)),
		Currency:    "USD",
		Channel:     "USDT",
		Account:     "TwithdrawAdmin",
		Status:      models.ResellerWithdrawStatusPaid,
		ProcessedBy: &admin.ID,
		ProcessedAt: &now,
	}
	if err := db.Create(&req).Error; err != nil {
		t.Fatalf("create withdraw request failed: %v", err)
	}

	got, total, err := repo.ListAdminResellerWithdrawRequests(ResellerAdminWithdrawListFilter{
		Page:     1,
		PageSize: 20,
		Keyword:  "TwithdrawAdmin",
		Currency: "USD",
		Status:   models.ResellerWithdrawStatusPaid,
	})
	if err != nil {
		t.Fatalf("ListAdminResellerWithdrawRequests failed: %v", err)
	}
	if total != 1 || len(got) != 1 {
		t.Fatalf("expected one withdraw row, total=%d len=%d rows=%+v", total, len(got), got)
	}
	if got[0].Profile == nil || got[0].Profile.User == nil || got[0].Profile.User.Email != "withdraw-admin@example.com" {
		t.Fatalf("expected profile user preload, got %+v", got[0].Profile)
	}
	if got[0].Processor == nil || got[0].Processor.Username != "reviewer" {
		t.Fatalf("expected processor preload, got %+v", got[0].Processor)
	}
}
