package service

import (
	"context"
	"strings"
	"testing"

	"github.com/dujiao-next/internal/models"
	"github.com/dujiao-next/internal/repository"
	"github.com/dujiao-next/internal/upstream"
)

type fakeFansGurusCatalogContentClient struct {
	details []upstream.FansGurusCatalogDetail
	err     error
}

type fakeTGXCatalogItemClient struct {
	items    []upstream.TGXCommodity
	profiles map[string]upstream.TGXCommodity
}

func (c fakeTGXCatalogItemClient) ListItems(context.Context) (*upstream.TGXItemsResponse, error) {
	return &upstream.TGXItemsResponse{Items: c.items}, nil
}

func (c fakeTGXCatalogItemClient) GetItem(_ context.Context, sharedCode string) (*upstream.TGXCommodity, error) {
	item, ok := c.profiles[sharedCode]
	if !ok {
		return nil, nil
	}
	return &item, nil
}

func (c fakeFansGurusCatalogContentClient) ListCatalogDetails(context.Context) ([]upstream.FansGurusCatalogDetail, error) {
	return c.details, c.err
}

func TestProviderCatalogContentSyncSanitizesFansGurusAndTGXCopy(t *testing.T) {
	db := setupProviderCatalogImportDB(t)
	if err := db.AutoMigrate(&models.ProviderCatalogContentSyncRun{}); err != nil {
		t.Fatalf("auto migrate content sync run: %v", err)
	}
	svc := NewProductMappingService(
		repository.NewProductMappingRepository(db), repository.NewSKUMappingRepository(db),
		repository.NewProductRepository(db), repository.NewProductSKURepository(db),
		repository.NewCategoryRepository(db), nil,
	)
	svc.SetProviderCatalogContentSyncRunRepository(repository.NewProviderCatalogContentSyncRunRepository(db))
	catalog := upstream.FilteredCatalog{
		FansGurus: []upstream.ProviderCatalogItem{{Provider: upstream.CatalogProviderFansGurus, Code: "14266", Name: "Instagram Followers", Category: "Instagram", UpstreamPrice: "1", TargetPrice: "1", Active: true}},
		TGX:       []upstream.ProviderCatalogItem{{Provider: upstream.CatalogProviderTGX, Code: "TGX-1", Name: "Outlook account", Category: "Outlook", UpstreamPrice: "1", TargetPrice: "1", Active: true}},
	}
	if _, err := svc.ImportProviderCatalogByProviderConnections(map[string]uint{upstream.CatalogProviderFansGurus: 10, upstream.CatalogProviderTGX: 20}, catalog); err != nil {
		t.Fatalf("import catalog: %v", err)
	}
	result, err := svc.SyncProviderCatalogContentWithClients(context.Background(), ProviderCatalogContentSyncInput{FansGurusConnectionID: 10, TGXConnectionID: 20},
		fakeFansGurusCatalogContentClient{details: []upstream.FansGurusCatalogDetail{{Service: 14266, Name: "Instagram Followers", Category: "Instagram 推荐", AverageTime: "4 hours", Description: "Quality: real<br>Source: our platform<br>Contact support at https://source.example/help<br>Start: 0-6 hours"}}},
		fakeTGXCatalogClient{items: []upstream.TGXCommodity{{Code: "TGX-1", Name: "Outlook account", Category: "Outlook", Description: "Account format: email----password<br>Contact platform merchant: https://tgx.example/help<br>Use Outlook after delivery", Price: "1"}}},
	)
	if err != nil {
		t.Fatalf("sync content: %v", err)
	}
	if result.Matched != 2 || result.Updated != 2 {
		t.Fatalf("result=%+v", result)
	}
	var mappings []models.ProductMapping
	if err := db.Order("provider ASC").Find(&mappings).Error; err != nil {
		t.Fatalf("mappings: %v", err)
	}
	if len(mappings) != 2 || mappings[0].CatalogSourceDescription == "" || mappings[1].CatalogSourceDescription == "" {
		t.Fatalf("source snapshots missing: %+v", mappings)
	}
	for _, mapping := range mappings {
		var product models.Product
		if err := db.First(&product, mapping.LocalProductID).Error; err != nil {
			t.Fatalf("product: %v", err)
		}
		content := product.ContentJSON["zh-CN"].(string)
		lower := strings.ToLower(content)
		for _, forbidden := range []string{"source.example", "tgx.example", "platform merchant", "our platform", "contact support", "供应商", "上游"} {
			if strings.Contains(lower, forbidden) {
				t.Fatalf("customer content leaked %q: %s", forbidden, content)
			}
		}
	}
	var run models.ProviderCatalogContentSyncRun
	if err := db.First(&run).Error; err != nil || run.Status != "success" || run.Updated != 2 {
		t.Fatalf("run=%+v err=%v", run, err)
	}
}

func TestProviderCatalogSyncKeepsSynchronizedContent(t *testing.T) {
	db := setupProviderCatalogImportDB(t)
	svc := NewProductMappingService(
		repository.NewProductMappingRepository(db), repository.NewSKUMappingRepository(db),
		repository.NewProductRepository(db), repository.NewProductSKURepository(db),
		repository.NewCategoryRepository(db), nil,
	)
	catalog := upstream.FilteredCatalog{FansGurus: []upstream.ProviderCatalogItem{{Provider: upstream.CatalogProviderFansGurus, Code: "14266", Name: "Instagram Followers", Category: "Instagram", UpstreamPrice: "1", TargetPrice: "1", Active: true}}}
	if _, err := svc.ImportProviderCatalogByProviderConnections(map[string]uint{upstream.CatalogProviderFansGurus: 10}, catalog); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SyncProviderCatalogContentWithClients(context.Background(), ProviderCatalogContentSyncInput{FansGurusConnectionID: 10}, fakeFansGurusCatalogContentClient{details: []upstream.FansGurusCatalogDetail{{Service: 14266, Name: "Instagram Followers", Description: "Verified detail"}}}, fakeTGXCatalogClient{}); err != nil {
		t.Fatal(err)
	}
	catalog.FansGurus[0].Name = "Instagram Followers Updated"
	if _, err := svc.ImportProviderCatalogByProviderConnections(map[string]uint{upstream.CatalogProviderFansGurus: 10}, catalog); err != nil {
		t.Fatal(err)
	}
	var product models.Product
	if err := db.First(&product).Error; err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(product.ContentJSON["zh-CN"].(string), "Verified detail") {
		t.Fatalf("catalog sync overwrote enriched content: %v", product.ContentJSON)
	}
}

func TestProviderCatalogCustomerContentUsesServiceTypeForCustomComments(t *testing.T) {
	content, _ := providerCatalogCustomerContent(providerCatalogContentSource{
		Provider:    upstream.CatalogProviderFansGurus,
		Name:        "Instagram comments",
		ServiceType: "Custom Comments",
		Description: "Quality: real",
	})
	zhCN, _ := content["zh-CN"].(string)
	if !strings.Contains(zhCN, "每行填写一条评论") {
		t.Fatalf("custom comments instruction missing: %s", zhCN)
	}
}

func TestSanitizeProviderCatalogLinesDropsExternalURLLines(t *testing.T) {
	lines := sanitizeProviderCatalogLines("Quality: real<br>Proof: https://example.com/image.jpg<br>Start: 0-6 hours")
	joined := strings.Join(lines, "\n")
	if strings.Contains(joined, "Proof") || strings.Contains(joined, "example.com") {
		t.Fatalf("external URL line leaked: %q", joined)
	}
	if !strings.Contains(joined, "Quality: real") || !strings.Contains(joined, "Start: 0-6 hours") {
		t.Fatalf("safe lines lost: %q", joined)
	}
}

func TestProviderCatalogContentRemovesExternalToolWorkflow(t *testing.T) {
	content, _ := providerCatalogCustomerContent(providerCatalogContentSource{
		Provider:    upstream.CatalogProviderTGX,
		Description: "默认发货心蓝格式：邮箱----密码----clientid----授权码<br>心蓝邮箱助手是付费软件（69元一年），可批量登录开通了 pop、imap 之类的邮箱<br>下载地址：https://www.bhdata.com/soft/list.html<br>步骤一：登陆心蓝邮箱助手，点击工具栏导入<br>步骤2：一行一个，确保导入时正确解析<br>账号可通过 OAuth2 登录",
	})
	zhCN, _ := content["zh-CN"].(string)
	for _, forbidden := range []string{"心蓝", "69元", "bhdata", "步骤一", "步骤2", "clientid", "授权码"} {
		if strings.Contains(zhCN, forbidden) {
			t.Fatalf("external tool workflow leaked %q: %s", forbidden, zhCN)
		}
	}
	for _, expected := range []string{"交付内容：邮箱地址、密码、客户端标识、授权信息。", "使用方式：请使用支持 OAuth2 登录的邮件客户端，通过 POP/IMAP 配置邮箱。"} {
		if !strings.Contains(zhCN, expected) {
			t.Fatalf("safe replacement missing %q: %s", expected, zhCN)
		}
	}
}

func TestProviderCatalogContentKeepsTutorialLinkButDropsDownloadLink(t *testing.T) {
	content, _ := providerCatalogCustomerContent(providerCatalogContentSource{
		Provider:    upstream.CatalogProviderTGX,
		Description: "token登录教程：https://2fa.free/jc/Twitter/11<br>下载地址：https://example.com/download<br>账号格式：账号----密码",
	})
	zhCN, _ := content["zh-CN"].(string)
	if !strings.Contains(zhCN, `href="https://2fa.free/jc/Twitter/11"`) || !strings.Contains(zhCN, "查看教程") {
		t.Fatalf("tutorial link missing: %s", zhCN)
	}
	for _, forbidden := range []string{"example.com", "下载地址", "https://2fa.free/jc/Twitter/11" + "</p>"} {
		if strings.Contains(zhCN, forbidden) {
			t.Fatalf("unexpected raw or download URL content %q: %s", forbidden, zhCN)
		}
	}
	if !strings.Contains(zhCN, "账号格式") {
		t.Fatalf("safe detail missing: %s", zhCN)
	}
}

func TestProviderCatalogContentSyncSkipsInactiveMappings(t *testing.T) {
	db := setupProviderCatalogImportDB(t)
	svc := NewProductMappingService(
		repository.NewProductMappingRepository(db), repository.NewSKUMappingRepository(db),
		repository.NewProductRepository(db), repository.NewProductSKURepository(db),
		repository.NewCategoryRepository(db), nil,
	)
	catalog := upstream.FilteredCatalog{FansGurus: []upstream.ProviderCatalogItem{{Provider: upstream.CatalogProviderFansGurus, Code: "14266", Name: "Instagram Followers", Category: "Instagram", UpstreamPrice: "1", TargetPrice: "1", Active: true}}}
	if _, err := svc.ImportProviderCatalogByProviderConnections(map[string]uint{upstream.CatalogProviderFansGurus: 10}, catalog); err != nil {
		t.Fatal(err)
	}
	var mapping models.ProductMapping
	if err := db.First(&mapping).Error; err != nil {
		t.Fatal(err)
	}
	mapping.IsActive = false
	if err := db.Save(&mapping).Error; err != nil {
		t.Fatal(err)
	}
	result, err := svc.SyncProviderCatalogContentWithClients(context.Background(), ProviderCatalogContentSyncInput{FansGurusConnectionID: 10}, fakeFansGurusCatalogContentClient{details: []upstream.FansGurusCatalogDetail{{Service: 14266, Name: "Instagram Followers", Description: "Should not apply"}}}, fakeTGXCatalogClient{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Updated != 0 || result.Skipped != 1 {
		t.Fatalf("result=%+v", result)
	}
}

func TestProviderCatalogContentSyncUsesTGXProfilePerMapping(t *testing.T) {
	db := setupProviderCatalogImportDB(t)
	if err := db.AutoMigrate(&models.ProviderCatalogContentSyncRun{}); err != nil {
		t.Fatal(err)
	}
	svc := NewProductMappingService(
		repository.NewProductMappingRepository(db), repository.NewSKUMappingRepository(db),
		repository.NewProductRepository(db), repository.NewProductSKURepository(db),
		repository.NewCategoryRepository(db), nil,
	)
	svc.SetProviderCatalogContentSyncRunRepository(repository.NewProviderCatalogContentSyncRunRepository(db))
	first, err := upstream.NewTGXCatalogItem(upstream.TGXCommodity{Code: "PROFILE-A", Name: "Outlook item A", Price: "1", ContactType: "2", DeliveryWay: "1"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := upstream.NewTGXCatalogItem(upstream.TGXCommodity{Code: "PROFILE-B", Name: "Outlook item B", Price: "1", ContactType: "1", DeliveryWay: "0"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ImportProviderCatalogByProviderConnections(map[string]uint{upstream.CatalogProviderTGX: 20}, upstream.FilteredCatalog{TGX: []upstream.ProviderCatalogItem{first, second}}); err != nil {
		t.Fatal(err)
	}
	result, err := svc.SyncProviderCatalogContentWithClients(context.Background(), ProviderCatalogContentSyncInput{TGXConnectionID: 20},
		fakeFansGurusCatalogContentClient{},
		fakeTGXCatalogItemClient{
			items: []upstream.TGXCommodity{{Code: "PROFILE-A", Name: "Outlook item A", Price: "1"}, {Code: "PROFILE-B", Name: "Outlook item B", Price: "1"}},
			profiles: map[string]upstream.TGXCommodity{
				"PROFILE-A": {Code: "PROFILE-A", Name: "Outlook item A", Price: "1", ContactType: "1", DeliveryWay: "0"},
				"PROFILE-B": {Code: "PROFILE-B", Name: "Outlook item B", Price: "1", ContactType: "2", DeliveryWay: "1"},
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.TGXProfilePulled != 2 || result.TGXProfileFailed != 0 {
		t.Fatalf("profile result=%+v", result)
	}
	var mappings []models.ProductMapping
	if err := db.Order("upstream_product_code ASC").Find(&mappings).Error; err != nil {
		t.Fatal(err)
	}
	if len(mappings) != 2 || mappings[0].UpstreamFulfillmentType != "auto" || mappings[1].UpstreamFulfillmentType != "manual" {
		t.Fatalf("profile delivery values were not stored per item: %+v", mappings)
	}
	for _, mapping := range mappings {
		var product models.Product
		if err := db.First(&product, mapping.LocalProductID).Error; err != nil {
			t.Fatal(err)
		}
		fields := product.ManualFormSchemaJSON["fields"].([]interface{})
		field := fields[0].(map[string]interface{})
		if mapping.UpstreamProductCode == "PROFILE-A" && field["type"] != "email" {
			t.Fatalf("PROFILE-A type=%v, want email", field["type"])
		}
		if mapping.UpstreamProductCode == "PROFILE-B" && field["type"] != "phone" {
			t.Fatalf("PROFILE-B type=%v, want phone", field["type"])
		}
	}
}
