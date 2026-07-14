package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/dujiao-next/internal/config"
	"github.com/dujiao-next/internal/constants"
	"github.com/dujiao-next/internal/models"
	"github.com/dujiao-next/internal/repository"
	"github.com/dujiao-next/internal/upstream"

	"github.com/glebarez/sqlite"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

// ── test helpers ──

func setupProcurementTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:procurement_test_%d?mode=memory&cache=shared", time.Now().UnixNano())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		DisableForeignKeyConstraintWhenMigrating: true,
	})
	if err != nil {
		t.Fatalf("open sqlite failed: %v", err)
	}
	if err := db.AutoMigrate(
		&models.Order{},
		&models.OrderItem{},
		&models.OrderRefundRecord{},
		&models.Fulfillment{},
		&models.ProcurementOrder{},
		&models.SiteConnection{},
		&models.ProductMapping{},
		&models.SKUMapping{},
	); err != nil {
		t.Fatalf("auto migrate failed: %v", err)
	}
	models.DB = db
	return db
}

// createProcTestOrder 创建一个测试订单
func createProcTestOrder(t *testing.T, db *gorm.DB, orderNo, status, fulfillmentType string) *models.Order {
	t.Helper()
	order := &models.Order{
		OrderNo:        orderNo,
		UserID:         1,
		Status:         status,
		Currency:       "CNY",
		OriginalAmount: models.NewMoneyFromDecimal(decimal.NewFromInt(100)),
		TotalAmount:    models.NewMoneyFromDecimal(decimal.NewFromInt(100)),
	}
	if err := db.Create(order).Error; err != nil {
		t.Fatalf("create order failed: %v", err)
	}
	item := &models.OrderItem{
		OrderID:         order.ID,
		ProductID:       1,
		SKUID:           1,
		Quantity:        1,
		FulfillmentType: fulfillmentType,
		TitleJSON:       models.JSON{"zh-CN": "Test Product"},
		UnitPrice:       models.NewMoneyFromDecimal(decimal.NewFromInt(100)),
		TotalPrice:      models.NewMoneyFromDecimal(decimal.NewFromInt(100)),
	}
	if err := db.Create(item).Error; err != nil {
		t.Fatalf("create order item failed: %v", err)
	}
	// 重新加载以包含 items
	var loaded models.Order
	if err := db.Preload("Items").First(&loaded, order.ID).Error; err != nil {
		t.Fatalf("reload order failed: %v", err)
	}
	return &loaded
}

// createTestProcurementOrder 创建一个测试采购单
func createTestProcurementOrder(t *testing.T, db *gorm.DB, connID, localOrderID uint, localOrderNo, status string) *models.ProcurementOrder {
	t.Helper()
	proc := &models.ProcurementOrder{
		ConnectionID:    connID,
		LocalOrderID:    localOrderID,
		LocalOrderNo:    localOrderNo,
		Status:          status,
		LocalSellAmount: models.NewMoneyFromDecimal(decimal.NewFromInt(100)),
		Currency:        "CNY",
		TraceID:         "test-trace-id",
	}
	if err := db.Create(proc).Error; err != nil {
		t.Fatalf("create procurement order failed: %v", err)
	}
	return proc
}

func setProcTestManualSubmission(t *testing.T, db *gorm.DB, orderID uint, submission models.JSON) {
	t.Helper()
	var item models.OrderItem
	if err := db.Where("order_id = ?", orderID).First(&item).Error; err != nil {
		t.Fatalf("load order item failed: %v", err)
	}
	item.ManualFormSubmissionJSON = submission
	if err := db.Save(&item).Error; err != nil {
		t.Fatalf("seed manual form failed: %v", err)
	}
}

func newTestProcurementService(
	db *gorm.DB,
	connSvc *SiteConnectionService,
) *ProcurementOrderService {
	svc := NewProcurementOrderService(
		repository.NewProcurementOrderRepository(db),
		repository.NewOrderRepository(db),
		repository.NewFulfillmentRepository(db),
		repository.NewProductMappingRepository(db),
		repository.NewSKUMappingRepository(db),
		connSvc,
		nil, // queueClient
		nil, // settingService
		config.EmailConfig{},
		nil, // fulfillmentService
	)
	return svc
}

type procurementCallbackStatusFixture struct {
	orderNo                   string
	initialOrderStatus        string
	initialProcurementStatus  string
	callbackStatus            string
	expectedProcurementStatus string
	expectedOrderStatus       string
}

func assertProcurementCallbackStatus(t *testing.T, fixture procurementCallbackStatusFixture) {
	t.Helper()
	db := setupProcurementTestDB(t)

	order := createProcTestOrder(t, db, fixture.orderNo, fixture.initialOrderStatus, constants.FulfillmentTypeUpstream)
	proc := createTestProcurementOrder(t, db, 1, order.ID, order.OrderNo, fixture.initialProcurementStatus)

	connSvc := NewSiteConnectionService(repository.NewSiteConnectionRepository(db), "test-key", t.TempDir())
	svc := newTestProcurementService(db, connSvc)

	if err := svc.HandleUpstreamCallback(proc.ID, fixture.callbackStatus, nil); err != nil {
		t.Fatalf("HandleUpstreamCallback: %v", err)
	}

	var updatedProc models.ProcurementOrder
	if err := db.First(&updatedProc, proc.ID).Error; err != nil {
		t.Fatalf("load procurement: %v", err)
	}
	if updatedProc.Status != fixture.expectedProcurementStatus {
		t.Errorf("expected procurement status %q, got %q", fixture.expectedProcurementStatus, updatedProc.Status)
	}

	var updatedOrder models.Order
	if err := db.First(&updatedOrder, order.ID).Error; err != nil {
		t.Fatalf("load order: %v", err)
	}
	if updatedOrder.Status != fixture.expectedOrderStatus {
		t.Errorf("expected order status %q, got %q", fixture.expectedOrderStatus, updatedOrder.Status)
	}
}

// ── Phase 1 tests: order rollback on procurement failure ──

func TestRejectProcurement_RollsBackOrderStatus(t *testing.T) {
	db := setupProcurementTestDB(t)

	order := createProcTestOrder(t, db, "PROC-REJECT-001", constants.OrderStatusFulfilling, constants.FulfillmentTypeUpstream)
	proc := createTestProcurementOrder(t, db, 1, order.ID, order.OrderNo, "pending")

	connSvc := NewSiteConnectionService(repository.NewSiteConnectionRepository(db), "test-key", t.TempDir())
	svc := newTestProcurementService(db, connSvc)

	svc.rejectProcurement(proc, "connection not found")

	// 验证采购单状态 = rejected
	var updatedProc models.ProcurementOrder
	if err := db.First(&updatedProc, proc.ID).Error; err != nil {
		t.Fatalf("load procurement: %v", err)
	}
	if updatedProc.Status != "rejected" {
		t.Errorf("expected procurement status 'rejected', got %q", updatedProc.Status)
	}

	// 验证本地订单状态从 fulfilling 回退到 paid
	var updatedOrder models.Order
	if err := db.First(&updatedOrder, order.ID).Error; err != nil {
		t.Fatalf("load order: %v", err)
	}
	if updatedOrder.Status != constants.OrderStatusPaid {
		t.Errorf("expected order status %q, got %q", constants.OrderStatusPaid, updatedOrder.Status)
	}
}

func TestHandleUpstreamCallback_Canceled_RollsBackOrder(t *testing.T) {
	db := setupProcurementTestDB(t)

	order := createProcTestOrder(t, db, "PROC-CANCEL-001", constants.OrderStatusFulfilling, constants.FulfillmentTypeUpstream)
	proc := createTestProcurementOrder(t, db, 1, order.ID, order.OrderNo, "accepted")

	connSvc := NewSiteConnectionService(repository.NewSiteConnectionRepository(db), "test-key", t.TempDir())
	svc := newTestProcurementService(db, connSvc)

	if err := svc.HandleUpstreamCallback(proc.ID, "canceled", nil); err != nil {
		t.Fatalf("HandleUpstreamCallback: %v", err)
	}

	// 验证采购单状态 = canceled
	var updatedProc models.ProcurementOrder
	if err := db.First(&updatedProc, proc.ID).Error; err != nil {
		t.Fatalf("load procurement: %v", err)
	}
	if updatedProc.Status != "canceled" {
		t.Errorf("expected procurement status 'canceled', got %q", updatedProc.Status)
	}

	// 验证本地订单状态从 fulfilling 回退到 paid
	var updatedOrder models.Order
	if err := db.First(&updatedOrder, order.ID).Error; err != nil {
		t.Fatalf("load order: %v", err)
	}
	if updatedOrder.Status != constants.OrderStatusPaid {
		t.Errorf("expected order status %q, got %q", constants.OrderStatusPaid, updatedOrder.Status)
	}
}

func TestHandleUpstreamCallback_Delivered_CreatesFulfillment(t *testing.T) {
	db := setupProcurementTestDB(t)

	order := createProcTestOrder(t, db, "PROC-DELIVER-001", constants.OrderStatusFulfilling, constants.FulfillmentTypeUpstream)
	proc := createTestProcurementOrder(t, db, 1, order.ID, order.OrderNo, "accepted")

	connSvc := NewSiteConnectionService(repository.NewSiteConnectionRepository(db), "test-key", t.TempDir())
	svc := newTestProcurementService(db, connSvc)

	now := time.Now()
	fulfillment := &upstream.UpstreamFulfillment{
		Type:        constants.FulfillmentTypeUpstream,
		Status:      constants.FulfillmentStatusDelivered,
		Payload:     "CDK-001\nCDK-002",
		DeliveredAt: &now,
	}

	if err := svc.HandleUpstreamCallback(proc.ID, "delivered", fulfillment); err != nil {
		t.Fatalf("HandleUpstreamCallback: %v", err)
	}

	// 验证采购单状态 = fulfilled
	var updatedProc models.ProcurementOrder
	if err := db.First(&updatedProc, proc.ID).Error; err != nil {
		t.Fatalf("load procurement: %v", err)
	}
	if updatedProc.Status != "fulfilled" {
		t.Errorf("expected procurement status 'fulfilled', got %q", updatedProc.Status)
	}

	// 验证本地订单状态 = delivered
	var updatedOrder models.Order
	if err := db.First(&updatedOrder, order.ID).Error; err != nil {
		t.Fatalf("load order: %v", err)
	}
	if updatedOrder.Status != constants.OrderStatusDelivered {
		t.Errorf("expected order status %q, got %q", constants.OrderStatusDelivered, updatedOrder.Status)
	}

	// 验证 Fulfillment 记录已创建
	var ff models.Fulfillment
	if err := db.Where("order_id = ?", order.ID).First(&ff).Error; err != nil {
		t.Fatalf("expected fulfillment record to exist: %v", err)
	}
	if ff.Payload != "CDK-001\nCDK-002" {
		t.Errorf("unexpected fulfillment payload: %q", ff.Payload)
	}
	if ff.Type != constants.FulfillmentTypeUpstream {
		t.Errorf("expected fulfillment type %q, got %q", constants.FulfillmentTypeUpstream, ff.Type)
	}
}

func TestHandleUpstreamCallback_PartiallyRefunded_AfterFulfilledUpdatesProcurementStatus(t *testing.T) {
	assertProcurementCallbackStatus(t, procurementCallbackStatusFixture{
		orderNo:                   "PROC-REFUND-KEEP-001",
		initialOrderStatus:        constants.OrderStatusDelivered,
		initialProcurementStatus:  constants.ProcurementStatusFulfilled,
		callbackStatus:            "partially_refunded",
		expectedProcurementStatus: constants.ProcurementStatusPartiallyRefunded,
		expectedOrderStatus:       constants.OrderStatusDelivered,
	})
}

func TestHandleUpstreamCallback_PartiallyRefunded_WhileFulfillingKeepsOrderStatus(t *testing.T) {
	assertProcurementCallbackStatus(t, procurementCallbackStatusFixture{
		orderNo:                   "PROC-REFUND-FULFILLING-001",
		initialOrderStatus:        constants.OrderStatusFulfilling,
		initialProcurementStatus:  constants.ProcurementStatusAccepted,
		callbackStatus:            "partially_refunded",
		expectedProcurementStatus: constants.ProcurementStatusPartiallyRefunded,
		expectedOrderStatus:       constants.OrderStatusFulfilling,
	})
}

func TestHandleUpstreamCallback_Refunded_AfterCompletedKeepsOrderStatus(t *testing.T) {
	assertProcurementCallbackStatus(t, procurementCallbackStatusFixture{
		orderNo:                   "PROC-REFUND-COMPLETED-001",
		initialOrderStatus:        constants.OrderStatusCompleted,
		initialProcurementStatus:  constants.ProcurementStatusFulfilled,
		callbackStatus:            "refunded",
		expectedProcurementStatus: constants.ProcurementStatusRefunded,
		expectedOrderStatus:       constants.OrderStatusCompleted,
	})
}

func TestProcurement_GetByID_DoesNotIncludeLocalRefundRecords(t *testing.T) {
	db := setupProcurementTestDB(t)

	parent := createProcTestOrder(t, db, "PROC-PARENT-001", constants.OrderStatusPaid, constants.FulfillmentTypeUpstream)
	child := createProcTestOrder(t, db, "PROC-CHILD-001", constants.OrderStatusPaid, constants.FulfillmentTypeUpstream)
	if err := db.Model(&child).Update("parent_id", parent.ID).Error; err != nil {
		t.Fatalf("set child parent: %v", err)
	}

	proc := createTestProcurementOrder(t, db, 1, child.ID, child.OrderNo, constants.ProcurementStatusAccepted)

	localRecord := &models.OrderRefundRecord{
		OrderID:    child.ID,
		Type:       constants.OrderRefundTypeManual,
		Amount:     models.NewMoneyFromDecimal(decimal.NewFromFloat(10.5)),
		Currency:   "CNY",
		Remark:     "local refund",
		GuestEmail: "guest-local@example.com",
	}
	if err := db.Create(localRecord).Error; err != nil {
		t.Fatalf("create local refund record: %v", err)
	}

	parentRecord := &models.OrderRefundRecord{
		OrderID:    parent.ID,
		Type:       constants.OrderRefundTypeWallet,
		Amount:     models.NewMoneyFromDecimal(decimal.NewFromFloat(7.25)),
		Currency:   "CNY",
		Remark:     "parent refund",
		GuestEmail: "guest-parent@example.com",
	}
	if err := db.Create(parentRecord).Error; err != nil {
		t.Fatalf("create parent refund record: %v", err)
	}

	connSvc := NewSiteConnectionService(repository.NewSiteConnectionRepository(db), "test-key", t.TempDir())
	svc := newTestProcurementService(db, connSvc)

	got, err := svc.GetByID(proc.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got == nil {
		t.Fatal("expected procurement order")
	}
	if got.UpstreamRefundedAmount != "" || len(got.UpstreamRefundRecords) != 0 {
		t.Fatalf("expected no upstream refund fields, got refunded_amount=%q records=%d", got.UpstreamRefundedAmount, len(got.UpstreamRefundRecords))
	}
}

func TestProcurement_FillParentOrderNo_BackfillsLocalRefundedAmountFromParent(t *testing.T) {
	db := setupProcurementTestDB(t)

	parent := createProcTestOrder(t, db, "PROC-PARENT-REFUND-001", constants.OrderStatusPartiallyRefunded, constants.FulfillmentTypeUpstream)
	if err := db.Model(&models.Order{}).Where("id = ?", parent.ID).Updates(map[string]interface{}{
		"refunded_amount": models.NewMoneyFromDecimal(decimal.NewFromFloat(12.34)),
	}).Error; err != nil {
		t.Fatalf("set parent refunded_amount: %v", err)
	}

	child := createProcTestOrder(t, db, "PROC-CHILD-REFUND-001", constants.OrderStatusPartiallyRefunded, constants.FulfillmentTypeUpstream)
	if err := db.Model(&models.Order{}).Where("id = ?", child.ID).Update("parent_id", parent.ID).Error; err != nil {
		t.Fatalf("set child parent: %v", err)
	}

	proc := createTestProcurementOrder(t, db, 1, child.ID, child.OrderNo, constants.ProcurementStatusAccepted)
	connSvc := NewSiteConnectionService(repository.NewSiteConnectionRepository(db), "test-key", t.TempDir())
	svc := newTestProcurementService(db, connSvc)

	got, err := svc.GetByID(proc.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got == nil || got.LocalOrder == nil {
		t.Fatalf("expected procurement with local_order, got %+v", got)
	}

	svc.FillParentOrderNo(got)

	if got.ParentOrderNo != parent.OrderNo {
		t.Fatalf("expected parent_order_no %q, got %q", parent.OrderNo, got.ParentOrderNo)
	}
	if got.LocalOrder.RefundedAmount.String() != "12.34" {
		t.Fatalf("expected local_order.refunded_amount 12.34, got %s", got.LocalOrder.RefundedAmount.String())
	}
}

func TestProcurement_List_BackfillsChildLocalRefundedAmountFromParent(t *testing.T) {
	db := setupProcurementTestDB(t)

	parent := createProcTestOrder(t, db, "PROC-LIST-PARENT-REFUND-001", constants.OrderStatusPartiallyRefunded, constants.FulfillmentTypeUpstream)
	if err := db.Model(&models.Order{}).Where("id = ?", parent.ID).Updates(map[string]interface{}{
		"refunded_amount": models.NewMoneyFromDecimal(decimal.NewFromFloat(8.88)),
	}).Error; err != nil {
		t.Fatalf("set parent refunded_amount: %v", err)
	}

	child := createProcTestOrder(t, db, "PROC-LIST-CHILD-REFUND-001", constants.OrderStatusPartiallyRefunded, constants.FulfillmentTypeUpstream)
	if err := db.Model(&models.Order{}).Where("id = ?", child.ID).Update("parent_id", parent.ID).Error; err != nil {
		t.Fatalf("set child parent: %v", err)
	}

	proc := createTestProcurementOrder(t, db, 1, child.ID, child.OrderNo, constants.ProcurementStatusAccepted)
	connSvc := NewSiteConnectionService(repository.NewSiteConnectionRepository(db), "test-key", t.TempDir())
	svc := newTestProcurementService(db, connSvc)

	orders, total, err := svc.List(repository.ProcurementOrderListFilter{
		LocalOrderNo: child.OrderNo,
		Pagination: repository.Pagination{
			Page:     1,
			PageSize: 20,
		},
	})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if total != 1 || len(orders) != 1 || orders[0].ID != proc.ID {
		t.Fatalf("unexpected procurement list result: total=%d len=%d orders=%+v", total, len(orders), orders)
	}
	if orders[0].ParentOrderNo != parent.OrderNo {
		t.Fatalf("expected parent_order_no %q, got %q", parent.OrderNo, orders[0].ParentOrderNo)
	}
	if orders[0].LocalOrder == nil {
		t.Fatalf("expected local_order in list result")
	}
	if orders[0].LocalOrder.RefundedAmount.String() != "8.88" {
		t.Fatalf("expected local_order.refunded_amount 8.88, got %s", orders[0].LocalOrder.RefundedAmount.String())
	}
}

func TestProcurement_List_DoesNotIncludeLocalRefundRecords(t *testing.T) {
	db := setupProcurementTestDB(t)

	order := createProcTestOrder(t, db, "PROC-LIST-REFUND-001", constants.OrderStatusPaid, constants.FulfillmentTypeUpstream)
	proc := createTestProcurementOrder(t, db, 1, order.ID, order.OrderNo, constants.ProcurementStatusAccepted)

	record := &models.OrderRefundRecord{
		OrderID:  order.ID,
		Type:     constants.OrderRefundTypeManual,
		Amount:   models.NewMoneyFromDecimal(decimal.NewFromInt(12)),
		Currency: "CNY",
		Remark:   "list refund",
		UserID:   1,
	}
	if err := db.Create(record).Error; err != nil {
		t.Fatalf("create refund record: %v", err)
	}

	connSvc := NewSiteConnectionService(repository.NewSiteConnectionRepository(db), "test-key", t.TempDir())
	svc := newTestProcurementService(db, connSvc)

	orders, total, err := svc.List(repository.ProcurementOrderListFilter{
		LocalOrderNo: order.OrderNo,
		Pagination: repository.Pagination{
			Page:     1,
			PageSize: 20,
		},
	})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if total != 1 {
		t.Fatalf("expected total 1, got %d", total)
	}
	if len(orders) != 1 || orders[0].ID != proc.ID {
		t.Fatalf("unexpected procurement list result: %+v", orders)
	}
	if orders[0].UpstreamRefundedAmount != "" || len(orders[0].UpstreamRefundRecords) != 0 {
		t.Fatalf("expected no upstream refund fields in list result, got refunded_amount=%q records=%d", orders[0].UpstreamRefundedAmount, len(orders[0].UpstreamRefundRecords))
	}
}

func TestProcurement_GetByID_SyncsUpstreamRefundStatusAndRecords(t *testing.T) {
	db := setupProcurementTestDB(t)
	order := createProcTestOrder(t, db, "PROC-UPSTREAM-REFUND-001", constants.OrderStatusDelivered, constants.FulfillmentTypeUpstream)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"order_id":        999,
			"order_no":        "UP-999",
			"status":          "partially_refunded",
			"amount":          "50.00",
			"refunded_amount": "10.00",
			"currency":        "CNY",
			"refund_records": []map[string]any{
				{
					"id":         101,
					"type":       "manual",
					"amount":     "10.00",
					"currency":   "CNY",
					"remark":     "upstream partial refund",
					"created_at": time.Now().Format(time.RFC3339),
				},
			},
		})
	}))
	defer server.Close()

	connSvc := NewSiteConnectionService(repository.NewSiteConnectionRepository(db), "test-key", t.TempDir())
	conn, err := connSvc.Create(CreateConnectionInput{
		Name:      "upstream-refund",
		BaseURL:   server.URL,
		ApiKey:    "key",
		ApiSecret: "secret",
		Protocol:  constants.ConnectionProtocolDujiaoNext,
	})
	if err != nil {
		t.Fatalf("create connection: %v", err)
	}

	proc := createTestProcurementOrder(t, db, conn.ID, order.ID, order.OrderNo, constants.ProcurementStatusFulfilled)
	if err := db.Model(&models.ProcurementOrder{}).Where("id = ?", proc.ID).Updates(map[string]interface{}{
		"upstream_order_id": uint(999),
		"upstream_order_no": "UP-999",
	}).Error; err != nil {
		t.Fatalf("set upstream order info: %v", err)
	}

	svc := newTestProcurementService(db, connSvc)
	got, err := svc.GetByID(proc.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got == nil {
		t.Fatal("expected procurement order")
	}
	if got.Status != constants.ProcurementStatusPartiallyRefunded {
		t.Fatalf("expected status %s, got %s", constants.ProcurementStatusPartiallyRefunded, got.Status)
	}
	if len(got.UpstreamRefundRecords) != 1 {
		t.Fatalf("expected 1 upstream_refund_records, got %d", len(got.UpstreamRefundRecords))
	}
	if id, ok := got.UpstreamRefundRecords[0]["id"].(int); !ok || id != 1 {
		t.Fatalf("expected upstream_refund_records[0].id = 1, got %#v", got.UpstreamRefundRecords[0]["id"])
	}
	if got.UpstreamRefundedAmount != "10.00" {
		t.Fatalf("expected upstream_refunded_amount 10.00, got %q", got.UpstreamRefundedAmount)
	}

	var refreshed models.ProcurementOrder
	if err := db.First(&refreshed, proc.ID).Error; err != nil {
		t.Fatalf("reload procurement order: %v", err)
	}
	if refreshed.Status != constants.ProcurementStatusPartiallyRefunded {
		t.Fatalf("expected persisted status %s, got %s", constants.ProcurementStatusPartiallyRefunded, refreshed.Status)
	}
}

func TestProcurement_List_SyncsUpstreamRefundStatusAndRecords(t *testing.T) {
	db := setupProcurementTestDB(t)
	order := createProcTestOrder(t, db, "PROC-UPSTREAM-REFUND-LIST-001", constants.OrderStatusDelivered, constants.FulfillmentTypeUpstream)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"order_id":        888,
			"order_no":        "UP-888",
			"status":          "partially_refunded",
			"amount":          "80.00",
			"refunded_amount": "8.00",
			"currency":        "CNY",
			"refund_records": []map[string]any{
				{"id": 201, "type": "wallet", "amount": "8.00", "currency": "CNY", "remark": "list upstream refund"},
			},
		})
	}))
	defer server.Close()

	connSvc := NewSiteConnectionService(repository.NewSiteConnectionRepository(db), "test-key", t.TempDir())
	conn, err := connSvc.Create(CreateConnectionInput{
		Name:      "upstream-refund-list",
		BaseURL:   server.URL,
		ApiKey:    "key",
		ApiSecret: "secret",
		Protocol:  constants.ConnectionProtocolDujiaoNext,
	})
	if err != nil {
		t.Fatalf("create connection: %v", err)
	}

	proc := createTestProcurementOrder(t, db, conn.ID, order.ID, order.OrderNo, constants.ProcurementStatusFulfilled)
	if err := db.Model(&models.ProcurementOrder{}).Where("id = ?", proc.ID).Updates(map[string]interface{}{
		"upstream_order_id": uint(888),
		"upstream_order_no": "UP-888",
	}).Error; err != nil {
		t.Fatalf("set upstream order info: %v", err)
	}

	svc := newTestProcurementService(db, connSvc)
	orders, total, err := svc.List(repository.ProcurementOrderListFilter{
		LocalOrderNo: order.OrderNo,
		Pagination: repository.Pagination{
			Page:     1,
			PageSize: 20,
		},
	})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if total != 1 || len(orders) != 1 {
		t.Fatalf("unexpected list result: total=%d len=%d", total, len(orders))
	}
	if orders[0].Status != constants.ProcurementStatusPartiallyRefunded {
		t.Fatalf("expected list status %s, got %s", constants.ProcurementStatusPartiallyRefunded, orders[0].Status)
	}
	if len(orders[0].UpstreamRefundRecords) != 1 {
		t.Fatalf("expected 1 upstream_refund_records, got %d", len(orders[0].UpstreamRefundRecords))
	}
	if id, ok := orders[0].UpstreamRefundRecords[0]["id"].(int); !ok || id != 1 {
		t.Fatalf("expected upstream_refund_records[0].id = 1, got %#v", orders[0].UpstreamRefundRecords[0]["id"])
	}
	if orders[0].UpstreamRefundedAmount != "8.00" {
		t.Fatalf("expected upstream_refunded_amount 8.00, got %q", orders[0].UpstreamRefundedAmount)
	}
}

func TestProcurement_GetByID_WithoutUpstreamRefundOmitsRefundFields(t *testing.T) {
	db := setupProcurementTestDB(t)
	order := createProcTestOrder(t, db, "PROC-UPSTREAM-NO-REFUND-001", constants.OrderStatusDelivered, constants.FulfillmentTypeUpstream)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"order_id":        777,
			"order_no":        "UP-777",
			"status":          "fulfilled",
			"amount":          "66.00",
			"refunded_amount": "0.00",
			"currency":        "CNY",
			"refund_records":  []map[string]any{},
		})
	}))
	defer server.Close()

	connSvc := NewSiteConnectionService(repository.NewSiteConnectionRepository(db), "test-key", t.TempDir())
	conn, err := connSvc.Create(CreateConnectionInput{
		Name:      "upstream-no-refund",
		BaseURL:   server.URL,
		ApiKey:    "key",
		ApiSecret: "secret",
		Protocol:  constants.ConnectionProtocolDujiaoNext,
	})
	if err != nil {
		t.Fatalf("create connection: %v", err)
	}

	proc := createTestProcurementOrder(t, db, conn.ID, order.ID, order.OrderNo, constants.ProcurementStatusFulfilled)
	if err := db.Model(&models.ProcurementOrder{}).Where("id = ?", proc.ID).Updates(map[string]interface{}{
		"upstream_order_id": uint(777),
		"upstream_order_no": "UP-777",
	}).Error; err != nil {
		t.Fatalf("set upstream order info: %v", err)
	}

	svc := newTestProcurementService(db, connSvc)
	got, err := svc.GetByID(proc.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got == nil {
		t.Fatal("expected procurement order")
	}
	if got.Status != constants.ProcurementStatusFulfilled {
		t.Fatalf("expected status %s, got %s", constants.ProcurementStatusFulfilled, got.Status)
	}
	if got.UpstreamRefundedAmount != "" {
		t.Fatalf("expected empty upstream_refunded_amount, got %q", got.UpstreamRefundedAmount)
	}
	if len(got.UpstreamRefundRecords) != 0 {
		t.Fatalf("expected empty upstream_refund_records, got %d", len(got.UpstreamRefundRecords))
	}

	payload, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal procurement order failed: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatalf("unmarshal procurement order payload failed: %v", err)
	}
	if _, ok := decoded["upstream_refunded_amount"]; ok {
		t.Fatalf("expected upstream_refunded_amount to be omitted when no upstream refund, payload=%s", string(payload))
	}
	if _, ok := decoded["upstream_refund_records"]; ok {
		t.Fatalf("expected upstream_refund_records to be omitted when no upstream refund, payload=%s", string(payload))
	}
	if _, ok := decoded["upstream_order_id"]; ok {
		t.Fatalf("expected upstream_order_id to be omitted from procurement payload, payload=%s", string(payload))
	}
}

func TestBuildUpstreamRefundRecords_SortsByCreatedAtAscAndRenumbersID(t *testing.T) {
	records := []models.JSON{
		{
			"id":         99,
			"type":       "wallet",
			"amount":     "20.00",
			"created_at": "2026-04-12T10:00:00Z",
		},
		{
			"id":         100,
			"type":       "wallet",
			"amount":     "10.00",
			"created_at": "2026-04-12T09:00:00Z",
		},
	}

	got := buildUpstreamRefundRecords(records)
	if len(got) != 2 {
		t.Fatalf("expected 2 records, got %d", len(got))
	}
	if amount, _ := got[0]["amount"].(string); amount != "10.00" {
		t.Fatalf("expected first amount 10.00, got %#v", got[0]["amount"])
	}
	if amount, _ := got[1]["amount"].(string); amount != "20.00" {
		t.Fatalf("expected second amount 20.00, got %#v", got[1]["amount"])
	}
	if id, ok := got[0]["id"].(int); !ok || id != 1 {
		t.Fatalf("expected first id 1, got %#v", got[0]["id"])
	}
	if id, ok := got[1]["id"].(int); !ok || id != 2 {
		t.Fatalf("expected second id 2, got %#v", got[1]["id"])
	}
}

// ── SubmitToUpstream tests ──

func TestSubmitToUpstream_Success(t *testing.T) {
	db := setupProcurementTestDB(t)

	order := createProcTestOrder(t, db, "PROC-SUBMIT-001", constants.OrderStatusPaid, constants.FulfillmentTypeUpstream)
	// 创建 product mapping 和 sku mapping
	pm := &models.ProductMapping{
		ConnectionID:      1,
		LocalProductID:    1,
		UpstreamProductID: 101,
		IsActive:          true,
	}
	db.Create(pm)
	sm := &models.SKUMapping{
		ProductMappingID: pm.ID,
		LocalSKUID:       1,
		UpstreamSKUID:    201,
		UpstreamIsActive: true,
	}
	db.Create(sm)

	// mock upstream server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"ok":       true,
			"order_id": 999,
			"order_no": "UP-999",
			"status":   "accepted",
			"amount":   "50.00",
			"currency": "CNY",
		})
	}))
	defer server.Close()

	connSvc := NewSiteConnectionService(repository.NewSiteConnectionRepository(db), "test-key", t.TempDir())
	conn, err := connSvc.Create(CreateConnectionInput{
		Name:      "test-upstream",
		BaseURL:   server.URL,
		ApiKey:    "key",
		ApiSecret: "secret",
		Protocol:  constants.ConnectionProtocolDujiaoNext,
	})
	if err != nil {
		t.Fatalf("create connection: %v", err)
	}

	proc := createTestProcurementOrder(t, db, conn.ID, order.ID, order.OrderNo, "pending")

	svc := newTestProcurementService(db, connSvc)

	if err := svc.SubmitToUpstream(proc.ID); err != nil {
		t.Fatalf("SubmitToUpstream: %v", err)
	}

	// 验证采购单状态 = accepted
	var updatedProc models.ProcurementOrder
	db.First(&updatedProc, proc.ID)
	if updatedProc.Status != "accepted" {
		t.Errorf("expected procurement status 'accepted', got %q", updatedProc.Status)
	}
	if updatedProc.UpstreamOrderID != 999 {
		t.Errorf("expected upstream_order_id=999, got %d", updatedProc.UpstreamOrderID)
	}

	// 验证本地订单状态 = fulfilling
	var updatedOrder models.Order
	db.First(&updatedOrder, order.ID)
	if updatedOrder.Status != constants.OrderStatusFulfilling {
		t.Errorf("expected order status %q, got %q", constants.OrderStatusFulfilling, updatedOrder.Status)
	}
}

func TestCreateForOrderRejectsInactiveMapping(t *testing.T) {
	db := setupProcurementTestDB(t)
	order := createProcTestOrder(t, db, "PROC-INACTIVE-MAPPING", constants.OrderStatusPaid, constants.FulfillmentTypeUpstream)
	mapping := &models.ProductMapping{
		ConnectionID:      1,
		LocalProductID:    1,
		UpstreamProductID: 101,
		IsActive:          true,
	}
	if err := db.Create(mapping).Error; err != nil {
		t.Fatalf("create mapping: %v", err)
	}
	if err := db.Model(&models.ProductMapping{}).Where("id = ?", mapping.ID).Update("is_active", false).Error; err != nil {
		t.Fatalf("deactivate mapping: %v", err)
	}
	svc := newTestProcurementService(db, NewSiteConnectionService(repository.NewSiteConnectionRepository(db), "test-key", t.TempDir()))
	if err := svc.CreateForOrder(order.ID); !errors.Is(err, ErrMappingInactive) {
		t.Fatalf("expected ErrMappingInactive, got %v", err)
	}
	var count int64
	if err := db.Model(&models.ProcurementOrder{}).Count(&count).Error; err != nil {
		t.Fatalf("count procurement: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected no procurement order, got %d", count)
	}
}

func TestSubmitToUpstream_NonRetryableError_Rejects(t *testing.T) {
	db := setupProcurementTestDB(t)

	order := createProcTestOrder(t, db, "PROC-NONRETRY-001", constants.OrderStatusFulfilling, constants.FulfillmentTypeUpstream)
	pm := &models.ProductMapping{ConnectionID: 1, LocalProductID: 1, UpstreamProductID: 101, IsActive: true}
	db.Create(pm)
	sm := &models.SKUMapping{ProductMappingID: pm.ID, LocalSKUID: 1, UpstreamSKUID: 201, UpstreamIsActive: true}
	db.Create(sm)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"ok":            false,
			"error_code":    "product_out_of_stock",
			"error_message": "product out of stock",
		})
	}))
	defer server.Close()

	connSvc := NewSiteConnectionService(repository.NewSiteConnectionRepository(db), "test-key", t.TempDir())
	conn, _ := connSvc.Create(CreateConnectionInput{
		Name: "test-upstream", BaseURL: server.URL,
		ApiKey: "key", ApiSecret: "secret", Protocol: constants.ConnectionProtocolDujiaoNext,
	})

	proc := createTestProcurementOrder(t, db, conn.ID, order.ID, order.OrderNo, "pending")
	svc := newTestProcurementService(db, connSvc)

	// 不可重试错误应返回 error
	_ = svc.SubmitToUpstream(proc.ID)

	// 验证采购单状态 = rejected
	var updatedProc models.ProcurementOrder
	db.First(&updatedProc, proc.ID)
	if updatedProc.Status != "rejected" {
		t.Errorf("expected procurement status 'rejected', got %q", updatedProc.Status)
	}

	// 验证本地订单状态回退到 paid
	var updatedOrder models.Order
	db.First(&updatedOrder, order.ID)
	if updatedOrder.Status != constants.OrderStatusPaid {
		t.Errorf("expected order status %q after rejection, got %q", constants.OrderStatusPaid, updatedOrder.Status)
	}
}

func TestSubmitToUpstream_RetryableError_Retries(t *testing.T) {
	db := setupProcurementTestDB(t)

	order := createProcTestOrder(t, db, "PROC-RETRY-001", constants.OrderStatusFulfilling, constants.FulfillmentTypeUpstream)
	pm := &models.ProductMapping{ConnectionID: 1, LocalProductID: 1, UpstreamProductID: 101, IsActive: true}
	db.Create(pm)
	sm := &models.SKUMapping{ProductMappingID: pm.ID, LocalSKUID: 1, UpstreamSKUID: 201, UpstreamIsActive: true}
	db.Create(sm)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"ok":            false,
			"error_code":    "server_error",
			"error_message": "temporary failure",
		})
	}))
	defer server.Close()

	connSvc := NewSiteConnectionService(repository.NewSiteConnectionRepository(db), "test-key", t.TempDir())
	conn, _ := connSvc.Create(CreateConnectionInput{
		Name: "test-upstream", BaseURL: server.URL,
		ApiKey: "key", ApiSecret: "secret", Protocol: constants.ConnectionProtocolDujiaoNext,
		RetryMax: 3,
	})

	proc := createTestProcurementOrder(t, db, conn.ID, order.ID, order.OrderNo, "pending")
	svc := newTestProcurementService(db, connSvc)

	// 可重试错误不应返回 error（已入队重试）
	if err := svc.SubmitToUpstream(proc.ID); err != nil {
		t.Fatalf("expected no error for retryable failure, got: %v", err)
	}

	// 验证采购单状态 = failed（而非 rejected）
	var updatedProc models.ProcurementOrder
	db.First(&updatedProc, proc.ID)
	if updatedProc.Status != "failed" {
		t.Errorf("expected procurement status 'failed', got %q", updatedProc.Status)
	}
	if updatedProc.RetryCount != 1 {
		t.Errorf("expected retry_count=1, got %d", updatedProc.RetryCount)
	}
}

func TestSubmitToUpstream_FansGurusProvider(t *testing.T) {
	db := setupProcurementTestDB(t)

	order := createProcTestOrder(t, db, "PROC-FG-001", constants.OrderStatusPaid, constants.FulfillmentTypeUpstream)
	setProcTestManualSubmission(t, db, order.ID, models.JSON{"link": "https://example.com/post"})
	pm := &models.ProductMapping{
		ConnectionID:        1,
		LocalProductID:      1,
		UpstreamProductCode: "12345",
		Provider:            upstream.CatalogProviderFansGurus,
		IsActive:            true,
	}
	db.Create(pm)
	sm := &models.SKUMapping{ProductMappingID: pm.ID, LocalSKUID: 1, UpstreamSKUCode: "12345", UpstreamIsActive: true}
	db.Create(sm)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Fatalf("ParseForm: %v", err)
		}
		if got := r.FormValue("action"); got != "add" {
			t.Fatalf("action=%s, want add", got)
		}
		if got := r.FormValue("service"); got != "12345" {
			t.Fatalf("service=%s, want 12345", got)
		}
		if got := r.FormValue("link"); got != "https://example.com/post" {
			t.Fatalf("link=%s", got)
		}
		if got := r.FormValue("quantity"); got != "1" {
			t.Fatalf("quantity=%s, want 1", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"order": 9988, "charge": "0.50"})
	}))
	defer server.Close()

	connSvc := NewSiteConnectionService(repository.NewSiteConnectionRepository(db), "test-key", t.TempDir())
	conn, err := connSvc.Create(CreateConnectionInput{
		Name: "fansgurus", BaseURL: server.URL,
		ApiKey: "fans-key", ApiSecret: "unused", Protocol: constants.ConnectionProtocolFansGurus,
	})
	if err != nil {
		t.Fatalf("create connection: %v", err)
	}
	if err := db.Model(pm).Update("connection_id", conn.ID).Error; err != nil {
		t.Fatalf("update mapping connection: %v", err)
	}

	proc := createTestProcurementOrder(t, db, conn.ID, order.ID, order.OrderNo, "pending")
	svc := newTestProcurementService(db, connSvc)

	if err := svc.SubmitToUpstream(proc.ID); err != nil {
		t.Fatalf("SubmitToUpstream: %v", err)
	}

	var updatedProc models.ProcurementOrder
	db.First(&updatedProc, proc.ID)
	if updatedProc.Status != constants.ProcurementStatusAccepted || updatedProc.UpstreamOrderNo != "9988" {
		t.Fatalf("unexpected procurement: %+v", updatedProc)
	}
	var updatedOrder models.Order
	db.First(&updatedOrder, order.ID)
	if updatedOrder.Status != constants.OrderStatusFulfilling {
		t.Fatalf("order status=%s, want %s", updatedOrder.Status, constants.OrderStatusFulfilling)
	}
}

func TestSubmitToUpstream_FansGurusAllSupportedServiceTypes(t *testing.T) {
	cases := []struct {
		name       string
		submission models.JSON
		assertForm func(*testing.T, *http.Request)
	}{
		{"default", models.JSON{"link": "https://example.com/post"}, func(t *testing.T, r *http.Request) {
			if r.FormValue("link") != "https://example.com/post" || r.FormValue("quantity") != "1" {
				t.Fatalf("default form=%v", r.Form)
			}
		}},
		{"custom_comments", models.JSON{"link": "https://example.com/post", "comments": "first\nsecond"}, func(t *testing.T, r *http.Request) {
			if r.FormValue("comments") != "first\nsecond" || r.FormValue("quantity") != "" {
				t.Fatalf("comments form=%v", r.Form)
			}
		}},
		{"poll", models.JSON{"link": "https://example.com/poll", "answer_number": "2"}, func(t *testing.T, r *http.Request) {
			if r.FormValue("answer_number") != "2" || r.FormValue("quantity") != "1" {
				t.Fatalf("poll form=%v", r.Form)
			}
		}},
		{"invites_from_groups", models.JSON{"link": "https://example.com/group", "groups": "group-a\ngroup-b"}, func(t *testing.T, r *http.Request) {
			if r.FormValue("groups") != "group-a\ngroup-b" || r.FormValue("quantity") != "1" {
				t.Fatalf("groups form=%v", r.Form)
			}
		}},
		{"subscriptions", models.JSON{"username": "target_user", "min": 10, "max": 30, "posts": 5, "old_posts": 1, "delay": 60, "expiry": "2027-01-01"}, func(t *testing.T, r *http.Request) {
			if r.FormValue("username") != "target_user" || r.FormValue("min") != "10" || r.FormValue("max") != "30" || r.FormValue("quantity") != "" {
				t.Fatalf("subscription form=%v", r.Form)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := setupProcurementTestDB(t)
			order := createProcTestOrder(t, db, "PROC-FG-"+tc.name, constants.OrderStatusPaid, constants.FulfillmentTypeUpstream)
			setProcTestManualSubmission(t, db, order.ID, tc.submission)
			mapping := &models.ProductMapping{LocalProductID: 1, UpstreamProductCode: "12345", Provider: upstream.CatalogProviderFansGurus, IsActive: true}
			if err := db.Create(mapping).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Create(&models.SKUMapping{ProductMappingID: mapping.ID, LocalSKUID: 1, UpstreamSKUCode: "12345", UpstreamIsActive: true}).Error; err != nil {
				t.Fatal(err)
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if err := r.ParseForm(); err != nil {
					t.Fatal(err)
				}
				if r.FormValue("action") != "add" || r.FormValue("service") != "12345" {
					t.Fatalf("base form=%v", r.Form)
				}
				tc.assertForm(t, r)
				_ = json.NewEncoder(w).Encode(map[string]any{"order": 9988, "charge": "0.50"})
			}))
			defer server.Close()
			connSvc := NewSiteConnectionService(repository.NewSiteConnectionRepository(db), "test-key", t.TempDir())
			conn, err := connSvc.Create(CreateConnectionInput{Name: "fansgurus", BaseURL: server.URL, ApiKey: "fans-key", ApiSecret: "unused", Protocol: constants.ConnectionProtocolFansGurus})
			if err != nil {
				t.Fatal(err)
			}
			if err := db.Model(mapping).Update("connection_id", conn.ID).Error; err != nil {
				t.Fatal(err)
			}
			proc := createTestProcurementOrder(t, db, conn.ID, order.ID, order.OrderNo, constants.ProcurementStatusPending)
			if err := newTestProcurementService(db, connSvc).SubmitToUpstream(proc.ID); err != nil {
				t.Fatal(err)
			}
			var got models.ProcurementOrder
			if err := db.First(&got, proc.ID).Error; err != nil || got.Status != constants.ProcurementStatusAccepted {
				t.Fatalf("procurement=%+v err=%v", got, err)
			}
		})
	}
}

func TestSubmitToUpstream_FansGurusUnavailableFailsForUserWithoutRetry(t *testing.T) {
	db := setupProcurementTestDB(t)

	order := createProcTestOrder(t, db, "PROC-FG-UNAVAILABLE-001", constants.OrderStatusFulfilling, constants.FulfillmentTypeUpstream)
	setProcTestManualSubmission(t, db, order.ID, models.JSON{"link": "https://example.com/post"})
	pm := &models.ProductMapping{
		ConnectionID:        1,
		LocalProductID:      1,
		UpstreamProductCode: "12345",
		Provider:            upstream.CatalogProviderFansGurus,
		IsActive:            true,
	}
	db.Create(pm)
	sm := &models.SKUMapping{ProductMappingID: pm.ID, LocalSKUID: 1, UpstreamSKUCode: "12345", UpstreamIsActive: true}
	db.Create(sm)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Fatalf("ParseForm: %v", err)
		}
		if got := r.FormValue("action"); got != "add" {
			t.Fatalf("action=%s, want add", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`not-json`))
	}))
	defer server.Close()

	connSvc := NewSiteConnectionService(repository.NewSiteConnectionRepository(db), "test-key", t.TempDir())
	conn, err := connSvc.Create(CreateConnectionInput{
		Name: "fansgurus", BaseURL: server.URL,
		ApiKey: "fans-key", ApiSecret: "unused", Protocol: constants.ConnectionProtocolFansGurus,
		RetryMax: 3, RetryIntervals: "[30,60]",
	})
	if err != nil {
		t.Fatalf("create connection: %v", err)
	}
	if err := db.Model(pm).Update("connection_id", conn.ID).Error; err != nil {
		t.Fatalf("update mapping connection: %v", err)
	}

	proc := createTestProcurementOrder(t, db, conn.ID, order.ID, order.OrderNo, constants.ProcurementStatusPending)
	svc := newTestProcurementService(db, connSvc)

	if err := svc.SubmitToUpstream(proc.ID); err != nil {
		t.Fatalf("SubmitToUpstream: %v", err)
	}

	var updatedProc models.ProcurementOrder
	db.First(&updatedProc, proc.ID)
	if updatedProc.Status != constants.ProcurementStatusFailed {
		t.Fatalf("status=%s, want %s", updatedProc.Status, constants.ProcurementStatusFailed)
	}
	if updatedProc.RetryCount != 0 || updatedProc.NextRetryAt != nil {
		t.Fatalf("unavailable provider submit must not auto retry: retry_count=%d next_retry_at=%v", updatedProc.RetryCount, updatedProc.NextRetryAt)
	}
	if updatedProc.ErrorMessage != providerSubmitTemporarilyUnavailable {
		t.Fatalf("error_message=%q", updatedProc.ErrorMessage)
	}
	var updatedOrder models.Order
	db.First(&updatedOrder, order.ID)
	if updatedOrder.Status != constants.OrderStatusPaid {
		t.Fatalf("order status=%s, want %s", updatedOrder.Status, constants.OrderStatusPaid)
	}
}

func TestSubmitToUpstream_TGXProviderImmediateSecret(t *testing.T) {
	db := setupProcurementTestDB(t)

	order := createProcTestOrder(t, db, "PROC-TGX-001", constants.OrderStatusPaid, constants.FulfillmentTypeUpstream)
	setProcTestManualSubmission(t, db, order.ID, models.JSON{"email": "buyer@example.com"})
	pm := &models.ProductMapping{
		ConnectionID:        1,
		LocalProductID:      1,
		UpstreamProductCode: "IG-001",
		Provider:            upstream.CatalogProviderTGX,
		IsActive:            true,
	}
	db.Create(pm)
	sm := &models.SKUMapping{ProductMappingID: pm.ID, LocalSKUID: 1, UpstreamSKUCode: "IG-001|普通", UpstreamIsActive: true}
	db.Create(sm)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Fatalf("ParseForm: %v", err)
		}
		if r.URL.Path == "/commodity/inventory" {
			if got := r.FormValue("sharedCode"); got != "IG-001" {
				t.Fatalf("sharedCode=%s, want IG-001", got)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "data": map[string]any{"count": 5}})
			return
		}
		if r.URL.Path == "/commodity/inventoryState" {
			if got := r.FormValue("num"); got != "1" {
				t.Fatalf("num=%s, want 1", got)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "data": map[string]any{}})
			return
		}
		if r.URL.Path != "/commodity/trade" {
			t.Fatalf("path=%s, want /commodity/trade", r.URL.Path)
		}
		if got := r.FormValue("app_id"); got != "tgx-app-id" {
			t.Fatalf("app_id=%s, want tgx-app-id", got)
		}
		if got := r.FormValue("shared_code"); got != "IG-001" {
			t.Fatalf("shared_code=%s, want IG-001", got)
		}
		if got := r.FormValue("race"); got != "普通" {
			t.Fatalf("race=%s, want 普通", got)
		}
		if got := r.FormValue("request_no"); got != order.OrderNo {
			t.Fatalf("request_no=%s, want %s", got, order.OrderNo)
		}
		if got := r.FormValue("email"); got != "buyer@example.com" {
			t.Fatalf("email=%s, want buyer@example.com", got)
		}
		if got := r.FormValue("app_key"); got != "tgx-app-key" {
			t.Fatalf("app_key=%s, want configured key", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 200,
			"data": map[string]any{"trade_no": "TGX-9988", "secret": "account:pass", "status": 1},
		})
	}))
	defer server.Close()

	connSvc := NewSiteConnectionService(repository.NewSiteConnectionRepository(db), "test-key", t.TempDir())
	conn, err := connSvc.Create(CreateConnectionInput{
		Name: "tgx", BaseURL: server.URL,
		ApiKey: "tgx-app-id", ApiSecret: "tgx-app-key", Protocol: constants.ConnectionProtocolTGXAccount,
	})
	if err != nil {
		t.Fatalf("create connection: %v", err)
	}
	if err := db.Model(pm).Update("connection_id", conn.ID).Error; err != nil {
		t.Fatalf("update mapping connection: %v", err)
	}

	proc := createTestProcurementOrder(t, db, conn.ID, order.ID, order.OrderNo, "pending")
	svc := newTestProcurementService(db, connSvc)

	if err := svc.SubmitToUpstream(proc.ID); err != nil {
		t.Fatalf("SubmitToUpstream: %v", err)
	}

	var updatedProc models.ProcurementOrder
	db.First(&updatedProc, proc.ID)
	if updatedProc.Status != constants.ProcurementStatusFulfilled || updatedProc.UpstreamOrderNo != "TGX-9988" {
		t.Fatalf("unexpected procurement: %+v", updatedProc)
	}
	var fulfillment models.Fulfillment
	if err := db.Where("order_id = ?", order.ID).First(&fulfillment).Error; err != nil {
		t.Fatalf("load fulfillment: %v", err)
	}
	if fulfillment.Payload != "account:pass" {
		t.Fatalf("payload=%q, want account:pass", fulfillment.Payload)
	}
}

func TestSubmitToUpstream_TGXProviderFailsSafelyWithoutTradeNo(t *testing.T) {
	db := setupProcurementTestDB(t)

	order := createProcTestOrder(t, db, "PROC-TGX-RECOVER-001", constants.OrderStatusPaid, constants.FulfillmentTypeUpstream)
	setProcTestManualSubmission(t, db, order.ID, models.JSON{"email": "buyer@example.com"})
	pm := &models.ProductMapping{
		ConnectionID:        1,
		LocalProductID:      1,
		UpstreamProductCode: "IG-001",
		Provider:            upstream.CatalogProviderTGX,
		IsActive:            true,
	}
	db.Create(pm)
	sm := &models.SKUMapping{ProductMappingID: pm.ID, LocalSKUID: 1, UpstreamSKUCode: "IG-001|普通", UpstreamIsActive: true}
	db.Create(sm)

	var tradeCalls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Fatalf("ParseForm: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/commodity/inventory":
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "data": map[string]any{"count": 5}})
		case "/commodity/inventoryState":
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "data": map[string]any{}})
		case "/commodity/trade":
			tradeCalls++
			if got := r.FormValue("request_no"); got != order.OrderNo {
				t.Fatalf("request_no=%s, want %s", got, order.OrderNo)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "data": map[string]string{}})
		default:
			t.Fatalf("path=%s", r.URL.Path)
		}
	}))
	defer server.Close()

	connSvc := NewSiteConnectionService(repository.NewSiteConnectionRepository(db), "test-key", t.TempDir())
	conn, err := connSvc.Create(CreateConnectionInput{
		Name: "tgx", BaseURL: server.URL,
		ApiKey: "tgx-app-id", ApiSecret: "tgx-app-key", Protocol: constants.ConnectionProtocolTGXAccount,
	})
	if err != nil {
		t.Fatalf("create connection: %v", err)
	}
	if err := db.Model(pm).Update("connection_id", conn.ID).Error; err != nil {
		t.Fatalf("update mapping connection: %v", err)
	}

	proc := createTestProcurementOrder(t, db, conn.ID, order.ID, order.OrderNo, constants.ProcurementStatusPending)
	svc := newTestProcurementService(db, connSvc)

	if err := svc.SubmitToUpstream(proc.ID); err != nil {
		t.Fatalf("SubmitToUpstream: %v", err)
	}
	if tradeCalls != 1 {
		t.Fatalf("trade calls=%d, want 1", tradeCalls)
	}

	var updatedProc models.ProcurementOrder
	db.First(&updatedProc, proc.ID)
	if updatedProc.Status != constants.ProcurementStatusFailed || updatedProc.UpstreamOrderNo != "" {
		t.Fatalf("unexpected procurement: %+v", updatedProc)
	}
	if updatedProc.ErrorMessage != providerSubmitTemporarilyUnavailable {
		t.Fatalf("error_message=%q", updatedProc.ErrorMessage)
	}
	var fulfillmentCount int64
	if err := db.Model(&models.Fulfillment{}).Where("order_id = ?", order.ID).Count(&fulfillmentCount).Error; err != nil {
		t.Fatalf("count fulfillment: %v", err)
	}
	if fulfillmentCount != 0 {
		t.Fatalf("fulfillment count=%d, want 0", fulfillmentCount)
	}
}

func TestHandleSubmitFailure_MaxRetriesExhausted(t *testing.T) {
	db := setupProcurementTestDB(t)

	order := createProcTestOrder(t, db, "PROC-MAXRETRY-001", constants.OrderStatusFulfilling, constants.FulfillmentTypeUpstream)

	connSvc := NewSiteConnectionService(repository.NewSiteConnectionRepository(db), "test-key", t.TempDir())
	conn := &models.SiteConnection{
		RetryMax:       2,
		RetryIntervals: "[30,60]",
	}
	db.Create(conn)

	proc := createTestProcurementOrder(t, db, conn.ID, order.ID, order.OrderNo, "failed")
	// 设置 retry_count 已达上限
	db.Model(proc).Update("retry_count", 2)

	svc := newTestProcurementService(db, connSvc)

	// 模拟调用 handleSubmitFailure（可重试但已达上限）
	_ = svc.handleSubmitFailure(proc, conn, "timeout after retries", true)

	// 验证采购单状态 = rejected
	var updatedProc models.ProcurementOrder
	db.First(&updatedProc, proc.ID)
	if updatedProc.Status != "rejected" {
		t.Errorf("expected procurement status 'rejected', got %q", updatedProc.Status)
	}

	// 验证本地订单回退到 paid
	var updatedOrder models.Order
	db.First(&updatedOrder, order.ID)
	if updatedOrder.Status != constants.OrderStatusPaid {
		t.Errorf("expected order status %q, got %q", constants.OrderStatusPaid, updatedOrder.Status)
	}
}

func TestProviderFulfillmentEndToEnd_FansGurusMock(t *testing.T) {
	db := setupProcurementTestDB(t)

	order := createProcTestOrder(t, db, "PROC-E2E-FG-001", constants.OrderStatusPaid, constants.FulfillmentTypeUpstream)
	setProcTestManualSubmission(t, db, order.ID, models.JSON{"link": "https://example.com/post"})

	var addCalls int
	var statusCalls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Fatalf("ParseForm: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.FormValue("action") {
		case "add":
			addCalls++
			if got := r.FormValue("service"); got != "12345" {
				t.Fatalf("service=%s, want 12345", got)
			}
			if got := r.FormValue("link"); got != "https://example.com/post" {
				t.Fatalf("link=%s, want https://example.com/post", got)
			}
			if got := r.FormValue("quantity"); got != "1" {
				t.Fatalf("quantity=%s, want 1", got)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"order": 9988, "charge": "0.50"})
		case "status":
			statusCalls++
			if got := r.FormValue("order"); got != "9988" {
				t.Fatalf("order=%s, want 9988", got)
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"status": "Completed", "charge": "0.50", "currency": "USD"})
		default:
			t.Fatalf("unexpected action %q", r.FormValue("action"))
		}
	}))
	defer server.Close()

	connSvc := NewSiteConnectionService(repository.NewSiteConnectionRepository(db), "test-key", t.TempDir())
	conn, err := connSvc.Create(CreateConnectionInput{
		Name: "fansgurus", BaseURL: server.URL,
		ApiKey: "fans-key", ApiSecret: "unused", Protocol: constants.ConnectionProtocolFansGurus,
	})
	if err != nil {
		t.Fatalf("create connection: %v", err)
	}
	pm := &models.ProductMapping{
		ConnectionID:        conn.ID,
		LocalProductID:      1,
		UpstreamProductCode: "12345",
		Provider:            upstream.CatalogProviderFansGurus,
		IsActive:            true,
	}
	if err := db.Create(pm).Error; err != nil {
		t.Fatalf("create product mapping: %v", err)
	}
	if err := db.Create(&models.SKUMapping{ProductMappingID: pm.ID, LocalSKUID: 1, UpstreamSKUCode: "12345", UpstreamIsActive: true}).Error; err != nil {
		t.Fatalf("create sku mapping: %v", err)
	}
	svc := newTestProcurementService(db, connSvc)

	if err := svc.CreateForOrder(order.ID); err != nil {
		t.Fatalf("CreateForOrder: %v", err)
	}
	proc, err := repository.NewProcurementOrderRepository(db).GetByLocalOrderID(order.ID)
	if err != nil {
		t.Fatalf("load procurement: %v", err)
	}
	if proc == nil || proc.Status != constants.ProcurementStatusPending {
		t.Fatalf("procurement after create=%+v, want pending", proc)
	}

	if err := svc.SubmitToUpstream(proc.ID); err != nil {
		t.Fatalf("SubmitToUpstream: %v", err)
	}
	var submitted models.ProcurementOrder
	db.First(&submitted, proc.ID)
	if submitted.Status != constants.ProcurementStatusAccepted || submitted.UpstreamOrderNo != "9988" {
		t.Fatalf("procurement after submit=%+v, want accepted upstream 9988", submitted)
	}
	var fulfillingOrder models.Order
	db.First(&fulfillingOrder, order.ID)
	if fulfillingOrder.Status != constants.OrderStatusFulfilling {
		t.Fatalf("order after submit status=%s, want %s", fulfillingOrder.Status, constants.OrderStatusFulfilling)
	}

	svc.SyncAcceptedOrders()

	var fulfilledProc models.ProcurementOrder
	db.First(&fulfilledProc, proc.ID)
	if fulfilledProc.Status != constants.ProcurementStatusFulfilled {
		t.Fatalf("procurement after sync status=%s, want %s", fulfilledProc.Status, constants.ProcurementStatusFulfilled)
	}
	var deliveredOrder models.Order
	db.First(&deliveredOrder, order.ID)
	if deliveredOrder.Status != constants.OrderStatusDelivered {
		t.Fatalf("order after sync status=%s, want %s", deliveredOrder.Status, constants.OrderStatusDelivered)
	}
	if addCalls != 1 || statusCalls != 1 {
		t.Fatalf("addCalls=%d statusCalls=%d, want 1/1", addCalls, statusCalls)
	}
}

func TestProviderFulfillmentEndToEnd_TGXMockDelayedSecret(t *testing.T) {
	db := setupProcurementTestDB(t)

	order := createProcTestOrder(t, db, "PROC-E2E-TGX-001", constants.OrderStatusPaid, constants.FulfillmentTypeUpstream)
	setProcTestManualSubmission(t, db, order.ID, models.JSON{"email": "buyer@example.com"})

	var tradeCalls int
	var queryCalls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Fatalf("ParseForm: %v", err)
		}
		if got := r.FormValue("app_id"); got != "tgx-app-id" {
			t.Fatalf("app_id=%s, want tgx-app-id", got)
		}
		if got := r.FormValue("app_key"); got != "tgx-app-key" {
			t.Fatalf("app_key=%s, want configured key", got)
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/commodity/inventory":
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "data": map[string]any{"count": 5}})
		case "/commodity/inventoryState":
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "data": map[string]any{}})
		case "/commodity/trade":
			tradeCalls++
			if got := r.FormValue("shared_code"); got != "IG-001" {
				t.Fatalf("shared_code=%s, want IG-001", got)
			}
			if got := r.FormValue("race"); got != "普通" {
				t.Fatalf("race=%s, want 普通", got)
			}
			if got := r.FormValue("request_no"); got != order.OrderNo {
				t.Fatalf("request_no=%s, want %s", got, order.OrderNo)
			}
			if got := r.FormValue("email"); got != "buyer@example.com" {
				t.Fatalf("email=%s, want buyer@example.com", got)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 200,
				"data": map[string]any{"trade_no": "TGX-E2E-9988", "status": 1},
			})
		case "/commodity/query":
			queryCalls++
			if got := r.FormValue("tradeNo"); got != "TGX-E2E-9988" {
				t.Fatalf("tradeNo=%s, want TGX-E2E-9988", got)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 200,
				"data": map[string]any{"secret": "account:pass", "status": 1, "delivery_status": 1},
			})
		default:
			t.Fatalf("path=%s", r.URL.Path)
		}
	}))
	defer server.Close()

	connSvc := NewSiteConnectionService(repository.NewSiteConnectionRepository(db), "test-key", t.TempDir())
	conn, err := connSvc.Create(CreateConnectionInput{
		Name: "tgx", BaseURL: server.URL,
		ApiKey: "tgx-app-id", ApiSecret: "tgx-app-key", Protocol: constants.ConnectionProtocolTGXAccount,
	})
	if err != nil {
		t.Fatalf("create connection: %v", err)
	}
	pm := &models.ProductMapping{
		ConnectionID:        conn.ID,
		LocalProductID:      1,
		UpstreamProductCode: "IG-001",
		Provider:            upstream.CatalogProviderTGX,
		IsActive:            true,
	}
	if err := db.Create(pm).Error; err != nil {
		t.Fatalf("create product mapping: %v", err)
	}
	if err := db.Create(&models.SKUMapping{ProductMappingID: pm.ID, LocalSKUID: 1, UpstreamSKUCode: "IG-001|普通", UpstreamIsActive: true}).Error; err != nil {
		t.Fatalf("create sku mapping: %v", err)
	}
	svc := newTestProcurementService(db, connSvc)

	if err := svc.CreateForOrder(order.ID); err != nil {
		t.Fatalf("CreateForOrder: %v", err)
	}
	proc, err := repository.NewProcurementOrderRepository(db).GetByLocalOrderID(order.ID)
	if err != nil {
		t.Fatalf("load procurement: %v", err)
	}
	if proc == nil || proc.Status != constants.ProcurementStatusPending {
		t.Fatalf("procurement after create=%+v, want pending", proc)
	}

	if err := svc.SubmitToUpstream(proc.ID); err != nil {
		t.Fatalf("SubmitToUpstream: %v", err)
	}
	var submitted models.ProcurementOrder
	db.First(&submitted, proc.ID)
	if submitted.Status != constants.ProcurementStatusAccepted || submitted.UpstreamOrderNo != "TGX-E2E-9988" {
		t.Fatalf("procurement after submit=%+v, want accepted upstream TGX-E2E-9988", submitted)
	}
	var fulfillingOrder models.Order
	db.First(&fulfillingOrder, order.ID)
	if fulfillingOrder.Status != constants.OrderStatusFulfilling {
		t.Fatalf("order after submit status=%s, want %s", fulfillingOrder.Status, constants.OrderStatusFulfilling)
	}

	svc.SyncAcceptedOrders()

	var fulfilledProc models.ProcurementOrder
	db.First(&fulfilledProc, proc.ID)
	if fulfilledProc.Status != constants.ProcurementStatusFulfilled {
		t.Fatalf("procurement after sync status=%s, want %s", fulfilledProc.Status, constants.ProcurementStatusFulfilled)
	}
	var deliveredOrder models.Order
	db.First(&deliveredOrder, order.ID)
	if deliveredOrder.Status != constants.OrderStatusDelivered {
		t.Fatalf("order after sync status=%s, want %s", deliveredOrder.Status, constants.OrderStatusDelivered)
	}
	var fulfillment models.Fulfillment
	if err := db.Where("order_id = ?", order.ID).First(&fulfillment).Error; err != nil {
		t.Fatalf("load fulfillment: %v", err)
	}
	if fulfillment.Payload != "account:pass" {
		t.Fatalf("payload=%q, want account:pass", fulfillment.Payload)
	}
	if tradeCalls != 1 || queryCalls != 1 {
		t.Fatalf("tradeCalls=%d queryCalls=%d, want 1/1", tradeCalls, queryCalls)
	}
}

func TestCancelManual_ProviderAcceptedUnsupported(t *testing.T) {
	db := setupProcurementTestDB(t)

	order := createProcTestOrder(t, db, "PROC-CANCEL-PROVIDER-001", constants.OrderStatusFulfilling, constants.FulfillmentTypeUpstream)
	conn := &models.SiteConnection{
		Name:     "FansGurus",
		Protocol: constants.ConnectionProtocolFansGurus,
		BaseURL:  "https://fansgurus.test/api",
		ApiKey:   "test-key",
		Status:   constants.ConnectionStatusActive,
	}
	if err := db.Create(conn).Error; err != nil {
		t.Fatalf("create connection: %v", err)
	}
	proc := createTestProcurementOrder(t, db, conn.ID, order.ID, order.OrderNo, constants.ProcurementStatusAccepted)
	if err := db.Model(proc).Updates(map[string]interface{}{
		"upstream_order_id": uint(123),
		"upstream_order_no": "123",
	}).Error; err != nil {
		t.Fatalf("seed procurement upstream id: %v", err)
	}

	connSvc := NewSiteConnectionService(repository.NewSiteConnectionRepository(db), "test-key", t.TempDir())
	svc := newTestProcurementService(db, connSvc)

	if err := svc.CancelManual(proc.ID); err != ErrProcurementCancelUnsupported {
		t.Fatalf("CancelManual error=%v, want %v", err, ErrProcurementCancelUnsupported)
	}

	var updatedProc models.ProcurementOrder
	if err := db.First(&updatedProc, proc.ID).Error; err != nil {
		t.Fatalf("load procurement: %v", err)
	}
	if updatedProc.Status != constants.ProcurementStatusAccepted {
		t.Fatalf("status=%s, want %s", updatedProc.Status, constants.ProcurementStatusAccepted)
	}
}

func TestCancelManual_FailedLocalOnlyCancels(t *testing.T) {
	db := setupProcurementTestDB(t)

	order := createProcTestOrder(t, db, "PROC-CANCEL-FAILED-001", constants.OrderStatusPaid, constants.FulfillmentTypeUpstream)
	conn := &models.SiteConnection{
		Name:     "TGX",
		Protocol: constants.ConnectionProtocolTGXAccount,
		BaseURL:  "https://tgx.test/shared",
		ApiKey:   "app-id",
		Status:   constants.ConnectionStatusActive,
	}
	if err := db.Create(conn).Error; err != nil {
		t.Fatalf("create connection: %v", err)
	}
	proc := createTestProcurementOrder(t, db, conn.ID, order.ID, order.OrderNo, constants.ProcurementStatusFailed)

	connSvc := NewSiteConnectionService(repository.NewSiteConnectionRepository(db), "test-key", t.TempDir())
	svc := newTestProcurementService(db, connSvc)

	if err := svc.CancelManual(proc.ID); err != nil {
		t.Fatalf("CancelManual: %v", err)
	}

	var updatedProc models.ProcurementOrder
	if err := db.First(&updatedProc, proc.ID).Error; err != nil {
		t.Fatalf("load procurement: %v", err)
	}
	if updatedProc.Status != constants.ProcurementStatusCanceled {
		t.Fatalf("status=%s, want %s", updatedProc.Status, constants.ProcurementStatusCanceled)
	}
}

// ── CreateForOrder tests ──

func TestCreateForOrder_SkipsNonUpstreamItems(t *testing.T) {
	db := setupProcurementTestDB(t)

	order := createProcTestOrder(t, db, "PROC-SKIP-001", constants.OrderStatusPaid, constants.FulfillmentTypeAuto)

	connSvc := NewSiteConnectionService(repository.NewSiteConnectionRepository(db), "test-key", t.TempDir())
	svc := newTestProcurementService(db, connSvc)

	if err := svc.CreateForOrder(order.ID); err != nil {
		t.Fatalf("CreateForOrder: %v", err)
	}

	// 验证没有创建采购单
	var count int64
	db.Model(&models.ProcurementOrder{}).Count(&count)
	if count != 0 {
		t.Errorf("expected no procurement orders for auto fulfillment, got %d", count)
	}
}

func TestCreateForOrder_IdempotentSkipsDuplicate(t *testing.T) {
	db := setupProcurementTestDB(t)

	order := createProcTestOrder(t, db, "PROC-DUP-001", constants.OrderStatusPaid, constants.FulfillmentTypeUpstream)
	pm := &models.ProductMapping{ConnectionID: 1, LocalProductID: 1, UpstreamProductID: 101, IsActive: true}
	db.Create(pm)

	connSvc := NewSiteConnectionService(repository.NewSiteConnectionRepository(db), "test-key", t.TempDir())
	svc := newTestProcurementService(db, connSvc)

	// 第一次创建成功
	if err := svc.CreateForOrder(order.ID); err != nil {
		t.Fatalf("first CreateForOrder: %v", err)
	}

	// 第二次应该返回 ErrProcurementExists
	err := svc.CreateForOrder(order.ID)
	if err != ErrProcurementExists {
		t.Errorf("expected ErrProcurementExists on duplicate, got: %v", err)
	}
}

// ── PollUpstreamStatus test ──

func TestPollUpstreamStatus_Delivered(t *testing.T) {
	db := setupProcurementTestDB(t)

	order := createProcTestOrder(t, db, "PROC-POLL-001", constants.OrderStatusFulfilling, constants.FulfillmentTypeUpstream)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		now := time.Now()
		json.NewEncoder(w).Encode(map[string]any{
			"order_id": 999,
			"order_no": "UP-999",
			"status":   "delivered",
			"amount":   "50.00",
			"currency": "CNY",
			"fulfillment": map[string]any{
				"type":         "auto",
				"status":       "delivered",
				"payload":      "KEY-001\nKEY-002",
				"delivered_at": now.Format(time.RFC3339),
			},
		})
	}))
	defer server.Close()

	connSvc := NewSiteConnectionService(repository.NewSiteConnectionRepository(db), "test-key", t.TempDir())
	conn, _ := connSvc.Create(CreateConnectionInput{
		Name: "poll-upstream", BaseURL: server.URL,
		ApiKey: "key", ApiSecret: "secret", Protocol: constants.ConnectionProtocolDujiaoNext,
	})

	proc := createTestProcurementOrder(t, db, conn.ID, order.ID, order.OrderNo, "accepted")
	db.Model(proc).Updates(map[string]interface{}{
		"upstream_order_id": uint(999),
		"upstream_order_no": "UP-999",
	})

	svc := newTestProcurementService(db, connSvc)

	if err := svc.PollUpstreamStatus(proc.ID); err != nil {
		t.Fatalf("PollUpstreamStatus: %v", err)
	}

	// 验证采购单状态 = fulfilled
	var updatedProc models.ProcurementOrder
	db.First(&updatedProc, proc.ID)
	if updatedProc.Status != "fulfilled" {
		t.Errorf("expected procurement status 'fulfilled', got %q", updatedProc.Status)
	}

	// 验证本地订单状态 = delivered
	var updatedOrder models.Order
	db.First(&updatedOrder, order.ID)
	if updatedOrder.Status != constants.OrderStatusDelivered {
		t.Errorf("expected order status %q, got %q", constants.OrderStatusDelivered, updatedOrder.Status)
	}
}

func TestPollUpstreamStatus_FulfilledMappedToDelivered(t *testing.T) {
	db := setupProcurementTestDB(t)

	order := createProcTestOrder(t, db, "PROC-POLL-FULLFILLED-001", constants.OrderStatusFulfilling, constants.FulfillmentTypeUpstream)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		now := time.Now()
		_ = json.NewEncoder(w).Encode(map[string]any{
			"order_id": 1001,
			"order_no": "UP-1001",
			"status":   "fulfilled",
			"amount":   "50.00",
			"currency": "CNY",
			"fulfillment": map[string]any{
				"type":         "auto",
				"status":       "delivered",
				"payload":      "KEY-003\nKEY-004",
				"delivered_at": now.Format(time.RFC3339),
			},
		})
	}))
	defer server.Close()

	connSvc := NewSiteConnectionService(repository.NewSiteConnectionRepository(db), "test-key", t.TempDir())
	conn, _ := connSvc.Create(CreateConnectionInput{
		Name: "poll-upstream-fulfilled", BaseURL: server.URL,
		ApiKey: "key", ApiSecret: "secret", Protocol: constants.ConnectionProtocolDujiaoNext,
	})

	proc := createTestProcurementOrder(t, db, conn.ID, order.ID, order.OrderNo, "accepted")
	db.Model(proc).Updates(map[string]interface{}{
		"upstream_order_id": uint(1001),
		"upstream_order_no": "UP-1001",
	})

	svc := newTestProcurementService(db, connSvc)
	if err := svc.PollUpstreamStatus(proc.ID); err != nil {
		t.Fatalf("PollUpstreamStatus: %v", err)
	}

	var updatedProc models.ProcurementOrder
	db.First(&updatedProc, proc.ID)
	if updatedProc.Status != "fulfilled" {
		t.Errorf("expected procurement status 'fulfilled', got %q", updatedProc.Status)
	}

	var updatedOrder models.Order
	db.First(&updatedOrder, order.ID)
	if updatedOrder.Status != constants.OrderStatusDelivered {
		t.Errorf("expected order status %q, got %q", constants.OrderStatusDelivered, updatedOrder.Status)
	}
}

func TestPollUpstreamStatus_FansGurusCompleted(t *testing.T) {
	db := setupProcurementTestDB(t)

	order := createProcTestOrder(t, db, "PROC-POLL-FG-001", constants.OrderStatusFulfilling, constants.FulfillmentTypeUpstream)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Fatalf("ParseForm: %v", err)
		}
		if got := r.FormValue("action"); got != "status" {
			t.Fatalf("action=%s, want status", got)
		}
		if got := r.FormValue("order"); got != "9988" {
			t.Fatalf("order=%s, want 9988", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "Completed", "charge": "0.50", "currency": "USD"})
	}))
	defer server.Close()

	connSvc := NewSiteConnectionService(repository.NewSiteConnectionRepository(db), "test-key", t.TempDir())
	conn, err := connSvc.Create(CreateConnectionInput{
		Name: "fansgurus", BaseURL: server.URL,
		ApiKey: "fans-key", ApiSecret: "unused", Protocol: constants.ConnectionProtocolFansGurus,
	})
	if err != nil {
		t.Fatalf("create connection: %v", err)
	}
	proc := createTestProcurementOrder(t, db, conn.ID, order.ID, order.OrderNo, constants.ProcurementStatusAccepted)
	db.Model(proc).Updates(map[string]interface{}{
		"upstream_order_id": uint(9988),
		"upstream_order_no": "9988",
	})
	svc := newTestProcurementService(db, connSvc)

	if err := svc.PollUpstreamStatus(proc.ID); err != nil {
		t.Fatalf("PollUpstreamStatus: %v", err)
	}

	var updatedProc models.ProcurementOrder
	db.First(&updatedProc, proc.ID)
	if updatedProc.Status != constants.ProcurementStatusFulfilled {
		t.Fatalf("procurement status=%s, want %s", updatedProc.Status, constants.ProcurementStatusFulfilled)
	}
	var updatedOrder models.Order
	db.First(&updatedOrder, order.ID)
	if updatedOrder.Status != constants.OrderStatusDelivered {
		t.Fatalf("order status=%s, want %s", updatedOrder.Status, constants.OrderStatusDelivered)
	}
}

func TestSyncAcceptedOrders_FansGurusCompleted(t *testing.T) {
	db := setupProcurementTestDB(t)

	order := createProcTestOrder(t, db, "PROC-SYNC-FG-001", constants.OrderStatusFulfilling, constants.FulfillmentTypeUpstream)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Fatalf("ParseForm: %v", err)
		}
		if got := r.FormValue("action"); got != "status" {
			t.Fatalf("action=%s, want status", got)
		}
		if got := r.FormValue("order"); got != "9989" {
			t.Fatalf("order=%s, want 9989", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "Completed", "charge": "0.50", "currency": "USD"})
	}))
	defer server.Close()

	connSvc := NewSiteConnectionService(repository.NewSiteConnectionRepository(db), "test-key", t.TempDir())
	conn, err := connSvc.Create(CreateConnectionInput{
		Name: "fansgurus", BaseURL: server.URL,
		ApiKey: "fans-key", ApiSecret: "unused", Protocol: constants.ConnectionProtocolFansGurus,
	})
	if err != nil {
		t.Fatalf("create connection: %v", err)
	}
	proc := createTestProcurementOrder(t, db, conn.ID, order.ID, order.OrderNo, constants.ProcurementStatusAccepted)
	db.Model(proc).Updates(map[string]interface{}{
		"upstream_order_id": uint(9989),
		"upstream_order_no": "9989",
	})
	svc := newTestProcurementService(db, connSvc)

	svc.SyncAcceptedOrders()

	var updatedProc models.ProcurementOrder
	db.First(&updatedProc, proc.ID)
	if updatedProc.Status != constants.ProcurementStatusFulfilled {
		t.Fatalf("procurement status=%s, want %s", updatedProc.Status, constants.ProcurementStatusFulfilled)
	}
	var updatedOrder models.Order
	db.First(&updatedOrder, order.ID)
	if updatedOrder.Status != constants.OrderStatusDelivered {
		t.Fatalf("order status=%s, want %s", updatedOrder.Status, constants.OrderStatusDelivered)
	}
}

func TestPollUpstreamStatus_TGXQueryDeliveredSecret(t *testing.T) {
	db := setupProcurementTestDB(t)

	order := createProcTestOrder(t, db, "PROC-POLL-TGX-001", constants.OrderStatusFulfilling, constants.FulfillmentTypeUpstream)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/commodity/query" {
			t.Fatalf("path=%s, want /commodity/query", r.URL.Path)
		}
		if err := r.ParseForm(); err != nil {
			t.Fatalf("ParseForm: %v", err)
		}
		if got := r.FormValue("tradeNo"); got != "TGX-9988" {
			t.Fatalf("tradeNo=%s, want TGX-9988", got)
		}
		if got := r.FormValue("app_id"); got != "tgx-app-id" {
			t.Fatalf("app_id=%s, want tgx-app-id", got)
		}
		if got := r.FormValue("app_key"); got != "tgx-app-key" {
			t.Fatalf("app_key=%s, want configured key", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 200,
			"data": map[string]any{"secret": "account:pass", "status": 1, "delivery_status": 1},
		})
	}))
	defer server.Close()

	connSvc := NewSiteConnectionService(repository.NewSiteConnectionRepository(db), "test-key", t.TempDir())
	conn, err := connSvc.Create(CreateConnectionInput{
		Name: "tgx", BaseURL: server.URL,
		ApiKey: "tgx-app-id", ApiSecret: "tgx-app-key", Protocol: constants.ConnectionProtocolTGXAccount,
	})
	if err != nil {
		t.Fatalf("create connection: %v", err)
	}
	proc := createTestProcurementOrder(t, db, conn.ID, order.ID, order.OrderNo, constants.ProcurementStatusAccepted)
	db.Model(proc).Update("upstream_order_no", "TGX-9988")
	svc := newTestProcurementService(db, connSvc)

	if err := svc.PollUpstreamStatus(proc.ID); err != nil {
		t.Fatalf("PollUpstreamStatus: %v", err)
	}

	var updatedProc models.ProcurementOrder
	db.First(&updatedProc, proc.ID)
	if updatedProc.Status != constants.ProcurementStatusFulfilled {
		t.Fatalf("procurement status=%s, want %s", updatedProc.Status, constants.ProcurementStatusFulfilled)
	}
	var fulfillment models.Fulfillment
	if err := db.Where("order_id = ?", order.ID).First(&fulfillment).Error; err != nil {
		t.Fatalf("load fulfillment: %v", err)
	}
	if fulfillment.Payload != "account:pass" {
		t.Fatalf("payload=%q, want account:pass", fulfillment.Payload)
	}
}

func TestPollUpstreamStatus_TGXQueryPendingKeepsAccepted(t *testing.T) {
	db := setupProcurementTestDB(t)

	order := createProcTestOrder(t, db, "PROC-POLL-TGX-PENDING-001", constants.OrderStatusFulfilling, constants.FulfillmentTypeUpstream)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 200,
			"data": map[string]any{"status": 1, "delivery_status": 0},
		})
	}))
	defer server.Close()

	connSvc := NewSiteConnectionService(repository.NewSiteConnectionRepository(db), "test-key", t.TempDir())
	conn, err := connSvc.Create(CreateConnectionInput{
		Name: "tgx", BaseURL: server.URL,
		ApiKey: "tgx-app-id", ApiSecret: "tgx-app-key", Protocol: constants.ConnectionProtocolTGXAccount,
	})
	if err != nil {
		t.Fatalf("create connection: %v", err)
	}
	proc := createTestProcurementOrder(t, db, conn.ID, order.ID, order.OrderNo, constants.ProcurementStatusAccepted)
	db.Model(proc).Update("upstream_order_no", "TGX-PENDING")
	svc := newTestProcurementService(db, connSvc)

	if err := svc.PollUpstreamStatus(proc.ID); err != nil {
		t.Fatalf("PollUpstreamStatus: %v", err)
	}

	var updatedProc models.ProcurementOrder
	db.First(&updatedProc, proc.ID)
	if updatedProc.Status != constants.ProcurementStatusAccepted {
		t.Fatalf("procurement status=%s, want %s", updatedProc.Status, constants.ProcurementStatusAccepted)
	}
	var count int64
	db.Model(&models.Fulfillment{}).Where("order_id = ?", order.ID).Count(&count)
	if count != 0 {
		t.Fatalf("fulfillment count=%d, want 0", count)
	}
}

// ── Unit tests for pure functions ──

func TestParseRetryIntervals(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected []time.Duration
	}{
		{
			name:     "empty string returns defaults",
			input:    "",
			expected: []time.Duration{30 * time.Second, 60 * time.Second, 300 * time.Second},
		},
		{
			name:     "valid array",
			input:    "[10,20,30]",
			expected: []time.Duration{10 * time.Second, 20 * time.Second, 30 * time.Second},
		},
		{
			name:     "with spaces",
			input:    "[ 10 , 20 , 30 ]",
			expected: []time.Duration{10 * time.Second, 20 * time.Second, 30 * time.Second},
		},
		{
			name:     "invalid entries skipped",
			input:    "[10,abc,30]",
			expected: []time.Duration{10 * time.Second, 30 * time.Second},
		},
		{
			name:     "all invalid returns defaults",
			input:    "[abc,def]",
			expected: []time.Duration{30 * time.Second, 60 * time.Second, 300 * time.Second},
		},
		{
			name:     "negative values skipped",
			input:    "[10,-5,30]",
			expected: []time.Duration{10 * time.Second, 30 * time.Second},
		},
		{
			name:     "zero values skipped",
			input:    "[0,10]",
			expected: []time.Duration{10 * time.Second},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := parseRetryIntervals(tt.input)
			if len(result) != len(tt.expected) {
				t.Fatalf("expected %d intervals, got %d: %v", len(tt.expected), len(result), result)
			}
			for i, d := range result {
				if d != tt.expected[i] {
					t.Errorf("interval[%d]: expected %v, got %v", i, tt.expected[i], d)
				}
			}
		})
	}
}

func TestIsRetryableErrorCode(t *testing.T) {
	nonRetryable := []string{
		"insufficient_balance",
		"payment_failed",
		"product_unavailable",
		"sku_unavailable",
		"invalid_request",
		"unauthorized",
		"forbidden",
		"duplicate_order",
		"product_out_of_stock",
	}
	for _, code := range nonRetryable {
		if isRetryableErrorCode(code) {
			t.Errorf("expected %q to be non-retryable", code)
		}
	}

	retryable := []string{
		"server_error",
		"timeout",
		"network_error",
		"unknown_error",
		"",
	}
	for _, code := range retryable {
		if !isRetryableErrorCode(code) {
			t.Errorf("expected %q to be retryable", code)
		}
	}

	// 测试带空格的情况
	if isRetryableErrorCode("  unauthorized  ") {
		t.Error("expected trimmed 'unauthorized' to be non-retryable")
	}
}
