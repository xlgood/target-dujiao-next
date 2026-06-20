package service

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/dujiao-next/internal/config"
	"github.com/dujiao-next/internal/models"
)

type resellerResolverRepoStub struct {
	domain *models.ResellerDomain
	err    error
	calls  int
}

func (s *resellerResolverRepoStub) FindActiveVerifiedDomain(host string) (*models.ResellerDomain, error) {
	s.calls++
	if s.err != nil {
		return nil, s.err
	}
	return s.domain, nil
}

func TestResellerDomainResolverDisabledTreatsEmptyHostAsMain(t *testing.T) {
	repo := &resellerResolverRepoStub{}
	resolver := NewResellerDomainResolver(repo, config.ResellerConfig{Enabled: false})
	tenant, err := resolver.ResolveHost(context.Background(), "")
	if err != nil {
		t.Fatalf("disabled resolver should not fail on empty host: %v", err)
	}
	if !tenant.IsMain || tenant.Unavailable || tenant.ResellerID != nil {
		t.Fatalf("disabled resolver should return main tenant for empty host, got %+v", tenant)
	}
	if repo.calls != 0 {
		t.Fatalf("disabled resolver should not query repo, calls=%d", repo.calls)
	}
}

func TestResellerDomainResolverMainHost(t *testing.T) {
	repo := &resellerResolverRepoStub{}
	resolver := NewResellerDomainResolver(repo, config.ResellerConfig{Enabled: true, MainHosts: []string{"main.example.test"}})
	tenant, err := resolver.ResolveHost(context.Background(), "MAIN.example.test:443")
	if err != nil {
		t.Fatalf("resolve main host failed: %v", err)
	}
	if !tenant.IsMain || tenant.Unavailable {
		t.Fatalf("expected main tenant, got %+v", tenant)
	}
	if repo.calls != 0 {
		t.Fatalf("main host should not query repo, calls=%d", repo.calls)
	}
}

func TestResellerDomainResolverActiveDomain(t *testing.T) {
	id := uint(7)
	repo := &resellerResolverRepoStub{domain: &models.ResellerDomain{
		ID:         11,
		ResellerID: id,
		Domain:     "shop.example.test",
		Profile:    &models.ResellerProfile{ID: id, UserID: 88, Status: models.ResellerProfileStatusActive},
	}}
	resolver := NewResellerDomainResolver(repo, config.ResellerConfig{Enabled: true, MainHosts: []string{"main.example.test"}})
	tenant, err := resolver.ResolveHost(context.Background(), "shop.example.test")
	if err != nil {
		t.Fatalf("resolve reseller host failed: %v", err)
	}
	if tenant.IsMain || tenant.ResellerID == nil || *tenant.ResellerID != id {
		t.Fatalf("expected reseller tenant, got %+v", tenant)
	}
	if tenant.ResellerUserID != 88 {
		t.Fatalf("expected reseller user id 88, got %d", tenant.ResellerUserID)
	}
}

func TestResellerDomainResolverInactiveProfileUnavailable(t *testing.T) {
	id := uint(7)
	repo := &resellerResolverRepoStub{domain: &models.ResellerDomain{
		ID:         11,
		ResellerID: id,
		Domain:     "shop.example.test",
		Status:     models.ResellerDomainStatusActive,
		Profile:    &models.ResellerProfile{ID: id, UserID: 88, Status: models.ResellerProfileStatusDisabled},
	}}
	resolver := NewResellerDomainResolver(repo, config.ResellerConfig{Enabled: true, MainHosts: []string{"main.example.test"}})
	tenant, err := resolver.ResolveHost(context.Background(), "shop.example.test")
	if err != nil {
		t.Fatalf("resolve disabled profile host failed: %v", err)
	}
	if !tenant.Unavailable || tenant.ResellerID != nil {
		t.Fatalf("expected disabled profile to be unavailable, got %+v", tenant)
	}
}

func TestResellerDomainResolverRequestUsesTrustedForwardedHost(t *testing.T) {
	id := uint(8)
	repo := &resellerResolverRepoStub{domain: &models.ResellerDomain{
		ID:         12,
		ResellerID: id,
		Domain:     "shop.example.test",
		Profile:    &models.ResellerProfile{ID: id, UserID: 89, Status: models.ResellerProfileStatusActive},
	}}
	resolver := NewResellerDomainResolver(repo, config.ResellerConfig{
		Enabled:              true,
		MainHosts:            []string{"main.example.test"},
		TrustedForwardedHost: true,
	})
	req, err := http.NewRequest(http.MethodGet, "https://internal.example.test/api/v1/public/config", nil)
	if err != nil {
		t.Fatalf("new request failed: %v", err)
	}
	req.Host = "internal.example.test"
	req.Header.Set("X-Forwarded-Host", "shop.example.test")
	tenant, err := resolver.ResolveRequest(context.Background(), req)
	if err != nil {
		t.Fatalf("resolve request failed: %v", err)
	}
	if tenant.ResellerID == nil || *tenant.ResellerID != id {
		t.Fatalf("expected forwarded reseller tenant, got %+v", tenant)
	}
	if repo.calls != 1 {
		t.Fatalf("expected one repo lookup for forwarded host, calls=%d", repo.calls)
	}
}

func TestResellerDomainResolverUnknownDomainUnavailable(t *testing.T) {
	repo := &resellerResolverRepoStub{}
	resolver := NewResellerDomainResolver(repo, config.ResellerConfig{Enabled: true, MainHosts: []string{"main.example.test"}})
	tenant, err := resolver.ResolveHost(context.Background(), "unknown.example.test")
	if err != nil {
		t.Fatalf("unknown domain should not return technical error: %v", err)
	}
	if !tenant.Unavailable || tenant.UnavailableReason != ResellerDomainUnavailableNotFound {
		t.Fatalf("expected unavailable not_found, got %+v", tenant)
	}
}

func TestResellerDomainResolverRepoError(t *testing.T) {
	repo := &resellerResolverRepoStub{err: errors.New("db down")}
	resolver := NewResellerDomainResolver(repo, config.ResellerConfig{Enabled: true, MainHosts: []string{"main.example.test"}})
	_, err := resolver.ResolveHost(context.Background(), "shop.example.test")
	if err == nil {
		t.Fatal("expected repository error")
	}
}
