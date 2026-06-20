package service

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/dujiao-next/internal/cache"
	"github.com/dujiao-next/internal/config"
	"github.com/dujiao-next/internal/models"
)

const (
	ResellerDomainUnavailableNotFound = "not_found"
)

type ResellerDomainLookupRepository interface {
	FindActiveVerifiedDomain(host string) (*models.ResellerDomain, error)
}

// ResellerDomainResolver 将请求 Host 解析为主站或分销站上下文。
type ResellerDomainResolver struct {
	repo      ResellerDomainLookupRepository
	cfg       config.ResellerConfig
	mainHosts map[string]struct{}
}

func NewResellerDomainResolver(repo ResellerDomainLookupRepository, cfg config.ResellerConfig) *ResellerDomainResolver {
	mainHosts := make(map[string]struct{}, len(cfg.MainHosts))
	for _, host := range cfg.MainHosts {
		normalized := NormalizeResellerHost(host)
		if normalized != "" {
			mainHosts[normalized] = struct{}{}
		}
	}
	return &ResellerDomainResolver{repo: repo, cfg: cfg, mainHosts: mainHosts}
}

func (r *ResellerDomainResolver) ResolveRequest(ctx context.Context, req *http.Request) (TenantContext, error) {
	if r == nil {
		return MainTenantContext(""), nil
	}
	return r.ResolveHost(ctx, ResolveResellerRequestHost(req, r.cfg))
}

func (r *ResellerDomainResolver) ResolveHost(ctx context.Context, rawHost string) (TenantContext, error) {
	host := NormalizeResellerHost(rawHost)
	if r == nil || !r.cfg.Enabled {
		return MainTenantContext(host), nil
	}
	if host == "" {
		return MainTenantContext(host), nil
	}
	if _, ok := r.mainHosts[host]; ok {
		return MainTenantContext(host), nil
	}
	var cached cache.ResellerDomainCacheValue
	if hit, err := cache.GetResellerDomain(ctx, host, &cached); err == nil && hit {
		return ResellerTenantContext(host, cached.ResellerID, cached.ResellerUserID, cached.PrimaryDomain), nil
	}
	if hit, err := cache.GetResellerDomainNotFound(ctx, host); err == nil && hit {
		return UnavailableTenantContext(host, ResellerDomainUnavailableNotFound), nil
	}
	if r.repo == nil {
		return TenantContext{}, errors.New("reseller domain repository is nil")
	}
	domain, err := r.repo.FindActiveVerifiedDomain(host)
	if err != nil {
		return TenantContext{}, err
	}
	if domain == nil || domain.Profile == nil || domain.Profile.Status != models.ResellerProfileStatusActive {
		_ = cache.SetResellerDomainNotFound(ctx, host)
		return UnavailableTenantContext(host, ResellerDomainUnavailableNotFound), nil
	}
	primaryDomain := strings.TrimSpace(domain.Domain)
	value := cache.ResellerDomainCacheValue{
		ResellerID:         domain.ResellerID,
		ResellerUserID:     domain.Profile.UserID,
		Domain:             domain.Domain,
		PrimaryDomain:      primaryDomain,
		Status:             domain.Status,
		VerificationStatus: domain.VerificationStatus,
	}
	_ = cache.SetResellerDomain(ctx, host, value)
	return ResellerTenantContext(host, domain.ResellerID, domain.Profile.UserID, primaryDomain), nil
}
