package upstream

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestParseFansGurusCatalogDetails(t *testing.T) {
	page := []byte("<!doctype html><script>let data = [{id:`222057`,name:`Instagram 粉丝（推荐）`,services:[{id:`14266`,name:\"Instagram \\u771f\\u5b9e\\u7c89\\u4e1d\",rate:`$0.748`,min:`100`,max:`1 000 000`,average_time:`4 小时 25 分钟`,description:\"质量：真实<br />\\n📍 来源：我们的平台<br />\\n查看 https:\\/\\/example.com\\/proof<br />\\n开始时间：0-6 小时\",favorite:false}]}];</script>")
	details, err := parseFansGurusCatalogDetails(page)
	if err != nil {
		t.Fatalf("parse catalog: %v", err)
	}
	if len(details) != 1 {
		t.Fatalf("details=%d, want 1", len(details))
	}
	got := details[0]
	if got.Service != 14266 || got.Category != "Instagram 粉丝（推荐）" || got.Min != 100 || got.Max != 1000000 {
		t.Fatalf("unexpected detail: %+v", got)
	}
	if got.Name != "Instagram 真实粉丝" || got.AverageTime != "4 小时 25 分钟" || !strings.Contains(got.Description, "https://example.com/proof") {
		t.Fatalf("decoded fields: %+v", got)
	}
}

func TestFansGurusCatalogDetailsUsesOnePublicRequest(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.Path != "/zh/services" || r.Method != http.MethodGet {
			t.Fatalf("request %s %s, want GET /zh/services", r.Method, r.URL.Path)
		}
		_, _ = w.Write([]byte("<script>let data = [{id:`1`,name:`Instagram`,services:[{id:`14266`,name:`Instagram Followers`,min:`100`,max:`1000`,average_time:`1 hour`,description:`Details`}]}];</script>"))
	}))
	defer server.Close()
	client := NewFansGurusClient(server.URL+"/api/v2", "not-used")
	details, err := client.ListCatalogDetails(context.Background())
	if err != nil {
		t.Fatalf("ListCatalogDetails: %v", err)
	}
	if requests != 1 || len(details) != 1 || details[0].Service != 14266 {
		t.Fatalf("requests=%d details=%+v", requests, details)
	}
}
