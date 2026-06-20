package router

import (
	"context"
	"net/http"

	"github.com/dujiao-next/internal/http/response"
	"github.com/dujiao-next/internal/i18n"
	"github.com/dujiao-next/internal/service"
	"github.com/gin-gonic/gin"
)

type ResellerTenantResolver interface {
	ResolveRequest(ctx context.Context, req *http.Request) (service.TenantContext, error)
}

func ResellerTenantMiddleware(resolver ResellerTenantResolver) gin.HandlerFunc {
	return func(c *gin.Context) {
		if resolver == nil {
			c.Next()
			return
		}
		tenant, err := resolver.ResolveRequest(c.Request.Context(), c.Request)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"code": "internal_error", "message": "failed to resolve tenant"})
			return
		}
		if tenant.Unavailable {
			c.AbortWithStatusJSON(http.StatusNotFound, gin.H{"code": "not_found", "message": "site unavailable"})
			return
		}
		ctx := service.WithTenantContext(c.Request.Context(), tenant)
		c.Request = c.Request.WithContext(ctx)
		c.Set("tenant", tenant)
		c.Next()
	}
}

func RequireMainTenantForResellerConsole() gin.HandlerFunc {
	return func(c *gin.Context) {
		tenant, ok := service.TenantFromContext(c.Request.Context())
		if ok && tenant.ResellerID != nil && !tenant.IsMain && !tenant.Unavailable {
			msg := i18n.T(i18n.ResolveLocale(c), "error.forbidden")
			response.Forbidden(c, msg)
			c.Abort()
			return
		}
		c.Next()
	}
}
