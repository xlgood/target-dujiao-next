package admin

import (
	"context"
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
	"github.com/dujiao-next/internal/queue"
	"github.com/dujiao-next/internal/repository"
	"github.com/dujiao-next/internal/service"
	"github.com/dujiao-next/internal/upstream"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

type adminProviderCatalogFakeFansClient struct {
	services []upstream.FansGurusService
}

func (c adminProviderCatalogFakeFansClient) ListServices(ctx context.Context) ([]upstream.FansGurusService, error) {
	return c.services, nil
}

type adminProviderCatalogFakeTGXClient struct {
	items []upstream.TGXCommodity
}

func (c adminProviderCatalogFakeTGXClient) ListItems(ctx context.Context) (*upstream.TGXItemsResponse, error) {
	return &upstream.TGXItemsResponse{Items: c.items}, nil
}

func setupAdminProviderCatalogSyncHandlerTest(t *testing.T) (*Handler, *gorm.DB, uint, uint) {
	t.Helper()
	gin.SetMode(gin.TestMode)

	dsn := fmt.Sprintf("file:admin_provider_catalog_sync_handler_test_%d?mode=memory&cache=shared", time.Now().UnixNano())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite failed: %v", err)
	}
	if err := db.AutoMigrate(
		&models.Category{},
		&models.Product{},
		&models.ProductSKU{},
		&models.SiteConnection{},
		&models.ProductMapping{},
		&models.SKUMapping{},
		&models.ProviderCatalogSyncRun{},
	); err != nil {
		t.Fatalf("auto migrate failed: %v", err)
	}

	categoryRepo := repository.NewCategoryRepository(db)
	categoryService := service.NewCategoryService(categoryRepo)
	connService := service.NewSiteConnectionService(repository.NewSiteConnectionRepository(db), "test-secret-key", t.TempDir())
	fansConn, err := connService.Create(service.CreateConnectionInput{
		Name:      "FansGurus",
		BaseURL:   "https://fansgurus.example/api/v2",
		ApiKey:    "fans-key",
		ApiSecret: "unused",
		Protocol:  constants.ConnectionProtocolFansGurus,
	})
	if err != nil {
		t.Fatalf("create fansgurus connection failed: %v", err)
	}
	tgxConn, err := connService.Create(service.CreateConnectionInput{
		Name:               "TGX",
		BaseURL:            "https://tgx.example/shared",
		ApiKey:             "tgx-app-id",
		ApiSecret:          "tgx-app-key",
		Protocol:           constants.ConnectionProtocolTGXAccount,
		ExchangeRate:       0.14,
		PriceMarkupPercent: 20,
	})
	if err != nil {
		t.Fatalf("create tgx connection failed: %v", err)
	}

	mappingService := service.NewProductMappingService(
		repository.NewProductMappingRepository(db),
		repository.NewSKUMappingRepository(db),
		repository.NewProductRepository(db),
		repository.NewProductSKURepository(db),
		categoryRepo,
		connService,
	)
	mappingService.SetCategoryService(categoryService)
	mappingService.SetProviderCatalogSyncRunRepository(repository.NewProviderCatalogSyncRunRepository(db))

	h := &Handler{Container: &provider.Container{
		SiteConnectionService: connService,
		CategoryService:       categoryService,
		ProductMappingService: mappingService,
	}}
	return h, db, fansConn.ID, tgxConn.ID
}

func TestSyncProviderCatalogImportsThroughAdminTrigger(t *testing.T) {
	h, db, fansConnID, tgxConnID := setupAdminProviderCatalogSyncHandlerTest(t)
	factoryCalls := 0
	enqueueCalls := 0
	h.ProviderCatalogInventoryEnqueue = func() error {
		enqueueCalls++
		return nil
	}
	h.ProviderCatalogClientFactory = func(fansConn, tgxConn *models.SiteConnection, decryptSecret func(string) (string, error)) (service.FansGurusCatalogClient, service.TGXCatalogClient, error) {
		factoryCalls++
		if fansConn.ID != fansConnID || tgxConn.ID != tgxConnID {
			t.Fatalf("unexpected connections: fans=%d tgx=%d", fansConn.ID, tgxConn.ID)
		}
		secret, err := decryptSecret(tgxConn.ApiSecret)
		if err != nil {
			t.Fatalf("decrypt tgx secret failed: %v", err)
		}
		if secret != "tgx-app-key" {
			t.Fatalf("decrypted secret=%q, want tgx-app-key", secret)
		}
		return adminProviderCatalogFakeFansClient{services: []upstream.FansGurusService{
				{Service: 11, Name: "Instagram Followers", Category: "Instagram", Rate: "2.00", Min: 500, Max: 10000},
				{Service: 12, Name: "YouTube Views", Category: "YouTube", Rate: "1.00", Min: 100, Max: 10000},
			}},
			adminProviderCatalogFakeTGXClient{items: []upstream.TGXCommodity{
				{Code: "IG-ACCOUNT", Name: "Instagram Account", Price: "100.00", Minimum: 1},
			}},
			nil
	}

	body := fmt.Sprintf(`{"fansgurus_connection_id":%d,"tgx_connection_id":%d}`, fansConnID, tgxConnID)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/provider-catalog/sync", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = req

	h.SyncProviderCatalog(c)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", w.Code, w.Body.String())
	}
	if factoryCalls != 1 {
		t.Fatalf("factoryCalls=%d, want 1", factoryCalls)
	}
	if enqueueCalls != 1 {
		t.Fatalf("enqueueCalls=%d, want 1", enqueueCalls)
	}

	var resp struct {
		StatusCode int                               `json:"status_code"`
		Data       service.ProviderCatalogSyncResult `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.StatusCode != 0 || resp.Data.Imported != 3 || resp.Data.FilteredPlatform != 0 || !resp.Data.InventoryRefreshQueued || resp.Data.InventoryRefreshStatus != "queued" {
		t.Fatalf("unexpected response: %+v", resp)
	}

	var mappings []models.ProductMapping
	if err := db.Order("provider ASC").Find(&mappings).Error; err != nil {
		t.Fatalf("load mappings: %v", err)
	}
	if len(mappings) != 3 {
		t.Fatalf("mapping count=%d, want 3", len(mappings))
	}
}

func TestSyncProviderCatalogKeepsImportedResultWhenInventoryQueueDisabled(t *testing.T) {
	h, _, fansConnID, tgxConnID := setupAdminProviderCatalogSyncHandlerTest(t)
	h.ProviderCatalogClientFactory = func(_, _ *models.SiteConnection, _ func(string) (string, error)) (service.FansGurusCatalogClient, service.TGXCatalogClient, error) {
		return adminProviderCatalogFakeFansClient{}, adminProviderCatalogFakeTGXClient{}, nil
	}

	body := fmt.Sprintf(`{"fansgurus_connection_id":%d,"tgx_connection_id":%d}`, fansConnID, tgxConnID)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/provider-catalog/sync", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = req

	h.SyncProviderCatalog(c)

	var resp struct {
		StatusCode int                               `json:"status_code"`
		Data       service.ProviderCatalogSyncResult `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.StatusCode != 0 || resp.Data.InventoryRefreshQueued || resp.Data.InventoryRefreshStatus != "queue_disabled" {
		t.Fatalf("unexpected response: %+v", resp)
	}
}

func TestSyncProviderCatalogKeepsImportedResultWhenInventoryEnqueueFails(t *testing.T) {
	h, _, fansConnID, tgxConnID := setupAdminProviderCatalogSyncHandlerTest(t)
	h.ProviderCatalogClientFactory = func(_, _ *models.SiteConnection, _ func(string) (string, error)) (service.FansGurusCatalogClient, service.TGXCatalogClient, error) {
		return adminProviderCatalogFakeFansClient{}, adminProviderCatalogFakeTGXClient{}, nil
	}
	h.ProviderCatalogInventoryEnqueue = func() error { return fmt.Errorf("queue unavailable") }

	body := fmt.Sprintf(`{"fansgurus_connection_id":%d,"tgx_connection_id":%d}`, fansConnID, tgxConnID)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/provider-catalog/sync", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = req

	h.SyncProviderCatalog(c)

	var resp struct {
		StatusCode int                               `json:"status_code"`
		Data       service.ProviderCatalogSyncResult `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.StatusCode != 0 || resp.Data.InventoryRefreshQueued || resp.Data.InventoryRefreshStatus != "enqueue_failed" {
		t.Fatalf("unexpected response: %+v", resp)
	}
}

func TestSyncProviderCatalogRejectsWrongConnectionProtocol(t *testing.T) {
	h, _, fansConnID, tgxConnID := setupAdminProviderCatalogSyncHandlerTest(t)
	wrong, err := h.SiteConnectionService.Create(service.CreateConnectionInput{
		Name:      "wrong",
		BaseURL:   "https://wrong.example",
		ApiKey:    "key",
		ApiSecret: "secret",
		Protocol:  constants.ConnectionProtocolDujiaoNext,
	})
	if err != nil {
		t.Fatalf("create wrong connection failed: %v", err)
	}
	h.ProviderCatalogClientFactory = func(fansConn, tgxConn *models.SiteConnection, decryptSecret func(string) (string, error)) (service.FansGurusCatalogClient, service.TGXCatalogClient, error) {
		t.Fatalf("factory should not be called for invalid protocol")
		return nil, nil, nil
	}

	body := fmt.Sprintf(`{"fansgurus_connection_id":%d,"tgx_connection_id":%d}`, wrong.ID, tgxConnID)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/provider-catalog/sync", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = req

	h.SyncProviderCatalog(c)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		StatusCode int `json:"status_code"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.StatusCode == 0 {
		t.Fatalf("expected business error for wrong protocol")
	}
	_ = fansConnID
}

func TestSyncProviderCatalogContentEnqueuesMetadataOnlyTask(t *testing.T) {
	h, _, fansConnID, tgxConnID := setupAdminProviderCatalogSyncHandlerTest(t)
	var got queue.ProviderCatalogContentSyncPayload
	calls := 0
	h.ProviderCatalogContentEnqueue = func(payload queue.ProviderCatalogContentSyncPayload) error {
		calls++
		got = payload
		return nil
	}

	body := fmt.Sprintf(`{"fansgurus_connection_id":%d,"tgx_connection_id":%d}`, fansConnID, tgxConnID)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/provider-catalog/content/sync", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = req

	h.SyncProviderCatalogContent(c)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", w.Code, w.Body.String())
	}
	if calls != 1 || got.FansGurusConnectionID != fansConnID || got.TGXConnectionID != tgxConnID {
		t.Fatalf("enqueue calls=%d payload=%+v", calls, got)
	}
	var resp struct {
		StatusCode int `json:"status_code"`
		Data       struct {
			Status string `json:"status"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.StatusCode != 0 || resp.Data.Status != "queued" {
		t.Fatalf("unexpected response: %+v", resp)
	}
}

func TestSyncProviderCatalogContentReportsQueueDisabled(t *testing.T) {
	h, _, fansConnID, tgxConnID := setupAdminProviderCatalogSyncHandlerTest(t)
	body := fmt.Sprintf(`{"fansgurus_connection_id":%d,"tgx_connection_id":%d}`, fansConnID, tgxConnID)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/provider-catalog/content/sync", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = req

	h.SyncProviderCatalogContent(c)

	var resp struct {
		StatusCode int `json:"status_code"`
		Data       struct {
			Status string `json:"status"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.StatusCode != 0 || resp.Data.Status != "queue_disabled" {
		t.Fatalf("unexpected response: %+v", resp)
	}
}

func TestSyncProviderCatalogContentRejectsWrongConnectionProtocol(t *testing.T) {
	h, _, _, tgxConnID := setupAdminProviderCatalogSyncHandlerTest(t)
	wrong, err := h.SiteConnectionService.Create(service.CreateConnectionInput{
		Name:      "wrong",
		BaseURL:   "https://wrong.example",
		ApiKey:    "key",
		ApiSecret: "secret",
		Protocol:  constants.ConnectionProtocolDujiaoNext,
	})
	if err != nil {
		t.Fatalf("create wrong connection failed: %v", err)
	}
	h.ProviderCatalogContentEnqueue = func(queue.ProviderCatalogContentSyncPayload) error {
		t.Fatal("enqueue should not be called for invalid protocol")
		return nil
	}

	body := fmt.Sprintf(`{"fansgurus_connection_id":%d,"tgx_connection_id":%d}`, wrong.ID, tgxConnID)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/provider-catalog/content/sync", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = req

	h.SyncProviderCatalogContent(c)

	var resp struct {
		StatusCode int `json:"status_code"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.StatusCode == 0 {
		t.Fatalf("expected business error for wrong protocol")
	}
}
