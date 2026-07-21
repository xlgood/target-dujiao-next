package upstream

import (
	"encoding/json"
	"testing"
)

func TestNormalizePlatformAliases(t *testing.T) {
	cases := []struct {
		name string
		text string
		want string
	}{
		{name: "twitter", text: "Twitter / X Followers", want: "x"},
		{name: "instagram", text: "IG aged accounts", want: "instagram"},
		{name: "tiktok", text: "Tik Tok views", want: "tiktok"},
		{name: "tiktok wins over incidental x", text: "TikTok account with X verification", want: "tiktok"},
		{name: "facebook", text: "FB page likes", want: "facebook"},
		{name: "youtube", text: "YT subscribers", want: "youtube"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := NormalizePlatform(tc.text); got != tc.want {
				t.Fatalf("NormalizePlatform(%q)=%q, want %q", tc.text, got, tc.want)
			}
		})
	}
}

func TestProviderCatalogPlatformPrefersTitle(t *testing.T) {
	cases := []struct {
		name string
		item ProviderCatalogItem
		want string
	}{
		{
			name: "title wins over unrelated category",
			item: ProviderCatalogItem{Name: "Instagram aged account", Category: "Facebook"},
			want: "instagram",
		},
		{name: "gmail takes priority over category", item: ProviderCatalogItem{Name: "Gmail account", Category: "YouTube"}, want: "gmail"},
		{name: "outlook", item: ProviderCatalogItem{Name: "Outlook account"}, want: "outlook"},
		{
			name: "facebook account may mention hotmail verification",
			item: ProviderCatalogItem{Name: "FB aged account with Hotmail verification", Category: "Facebook"},
			want: "facebook",
		},
		{
			name: "category fallback remains supported",
			item: ProviderCatalogItem{Name: "Aged account", Category: "Facebook"},
			want: "facebook",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.item.Platform(); got != tc.want {
				t.Fatalf("Platform()=%q, want %q", got, tc.want)
			}
		})
	}
}

func TestContainsTelegramCatalogText(t *testing.T) {
	cases := []struct {
		name string
		text string
		want bool
	}{
		{name: "telegram", text: "Telegram channel members", want: true},
		{name: "tme", text: "join via https://t.me/example", want: true},
		{name: "chinese", text: "电报群成员", want: true},
		{name: "tg boundary", text: "TG group members", want: true},
		{name: "no tg false positive", text: "instagram followers", want: false},
		{name: "word containing tg", text: "bright growth service", want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ContainsTelegramCatalogText(tc.text); got != tc.want {
				t.Fatalf("ContainsTelegramCatalogText(%q)=%v, want %v", tc.text, got, tc.want)
			}
		})
	}
}

func TestBuildFilteredCatalogUsesIndependentProviderAllowlists(t *testing.T) {
	fans := []ProviderCatalogItem{
		{Provider: CatalogProviderFansGurus, Code: "fg-ig", Name: "Instagram Followers", Category: "Instagram", Active: true},
		{Provider: CatalogProviderFansGurus, Code: "fg-vk", Name: "VK Followers", Category: "VK", Active: true},
		{Provider: CatalogProviderFansGurus, Code: "fg-tg", Name: "Telegram Members", Category: "Telegram", Active: true},
	}
	tgx := []ProviderCatalogItem{
		{Provider: CatalogProviderTGX, Code: "tgx-ig", Name: "IG Account", Description: "Instagram aged account", Active: true},
		{Provider: CatalogProviderTGX, Code: "tgx-mail", Name: "Gmail Account", Active: true},
		{Provider: CatalogProviderTGX, Code: "tgx-tg", Name: "Account", RawText: []string{`{"label":"纸飞机 username"}`}, Active: true},
	}

	result := BuildFilteredCatalog(fans, tgx)

	if len(result.SupportedPlatforms) != 3 {
		t.Fatalf("supported platforms=%v, want gmail, instagram, vk", result.SupportedPlatforms)
	}
	if len(result.FansGurus) != 2 {
		t.Fatalf("fans kept=%+v, want instagram and vk", result.FansGurus)
	}
	if len(result.TGX) != 2 {
		t.Fatalf("tgx kept=%+v, want instagram and gmail", result.TGX)
	}
	if len(result.FilteredTelegram) != 2 {
		t.Fatalf("telegram filtered count=%d, want 2", len(result.FilteredTelegram))
	}
	if len(result.FilteredPlatform) != 0 {
		t.Fatalf("platform filtered count=%d, want 0", len(result.FilteredPlatform))
	}
}

func TestBuildFilteredCatalogFiltersInactiveAndProviderDisallowedPlatforms(t *testing.T) {
	fans := []ProviderCatalogItem{
		{Provider: CatalogProviderFansGurus, Code: "fg-ig", Name: "Instagram Followers", Category: "Instagram", Active: true},
		{Provider: CatalogProviderFansGurus, Code: "fg-fb", Name: "Facebook Likes", Category: "Facebook", Active: false},
	}
	tgx := []ProviderCatalogItem{
		{Provider: CatalogProviderTGX, Code: "tgx-ig", Name: "Instagram Account", Active: true},
		{Provider: CatalogProviderTGX, Code: "tgx-fb", Name: "Facebook Account", Active: true},
	}

	result := BuildFilteredCatalog(fans, tgx)

	if len(result.SupportedPlatforms) != 2 {
		t.Fatalf("supported platforms=%v, want facebook and instagram", result.SupportedPlatforms)
	}
	if len(result.FilteredInactive) != 1 || result.FilteredInactive[0].Code != "fg-fb" {
		t.Fatalf("inactive filtered=%+v, want fg-fb", result.FilteredInactive)
	}
	if len(result.FilteredPlatform) != 0 {
		t.Fatalf("platform filtered=%+v, want none", result.FilteredPlatform)
	}
}

func TestProviderCatalogItemPriceRules(t *testing.T) {
	fansItem, err := NewFansGurusCatalogItem(FansGurusService{
		Service:  123,
		Name:     "Instagram Followers",
		Category: "Instagram",
		Rate:     "2.00",
		Min:      500,
		Max:      10000,
	})
	if err != nil {
		t.Fatalf("NewFansGurusCatalogItem: %v", err)
	}
	if fansItem.Code != "123" {
		t.Fatalf("fans code=%q, want 123", fansItem.Code)
	}
	if fansItem.TargetPrice != "2.00" {
		t.Fatalf("fans target=%s, want raw upstream amount", fansItem.TargetPrice)
	}
	if fansItem.PriceQuantityBasis != 0 {
		t.Fatalf("fans price quantity basis=%d, want unknown", fansItem.PriceQuantityBasis)
	}
	if fansItem.MinQuantity != 500 || fansItem.MaxQuantity != 10000 {
		t.Fatalf("fans quantity range=%d/%d", fansItem.MinQuantity, fansItem.MaxQuantity)
	}

	tgxItem, err := NewTGXCatalogItem(TGXCommodity{
		Code:        "IG-001",
		Name:        "Instagram Account",
		Description: "aged account",
		Price:       "100.00",
		Minimum:     1,
	})
	if err != nil {
		t.Fatalf("NewTGXCatalogItem: %v", err)
	}
	if tgxItem.TargetPrice != "100.00" {
		t.Fatalf("tgx target=%s, want 100.00", tgxItem.TargetPrice)
	}
	if tgxItem.PriceQuantityBasis != 1 {
		t.Fatalf("tgx price quantity basis=%d, want 1", tgxItem.PriceQuantityBasis)
	}
}

func TestFansGurusCatalogItemUsesExplicitQuantityBasis(t *testing.T) {
	service := FansGurusService{Service: 123, Name: "Instagram Followers", Category: "Instagram", Rate: "2.00", Raw: json.RawMessage(`{"service":123,"price_per":"1000"}`)}
	item, err := NewFansGurusCatalogItem(service)
	if err != nil {
		t.Fatalf("NewFansGurusCatalogItem: %v", err)
	}
	if item.PriceQuantityBasis != 1000 {
		t.Fatalf("basis=%d, want 1000", item.PriceQuantityBasis)
	}
}

func TestNewFansGurusCatalogItemEnablesSupportedServiceTypes(t *testing.T) {
	item, err := NewFansGurusCatalogItem(FansGurusService{
		Service:  456,
		Name:     "Instagram Custom Comments",
		Category: "Instagram",
		Type:     "Custom Comments",
		Rate:     "2.00",
	})
	if err != nil {
		t.Fatalf("NewFansGurusCatalogItem: %v", err)
	}
	if !item.Active {
		t.Fatalf("supported FansGurus type must be imported: %+v", item)
	}
}

func TestNewTGXCatalogItemParsesConfigVariantsAndWidget(t *testing.T) {
	item, err := NewTGXCatalogItem(TGXCommodity{
		Code:        "IG-001",
		Name:        "Instagram Account",
		Description: "aged account",
		Price:       "100.00",
		Config:      []byte(`{"category[普通]":"100.00","category[高级]":"150.00"}`),
		Widget:      []byte(`[{"name":"email","label":"Email","type":"text","required":true}]`),
		Minimum:     1,
	})
	if err != nil {
		t.Fatalf("NewTGXCatalogItem: %v", err)
	}
	if len(item.Variants) != 2 {
		t.Fatalf("variant count=%d, want 2: %+v", len(item.Variants), item.Variants)
	}
	if item.Variants[0].Name != "普通" || item.Variants[0].TargetPrice != "100.00" {
		t.Fatalf("unexpected first variant: %+v", item.Variants[0])
	}
	if item.Variants[1].Name != "高级" || item.Variants[1].TargetPrice != "150.00" {
		t.Fatalf("unexpected second variant: %+v", item.Variants[1])
	}
	fields, ok := item.ManualSchema["fields"].([]map[string]interface{})
	if !ok || len(fields) != 1 {
		t.Fatalf("unexpected manual schema: %+v", item.ManualSchema)
	}
	if fields[0]["key"] != "email" || fields[0]["required"] != true {
		t.Fatalf("unexpected field: %+v", fields[0])
	}
}

func TestNewTGXCatalogItemParsesDocumentedStringFields(t *testing.T) {
	item, err := NewTGXCatalogItem(TGXCommodity{
		Code:     "IG-001",
		Category: "Instagram",
		Name:     "Aged account",
		Price:    "100.00",
		Config:   json.RawMessage(`"category[Standard]=100.00\ncategory[Premium]=200.00"`),
		Widget:   json.RawMessage(`"[{\"cn\":\"Email\",\"name\":\"email\",\"type\":\"input\"}]"`),
		Minimum:  1,
	})
	if err != nil {
		t.Fatalf("NewTGXCatalogItem: %v", err)
	}
	if item.Category != "Instagram" || len(item.Variants) != 2 {
		t.Fatalf("unexpected item: %+v", item)
	}
	fields, _ := item.ManualSchema["fields"].([]map[string]interface{})
	label, _ := fields[0]["label"].(map[string]interface{})
	if len(fields) != 1 || label["zh-CN"] != "Email" {
		t.Fatalf("unexpected manual schema: %+v", item.ManualSchema)
	}
}

func TestNewTGXCatalogItemUsesDocumentedCoverSortAndContactType(t *testing.T) {
	item, err := NewTGXCatalogItem(TGXCommodity{
		Code:        "IG-001",
		Name:        "Instagram Account",
		Category:    "Instagram",
		Price:       "100.00",
		Cover:       "https://cdn.tgx.test/cover.jpg",
		Sort:        42,
		ContactType: "1",
	})
	if err != nil {
		t.Fatalf("NewTGXCatalogItem: %v", err)
	}
	if len(item.Images) != 1 || item.Images[0] != "https://cdn.tgx.test/cover.jpg" || item.SortOrder != 42 {
		t.Fatalf("unexpected media and sort: %+v", item)
	}
	fields, _ := item.ManualSchema["fields"].([]map[string]interface{})
	if len(fields) != 1 || fields[0]["key"] != "contact" || fields[0]["type"] != "email" || fields[0]["required"] != true {
		t.Fatalf("unexpected contact schema: %+v", item.ManualSchema)
	}
	if _, ok := fields[0]["label"].(map[string]interface{}); !ok {
		t.Fatalf("contact label must be localized: %+v", fields[0])
	}
}

func TestNewTGXCatalogItemUsesEachItemsDeliveryWay(t *testing.T) {
	autoItem, err := NewTGXCatalogItem(TGXCommodity{Code: "AUTO", Name: "Automatic item", Price: "1", DeliveryWay: "0"})
	if err != nil {
		t.Fatalf("NewTGXCatalogItem auto: %v", err)
	}
	manualItem, err := NewTGXCatalogItem(TGXCommodity{Code: "MANUAL", Name: "Manual item", Price: "1", DeliveryWay: "1"})
	if err != nil {
		t.Fatalf("NewTGXCatalogItem manual: %v", err)
	}
	unknownItem, err := NewTGXCatalogItem(TGXCommodity{Code: "UNKNOWN", Name: "Unknown item", Price: "1", DeliveryWay: "unexpected"})
	if err != nil {
		t.Fatalf("NewTGXCatalogItem unknown: %v", err)
	}
	if autoItem.UpstreamFulfillmentType != "auto" || manualItem.UpstreamFulfillmentType != "manual" || unknownItem.UpstreamFulfillmentType != "manual" {
		t.Fatalf("delivery types must be item-specific and conservative: auto=%q manual=%q unknown=%q", autoItem.UpstreamFulfillmentType, manualItem.UpstreamFulfillmentType, unknownItem.UpstreamFulfillmentType)
	}
}

func TestNewTGXCatalogItemConvertsUnspecifiedSelectToText(t *testing.T) {
	item, err := NewTGXCatalogItem(TGXCommodity{
		Code:   "IG-001",
		Name:   "Instagram Account",
		Price:  "100.00",
		Widget: json.RawMessage(`[{"cn":"区服","name":"server","type":"select"}]`),
	})
	if err != nil {
		t.Fatalf("NewTGXCatalogItem: %v", err)
	}
	fields, _ := item.ManualSchema["fields"].([]map[string]interface{})
	if len(fields) != 1 || fields[0]["type"] != "text" {
		t.Fatalf("unexpected widget schema: %+v", item.ManualSchema)
	}
}

func TestNewTGXCatalogItemNormalizesFacelookTitle(t *testing.T) {
	item, err := NewTGXCatalogItem(TGXCommodity{
		Code:     "FB-001",
		Name:     "Facelook aged account",
		Category: "Facebook",
		Price:    "100.00",
	})
	if err != nil {
		t.Fatalf("NewTGXCatalogItem: %v", err)
	}
	if item.Name != "Facebook aged account" || item.Platform() != "facebook" {
		t.Fatalf("unexpected normalized item: %+v", item)
	}
}
