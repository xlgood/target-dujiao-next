package admin

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/dujiao-next/internal/config"
	"github.com/dujiao-next/internal/http/response"
	"github.com/dujiao-next/internal/models"
	"github.com/dujiao-next/internal/provider"
	"github.com/dujiao-next/internal/repository"
	"github.com/dujiao-next/internal/service"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func setupAdminResellerManagementHandlerTest(t *testing.T) (*Handler, *gorm.DB) {
	t.Helper()
	gin.SetMode(gin.TestMode)

	dsn := fmt.Sprintf("file:admin_reseller_management_%d?mode=memory&cache=shared", time.Now().UnixNano())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db failed: %v", err)
	}
	if err := db.AutoMigrate(&models.User{}, &models.Admin{}, &models.AuthzAuditLog{}, &models.ResellerProfile{}, &models.ResellerDomain{}, &models.ResellerSiteConfig{}); err != nil {
		t.Fatalf("migrate failed: %v", err)
	}

	resellerRepo := repository.NewResellerRepository(db)
	auditRepo := repository.NewAuthzAuditLogRepository(db)
	return New(&provider.Container{
		ResellerRepo: resellerRepo,
		ResellerManagementService: service.NewResellerManagementService(resellerRepo, config.ResellerConfig{
			Enabled:          true,
			SelfApplyEnabled: true,
			SubdomainBase:    "shop.example.test",
			MainHosts:        []string{"main.example.test"},
		}),
		AuthzAuditService: service.NewAuthzAuditService(auditRepo),
	}), db
}

func newAdminResellerManagementContext(method, target string, body *strings.Reader) (*gin.Context, *httptest.ResponseRecorder) {
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	if body == nil {
		body = strings.NewReader("")
	}
	c.Request = httptest.NewRequest(method, target, body)
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set("admin_id", uint(99))
	c.Set("username", "finance-admin")
	c.Set("request_id", "req-reseller-management")
	return c, recorder
}

func seedAdminResellerManagementProfile(t *testing.T, db *gorm.DB, status string) models.ResellerProfile {
	t.Helper()
	user := models.User{Email: fmt.Sprintf("reseller-management-%d@example.test", time.Now().UnixNano()), PasswordHash: "hash", DisplayName: "Reseller Management"}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user failed: %v", err)
	}
	profile := models.ResellerProfile{UserID: user.ID, Status: status, ApplyReason: "please approve", SettlementStatus: models.ResellerSettlementStatusNormal}
	if err := db.Create(&profile).Error; err != nil {
		t.Fatalf("create profile failed: %v", err)
	}
	return profile
}

func TestAdminResellerManagementApproveProfileCreatesDomainAndAudit(t *testing.T) {
	h, db := setupAdminResellerManagementHandlerTest(t)
	profile := seedAdminResellerManagementProfile(t, db, models.ResellerProfileStatusPendingReview)
	c, recorder := newAdminResellerManagementContext(http.MethodPost, fmt.Sprintf("/admin/resellers/profiles/%d/approve", profile.ID), strings.NewReader(`{"default_markup_percent":"8.00","max_markup_percent":"40.00"}`))
	c.Params = gin.Params{{Key: "id", Value: fmt.Sprintf("%d", profile.ID)}}

	h.ApproveResellerProfile(c)
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", recorder.Code, recorder.Body.String())
	}
	var domain models.ResellerDomain
	if err := db.Where("reseller_id = ? AND type = ?", profile.ID, models.ResellerDomainTypeSubdomain).First(&domain).Error; err != nil {
		t.Fatalf("expected system domain: %v", err)
	}
	if domain.Domain != fmt.Sprintf("r%d.shop.example.test", profile.ID) || domain.Status != models.ResellerDomainStatusActive || domain.VerificationStatus != models.ResellerDomainVerificationVerified {
		t.Fatalf("unexpected system domain: %+v", domain)
	}
	var auditCount int64
	if err := db.Model(&models.AuthzAuditLog{}).Where("action = ?", "reseller_profile_approve").Count(&auditCount).Error; err != nil {
		t.Fatalf("count audit failed: %v", err)
	}
	if auditCount != 1 {
		t.Fatalf("expected one audit log, got %d", auditCount)
	}
}

func TestAdminResellerManagementApproveProfileReportsMissingSubdomainBase(t *testing.T) {
	h, db := setupAdminResellerManagementHandlerTest(t)
	h.ResellerManagementService = service.NewResellerManagementService(h.ResellerRepo, config.ResellerConfig{
		Enabled:          true,
		SelfApplyEnabled: true,
	})
	profile := seedAdminResellerManagementProfile(t, db, models.ResellerProfileStatusPendingReview)
	c, recorder := newAdminResellerManagementContext(http.MethodPost, fmt.Sprintf("/admin/resellers/profiles/%d/approve", profile.ID), strings.NewReader(`{"default_markup_percent":"8.00","max_markup_percent":"40.00"}`))
	c.Params = gin.Params{{Key: "id", Value: fmt.Sprintf("%d", profile.ID)}}

	h.ApproveResellerProfile(c)
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected http 200 envelope, got %d body=%s", recorder.Code, recorder.Body.String())
	}
	var resp struct {
		StatusCode int    `json:"status_code"`
		Msg        string `json:"msg"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response failed: %v", err)
	}
	if resp.StatusCode != response.CodeBadRequest {
		t.Fatalf("expected status_code=400, got %+v body=%s", resp, recorder.Body.String())
	}
	if !strings.Contains(resp.Msg, "分销系统二级域名基础域名未配置") {
		t.Fatalf("expected missing subdomain base message, got %+v body=%s", resp, recorder.Body.String())
	}
}

func TestAdminResellerManagementListProfilesFilters(t *testing.T) {
	h, db := setupAdminResellerManagementHandlerTest(t)
	profile := seedAdminResellerManagementProfile(t, db, models.ResellerProfileStatusPendingReview)
	activeProfile := seedAdminResellerManagementProfile(t, db, models.ResellerProfileStatusActive)
	c, recorder := newAdminResellerManagementContext(http.MethodGet, "/admin/resellers/profiles?status=pending_review&page=1&page_size=20", nil)

	h.ListResellerProfiles(c)
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), fmt.Sprintf(`"id":%d`, profile.ID)) || strings.Contains(recorder.Body.String(), fmt.Sprintf(`"user_id":%d`, activeProfile.UserID)) {
		t.Fatalf("unexpected list body: %s", recorder.Body.String())
	}
}

func TestAdminResellerManagementApproveDomain(t *testing.T) {
	h, db := setupAdminResellerManagementHandlerTest(t)
	profile := seedAdminResellerManagementProfile(t, db, models.ResellerProfileStatusActive)
	domain := models.ResellerDomain{ResellerID: profile.ID, Domain: "custom.example.test", Type: models.ResellerDomainTypeCustom, VerificationStatus: models.ResellerDomainVerificationPending, Status: models.ResellerDomainStatusPendingReview}
	if err := db.Create(&domain).Error; err != nil {
		t.Fatalf("create domain failed: %v", err)
	}
	c, recorder := newAdminResellerManagementContext(http.MethodPost, fmt.Sprintf("/admin/resellers/domains/%d/approve", domain.ID), strings.NewReader(`{}`))
	c.Params = gin.Params{{Key: "id", Value: fmt.Sprintf("%d", domain.ID)}}

	h.ApproveResellerDomain(c)
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", recorder.Code, recorder.Body.String())
	}
	var loaded models.ResellerDomain
	if err := db.First(&loaded, domain.ID).Error; err != nil {
		t.Fatalf("load domain failed: %v", err)
	}
	if loaded.Status != models.ResellerDomainStatusActive || loaded.VerificationStatus != models.ResellerDomainVerificationVerified || loaded.VerifiedAt == nil {
		t.Fatalf("unexpected approved domain: %+v", loaded)
	}
}
