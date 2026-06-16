package admin

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/dujiao-next/internal/constants"
	"github.com/dujiao-next/internal/models"
	"github.com/dujiao-next/internal/provider"
	"github.com/dujiao-next/internal/repository"
	"github.com/dujiao-next/internal/service"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

func setupAdminResellerFinanceHandlerTest(t *testing.T) (*Handler, *gorm.DB) {
	t.Helper()
	gin.SetMode(gin.TestMode)

	dsn := fmt.Sprintf("file:admin_reseller_finance_handler_test_%d?mode=memory&cache=shared", time.Now().UnixNano())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite failed: %v", err)
	}
	if err := db.AutoMigrate(
		&models.Admin{},
		&models.User{},
		&models.Order{},
		&models.ResellerProfile{},
		&models.ResellerLedgerEntry{},
		&models.ResellerWithdrawRequest{},
		&models.ResellerBalanceAccount{},
	); err != nil {
		t.Fatalf("auto migrate failed: %v", err)
	}

	resellerRepo := repository.NewResellerRepository(db)
	h := New(&provider.Container{
		ResellerRepo:              resellerRepo,
		ResellerAccountingService: service.NewResellerAccountingService(resellerRepo, service.ResellerAccountingOptions{ConfirmDays: 7}),
	})
	return h, db
}

func seedAdminResellerFinanceProfile(t *testing.T, db *gorm.DB, email string) models.ResellerProfile {
	t.Helper()
	user := models.User{
		Email:        email,
		DisplayName:  "Reseller " + email,
		PasswordHash: "hash",
		Status:       constants.UserStatusActive,
	}
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

func seedAdminResellerFinanceLedger(t *testing.T, db *gorm.DB, profile models.ResellerProfile, orderNo string, key string, amount decimal.Decimal) models.ResellerLedgerEntry {
	t.Helper()
	now := time.Now().UTC().Add(-time.Hour)
	order := models.Order{
		OrderNo:     orderNo,
		Status:      constants.OrderStatusPaid,
		TotalAmount: models.NewMoneyFromDecimal(decimal.NewFromInt(100)),
		Currency:    "USD",
	}
	if err := db.Create(&order).Error; err != nil {
		t.Fatalf("create order failed: %v", err)
	}
	entry := models.ResellerLedgerEntry{
		ResellerID:     profile.ID,
		OrderID:        &order.ID,
		Type:           models.ResellerLedgerTypeOrderProfit,
		Amount:         models.NewMoneyFromDecimal(amount),
		Currency:       "USD",
		IdempotencyKey: key,
		Status:         models.ResellerLedgerStatusAvailable,
		AvailableAt:    &now,
	}
	if err := db.Create(&entry).Error; err != nil {
		t.Fatalf("create ledger failed: %v", err)
	}
	return entry
}

func TestAdminResellerFinanceListLedgerEntries(t *testing.T) {
	h, db := setupAdminResellerFinanceHandlerTest(t)
	profile := seedAdminResellerFinanceProfile(t, db, "reseller-ledger-admin@example.com")
	entry := seedAdminResellerFinanceLedger(t, db, profile, "RFIN-ORDER-001", "admin-ledger-list-1", decimal.NewFromInt(12))
	other := seedAdminResellerFinanceProfile(t, db, "other-ledger-admin@example.com")
	seedAdminResellerFinanceLedger(t, db, other, "RFIN-ORDER-002", "admin-ledger-list-2", decimal.NewFromInt(8))

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/admin/resellers/ledger-entries?page=1&page_size=20&keyword=reseller-ledger-admin@example.com&order_no=RFIN-ORDER-001", nil)

	h.ListResellerLedgerEntries(c)

	if w.Code != http.StatusOK {
		t.Fatalf("status want 200 got %d body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		StatusCode int `json:"status_code"`
		Pagination struct {
			Total int64 `json:"total"`
		} `json:"pagination"`
		Data []map[string]interface{} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response failed: %v", err)
	}
	if resp.StatusCode != 0 {
		t.Fatalf("status_code want 0 got %d", resp.StatusCode)
	}
	if resp.Pagination.Total != 1 || len(resp.Data) != 1 {
		t.Fatalf("unexpected list result total=%d len=%d data=%+v", resp.Pagination.Total, len(resp.Data), resp.Data)
	}
	if uint(resp.Data[0]["id"].(float64)) != entry.ID {
		t.Fatalf("unexpected ledger id: %+v", resp.Data[0]["id"])
	}
	profileData, ok := resp.Data[0]["profile"].(map[string]interface{})
	if !ok || uint(profileData["id"].(float64)) != profile.ID {
		t.Fatalf("expected profile in response, got %+v", resp.Data[0]["profile"])
	}
}

func TestAdminResellerFinanceListBalanceAccounts(t *testing.T) {
	h, db := setupAdminResellerFinanceHandlerTest(t)
	profile := seedAdminResellerFinanceProfile(t, db, "reseller-balance-admin@example.com")
	account := models.ResellerBalanceAccount{
		ResellerID:           profile.ID,
		Currency:             "USD",
		Status:               models.ResellerBalanceStatusNormal,
		AvailableAmountCache: models.NewMoneyFromDecimal(decimal.NewFromInt(100)),
		LockedAmountCache:    models.NewMoneyFromDecimal(decimal.NewFromInt(20)),
		NegativeAmountCache:  models.NewMoneyFromDecimal(decimal.Zero),
	}
	if err := db.Create(&account).Error; err != nil {
		t.Fatalf("create balance account failed: %v", err)
	}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/admin/resellers/balance-accounts?page=1&page_size=20&keyword=reseller-balance-admin@example.com&currency=USD", nil)

	h.ListResellerBalanceAccounts(c)

	if w.Code != http.StatusOK {
		t.Fatalf("status want 200 got %d body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		StatusCode int `json:"status_code"`
		Pagination struct {
			Total int64 `json:"total"`
		} `json:"pagination"`
		Data []map[string]interface{} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response failed: %v", err)
	}
	if resp.StatusCode != 0 || resp.Pagination.Total != 1 || len(resp.Data) != 1 {
		t.Fatalf("unexpected response: %+v", resp)
	}
	if uint(resp.Data[0]["id"].(float64)) != account.ID {
		t.Fatalf("unexpected balance account id: %+v", resp.Data[0]["id"])
	}
}

func TestAdminResellerFinanceReviewWithdraw(t *testing.T) {
	h, db := setupAdminResellerFinanceHandlerTest(t)
	profile := seedAdminResellerFinanceProfile(t, db, "reseller-withdraw-admin@example.com")
	admin := models.Admin{Username: "reviewer", PasswordHash: "hash"}
	if err := db.Create(&admin).Error; err != nil {
		t.Fatalf("create admin failed: %v", err)
	}
	seedAdminResellerFinanceLedger(t, db, profile, "RFIN-WITHDRAW-001", "admin-withdraw-ledger-1", decimal.NewFromInt(40))
	rejectReq, err := h.ResellerAccountingService.ApplyWithdraw(profile.ID, service.ResellerWithdrawApplyInput{
		Amount:   decimal.NewFromInt(20),
		Currency: "USD",
		Channel:  "USDT",
		Account:  "TwithdrawReject",
	})
	if err != nil {
		t.Fatalf("apply reject withdraw failed: %v", err)
	}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set("admin_id", admin.ID)
	c.Params = gin.Params{{Key: "id", Value: fmt.Sprintf("%d", rejectReq.ID)}}
	c.Request = httptest.NewRequest(http.MethodPost, fmt.Sprintf("/admin/resellers/withdraws/%d/reject", rejectReq.ID), strings.NewReader(`{"reason":"bad account"}`))
	c.Request.Header.Set("Content-Type", "application/json")

	h.RejectResellerWithdraw(c)

	if w.Code != http.StatusOK {
		t.Fatalf("reject status want 200 got %d body=%s", w.Code, w.Body.String())
	}
	var rejected models.ResellerWithdrawRequest
	if err := db.First(&rejected, rejectReq.ID).Error; err != nil {
		t.Fatalf("load rejected withdraw failed: %v", err)
	}
	if rejected.Status != models.ResellerWithdrawStatusRejected || rejected.RejectReason != "bad account" {
		t.Fatalf("unexpected rejected withdraw: %+v", rejected)
	}

	seedAdminResellerFinanceLedger(t, db, profile, "RFIN-WITHDRAW-002", "admin-withdraw-ledger-2", decimal.NewFromInt(30))
	payReq, err := h.ResellerAccountingService.ApplyWithdraw(profile.ID, service.ResellerWithdrawApplyInput{
		Amount:   decimal.NewFromInt(15),
		Currency: "USD",
		Channel:  "USDT",
		Account:  "TwithdrawPay",
	})
	if err != nil {
		t.Fatalf("apply pay withdraw failed: %v", err)
	}

	w = httptest.NewRecorder()
	c, _ = gin.CreateTestContext(w)
	c.Set("admin_id", admin.ID)
	c.Params = gin.Params{{Key: "id", Value: fmt.Sprintf("%d", payReq.ID)}}
	c.Request = httptest.NewRequest(http.MethodPost, fmt.Sprintf("/admin/resellers/withdraws/%d/pay", payReq.ID), strings.NewReader(`{}`))
	c.Request.Header.Set("Content-Type", "application/json")

	h.PayResellerWithdraw(c)

	if w.Code != http.StatusOK {
		t.Fatalf("pay status want 200 got %d body=%s", w.Code, w.Body.String())
	}
	var paid models.ResellerWithdrawRequest
	if err := db.First(&paid, payReq.ID).Error; err != nil {
		t.Fatalf("load paid withdraw failed: %v", err)
	}
	if paid.Status != models.ResellerWithdrawStatusPaid {
		t.Fatalf("unexpected paid withdraw status: %+v", paid)
	}
}
