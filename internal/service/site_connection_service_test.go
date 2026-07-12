package service

import (
	"errors"
	"strings"
	"testing"

	"github.com/dujiao-next/internal/constants"
	"github.com/dujiao-next/internal/crypto"
	"github.com/dujiao-next/internal/models"
	"github.com/dujiao-next/internal/repository"

	"github.com/shopspring/decimal"
)

func TestSiteConnectionServicePingReturnsAdapterCreationError(t *testing.T) {
	appSecretKey := "test-secret-key"
	encrypted, err := crypto.Encrypt(crypto.DeriveKey(appSecretKey), "upstream-secret")
	if err != nil {
		t.Fatalf("encrypt secret failed: %v", err)
	}
	repo := &siteConnectionRepoStub{
		conn: &models.SiteConnection{
			ID:        1,
			Name:      "unsupported upstream",
			BaseURL:   "https://upstream.example.com",
			ApiKey:    "upstream-key",
			ApiSecret: encrypted,
			Protocol:  "unsupported-protocol",
			Status:    constants.ConnectionStatusPending,
		},
	}
	svc := NewSiteConnectionService(repo, appSecretKey, t.TempDir())

	result, err := svc.Ping(1)

	if err == nil {
		t.Fatalf("expected adapter creation error")
	}
	if result != nil {
		t.Fatalf("expected nil ping result, got %#v", result)
	}
	if !strings.Contains(err.Error(), "unsupported protocol") {
		t.Fatalf("expected unsupported protocol error, got %v", err)
	}
	if repo.updated {
		t.Fatalf("connection should not be updated when adapter creation fails")
	}
}

func TestSiteConnectionServiceCreateAllowsFansGurusWithoutSecret(t *testing.T) {
	repo := &siteConnectionRepoStub{}
	svc := NewSiteConnectionService(repo, "test-secret-key", t.TempDir())

	conn, err := svc.Create(CreateConnectionInput{
		Name:     "FansGurus",
		BaseURL:  "https://fansgurus.example.com/api/v2",
		ApiKey:   "fansgurus-api-key",
		Protocol: constants.ConnectionProtocolFansGurus,
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if conn.ApiSecret != "" {
		t.Fatalf("ApiSecret = %q, want empty", conn.ApiSecret)
	}
}

func TestSiteConnectionServiceCreateRequiresSecretForTGX(t *testing.T) {
	repo := &siteConnectionRepoStub{}
	svc := NewSiteConnectionService(repo, "test-secret-key", t.TempDir())

	_, err := svc.Create(CreateConnectionInput{
		Name:     "TGX",
		BaseURL:  "https://tgx.example.com/shared",
		ApiKey:   "tgx-app-id",
		Protocol: constants.ConnectionProtocolTGXAccount,
	})
	if !errors.Is(err, ErrConnectionInvalid) {
		t.Fatalf("Create() error = %v, want ErrConnectionInvalid", err)
	}
}

type fakeMarkupReapplier struct {
	calls []uint
}

func (f *fakeMarkupReapplier) ReapplyMarkup(connectionID uint) (int, error) {
	f.calls = append(f.calls, connectionID)
	return 0, nil
}

func newReapplyTestConn() *models.SiteConnection {
	return &models.SiteConnection{
		ID:                 7,
		Name:               "conn",
		BaseURL:            "https://up.example.com",
		ApiKey:             "key",
		Protocol:           "dujiao-next",
		Status:             constants.ConnectionStatusActive,
		ExchangeRate:       decimal.NewFromInt(1),
		PriceMarkupPercent: decimal.Zero,
		PriceRoundingMode:  "none",
	}
}

func TestSiteConnectionServiceUpdateTriggersReapplyWhenExchangeRateChanges(t *testing.T) {
	repo := &siteConnectionRepoStub{conn: newReapplyTestConn()}
	svc := NewSiteConnectionService(repo, "test-secret-key", t.TempDir())
	reapplier := &fakeMarkupReapplier{}
	svc.SetMarkupReapplier(reapplier)

	rate := 6.9
	if _, err := svc.Update(7, UpdateConnectionInput{ExchangeRate: &rate}); err != nil {
		t.Fatalf("update failed: %v", err)
	}
	if len(reapplier.calls) != 1 || reapplier.calls[0] != 7 {
		t.Fatalf("expected reapply called once for conn 7, got %#v", reapplier.calls)
	}
}

func TestSiteConnectionServiceUpdateSkipsReapplyWhenPriceConfigUnchanged(t *testing.T) {
	repo := &siteConnectionRepoStub{conn: newReapplyTestConn()}
	svc := NewSiteConnectionService(repo, "test-secret-key", t.TempDir())
	reapplier := &fakeMarkupReapplier{}
	svc.SetMarkupReapplier(reapplier)

	// 只改名字，汇率传入与现值相同的 1 → 定价配置未变，不应触发重算。
	name := "renamed"
	rate := 1.0
	if _, err := svc.Update(7, UpdateConnectionInput{Name: name, ExchangeRate: &rate}); err != nil {
		t.Fatalf("update failed: %v", err)
	}
	if len(reapplier.calls) != 0 {
		t.Fatalf("expected no reapply when price config unchanged, got %#v", reapplier.calls)
	}
}

type siteConnectionRepoStub struct {
	conn    *models.SiteConnection
	updated bool
}

func (r *siteConnectionRepoStub) GetByID(id uint) (*models.SiteConnection, error) {
	if r.conn != nil && r.conn.ID == id {
		copy := *r.conn
		return &copy, nil
	}
	return nil, nil
}

func (r *siteConnectionRepoStub) GetByApiKey(apiKey string) (*models.SiteConnection, error) {
	if r.conn != nil && r.conn.ApiKey == apiKey {
		copy := *r.conn
		return &copy, nil
	}
	return nil, nil
}

func (r *siteConnectionRepoStub) Create(conn *models.SiteConnection) error {
	copy := *conn
	r.conn = &copy
	return nil
}

func (r *siteConnectionRepoStub) Update(conn *models.SiteConnection) error {
	r.updated = true
	copy := *conn
	r.conn = &copy
	return nil
}

func (r *siteConnectionRepoStub) Delete(id uint) error {
	if r.conn != nil && r.conn.ID == id {
		r.conn = nil
	}
	return nil
}

func (r *siteConnectionRepoStub) List(repository.SiteConnectionListFilter) ([]models.SiteConnection, int64, error) {
	if r.conn == nil {
		return nil, 0, nil
	}
	return []models.SiteConnection{*r.conn}, 1, nil
}

func (r *siteConnectionRepoStub) ListActive() ([]models.SiteConnection, error) {
	if r.conn == nil || r.conn.Status != constants.ConnectionStatusActive {
		return nil, nil
	}
	return []models.SiteConnection{*r.conn}, nil
}
