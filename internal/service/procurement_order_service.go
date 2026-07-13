package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/dujiao-next/internal/config"
	"github.com/dujiao-next/internal/constants"
	"github.com/dujiao-next/internal/logger"
	"github.com/dujiao-next/internal/models"
	"github.com/dujiao-next/internal/queue"
	"github.com/dujiao-next/internal/repository"
	"github.com/dujiao-next/internal/upstream"

	"github.com/google/uuid"
	"github.com/hibiken/asynq"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

var (
	ErrProcurementNotFound          = errors.New("procurement order not found")
	ErrProcurementExists            = errors.New("procurement order already exists")
	ErrProcurementStatusInvalid     = errors.New("procurement order status invalid")
	ErrProcurementRetryDenied       = errors.New("procurement order retry denied")
	ErrProcurementCancelUnsupported = errors.New("procurement order cancel unsupported")
)

const providerSubmitTemporarilyUnavailable = "provider_submit_temporarily_unavailable"

// ProcurementOrderService 采购单服务
type ProcurementOrderService struct {
	procRepo              repository.ProcurementOrderRepository
	orderRepo             repository.OrderRepository
	fulfillmentRepo       repository.FulfillmentRepository
	mappingRepo           repository.ProductMappingRepository
	skuMapRepo            repository.SKUMappingRepository
	connSvc               *SiteConnectionService
	queueClient           *queue.Client
	settingService        *SettingService
	defaultEmailConfig    config.EmailConfig
	fulfillSvc            *FulfillmentService
	downstreamCallbackSvc *DownstreamCallbackService
	notificationSvc       *NotificationService
}

// SetDownstreamCallbackService 设置下游回调服务（解决循环依赖）
func (s *ProcurementOrderService) SetDownstreamCallbackService(svc *DownstreamCallbackService) {
	s.downstreamCallbackSvc = svc
}

// SetNotificationService 设置通知服务（解决循环依赖）
func (s *ProcurementOrderService) SetNotificationService(svc *NotificationService) {
	s.notificationSvc = svc
}

// NewProcurementOrderService 创建采购单服务
func NewProcurementOrderService(
	procRepo repository.ProcurementOrderRepository,
	orderRepo repository.OrderRepository,
	fulfillmentRepo repository.FulfillmentRepository,
	mappingRepo repository.ProductMappingRepository,
	skuMapRepo repository.SKUMappingRepository,
	connSvc *SiteConnectionService,
	queueClient *queue.Client,
	settingService *SettingService,
	defaultEmailConfig config.EmailConfig,
	fulfillSvc *FulfillmentService,
) *ProcurementOrderService {
	return &ProcurementOrderService{
		procRepo:           procRepo,
		orderRepo:          orderRepo,
		fulfillmentRepo:    fulfillmentRepo,
		mappingRepo:        mappingRepo,
		skuMapRepo:         skuMapRepo,
		connSvc:            connSvc,
		queueClient:        queueClient,
		settingService:     settingService,
		defaultEmailConfig: defaultEmailConfig,
		fulfillSvc:         fulfillSvc,
	}
}

// CreateForOrder 为已支付订单创建采购单（上游交付类型）
func (s *ProcurementOrderService) CreateForOrder(orderID uint) error {
	order, err := s.orderRepo.GetByID(orderID)
	if err != nil {
		return fmt.Errorf("load order: %w", err)
	}
	if order == nil {
		return ErrOrderNotFound
	}

	// 父订单有子订单：遍历子订单
	if order.ParentID == nil && len(order.Children) > 0 {
		for i := range order.Children {
			child := &order.Children[i]
			if !s.hasUpstreamItems(child) {
				continue
			}
			if err := s.createProcurementForSingleOrder(child); err != nil {
				logger.Warnw("procurement_create_child_failed",
					"parent_order_id", orderID,
					"child_order_id", child.ID,
					"error", err,
				)
				return err
			}
		}
		return nil
	}

	// 单订单
	if !s.hasUpstreamItems(order) {
		return nil
	}
	return s.createProcurementForSingleOrder(order)
}

// createProcurementForSingleOrder 为单个订单创建采购单
func (s *ProcurementOrderService) createProcurementForSingleOrder(order *models.Order) error {
	// 检查是否已存在
	existing, err := s.procRepo.GetByLocalOrderID(order.ID)
	if err != nil {
		return fmt.Errorf("check existing procurement: %w", err)
	}
	if existing != nil {
		return ErrProcurementExists
	}

	if len(order.Items) == 0 {
		return fmt.Errorf("order %d has no items", order.ID)
	}
	item := order.Items[0]

	// 查找商品映射
	mapping, err := s.mappingRepo.GetByLocalProductID(item.ProductID)
	if err != nil {
		return fmt.Errorf("lookup product mapping: %w", err)
	}
	if mapping == nil {
		return fmt.Errorf("no product mapping for product %d", item.ProductID)
	}
	if !mapping.IsActive || mapping.UpstreamStatus == models.UpstreamStatusInactive || mapping.UpstreamStatus == models.UpstreamStatusDeleted {
		return ErrMappingInactive
	}
	conn, err := s.connSvc.GetByID(mapping.ConnectionID)
	if err != nil {
		return fmt.Errorf("load connection: %w", err)
	}
	if conn == nil || conn.Status == constants.ConnectionStatusDisabled {
		return ErrConnectionInvalid
	}

	procOrder := &models.ProcurementOrder{
		ConnectionID:    mapping.ConnectionID,
		LocalOrderID:    order.ID,
		LocalOrderNo:    order.OrderNo,
		Status:          "pending",
		LocalSellAmount: order.TotalAmount,
		Currency:        order.Currency,
		TraceID:         uuid.NewString(),
	}

	if err := s.procRepo.Create(procOrder); err != nil {
		return fmt.Errorf("create procurement order: %w", err)
	}

	logger.Infow("procurement_order_created",
		"procurement_order_id", procOrder.ID,
		"local_order_id", order.ID,
		"connection_id", mapping.ConnectionID,
	)

	// 入队提交任务
	if s.queueClient != nil {
		if err := s.queueClient.EnqueueProcurementSubmit(queue.ProcurementSubmitPayload{
			ProcurementOrderID: procOrder.ID,
		}); err != nil {
			logger.Warnw("procurement_enqueue_submit_failed",
				"procurement_order_id", procOrder.ID,
				"error", err,
			)
		}
	}

	return nil
}

// SubmitToUpstream Worker 调用：向上游站点提交采购单
func (s *ProcurementOrderService) SubmitToUpstream(procurementOrderID uint) error {
	procOrder, err := s.procRepo.GetByID(procurementOrderID)
	if err != nil {
		return fmt.Errorf("load procurement order: %w", err)
	}
	if procOrder == nil {
		return ErrProcurementNotFound
	}

	// 校验状态
	if procOrder.Status != "pending" && procOrder.Status != "failed" {
		return ErrProcurementStatusInvalid
	}

	// 获取连接和适配器
	conn, err := s.connSvc.GetByID(procOrder.ConnectionID)
	if err != nil {
		s.markProcurementError(procOrder, fmt.Sprintf("load connection failed: %v", err))
		return fmt.Errorf("load connection: %w", err)
	}
	if conn == nil {
		s.rejectProcurement(procOrder, fmt.Sprintf("connection %d not found", procOrder.ConnectionID))
		return nil // 永久性错误，不重试
	}
	if conn.Status == constants.ConnectionStatusDisabled {
		s.rejectProcurement(procOrder, fmt.Sprintf("connection %d is not active", procOrder.ConnectionID))
		return nil
	}
	switch conn.Protocol {
	case constants.ConnectionProtocolFansGurus, constants.ConnectionProtocolTGXAccount:
		return s.submitProviderProcurement(procOrder, conn)
	}

	adapter, err := s.connSvc.GetAdapter(conn)
	if err != nil {
		s.rejectProcurement(procOrder, fmt.Sprintf("get adapter failed: %v", err))
		return nil // 配置错误，不重试
	}

	// 加载本地订单获取 SKU 信息
	localOrder, err := s.orderRepo.GetByID(procOrder.LocalOrderID)
	if err != nil {
		s.markProcurementError(procOrder, fmt.Sprintf("load local order failed: %v", err))
		return fmt.Errorf("load local order: %w", err)
	}
	if localOrder == nil {
		s.rejectProcurement(procOrder, fmt.Sprintf("local order %d not found", procOrder.LocalOrderID))
		return nil // 永久性错误，不重试
	}
	if len(localOrder.Items) == 0 {
		s.rejectProcurement(procOrder, fmt.Sprintf("local order %d has no items", localOrder.ID))
		return nil // 永久性错误，不重试
	}
	item := localOrder.Items[0]

	// 查找 SKU 映射
	skuMapping, err := s.skuMapRepo.GetByLocalSKUID(item.SKUID)
	if err != nil {
		s.markProcurementError(procOrder, fmt.Sprintf("lookup sku mapping failed: %v", err))
		return fmt.Errorf("lookup sku mapping: %w", err)
	}
	if skuMapping == nil {
		s.rejectProcurement(procOrder, fmt.Sprintf("no sku mapping for local sku %d", item.SKUID))
		return nil // 永久性错误，不重试
	}

	// 构建上游请求
	req := upstream.CreateUpstreamOrderReq{
		SKUID:             skuMapping.UpstreamSKUID,
		Quantity:          item.Quantity,
		DownstreamOrderNo: localOrder.OrderNo,
		TraceID:           procOrder.TraceID,
		CallbackURL:       conn.CallbackURL,
	}

	// 传递人工表单数据（如有）
	if len(item.ManualFormSubmissionJSON) > 0 {
		req.ManualFormData = item.ManualFormSubmissionJSON
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	resp, err := adapter.CreateOrder(ctx, req)
	if err != nil {
		return s.handleSubmitFailure(procOrder, conn, fmt.Sprintf("upstream request error: %v", err), true)
	}

	if !resp.OK {
		retryable := isRetryableErrorCode(resp.ErrorCode)
		errMsg := resp.ErrorMessage
		if errMsg == "" {
			errMsg = resp.ErrorCode
		}
		return s.handleSubmitFailure(procOrder, conn, errMsg, retryable)
	}

	// 成功：更新状态，重置 retry_count 用于轮询阶段
	now := time.Now()
	updates := map[string]interface{}{
		"upstream_order_id": resp.OrderID,
		"upstream_order_no": resp.OrderNo,
		"upstream_amount":   resp.Amount,
		"upstream_currency": resp.Currency,
		"error_message":     "",
		"retry_count":       0,
		"updated_at":        now,
	}
	if err := s.procRepo.UpdateStatus(procOrder.ID, "accepted", updates); err != nil {
		return fmt.Errorf("update procurement status: %w", err)
	}

	logger.Infow("procurement_order_accepted",
		"procurement_order_id", procOrder.ID,
		"upstream_order_id", resp.OrderID,
		"upstream_order_no", resp.OrderNo,
	)

	// 更新本地订单状态为 fulfilling
	_ = s.orderRepo.UpdateStatus(localOrder.ID, constants.OrderStatusFulfilling, map[string]interface{}{
		"updated_at": now,
	})

	// 入队轮询任务（30s 延迟，作为回调的 fallback）
	if s.queueClient != nil {
		_ = s.queueClient.EnqueueProcurementPollStatus(queue.ProcurementPollStatusPayload{
			ProcurementOrderID: procOrder.ID,
		}, 30*time.Second)
	}

	return nil
}

func (s *ProcurementOrderService) submitProviderProcurement(procOrder *models.ProcurementOrder, conn *models.SiteConnection) error {
	localOrder, item, mapping, skuMapping, err := s.loadProviderProcurementContext(procOrder)
	if err != nil {
		s.rejectProcurement(procOrder, err.Error())
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	switch conn.Protocol {
	case constants.ConnectionProtocolFansGurus:
		return s.submitFansGurusProcurement(ctx, procOrder, conn, localOrder, item, mapping)
	case constants.ConnectionProtocolTGXAccount:
		return s.submitTGXProcurement(ctx, procOrder, conn, localOrder, item, skuMapping)
	default:
		s.rejectProcurement(procOrder, fmt.Sprintf("unsupported provider protocol %s", conn.Protocol))
		return nil
	}
}

func (s *ProcurementOrderService) loadProviderProcurementContext(procOrder *models.ProcurementOrder) (*models.Order, models.OrderItem, *models.ProductMapping, *models.SKUMapping, error) {
	localOrder, err := s.orderRepo.GetByID(procOrder.LocalOrderID)
	if err != nil {
		return nil, models.OrderItem{}, nil, nil, fmt.Errorf("load local order failed: %w", err)
	}
	if localOrder == nil {
		return nil, models.OrderItem{}, nil, nil, fmt.Errorf("local order %d not found", procOrder.LocalOrderID)
	}
	if len(localOrder.Items) == 0 {
		return nil, models.OrderItem{}, nil, nil, fmt.Errorf("local order %d has no items", localOrder.ID)
	}
	item := localOrder.Items[0]

	mapping, err := s.mappingRepo.GetByLocalProductID(item.ProductID)
	if err != nil {
		return nil, models.OrderItem{}, nil, nil, fmt.Errorf("lookup product mapping failed: %w", err)
	}
	if mapping == nil {
		return nil, models.OrderItem{}, nil, nil, fmt.Errorf("no product mapping for product %d", item.ProductID)
	}
	if !mapping.IsActive || mapping.UpstreamStatus == models.UpstreamStatusInactive || mapping.UpstreamStatus == models.UpstreamStatusDeleted {
		return nil, models.OrderItem{}, nil, nil, ErrMappingInactive
	}
	skuMapping, err := s.skuMapRepo.GetByLocalSKUID(item.SKUID)
	if err != nil {
		return nil, models.OrderItem{}, nil, nil, fmt.Errorf("lookup sku mapping failed: %w", err)
	}
	if skuMapping == nil {
		return nil, models.OrderItem{}, nil, nil, fmt.Errorf("no sku mapping for local sku %d", item.SKUID)
	}
	return localOrder, item, mapping, skuMapping, nil
}

func (s *ProcurementOrderService) submitFansGurusProcurement(ctx context.Context, procOrder *models.ProcurementOrder, conn *models.SiteConnection, localOrder *models.Order, item models.OrderItem, mapping *models.ProductMapping) error {
	serviceID, err := strconv.ParseUint(strings.TrimSpace(mapping.UpstreamProductCode), 10, 64)
	if err != nil || serviceID == 0 {
		s.rejectProcurement(procOrder, fmt.Sprintf("invalid fansgurus service id %q", mapping.UpstreamProductCode))
		return nil
	}
	req := upstream.FansGurusAddOrderRequest{
		Service:  uint(serviceID),
		Link:     jsonStringValue(item.ManualFormSubmissionJSON, "link"),
		Quantity: item.Quantity,
	}
	if req.Link == "" {
		s.rejectProcurement(procOrder, "fansgurus link is required")
		return nil
	}

	client := upstream.NewFansGurusClient(conn.BaseURL, conn.ApiKey)
	resp, err := client.AddOrder(ctx, req)
	if err != nil {
		if isDefinitiveFansGurusSubmitError(err) {
			return s.handleSubmitFailure(procOrder, conn, fmt.Sprintf("fansgurus request error: %v", err), false)
		}
		return s.failProviderSubmitForUser(procOrder, localOrder, fmt.Sprintf("fansgurus submit result unavailable: %v", err))
	}
	if resp == nil || resp.Order == 0 {
		return s.failProviderSubmitForUser(procOrder, localOrder, "fansgurus submit result unavailable: empty order id returned")
	}

	now := time.Now()
	updates := map[string]interface{}{
		"upstream_order_id": resp.Order,
		"upstream_order_no": fmt.Sprintf("%d", resp.Order),
		"upstream_amount":   resp.Charge,
		"upstream_currency": "USD",
		"error_message":     "",
		"retry_count":       0,
		"updated_at":        now,
	}
	if err := s.procRepo.UpdateStatus(procOrder.ID, constants.ProcurementStatusAccepted, updates); err != nil {
		return fmt.Errorf("update procurement status: %w", err)
	}
	_ = s.orderRepo.UpdateStatus(localOrder.ID, constants.OrderStatusFulfilling, map[string]interface{}{"updated_at": now})
	if s.queueClient != nil {
		_ = s.queueClient.EnqueueProcurementPollStatus(queue.ProcurementPollStatusPayload{ProcurementOrderID: procOrder.ID}, 30*time.Second)
	}
	return nil
}

func (s *ProcurementOrderService) submitTGXProcurement(ctx context.Context, procOrder *models.ProcurementOrder, conn *models.SiteConnection, localOrder *models.Order, item models.OrderItem, skuMapping *models.SKUMapping) error {
	sharedCode, race := splitTGXUpstreamSKUCode(skuMapping.UpstreamSKUCode)
	if sharedCode == "" {
		s.rejectProcurement(procOrder, "tgx shared code is required")
		return nil
	}
	appKey, err := s.connSvc.DecryptSecret(conn.ApiSecret)
	if err != nil {
		s.rejectProcurement(procOrder, fmt.Sprintf("decrypt tgx app key failed: %v", err))
		return nil
	}
	req := upstream.TGXTradeRequest{
		SharedCode: sharedCode,
		Race:       race,
		Quantity:   item.Quantity,
		RequestNo:  localOrder.OrderNo,
		Widget:     jsonStringMap(item.ManualFormSubmissionJSON),
	}

	client := upstream.NewTGXClient(conn.BaseURL, conn.ApiKey, appKey)
	if err := s.verifyTGXInventory(ctx, client, skuMapping, sharedCode, race, item.Quantity); err != nil {
		if errors.Is(err, ErrUpstreamStockInsufficient) {
			s.rejectProcurement(procOrder, "tgx inventory is insufficient")
			return nil
		}
		return s.failProviderSubmitForUser(procOrder, localOrder, fmt.Sprintf("tgx inventory check unavailable: %v", err))
	}
	resp, err := client.Trade(ctx, req)
	if err != nil {
		if recovered, recoverErr := queryTGXTradeByRequestNo(client, localOrder.OrderNo); recoverErr == nil && recovered != nil && strings.TrimSpace(recovered.TradeNo) != "" {
			return s.acceptTGXProcurement(procOrder, localOrder, recovered.TradeNo, recovered.Secret, recovered.Status)
		}
		if isDefinitiveTGXSubmitError(err) {
			return s.handleSubmitFailure(procOrder, conn, fmt.Sprintf("tgx request error: %v", err), false)
		}
		return s.failProviderSubmitForUser(procOrder, localOrder, fmt.Sprintf("tgx submit result unavailable: %v", err))
	}
	if resp == nil || strings.TrimSpace(resp.TradeNo) == "" {
		if recovered, recoverErr := queryTGXTradeByRequestNo(client, localOrder.OrderNo); recoverErr == nil && recovered != nil && strings.TrimSpace(recovered.TradeNo) != "" {
			return s.acceptTGXProcurement(procOrder, localOrder, recovered.TradeNo, recovered.Secret, recovered.Status)
		}
		return s.failProviderSubmitForUser(procOrder, localOrder, "tgx submit result unavailable: empty trade number returned")
	}

	return s.acceptTGXProcurement(procOrder, localOrder, resp.TradeNo, resp.Secret, resp.Status)
}

func (s *ProcurementOrderService) verifyTGXInventory(ctx context.Context, client *upstream.TGXClient, skuMapping *models.SKUMapping, sharedCode, race string, quantity int) error {
	if client == nil || skuMapping == nil || sharedCode == "" || quantity <= 0 {
		return ErrUpstreamStockInsufficient
	}
	inventory, err := client.GetInventory(ctx, sharedCode, race)
	if err != nil {
		return err
	}
	state, err := client.GetInventoryState(ctx, sharedCode, race, quantity)
	if err != nil {
		return err
	}
	now := time.Now()
	skuMapping.StockSyncedAt = &now
	skuMapping.UpstreamIsActive = state != nil && state.Available
	if state == nil || !state.Available {
		skuMapping.UpstreamStock = 0
	} else if inventory != nil && inventory.Stock > 0 {
		// A zero value is ambiguous for inventory-hidden TGX products. The
		// inventoryState result remains authoritative in that case.
		skuMapping.UpstreamStock = inventory.Stock
	} else {
		skuMapping.UpstreamStock = -1
	}
	if s.skuMapRepo != nil {
		if updateErr := s.skuMapRepo.Update(skuMapping); updateErr != nil {
			return updateErr
		}
	}
	if state == nil || !state.Available {
		return ErrUpstreamStockInsufficient
	}
	if inventory != nil && inventory.Stock > 0 && inventory.Stock < quantity {
		return ErrUpstreamStockInsufficient
	}
	return nil
}

func (s *ProcurementOrderService) acceptTGXProcurement(procOrder *models.ProcurementOrder, localOrder *models.Order, tradeNo, secret, status string) error {
	now := time.Now()
	updates := map[string]interface{}{
		"upstream_order_no": strings.TrimSpace(tradeNo),
		"upstream_currency": "CNY",
		"error_message":     "",
		"retry_count":       0,
		"updated_at":        now,
	}
	if strings.TrimSpace(secret) != "" {
		updates["upstream_payload"] = secret
	}
	if err := s.procRepo.UpdateStatus(procOrder.ID, constants.ProcurementStatusAccepted, updates); err != nil {
		return fmt.Errorf("update procurement status: %w", err)
	}
	_ = s.orderRepo.UpdateStatus(localOrder.ID, constants.OrderStatusFulfilling, map[string]interface{}{"updated_at": now})
	if strings.TrimSpace(secret) != "" && isDeliveredProviderStatus(status) {
		return s.HandleUpstreamCallback(procOrder.ID, "delivered", &upstream.UpstreamFulfillment{
			Type:    constants.FulfillmentTypeUpstream,
			Status:  constants.FulfillmentStatusDelivered,
			Payload: secret,
		})
	}
	if s.queueClient != nil {
		_ = s.queueClient.EnqueueProcurementPollStatus(queue.ProcurementPollStatusPayload{ProcurementOrderID: procOrder.ID}, 30*time.Second)
	}
	return nil
}

func queryTGXTradeByRequestNo(client *upstream.TGXClient, requestNo string) (*upstream.TGXQueryResponse, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return client.QueryTradeByRequestNo(ctx, requestNo)
}

func (s *ProcurementOrderService) failProviderSubmitForUser(procOrder *models.ProcurementOrder, localOrder *models.Order, detail string) error {
	now := time.Now()
	updates := map[string]interface{}{
		"error_message": providerSubmitTemporarilyUnavailable,
		"retry_count":   0,
		"next_retry_at": nil,
		"updated_at":    now,
	}
	if err := s.procRepo.UpdateStatus(procOrder.ID, constants.ProcurementStatusFailed, updates); err != nil {
		return fmt.Errorf("update procurement status (failed): %w", err)
	}
	if localOrder != nil && localOrder.Status == constants.OrderStatusFulfilling {
		_ = s.orderRepo.UpdateStatus(localOrder.ID, constants.OrderStatusPaid, map[string]interface{}{"updated_at": now})
	}
	logger.Warnw("provider_submit_unavailable_user_notified",
		"procurement_order_id", procOrder.ID,
		"local_order_no", procOrder.LocalOrderNo,
		"error", detail,
	)
	s.notifyProcurementFailure(procOrder, detail)
	return nil
}

func isDefinitiveFansGurusSubmitError(err error) bool {
	if errors.Is(err, upstream.ErrFansGurusAuth) || errors.Is(err, upstream.ErrFansGurusValidation) {
		return true
	}
	var providerErr *upstream.FansGurusError
	if !errors.As(err, &providerErr) {
		return false
	}
	message := strings.ToLower(providerErr.Message)
	return strings.Contains(message, "balance") ||
		strings.Contains(message, "insufficient") ||
		strings.Contains(message, "not enough")
}

func isDefinitiveTGXSubmitError(err error) bool {
	if errors.Is(err, upstream.ErrTGXAuth) {
		return true
	}
	var providerErr *upstream.TGXError
	if !errors.As(err, &providerErr) {
		return false
	}
	normalized := strings.ToLower(strings.TrimSpace(providerErr.Code + " " + providerErr.Message))
	definitiveTokens := []string{
		"balance", "insufficient", "not enough",
		"stock", "inventory", "sold out", "unavailable",
		"invalid", "quantity", "shared_code", "race",
	}
	for _, token := range definitiveTokens {
		if strings.Contains(normalized, token) {
			return true
		}
	}
	return false
}

func splitTGXUpstreamSKUCode(code string) (string, string) {
	parts := strings.SplitN(strings.TrimSpace(code), "|", 2)
	if len(parts) == 0 {
		return "", ""
	}
	sharedCode := strings.TrimSpace(parts[0])
	race := ""
	if len(parts) == 2 {
		race = strings.TrimSpace(parts[1])
	}
	return sharedCode, race
}

func jsonStringValue(values models.JSON, key string) string {
	if values == nil {
		return ""
	}
	raw, ok := values[key]
	if !ok || raw == nil {
		return ""
	}
	switch v := raw.(type) {
	case string:
		return strings.TrimSpace(v)
	case json.Number:
		return strings.TrimSpace(v.String())
	default:
		return strings.TrimSpace(fmt.Sprint(v))
	}
}

func jsonStringMap(values models.JSON) map[string]string {
	result := make(map[string]string)
	for key := range values {
		if value := jsonStringValue(values, key); value != "" {
			result[key] = value
		}
	}
	return result
}

func isDeliveredProviderStatus(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "delivered", "completed", "fulfilled", "success":
		return true
	default:
		return false
	}
}

// markProcurementError 记录错误信息但不改变状态（用于瞬态错误，asynq 可重试）
func (s *ProcurementOrderService) markProcurementError(procOrder *models.ProcurementOrder, errMsg string) {
	now := time.Now()
	_ = s.procRepo.UpdateStatus(procOrder.ID, procOrder.Status, map[string]interface{}{
		"error_message": errMsg,
		"updated_at":    now,
	})
	logger.Warnw("procurement_prepare_error",
		"procurement_order_id", procOrder.ID,
		"error", errMsg,
	)
}

// rejectProcurement 将采购单标记为 rejected（用于永久性配置错误，不值得重试）
// 同时回退本地订单状态并通知管理员
func (s *ProcurementOrderService) rejectProcurement(procOrder *models.ProcurementOrder, errMsg string) {
	now := time.Now()
	_ = s.procRepo.UpdateStatus(procOrder.ID, "rejected", map[string]interface{}{
		"error_message": errMsg,
		"updated_at":    now,
	})
	logger.Warnw("procurement_rejected_config_error",
		"procurement_order_id", procOrder.ID,
		"error", errMsg,
	)
	s.rollbackLocalOrderOnProcurementFailure(procOrder, errMsg)
}

// rollbackLocalOrderOnProcurementFailure 采购单终态失败时回退本地订单状态并通知管理员
func (s *ProcurementOrderService) rollbackLocalOrderOnProcurementFailure(procOrder *models.ProcurementOrder, errMsg string) {
	localOrder, err := s.orderRepo.GetByID(procOrder.LocalOrderID)
	if err != nil || localOrder == nil {
		return
	}
	if localOrder.Status == constants.OrderStatusFulfilling {
		now := time.Now()
		_ = s.orderRepo.UpdateStatus(localOrder.ID, constants.OrderStatusPaid, map[string]interface{}{
			"updated_at": now,
		})
		// 如果是子订单，同步父订单状态
		if localOrder.ParentID != nil {
			_, _ = syncParentStatus(s.orderRepo, *localOrder.ParentID, now)
		}
		logger.Infow("procurement_failure_order_rolled_back",
			"procurement_order_id", procOrder.ID,
			"local_order_id", localOrder.ID,
			"from_status", constants.OrderStatusFulfilling,
			"to_status", constants.OrderStatusPaid,
		)
	}
	s.notifyProcurementFailure(procOrder, errMsg)
}

// notifyProcurementFailure 发送采购失败异常告警
func (s *ProcurementOrderService) notifyProcurementFailure(procOrder *models.ProcurementOrder, errMsg string) {
	if s.notificationSvc == nil {
		return
	}
	_ = s.notificationSvc.Enqueue(NotificationEnqueueInput{
		EventType: constants.NotificationEventExceptionAlert,
		BizType:   constants.NotificationBizTypeProcurement,
		BizID:     procOrder.ID,
		Data: models.JSON{
			"procurement_order_id": procOrder.ID,
			"local_order_no":       procOrder.LocalOrderNo,
			"error":                errMsg,
		},
	})
}

// handleSubmitFailure 处理提交失败
func (s *ProcurementOrderService) handleSubmitFailure(procOrder *models.ProcurementOrder, conn *models.SiteConnection, errMsg string, retryable bool) error {
	now := time.Now()

	if retryable && procOrder.RetryCount < conn.RetryMax {
		intervals := parseRetryIntervals(conn.RetryIntervals)
		idx := procOrder.RetryCount
		if idx >= len(intervals) {
			idx = len(intervals) - 1
		}
		delay := intervals[idx]
		nextRetry := now.Add(delay)

		updates := map[string]interface{}{
			"retry_count":   procOrder.RetryCount + 1,
			"next_retry_at": &nextRetry,
			"error_message": errMsg,
			"updated_at":    now,
		}
		if err := s.procRepo.UpdateStatus(procOrder.ID, "failed", updates); err != nil {
			return fmt.Errorf("update procurement status (failed): %w", err)
		}

		logger.Warnw("procurement_submit_failed_retryable",
			"procurement_order_id", procOrder.ID,
			"retry_count", procOrder.RetryCount+1,
			"next_retry_at", nextRetry,
			"error", errMsg,
		)

		// 入队重试
		if s.queueClient != nil {
			_ = s.queueClient.EnqueueProcurementSubmit(queue.ProcurementSubmitPayload{
				ProcurementOrderID: procOrder.ID,
			}, asynq.ProcessIn(delay))
		}

		return nil
	}

	// 不可重试或已达上限：拒绝
	updates := map[string]interface{}{
		"error_message": errMsg,
		"updated_at":    now,
	}
	if err := s.procRepo.UpdateStatus(procOrder.ID, "rejected", updates); err != nil {
		return fmt.Errorf("update procurement status (rejected): %w", err)
	}

	logger.Warnw("procurement_submit_rejected",
		"procurement_order_id", procOrder.ID,
		"error", errMsg,
	)

	// 回退本地订单状态并通知管理员
	s.rollbackLocalOrderOnProcurementFailure(procOrder, errMsg)

	return fmt.Errorf("procurement rejected: %s", errMsg)
}

// HandleUpstreamCallback 处理上游回调通知
func (s *ProcurementOrderService) HandleUpstreamCallback(procurementOrderID uint, upstreamStatus string, fulfillment *upstream.UpstreamFulfillment) error {
	procOrder, err := s.procRepo.GetByID(procurementOrderID)
	if err != nil {
		return fmt.Errorf("load procurement order: %w", err)
	}
	if procOrder == nil {
		return ErrProcurementNotFound
	}

	now := time.Now()
	upstreamStatus = strings.ToLower(strings.TrimSpace(upstreamStatus))

	switch upstreamStatus {
	case "delivered", "completed", "fulfilled":
		// 更新采购单状态
		updates := map[string]interface{}{
			"updated_at": now,
		}
		if fulfillment != nil {
			updates["upstream_payload"] = fulfillment.Payload
		}
		if err := s.procRepo.UpdateStatus(procOrder.ID, "fulfilled", updates); err != nil {
			return fmt.Errorf("update procurement status: %w", err)
		}

		// 在本地订单上创建交付记录
		if fulfillment != nil {
			if err := s.createUpstreamFulfillment(procOrder.LocalOrderID, fulfillment, now); err != nil {
				logger.Warnw("procurement_create_fulfillment_failed",
					"procurement_order_id", procOrder.ID,
					"local_order_id", procOrder.LocalOrderID,
					"error", err,
				)
				return err
			}
		}

		// 更新本地订单状态
		_ = s.orderRepo.UpdateStatus(procOrder.LocalOrderID, constants.OrderStatusDelivered, map[string]interface{}{
			"updated_at": now,
		})

		// 如果有父订单，同步父订单状态
		localOrder, _ := s.orderRepo.GetByID(procOrder.LocalOrderID)
		if localOrder != nil && localOrder.ParentID != nil {
			if status, syncErr := syncParentStatus(s.orderRepo, *localOrder.ParentID, now); syncErr != nil {
				logger.Warnw("procurement_sync_parent_status_failed",
					"procurement_order_id", procOrder.ID,
					"parent_order_id", *localOrder.ParentID,
					"error", syncErr,
				)
			} else if s.queueClient != nil {
				if status == "" {
					status = constants.OrderStatusDelivered
				}
				_, _ = enqueueOrderStatusEmailTaskIfEligible(s.orderRepo, s.queueClient, s.settingService, s.defaultEmailConfig, *localOrder.ParentID, status)
			}
		} else if localOrder != nil && s.queueClient != nil {
			_, _ = enqueueOrderStatusEmailTaskIfEligible(s.orderRepo, s.queueClient, s.settingService, s.defaultEmailConfig, localOrder.ID, constants.OrderStatusDelivered)
		}

		// 触发下游回调（多级连跳：本站作为中间节点，通知下游交付完成）
		if s.downstreamCallbackSvc != nil {
			s.downstreamCallbackSvc.EnqueueCallback(procOrder.LocalOrderID)
			// 如果有父订单，也通知父订单的下游
			if localOrder != nil && localOrder.ParentID != nil {
				s.downstreamCallbackSvc.EnqueueCallback(*localOrder.ParentID)
			}
		}

		// 通知 Bot 订单已交付
		if s.fulfillSvc != nil && localOrder != nil {
			notifyOrderID := localOrder.ID
			if localOrder.ParentID != nil {
				notifyOrderID = *localOrder.ParentID
			}
			go s.fulfillSvc.NotifyBotOrderFulfilled(localOrder.UserID, notifyOrderID)
		}

		logger.Infow("procurement_order_fulfilled",
			"procurement_order_id", procOrder.ID,
			"local_order_id", procOrder.LocalOrderID,
		)

	case "canceled":
		updates := map[string]interface{}{
			"updated_at": now,
		}
		if err := s.procRepo.UpdateStatus(procOrder.ID, "canceled", updates); err != nil {
			return fmt.Errorf("update procurement status: %w", err)
		}

		// 回退本地订单状态并通知管理员
		s.rollbackLocalOrderOnProcurementFailure(procOrder, "upstream canceled order")

		logger.Infow("procurement_order_canceled_by_upstream",
			"procurement_order_id", procOrder.ID,
			"local_order_id", procOrder.LocalOrderID,
		)
	case "refunded", "partially_refunded":
		updates := map[string]interface{}{
			"updated_at": now,
		}
		if fulfillment != nil {
			updates["upstream_payload"] = fulfillment.Payload
		}
		targetStatus := constants.ProcurementStatusPartiallyRefunded
		if upstreamStatus == "refunded" {
			targetStatus = constants.ProcurementStatusRefunded
		}
		if err := s.procRepo.UpdateStatus(procOrder.ID, targetStatus, updates); err != nil {
			return fmt.Errorf("update procurement status: %w", err)
		}
		logger.Infow("procurement_order_refunded",
			"procurement_order_id", procOrder.ID,
			"local_order_id", procOrder.LocalOrderID,
			"upstream_status", upstreamStatus,
			"local_status", targetStatus,
		)

	default:
		logger.Warnw("procurement_unknown_upstream_status",
			"procurement_order_id", procOrder.ID,
			"upstream_status", upstreamStatus,
		)
	}

	return nil
}

// createUpstreamFulfillment 在本地订单上创建上游交付记录
func (s *ProcurementOrderService) createUpstreamFulfillment(orderID uint, uf *upstream.UpstreamFulfillment, now time.Time) error {
	deliveredAt := uf.DeliveredAt
	if deliveredAt == nil {
		deliveredAt = &now
	}

	return s.orderRepo.Transaction(func(tx *gorm.DB) error {
		fulfillRepo := s.fulfillmentRepo.WithTx(tx)

		// 检查是否已有交付记录
		if _, found, err := fulfillRepo.FindByOrderIDForUpdate(orderID); err != nil {
			return err
		} else if found {
			return nil // 已存在,跳过
		}

		fulfillment := &models.Fulfillment{
			OrderID:       orderID,
			Type:          constants.FulfillmentTypeUpstream,
			Status:        constants.FulfillmentStatusDelivered,
			Payload:       uf.Payload,
			LogisticsJSON: uf.DeliveryData,
			DeliveredAt:   deliveredAt,
			CreatedAt:     now,
			UpdatedAt:     now,
		}
		return fulfillRepo.Create(fulfillment)
	})
}

// PollUpstreamStatus Worker 调用：轮询上游订单状态
func (s *ProcurementOrderService) PollUpstreamStatus(procurementOrderID uint) error {
	procOrder, err := s.procRepo.GetByID(procurementOrderID)
	if err != nil {
		return fmt.Errorf("load procurement order: %w", err)
	}
	if procOrder == nil {
		return ErrProcurementNotFound
	}

	// 只轮询 accepted 状态的订单
	if procOrder.Status != "accepted" {
		return nil
	}

	conn, err := s.connSvc.GetByID(procOrder.ConnectionID)
	if err != nil {
		return fmt.Errorf("load connection: %w", err)
	}
	if conn == nil {
		return ErrConnectionNotFound
	}
	switch conn.Protocol {
	case constants.ConnectionProtocolFansGurus, constants.ConnectionProtocolTGXAccount:
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		return s.pollProviderStatus(ctx, procOrder, conn, true)
	}

	adapter, err := s.connSvc.GetAdapter(conn)
	if err != nil {
		return fmt.Errorf("get adapter: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	detail, err := adapter.GetOrder(ctx, procOrder.UpstreamOrderID)
	if err != nil {
		logger.Warnw("procurement_poll_status_error",
			"procurement_order_id", procOrder.ID,
			"upstream_order_id", procOrder.UpstreamOrderID,
			"error", err,
		)
		// 轮询失败，重新入队
		return s.requeuePoll(procOrder, conn)
	}

	mappedStatus := mapProcurementUpstreamStatus(detail.Status)
	switch mappedStatus {
	case "delivered":
		return s.HandleUpstreamCallback(procOrder.ID, mappedStatus, detail.Fulfillment)
	case "canceled":
		return s.HandleUpstreamCallback(procOrder.ID, mappedStatus, nil)
	case "refunded", "partially_refunded":
		return s.HandleUpstreamCallback(procOrder.ID, mappedStatus, detail.Fulfillment)
	default:
		// 状态未变，继续轮询
		return s.requeuePoll(procOrder, conn)
	}
}

// pollIntervals 短期轮询间隔：捕获自动交付等快速场景（共约30分钟后停止）
// 超时后不标记失败，交由回调和定时巡检接管
var pollIntervals = []time.Duration{
	30 * time.Second, 30 * time.Second,
	1 * time.Minute, 1 * time.Minute,
	2 * time.Minute, 2 * time.Minute,
	5 * time.Minute, 5 * time.Minute,
	10 * time.Minute,
}

// requeuePoll 重新入队轮询任务
func (s *ProcurementOrderService) requeuePoll(procOrder *models.ProcurementOrder, _ *models.SiteConnection) error {
	if s.queueClient == nil {
		return nil
	}

	idx := procOrder.RetryCount
	if idx >= len(pollIntervals) {
		// 短期轮询结束，后续由定时巡检和回调接管，不标记失败
		logger.Infow("procurement_poll_handoff_to_periodic_sync",
			"procurement_order_id", procOrder.ID,
			"retry_count", procOrder.RetryCount,
		)
		return nil
	}

	delay := pollIntervals[idx]

	// 递增轮询计数
	now := time.Now()
	_ = s.procRepo.UpdateStatus(procOrder.ID, procOrder.Status, map[string]interface{}{
		"retry_count": procOrder.RetryCount + 1,
		"updated_at":  now,
	})

	return s.queueClient.EnqueueProcurementPollStatus(queue.ProcurementPollStatusPayload{
		ProcurementOrderID: procOrder.ID,
	}, delay)
}

// SyncAcceptedOrders 定时巡检：检查所有 accepted 状态的采购单，向上游查询最新状态
// 由 worker 定时任务调用（每30分钟）
func (s *ProcurementOrderService) SyncAcceptedOrders() {
	const pageSize = 200
	_, total, err := s.procRepo.List(repository.ProcurementOrderListFilter{
		Status:     "accepted",
		Pagination: repository.Pagination{Page: 1, PageSize: 1},
	})
	if err != nil {
		logger.Warnw("procurement_sync_accepted_count_failed", "error", err)
		return
	}
	for page := int((total + pageSize - 1) / pageSize); page >= 1; page-- {
		orders, _, err := s.procRepo.List(repository.ProcurementOrderListFilter{
			Status:     "accepted",
			Pagination: repository.Pagination{Page: page, PageSize: pageSize},
		})
		if err != nil {
			logger.Warnw("procurement_sync_accepted_list_failed", "page", page, "error", err)
			return
		}
		if len(orders) == 0 {
			continue
		}
		logger.Infow("procurement_sync_accepted_page", "page", page, "count", len(orders))
		for i := range orders {
			procOrder := &orders[i]
			conn, err := s.connSvc.GetByID(procOrder.ConnectionID)
			if err != nil || conn == nil {
				continue
			}
			if conn.Protocol == constants.ConnectionProtocolFansGurus || conn.Protocol == constants.ConnectionProtocolTGXAccount {
				ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
				if err := s.pollProviderStatus(ctx, procOrder, conn, false); err != nil {
					logger.Warnw("procurement_sync_provider_status_failed",
						"procurement_order_id", procOrder.ID,
						"protocol", conn.Protocol,
						"error", err,
					)
				}
				cancel()
				continue
			}
			if procOrder.UpstreamOrderID == 0 {
				continue
			}
			adapter, err := s.connSvc.GetAdapter(conn)
			if err != nil {
				continue
			}

			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			detail, err := adapter.GetOrder(ctx, procOrder.UpstreamOrderID)
			cancel()

			if err != nil {
				logger.Warnw("procurement_sync_accepted_poll_error",
					"procurement_order_id", procOrder.ID,
					"upstream_order_id", procOrder.UpstreamOrderID,
					"error", err,
				)
				continue
			}

			mappedStatus := mapProcurementUpstreamStatus(detail.Status)
			switch mappedStatus {
			case "delivered":
				if cbErr := s.HandleUpstreamCallback(procOrder.ID, mappedStatus, detail.Fulfillment); cbErr != nil {
					logger.Warnw("procurement_sync_accepted_deliver_failed",
						"procurement_order_id", procOrder.ID,
						"error", cbErr,
					)
				} else {
					logger.Infow("procurement_sync_accepted_delivered",
						"procurement_order_id", procOrder.ID,
					)
				}
			case "canceled":
				_ = s.HandleUpstreamCallback(procOrder.ID, mappedStatus, nil)
				logger.Infow("procurement_sync_accepted_canceled",
					"procurement_order_id", procOrder.ID,
				)
			case "refunded", "partially_refunded":
				if cbErr := s.HandleUpstreamCallback(procOrder.ID, mappedStatus, detail.Fulfillment); cbErr != nil {
					logger.Warnw("procurement_sync_accepted_refund_failed",
						"procurement_order_id", procOrder.ID,
						"upstream_status", mappedStatus,
						"error", cbErr,
					)
				} else {
					logger.Infow("procurement_sync_accepted_refunded",
						"procurement_order_id", procOrder.ID,
						"upstream_status", mappedStatus,
					)
				}
			default:
				// 检查是否超时（超过 24 小时仍在 accepted 状态）
				acceptedDuration := time.Since(procOrder.UpdatedAt)
				if acceptedDuration > 24*time.Hour {
					logger.Warnw("procurement_accepted_timeout",
						"procurement_order_id", procOrder.ID,
						"upstream_order_id", procOrder.UpstreamOrderID,
						"accepted_duration", acceptedDuration.String(),
					)
					s.notifyProcurementFailure(procOrder, fmt.Sprintf(
						"procurement order stuck in accepted for %s, upstream status: %s",
						acceptedDuration.Round(time.Hour), detail.Status))
				}
			}
		}
	}
}

func (s *ProcurementOrderService) pollProviderStatus(ctx context.Context, procOrder *models.ProcurementOrder, conn *models.SiteConnection, requeue bool) error {
	switch conn.Protocol {
	case constants.ConnectionProtocolFansGurus:
		return s.pollFansGurusStatus(ctx, procOrder, conn, requeue)
	case constants.ConnectionProtocolTGXAccount:
		return s.pollTGXStatus(ctx, procOrder, conn, requeue)
	default:
		return nil
	}
}

func (s *ProcurementOrderService) pollFansGurusStatus(ctx context.Context, procOrder *models.ProcurementOrder, conn *models.SiteConnection, requeue bool) error {
	if procOrder.UpstreamOrderID == 0 {
		return fmt.Errorf("fansgurus upstream order id is required")
	}
	client := upstream.NewFansGurusClient(conn.BaseURL, conn.ApiKey)
	status, err := client.GetOrderStatus(ctx, procOrder.UpstreamOrderID)
	if err != nil {
		logger.Warnw("procurement_fansgurus_poll_error",
			"procurement_order_id", procOrder.ID,
			"upstream_order_id", procOrder.UpstreamOrderID,
			"error", err,
		)
		if requeue {
			return s.requeuePoll(procOrder, conn)
		}
		return err
	}
	mappedStatus := mapFansGurusProcurementStatus(status.Status)
	return s.applyProviderPolledStatus(procOrder, conn, mappedStatus, nil, requeue)
}

func (s *ProcurementOrderService) pollTGXStatus(ctx context.Context, procOrder *models.ProcurementOrder, conn *models.SiteConnection, requeue bool) error {
	tradeNo := strings.TrimSpace(procOrder.UpstreamOrderNo)
	if tradeNo == "" {
		return fmt.Errorf("tgx trade number is required")
	}
	appKey, err := s.connSvc.DecryptSecret(conn.ApiSecret)
	if err != nil {
		return fmt.Errorf("decrypt tgx app key: %w", err)
	}
	client := upstream.NewTGXClient(conn.BaseURL, conn.ApiKey, appKey)
	status, err := client.QueryTrade(ctx, tradeNo)
	if err != nil {
		logger.Warnw("procurement_tgx_poll_error",
			"procurement_order_id", procOrder.ID,
			"upstream_order_no", procOrder.UpstreamOrderNo,
			"error", err,
		)
		if requeue {
			return s.requeuePoll(procOrder, conn)
		}
		return err
	}

	mappedStatus := mapTGXProcurementStatus(status.Status)
	var fulfillment *upstream.UpstreamFulfillment
	if strings.TrimSpace(status.Secret) != "" && mappedStatus == "delivered" {
		fulfillment = &upstream.UpstreamFulfillment{
			Type:    constants.FulfillmentTypeUpstream,
			Status:  constants.FulfillmentStatusDelivered,
			Payload: status.Secret,
		}
	} else if mappedStatus == "delivered" {
		mappedStatus = "pending"
	}
	return s.applyProviderPolledStatus(procOrder, conn, mappedStatus, fulfillment, requeue)
}

func (s *ProcurementOrderService) applyProviderPolledStatus(procOrder *models.ProcurementOrder, conn *models.SiteConnection, mappedStatus string, fulfillment *upstream.UpstreamFulfillment, requeue bool) error {
	switch mappedStatus {
	case "delivered":
		return s.HandleUpstreamCallback(procOrder.ID, mappedStatus, fulfillment)
	case "canceled":
		return s.HandleUpstreamCallback(procOrder.ID, mappedStatus, nil)
	case "refunded", "partially_refunded":
		return s.HandleUpstreamCallback(procOrder.ID, mappedStatus, fulfillment)
	default:
		if requeue {
			return s.requeuePoll(procOrder, conn)
		}
		return nil
	}
}

// GetByID 根据 ID 获取采购单
func (s *ProcurementOrderService) GetByID(id uint) (*models.ProcurementOrder, error) {
	procOrder, err := s.procRepo.GetByID(id)
	if err != nil {
		return nil, err
	}
	if procOrder == nil {
		return nil, ErrProcurementNotFound
	}
	s.fillUpstreamRefundRecordsForProcurementOrder(procOrder)
	return procOrder, nil
}

// GetByLocalOrderNo 根据本地订单号获取采购单
func (s *ProcurementOrderService) GetByLocalOrderNo(localOrderNo string) (*models.ProcurementOrder, error) {
	return s.procRepo.GetByLocalOrderNo(localOrderNo)
}

// IsUserRetryableTemporaryFailure reports whether a failed procurement order can be retried by the customer.
func (s *ProcurementOrderService) IsUserRetryableTemporaryFailure(procOrder *models.ProcurementOrder) bool {
	if procOrder == nil {
		return false
	}
	status := strings.ToLower(strings.TrimSpace(procOrder.Status))
	if status != constants.ProcurementStatusFailed && status != constants.ProcurementStatusRejected {
		return false
	}
	return strings.TrimSpace(procOrder.ErrorMessage) == providerSubmitTemporarilyUnavailable
}

// RetryUserTemporaryFailure lets a customer explicitly retry a temporary fulfillment submit failure.
func (s *ProcurementOrderService) RetryUserTemporaryFailure(id uint) error {
	procOrder, err := s.procRepo.GetByID(id)
	if err != nil {
		return fmt.Errorf("load procurement order: %w", err)
	}
	if procOrder == nil {
		return ErrProcurementNotFound
	}
	if !s.IsUserRetryableTemporaryFailure(procOrder) {
		return ErrProcurementRetryDenied
	}
	return s.RetryManual(id)
}

// List 列表查询采购单
func (s *ProcurementOrderService) List(filter repository.ProcurementOrderListFilter) ([]models.ProcurementOrder, int64, error) {
	orders, total, err := s.procRepo.List(filter)
	if err != nil {
		return nil, 0, err
	}
	s.fillParentOrderNos(orders)
	s.fillUpstreamRefundRecordsForProcurementOrders(orders)
	return orders, total, nil
}

// StatsByStatus 按状态聚合采购单数量（基于全量数据）
func (s *ProcurementOrderService) StatsByStatus(filter repository.ProcurementOrderListFilter) (map[string]int64, error) {
	return s.procRepo.StatsByStatus(filter)
}

// FillParentOrderNo 为单个采购单填充父订单号
func (s *ProcurementOrderService) FillParentOrderNo(order *models.ProcurementOrder) {
	if order == nil || order.LocalOrder == nil || order.LocalOrder.ParentID == nil {
		return
	}
	parentOrder, err := s.orderRepo.GetByID(*order.LocalOrder.ParentID)
	if err == nil && parentOrder != nil {
		order.ParentOrderNo = parentOrder.OrderNo
		applyProcurementLocalRefundedAmountFallback(order.LocalOrder, parentOrder)
	}
}

// fillParentOrderNos 为采购单批量填充父订单号
func (s *ProcurementOrderService) fillParentOrderNos(orders []models.ProcurementOrder) {
	// 收集需要查询的父订单 ID
	parentIDs := make(map[uint]bool)
	for i := range orders {
		if orders[i].LocalOrder != nil && orders[i].LocalOrder.ParentID != nil {
			parentIDs[*orders[i].LocalOrder.ParentID] = true
		}
	}
	if len(parentIDs) == 0 {
		return
	}

	ids := make([]uint, 0, len(parentIDs))
	for id := range parentIDs {
		ids = append(ids, id)
	}

	parentOrders, err := s.orderRepo.GetByIDs(ids)
	if err != nil {
		return
	}
	parentMap := make(map[uint]*models.Order, len(parentOrders))
	for _, o := range parentOrders {
		order := o
		parentMap[o.ID] = &order
	}

	for i := range orders {
		if orders[i].LocalOrder != nil && orders[i].LocalOrder.ParentID != nil {
			if parent := parentMap[*orders[i].LocalOrder.ParentID]; parent != nil {
				orders[i].ParentOrderNo = parent.OrderNo
				applyProcurementLocalRefundedAmountFallback(orders[i].LocalOrder, parent)
			}
		}
	}
}

// applyProcurementLocalRefundedAmountFallback 在子订单退款金额为空时回填父订单退款金额，便于采购单视图展示。
func applyProcurementLocalRefundedAmountFallback(localOrder *models.Order, parentOrder *models.Order) {
	if localOrder == nil || parentOrder == nil {
		return
	}
	localRefunded := localOrder.RefundedAmount.Decimal.Round(2)
	if localRefunded.GreaterThan(decimal.Zero) {
		return
	}
	parentRefunded := parentOrder.RefundedAmount.Decimal.Round(2)
	if parentRefunded.LessThanOrEqual(decimal.Zero) {
		return
	}
	localOrder.RefundedAmount = models.NewMoneyFromDecimal(parentRefunded)
}

// shouldSyncUpstreamRefundStatus 判断当前采购单状态是否需要从上游拉取退款信息。
func shouldSyncUpstreamRefundStatus(localStatus string) bool {
	switch strings.ToLower(strings.TrimSpace(localStatus)) {
	case constants.ProcurementStatusFulfilled,
		constants.ProcurementStatusCompleted,
		constants.ProcurementStatusPartiallyRefunded,
		constants.ProcurementStatusRefunded:
		return true
	default:
		return false
	}
}

// mapProcurementUpstreamStatus 统一映射上游状态别名，便于回调与轮询使用同一分支逻辑。
func mapProcurementUpstreamStatus(status string) string {
	normalized := strings.ToLower(strings.TrimSpace(status))
	switch normalized {
	case "delivered", "completed", "fulfilled":
		return "delivered"
	case "canceled", "cancelled":
		return "canceled"
	case "refunded", "partially_refunded":
		return normalized
	default:
		return normalized
	}
}

func mapFansGurusProcurementStatus(status string) string {
	normalized := normalizeProviderStatusToken(status)
	switch normalized {
	case "completed", "complete", "delivered":
		return "delivered"
	case "partial", "partially_completed", "partially_delivered", "partially_refunded":
		return "partially_refunded"
	case "canceled", "cancelled":
		return "canceled"
	case "refunded":
		return "refunded"
	default:
		return normalized
	}
}

func mapTGXProcurementStatus(status string) string {
	normalized := normalizeProviderStatusToken(status)
	switch normalized {
	case "completed", "complete", "delivered", "fulfilled", "success":
		return "delivered"
	case "canceled", "cancelled", "closed":
		return "canceled"
	case "refunded", "partially_refunded":
		return normalized
	default:
		return normalized
	}
}

func normalizeProviderStatusToken(status string) string {
	normalized := strings.ToLower(strings.TrimSpace(status))
	normalized = strings.ReplaceAll(normalized, "-", "_")
	normalized = strings.ReplaceAll(normalized, " ", "_")
	return normalized
}

// normalizeProcurementUpstreamStatus 规范化上游状态字符串（去空白+小写）。
func normalizeProcurementUpstreamStatus(status string) string {
	return strings.ToLower(strings.TrimSpace(status))
}

// buildUpstreamRefundRecords 标准化上游退款记录并按 created_at 升序排序，随后重排顺序ID。
func buildUpstreamRefundRecords(records []models.JSON) []models.JSON {
	if len(records) == 0 {
		return make([]models.JSON, 0)
	}
	normalized := make([]models.JSON, 0, len(records))
	for i := range records {
		record := make(models.JSON, len(records[i]))
		for k, v := range records[i] {
			record[k] = v
		}
		normalized = append(normalized, record)
	}
	sort.SliceStable(normalized, func(i, j int) bool {
		ti, okI := parseUpstreamRefundRecordCreatedAt(normalized[i]["created_at"])
		tj, okJ := parseUpstreamRefundRecordCreatedAt(normalized[j]["created_at"])
		switch {
		case okI && okJ:
			if ti.Equal(tj) {
				return false
			}
			return ti.Before(tj)
		case okI:
			return true
		case okJ:
			return false
		default:
			return false
		}
	})
	for i := range normalized {
		// 不透传上游退款记录主键，统一使用列表自增序号（按排序后序号）。
		normalized[i]["id"] = i + 1
	}
	return normalized
}

// parseUpstreamRefundRecordCreatedAt 解析上游退款记录中的 created_at 字段并返回可排序时间值。
func parseUpstreamRefundRecordCreatedAt(v interface{}) (time.Time, bool) {
	switch value := v.(type) {
	case time.Time:
		return value, true
	case *time.Time:
		if value == nil {
			return time.Time{}, false
		}
		return *value, true
	case string:
		s := strings.TrimSpace(value)
		if s == "" {
			return time.Time{}, false
		}
		formats := []string{
			time.RFC3339Nano,
			time.RFC3339,
			"2006-01-02 15:04:05.999999999",
			"2006-01-02 15:04:05",
		}
		for _, layout := range formats {
			if parsed, err := time.Parse(layout, s); err == nil {
				return parsed, true
			}
		}
		return time.Time{}, false
	case int64:
		return time.Unix(value, 0), true
	case int:
		return time.Unix(int64(value), 0), true
	case float64:
		return time.Unix(int64(value), 0), true
	default:
		return time.Time{}, false
	}
}

// fillUpstreamRefundRecordsForProcurementOrder 为单条采购单补充上游退款记录与退款金额，并同步退款状态。
func (s *ProcurementOrderService) fillUpstreamRefundRecordsForProcurementOrder(order *models.ProcurementOrder) {
	if order == nil {
		return
	}
	order.UpstreamRefundRecords = nil
	order.UpstreamRefundedAmount = ""
	if s.connSvc == nil || order.UpstreamOrderID == 0 || !shouldSyncUpstreamRefundStatus(order.Status) {
		return
	}
	conn, err := s.connSvc.GetByID(order.ConnectionID)
	if err != nil || conn == nil {
		return
	}
	adapter, err := s.connSvc.GetAdapter(conn)
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	detail, err := adapter.GetOrder(ctx, order.UpstreamOrderID)
	if err != nil || detail == nil {
		return
	}
	upstreamRefundRecords := buildUpstreamRefundRecords(detail.RefundRecords)
	upstreamRefundedAmount := strings.TrimSpace(detail.RefundedAmount)
	hasRefundRecords := len(upstreamRefundRecords) > 0
	hasRefundedAmount := isPositiveUpstreamRefundAmount(upstreamRefundedAmount)
	if hasRefundRecords {
		order.UpstreamRefundRecords = upstreamRefundRecords
	}
	if hasRefundedAmount {
		order.UpstreamRefundedAmount = upstreamRefundedAmount
	}

	upstreamStatus := normalizeProcurementUpstreamStatus(detail.Status)
	if upstreamStatus != "refunded" && upstreamStatus != "partially_refunded" {
		return
	}
	targetStatus := constants.ProcurementStatusPartiallyRefunded
	if upstreamStatus == "refunded" {
		targetStatus = constants.ProcurementStatusRefunded
	}
	if strings.EqualFold(strings.TrimSpace(order.Status), targetStatus) {
		order.Status = targetStatus
		return
	}
	if err := s.procRepo.UpdateStatus(order.ID, targetStatus, map[string]interface{}{"updated_at": time.Now()}); err != nil {
		logger.Warnw("procurement_sync_refund_status_failed",
			"procurement_order_id", order.ID,
			"upstream_order_id", order.UpstreamOrderID,
			"upstream_status", upstreamStatus,
			"error", err,
		)
		return
	}
	order.Status = targetStatus
}

// isPositiveUpstreamRefundAmount 判断上游退款金额字符串是否为正数。
func isPositiveUpstreamRefundAmount(raw string) bool {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return false
	}
	amount, err := decimal.NewFromString(trimmed)
	if err != nil {
		return false
	}
	return amount.Round(2).GreaterThan(decimal.Zero)
}

// fillUpstreamRefundRecordsForProcurementOrders 批量为采购单补充上游退款记录与退款金额。
func (s *ProcurementOrderService) fillUpstreamRefundRecordsForProcurementOrders(orders []models.ProcurementOrder) {
	for i := range orders {
		s.fillUpstreamRefundRecordsForProcurementOrder(&orders[i])
	}
}

// RetryManual 手动重试失败的采购单
func (s *ProcurementOrderService) RetryManual(id uint) error {
	procOrder, err := s.procRepo.GetByID(id)
	if err != nil {
		return fmt.Errorf("load procurement order: %w", err)
	}
	if procOrder == nil {
		return ErrProcurementNotFound
	}

	if procOrder.Status != "failed" && procOrder.Status != "rejected" {
		return ErrProcurementStatusInvalid
	}

	now := time.Now()
	updates := map[string]interface{}{
		"retry_count":   0,
		"next_retry_at": nil,
		"error_message": "",
		"updated_at":    now,
	}
	if err := s.procRepo.UpdateStatus(procOrder.ID, "pending", updates); err != nil {
		return fmt.Errorf("reset procurement status: %w", err)
	}

	logger.Infow("procurement_manual_retry",
		"procurement_order_id", procOrder.ID,
	)

	if s.queueClient != nil {
		return s.queueClient.EnqueueProcurementSubmit(queue.ProcurementSubmitPayload{
			ProcurementOrderID: procOrder.ID,
		})
	}
	return nil
}

// CancelManual 手动取消采购单
func (s *ProcurementOrderService) CancelManual(id uint) error {
	procOrder, err := s.procRepo.GetByID(id)
	if err != nil {
		return fmt.Errorf("load procurement order: %w", err)
	}
	if procOrder == nil {
		return ErrProcurementNotFound
	}

	// 已交付/已退款的不能取消
	if procOrder.Status == constants.ProcurementStatusFulfilled ||
		procOrder.Status == constants.ProcurementStatusCompleted ||
		procOrder.Status == constants.ProcurementStatusPartiallyRefunded ||
		procOrder.Status == constants.ProcurementStatusRefunded ||
		procOrder.Status == constants.ProcurementStatusCanceled {
		return ErrProcurementStatusInvalid
	}

	if procOrder.Status == constants.ProcurementStatusSubmitted ||
		procOrder.Status == constants.ProcurementStatusAccepted {
		conn, err := s.connSvc.GetByID(procOrder.ConnectionID)
		if err != nil {
			return fmt.Errorf("load connection: %w", err)
		}
		if conn == nil {
			return ErrConnectionNotFound
		}
		if conn.Protocol == constants.ConnectionProtocolFansGurus || conn.Protocol == constants.ConnectionProtocolTGXAccount {
			return ErrProcurementCancelUnsupported
		}
		if procOrder.Status == constants.ProcurementStatusSubmitted || procOrder.UpstreamOrderID == 0 {
			return ErrProcurementCancelUnsupported
		}
		adapter, adErr := s.connSvc.GetAdapter(conn)
		if adErr != nil {
			return fmt.Errorf("get adapter: %w", adErr)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if cancelErr := adapter.CancelOrder(ctx, procOrder.UpstreamOrderID); cancelErr != nil {
			return fmt.Errorf("cancel upstream order: %w", cancelErr)
		}
	}

	now := time.Now()
	updates := map[string]interface{}{
		"error_message": "manually canceled",
		"updated_at":    now,
	}
	if err := s.procRepo.UpdateStatus(procOrder.ID, "canceled", updates); err != nil {
		return fmt.Errorf("update procurement status: %w", err)
	}

	logger.Infow("procurement_manual_cancel",
		"procurement_order_id", procOrder.ID,
	)
	return nil
}

// hasUpstreamItems 检查订单是否包含上游交付类型的商品
func (s *ProcurementOrderService) hasUpstreamItems(order *models.Order) bool {
	for _, item := range order.Items {
		if strings.TrimSpace(item.FulfillmentType) == constants.FulfillmentTypeUpstream {
			return true
		}
	}
	return false
}

// isRetryableErrorCode 判断上游错误码是否可重试
func isRetryableErrorCode(code string) bool {
	nonRetryable := map[string]bool{
		"insufficient_balance": true,
		"payment_failed":       true,
		"product_unavailable":  true,
		"sku_unavailable":      true,
		"invalid_request":      true,
		"unauthorized":         true,
		"forbidden":            true,
		"duplicate_order":      true,
		"product_out_of_stock": true,
	}
	return !nonRetryable[strings.ToLower(strings.TrimSpace(code))]
}

// parseRetryIntervals 解析重试间隔配置（JSON 数组格式如 "[30,60,300]"）
func parseRetryIntervals(raw string) []time.Duration {
	raw = strings.TrimSpace(raw)
	// 移除方括号
	raw = strings.TrimPrefix(raw, "[")
	raw = strings.TrimSuffix(raw, "]")

	if raw == "" {
		return []time.Duration{30 * time.Second, 60 * time.Second, 300 * time.Second}
	}

	parts := strings.Split(raw, ",")
	intervals := make([]time.Duration, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		seconds, err := strconv.Atoi(part)
		if err != nil || seconds <= 0 {
			continue
		}
		intervals = append(intervals, time.Duration(seconds)*time.Second)
	}

	if len(intervals) == 0 {
		return []time.Duration{30 * time.Second, 60 * time.Second, 300 * time.Second}
	}
	return intervals
}
