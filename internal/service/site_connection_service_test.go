package service

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
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

func TestSiteConnectionServiceCheckActiveBalancesRecordsLowBalance(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"balance": "5.00", "currency": "USD"})
	}))
	defer server.Close()
	repo := &siteConnectionRepoStub{conn: &models.SiteConnection{
		ID: 9, Name: "FansGurus", BaseURL: server.URL, ApiKey: "key", Protocol: constants.ConnectionProtocolFansGurus,
		Status: constants.ConnectionStatusActive, BalanceAlertMinimum: decimal.NewFromInt(10),
	}}
	snapshotRepo := &balanceSnapshotRepoStub{}
	svc := NewSiteConnectionService(repo, "test-secret-key", t.TempDir())
	svc.SetBalanceSnapshotRepository(snapshotRepo)

	svc.CheckActiveBalances()

	if len(snapshotRepo.snapshots) != 1 {
		t.Fatalf("snapshot count=%d, want 1", len(snapshotRepo.snapshots))
	}
	got := snapshotRepo.snapshots[0]
	if got.Status != "low_balance" || got.Balance != "5.00" || got.Currency != "USD" {
		t.Fatalf("snapshot=%+v", got)
	}
}

type siteConnectionRepoStub struct {
	conn    *models.SiteConnection
	updated bool
}

type balanceSnapshotRepoStub struct {
	snapshots []models.ProviderBalanceSnapshot
}

func (r *balanceSnapshotRepoStub) Create(snapshot *models.ProviderBalanceSnapshot) error {
	r.snapshots = append(r.snapshots, *snapshot)
	return nil
}

func (r *balanceSnapshotRepoStub) Latest(connectionID uint) (*models.ProviderBalanceSnapshot, error) {
	for i := len(r.snapshots) - 1; i >= 0; i-- {
		if r.snapshots[i].ConnectionID == connectionID {
			copy := r.snapshots[i]
			return &copy, nil
		}
	}
	return nil, nil
}

func (r *balanceSnapshotRepoStub) List(filter repository.ProviderBalanceSnapshotListFilter) ([]models.ProviderBalanceSnapshot, int64, error) {
	result := make([]models.ProviderBalanceSnapshot, 0)
	for _, snapshot := range r.snapshots {
		if filter.ConnectionID > 0 && snapshot.ConnectionID != filter.ConnectionID {
			continue
		}
		if filter.Status != "" && snapshot.Status != filter.Status {
			continue
		}
		result = append(result, snapshot)
	}
	return result, int64(len(result)), nil
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
