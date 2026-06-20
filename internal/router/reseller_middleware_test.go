package router

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dujiao-next/internal/service"
	"github.com/gin-gonic/gin"
)

type middlewareResolverStub struct {
	tenant service.TenantContext
	err    error
}

func (s middlewareResolverStub) ResolveRequest(ctx context.Context, req *http.Request) (service.TenantContext, error) {
	if s.err != nil {
		return service.TenantContext{}, s.err
	}
	return s.tenant, nil
}

func TestResellerTenantMiddlewareSetsTenant(t *testing.T) {
	gin.SetMode(gin.TestMode)
	id := uint(9)
	resolver := middlewareResolverStub{tenant: service.TenantContext{Host: "shop.example.test", ResellerID: &id, ResellerUserID: 100}}
	r := gin.New()
	r.Use(ResellerTenantMiddleware(resolver))
	r.GET("/probe", func(c *gin.Context) {
		tenant, ok := service.TenantFromContext(c.Request.Context())
		if !ok {
			t.Fatal("tenant missing from request context")
		}
		if tenant.ResellerID == nil || *tenant.ResellerID != id {
			t.Fatalf("tenant reseller id mismatch: %+v", tenant)
		}
		c.String(http.StatusOK, "ok")
	})
	req := httptest.NewRequest(http.MethodGet, "/probe", nil)
	req.Host = "shop.example.test"
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestResellerTenantMiddlewareUnavailableReturns404(t *testing.T) {
	gin.SetMode(gin.TestMode)
	resolver := middlewareResolverStub{tenant: service.UnavailableTenantContext("unknown.example.test", service.ResellerDomainUnavailableNotFound)}
	r := gin.New()
	r.Use(ResellerTenantMiddleware(resolver))
	r.GET("/probe", func(c *gin.Context) {
		c.String(http.StatusOK, "should not run")
	})
	req := httptest.NewRequest(http.MethodGet, "/probe", nil)
	req.Host = "unknown.example.test"
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestResellerTenantMiddlewareOnlyBlocksStorefrontGroup(t *testing.T) {
	gin.SetMode(gin.TestMode)
	resolver := middlewareResolverStub{tenant: service.UnavailableTenantContext("unknown.example.test", service.ResellerDomainUnavailableNotFound)}
	r := gin.New()

	r.GET("/sitemap.xml", func(c *gin.Context) { c.String(http.StatusOK, "sitemap") })
	r.GET("/robots.txt", func(c *gin.Context) { c.String(http.StatusOK, "robots") })

	apiV1 := r.Group("/api/v1")
	storefront := apiV1.Group("")
	storefront.Use(ResellerTenantMiddleware(resolver))
	storefront.GET("/public/config", func(c *gin.Context) { c.String(http.StatusOK, "public") })
	storefront.POST("/guest/orders", func(c *gin.Context) { c.String(http.StatusOK, "guest") })
	storefront.POST("/auth/login", func(c *gin.Context) { c.String(http.StatusOK, "auth") })
	storefront.GET("/me", func(c *gin.Context) { c.String(http.StatusOK, "me") })

	apiV1.POST("/payments/callback", func(c *gin.Context) { c.String(http.StatusOK, "payment callback") })
	apiV1.POST("/upstream/callback", func(c *gin.Context) { c.String(http.StatusOK, "upstream callback") })
	apiV1.GET("/channel/telegram/config", func(c *gin.Context) { c.String(http.StatusOK, "channel") })
	apiV1.GET("/upstream/products", func(c *gin.Context) { c.String(http.StatusOK, "upstream api") })
	apiV1.GET("/admin/probe", func(c *gin.Context) { c.String(http.StatusOK, "admin") })
	r.GET("/health", func(c *gin.Context) { c.String(http.StatusOK, "health") })

	cases := []struct {
		method string
		path   string
		want   int
	}{
		{http.MethodGet, "/api/v1/public/config", http.StatusNotFound},
		{http.MethodPost, "/api/v1/guest/orders", http.StatusNotFound},
		{http.MethodPost, "/api/v1/auth/login", http.StatusNotFound},
		{http.MethodGet, "/api/v1/me", http.StatusNotFound},
		{http.MethodPost, "/api/v1/payments/callback", http.StatusOK},
		{http.MethodPost, "/api/v1/upstream/callback", http.StatusOK},
		{http.MethodGet, "/api/v1/channel/telegram/config", http.StatusOK},
		{http.MethodGet, "/api/v1/upstream/products", http.StatusOK},
		{http.MethodGet, "/api/v1/admin/probe", http.StatusOK},
		{http.MethodGet, "/health", http.StatusOK},
		{http.MethodGet, "/sitemap.xml", http.StatusOK},
		{http.MethodGet, "/robots.txt", http.StatusOK},
	}
	for _, tc := range cases {
		req := httptest.NewRequest(tc.method, tc.path, nil)
		req.Host = "unknown.example.test"
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != tc.want {
			t.Fatalf("%s %s status=%d want %d body=%s", tc.method, tc.path, w.Code, tc.want, w.Body.String())
		}
	}
}

func TestRequireMainTenantForResellerConsoleBlocksResellerTenant(t *testing.T) {
	gin.SetMode(gin.TestMode)
	resellerID := uint(9)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		tenant := service.ResellerTenantContext("shop.example.test", resellerID, 100, "shop.example.test")
		c.Request = c.Request.WithContext(service.WithTenantContext(c.Request.Context(), tenant))
		c.Next()
	})
	r.GET("/api/v1/user/reseller/profile", RequireMainTenantForResellerConsole(), func(c *gin.Context) {
		c.String(http.StatusOK, "should not run")
	})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/user/reseller/profile", nil)
	req.Host = "shop.example.test"
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"status_code":403`) {
		t.Fatalf("expected forbidden response, body=%s", w.Body.String())
	}
	if strings.Contains(w.Body.String(), "should not run") {
		t.Fatalf("handler should not run on reseller tenant, body=%s", w.Body.String())
	}
}

func TestRequireMainTenantForResellerConsoleAllowsMainTenant(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		tenant := service.MainTenantContext("main.example.test")
		c.Request = c.Request.WithContext(service.WithTenantContext(c.Request.Context(), tenant))
		c.Next()
	})
	r.GET("/api/v1/user/reseller/profile", RequireMainTenantForResellerConsole(), func(c *gin.Context) {
		c.String(http.StatusOK, "ok")
	})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/user/reseller/profile", nil)
	req.Host = "main.example.test"
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK || w.Body.String() != "ok" {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
}
