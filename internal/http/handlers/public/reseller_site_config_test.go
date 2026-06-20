package public

import (
	"net/http"
	"strings"
	"testing"

	"github.com/dujiao-next/internal/models"
	"github.com/dujiao-next/internal/provider"
	"github.com/dujiao-next/internal/repository"
	"github.com/dujiao-next/internal/service"
)

func TestPublicResellerSiteConfigUpdateAndGet(t *testing.T) {
	db := openPublicResellerHandlerTestDB(t)
	if err := db.AutoMigrate(&models.ResellerSiteConfig{}); err != nil {
		t.Fatalf("migrate site config failed: %v", err)
	}
	profile := seedPublicResellerHandlerProfile(t, db)
	repo := repository.NewResellerRepository(db)
	h := &Handler{Container: &provider.Container{
		ResellerSiteConfigService: service.NewResellerSiteConfigService(repo),
	}}

	body := []byte(`{"site_name":"Alice Store","logo":"/uploads/logo.png","support":{"telegram":"https://t.me/alice"}}`)
	c, recorder := newPublicResellerHandlerTestContext(http.MethodPut, "/api/v1/reseller/site-config", body, profile.UserID)
	h.UpdateResellerSiteConfig(c)
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", recorder.Code, recorder.Body.String())
	}

	c, recorder = newPublicResellerHandlerTestContext(http.MethodGet, "/api/v1/reseller/site-config", nil, profile.UserID)
	h.GetResellerSiteConfig(c)
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), `"site_name":"Alice Store"`) {
		t.Fatalf("expected saved site name in response, got %s", recorder.Body.String())
	}
	if strings.Contains(recorder.Body.String(), `"theme"`) {
		t.Fatalf("expected theme to be omitted from response, got %s", recorder.Body.String())
	}
}
