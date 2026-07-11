package service

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/dujiao-next/internal/models"
	"github.com/dujiao-next/internal/upstream"
)

type FansGurusCatalogClient interface {
	ListServices(ctx context.Context) ([]upstream.FansGurusService, error)
}

type TGXCatalogClient interface {
	ListItems(ctx context.Context) (*upstream.TGXItemsResponse, error)
}

type ProviderCatalogSyncInput struct {
	FansGurusConnectionID uint
	TGXConnectionID       uint
}

type ProviderCatalogSyncResult struct {
	FansGurusPulled    int
	TGXPulled          int
	SupportedPlatforms []string
	FilteredTelegram   int
	FilteredInactive   int
	FilteredPlatform   int
	Imported           int
	Skipped            int
	Deactivated        int
}

func (s *ProductMappingService) SyncProviderCatalogWithClients(
	ctx context.Context,
	input ProviderCatalogSyncInput,
	fansClient FansGurusCatalogClient,
	tgxClient TGXCatalogClient,
) (*ProviderCatalogSyncResult, error) {
	if fansClient == nil || tgxClient == nil {
		return nil, fmt.Errorf("provider catalog clients are required")
	}

	startedAt := time.Now()
	fansServices, err := fansClient.ListServices(ctx)
	if err != nil {
		s.recordProviderCatalogSyncRun(startedAt, nil, nil, nil, fmt.Errorf("list fansgurus services: %w", err))
		return nil, fmt.Errorf("list fansgurus services: %w", err)
	}
	tgxItems, err := tgxClient.ListItems(ctx)
	if err != nil {
		s.recordProviderCatalogSyncRun(startedAt, fansServices, nil, nil, fmt.Errorf("list tgx items: %w", err))
		return nil, fmt.Errorf("list tgx items: %w", err)
	}
	if tgxItems == nil {
		tgxItems = &upstream.TGXItemsResponse{}
	}

	fansCatalog := make([]upstream.ProviderCatalogItem, 0, len(fansServices))
	for _, service := range fansServices {
		item, err := upstream.NewFansGurusCatalogItem(service)
		if err != nil {
			return nil, fmt.Errorf("convert fansgurus service %d: %w", service.Service, err)
		}
		fansCatalog = append(fansCatalog, item)
	}

	tgxCatalog := make([]upstream.ProviderCatalogItem, 0, len(tgxItems.Items))
	for _, commodity := range tgxItems.Items {
		item, err := upstream.NewTGXCatalogItem(commodity)
		if err != nil {
			return nil, fmt.Errorf("convert tgx item %s: %w", commodity.Code, err)
		}
		tgxCatalog = append(tgxCatalog, item)
	}

	filtered := upstream.BuildFilteredCatalog(fansCatalog, tgxCatalog)
	importResult, err := s.ImportProviderCatalogByProviderConnections(map[string]uint{
		upstream.CatalogProviderFansGurus: input.FansGurusConnectionID,
		upstream.CatalogProviderTGX:       input.TGXConnectionID,
	}, filtered)
	if err != nil {
		s.recordProviderCatalogSyncRun(startedAt, fansServices, tgxItems.Items, nil, err)
		return nil, err
	}

	deactivated, err := s.deactivateStaleProviderCatalogMappings(input, filtered)
	if err != nil {
		s.recordProviderCatalogSyncRun(startedAt, fansServices, tgxItems.Items, nil, err)
		return nil, err
	}

	result := &ProviderCatalogSyncResult{
		FansGurusPulled:    len(fansServices),
		TGXPulled:          len(tgxItems.Items),
		SupportedPlatforms: filtered.SupportedPlatforms,
		FilteredTelegram:   len(filtered.FilteredTelegram),
		FilteredInactive:   len(filtered.FilteredInactive),
		FilteredPlatform:   len(filtered.FilteredPlatform),
		Imported:           importResult.Imported,
		Skipped:            importResult.Skipped,
		Deactivated:        deactivated,
	}
	s.recordProviderCatalogSyncRun(startedAt, fansServices, tgxItems.Items, result, nil)
	return result, nil
}

func (s *ProductMappingService) deactivateStaleProviderCatalogMappings(input ProviderCatalogSyncInput, catalog upstream.FilteredCatalog) (int, error) {
	total := 0
	fansCodes := providerCatalogCodes(catalog.FansGurus)
	tgxCodes := providerCatalogCodes(catalog.TGX)
	for _, target := range []struct {
		connectionID uint
		provider     string
		activeCodes  []string
	}{
		{input.FansGurusConnectionID, upstream.CatalogProviderFansGurus, fansCodes},
		{input.TGXConnectionID, upstream.CatalogProviderTGX, tgxCodes},
	} {
		if target.connectionID == 0 {
			continue
		}
		mappings, err := s.mappingRepo.ListActiveByProvider(target.connectionID, target.provider)
		if err != nil {
			return 0, err
		}
		active := make(map[string]struct{}, len(target.activeCodes))
		for _, code := range target.activeCodes {
			active[code] = struct{}{}
		}
		for i := range mappings {
			if _, ok := active[mappings[i].UpstreamProductCode]; ok {
				continue
			}
			mappings[i].IsActive = false
			mappings[i].UpstreamStatus = models.UpstreamStatusInactive
			if err := s.mappingRepo.Update(&mappings[i]); err != nil {
				return 0, err
			}
			if err := s.deactivateMappedProduct(&mappings[i]); err != nil {
				return 0, err
			}
			total++
		}
	}
	return total, nil
}

func providerCatalogCodes(items []upstream.ProviderCatalogItem) []string {
	codes := make([]string, 0, len(items))
	for _, item := range items {
		codes = append(codes, item.Code)
	}
	return codes
}

func (s *ProductMappingService) recordProviderCatalogSyncRun(startedAt time.Time, fansServices []upstream.FansGurusService, tgxItems []upstream.TGXCommodity, result *ProviderCatalogSyncResult, syncErr error) {
	if s == nil || s.syncRunRepo == nil {
		return
	}
	status := "success"
	errMsg := ""
	if syncErr != nil {
		status = "failed"
		errMsg = syncErr.Error()
	}
	run := &models.ProviderCatalogSyncRun{
		Status:           status,
		FansGurusPulled:  len(fansServices),
		TGXPulled:        len(tgxItems),
		RawFansGurusJSON: mustJSONMap("services", fansServices),
		RawTGXJSON:       mustJSONMap("items", tgxItems),
		ErrorMessage:     errMsg,
		StartedAt:        startedAt,
		FinishedAt:       time.Now(),
	}
	if result != nil {
		run.Imported = result.Imported
		run.Skipped = result.Skipped
		run.Deactivated = result.Deactivated
		run.FilteredTelegram = result.FilteredTelegram
		run.FilteredInactive = result.FilteredInactive
		run.FilteredPlatform = result.FilteredPlatform
		run.SupportedJSON = mustJSONMap("platforms", result.SupportedPlatforms)
	}
	_ = s.syncRunRepo.Create(run)
}

func mustJSONMap(key string, value interface{}) models.JSON {
	bytes, err := json.Marshal(value)
	if err != nil {
		return models.JSON{key: []interface{}{}}
	}
	var decoded interface{}
	if err := json.Unmarshal(bytes, &decoded); err != nil {
		return models.JSON{key: []interface{}{}}
	}
	return models.JSON{key: decoded}
}
