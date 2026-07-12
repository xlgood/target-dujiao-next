package upstream

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestTGXSignerSortsDropsEmptyAndDecodes(t *testing.T) {
	params := url.Values{
		"shared_code": []string{"ig account"},
		"race":        []string{"普通"},
		"empty":       []string{""},
		"sign":        []string{"old-sign"},
		"app_id":      []string{"test-app-id"},
	}

	signing := BuildTGXSignString(params, "app-secret")
	wantSigning := "app_id=test-app-id&race=普通&shared_code=ig account&key=app-secret"
	if signing != wantSigning {
		t.Fatalf("signing string=%q, want %q", signing, wantSigning)
	}

	sum := md5.Sum([]byte(wantSigning))
	wantSign := strings.ToUpper(hex.EncodeToString(sum[:]))
	if got := SignTGX(params, "app-secret"); got != wantSign {
		t.Fatalf("sign=%s, want %s", got, wantSign)
	}
}

func TestTGXClientConnectAndSignedRequest(t *testing.T) {
	server := newTGXTestServer(t, func(r *http.Request) interface{} {
		assertTGXPath(t, r, "/authentication/connect")
		assertTGXSigned(t, r, "test-app-id", "app-secret")
		return map[string]interface{}{
			"code": 0,
			"data": map[string]string{"shop_name": "TGX Shop", "balance": "88.00"},
		}
	})
	defer server.Close()

	client := NewTGXClient(server.URL, "test-app-id", "app-secret")
	resp, err := client.Connect(context.Background())
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if resp.ShopName != "TGX Shop" || resp.Balance != "88.00" {
		t.Fatalf("unexpected connect response: %+v", resp)
	}
}

func TestTGXClientListItems(t *testing.T) {
	server := newTGXTestServer(t, func(r *http.Request) interface{} {
		assertTGXPath(t, r, "/commodity/items")
		return map[string]interface{}{
			"code": 0,
			"data": map[string]interface{}{
				"items": []map[string]interface{}{
					{
						"code":           "IG-001",
						"name":           "Instagram Account",
						"description":    "aged account",
						"price":          "100.00",
						"user_price":     "120.00",
						"factory_price":  "80.00",
						"delivery_way":   "auto",
						"config":         json.RawMessage(`{"category[普通]":"100.00"}`),
						"widget":         json.RawMessage(`[{"name":"email","label":"Email"}]`),
						"minimum":        1,
						"purchase_count": 3,
					},
				},
			},
		}
	})
	defer server.Close()

	client := NewTGXClient(server.URL, "test-app-id", "app-secret")
	resp, err := client.ListItems(context.Background())
	if err != nil {
		t.Fatalf("ListItems: %v", err)
	}
	if len(resp.Items) != 1 || resp.Items[0].Code != "IG-001" || resp.Items[0].Price != "100.00" {
		t.Fatalf("unexpected items response: %+v", resp)
	}
}

func TestTGXClientListItemsFlattensDocumentedCategories(t *testing.T) {
	server := newTGXTestServer(t, func(r *http.Request) interface{} {
		assertTGXPath(t, r, "/commodity/items")
		if got := r.FormValue("app_key"); got != "app-secret" {
			t.Fatalf("app_key=%q, want app-secret", got)
		}
		return map[string]interface{}{
			"code": 200,
			"data": []map[string]interface{}{
				{
					"name": "Instagram",
					"children": []map[string]interface{}{
						{"code": "IG-001", "name": "Aged account", "price": "100.00", "minimum": 1},
					},
				},
			},
		}
	})
	defer server.Close()

	resp, err := NewTGXClient(server.URL, "test-app-id", "app-secret").ListItems(context.Background())
	if err != nil {
		t.Fatalf("ListItems: %v", err)
	}
	if len(resp.Items) != 1 || resp.Items[0].Code != "IG-001" || resp.Items[0].Category != "Instagram" {
		t.Fatalf("unexpected category catalog response: %+v", resp)
	}
}

func TestDecodeTGXCatalogCategoriesFlattensChildren(t *testing.T) {
	payload := []byte(`[{"name":"Instagram","children":[{"code":"IG-001","name":"Aged account","price":"100.00"}]}]`)
	items, categories, ok := decodeTGXCatalogCategories(payload)
	if !ok || len(categories) == 0 {
		t.Fatalf("catalog was not recognized: ok=%v categories=%s", ok, categories)
	}
	if len(items) != 1 || items[0].Code != "IG-001" || items[0].Category != "Instagram" {
		t.Fatalf("unexpected flattened items: %+v", items)
	}
}

func TestTGXClientInventoryTradeAndQuery(t *testing.T) {
	server := newTGXTestServer(t, func(r *http.Request) interface{} {
		switch r.URL.Path {
		case "/commodity/inventory":
			if got := r.FormValue("shared_code"); got != "IG-001" {
				t.Fatalf("shared_code=%s, want IG-001", got)
			}
			if got := r.FormValue("race"); got != "普通" {
				t.Fatalf("race=%s, want 普通", got)
			}
			return map[string]interface{}{"code": 0, "data": map[string]interface{}{"code": "IG-001", "race": "普通", "price": "100.00", "stock": 5}}
		case "/commodity/inventoryState":
			if got := r.FormValue("quantity"); got != "2" {
				t.Fatalf("quantity=%s, want 2", got)
			}
			return map[string]interface{}{"code": 0, "data": map[string]interface{}{"available": true, "quantity": 2}}
		case "/commodity/trade":
			if got := r.FormValue("request_no"); got != "local-order-1" {
				t.Fatalf("request_no=%s, want local-order-1", got)
			}
			if got := r.FormValue("email"); got != "buyer@example.com" {
				t.Fatalf("email=%s, want buyer@example.com", got)
			}
			return map[string]interface{}{"code": 0, "data": map[string]string{"trade_no": "T202607080001", "secret": "account-secret", "status": "completed"}}
		case "/commodity/query":
			if got := r.FormValue("trade_no"); got != "T202607080001" {
				t.Fatalf("trade_no=%s, want T202607080001", got)
			}
			return map[string]interface{}{"code": 0, "data": map[string]string{"trade_no": "T202607080001", "secret": "account-secret", "status": "completed"}}
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		return nil
	})
	defer server.Close()

	client := NewTGXClient(server.URL, "test-app-id", "app-secret")
	inventory, err := client.GetInventory(context.Background(), "IG-001", "普通")
	if err != nil {
		t.Fatalf("GetInventory: %v", err)
	}
	if inventory.Stock != 5 || inventory.Price != "100.00" {
		t.Fatalf("unexpected inventory: %+v", inventory)
	}

	state, err := client.GetInventoryState(context.Background(), "IG-001", "普通", 2)
	if err != nil {
		t.Fatalf("GetInventoryState: %v", err)
	}
	if !state.Available || state.Quantity != 2 {
		t.Fatalf("unexpected inventory state: %+v", state)
	}

	trade, err := client.Trade(context.Background(), TGXTradeRequest{
		SharedCode: "IG-001",
		Race:       "普通",
		Quantity:   2,
		RequestNo:  "local-order-1",
		Widget:     map[string]string{"email": "buyer@example.com"},
	})
	if err != nil {
		t.Fatalf("Trade: %v", err)
	}
	if trade.TradeNo != "T202607080001" || trade.Secret != "account-secret" {
		t.Fatalf("unexpected trade: %+v", trade)
	}

	query, err := client.QueryTrade(context.Background(), trade.TradeNo)
	if err != nil {
		t.Fatalf("QueryTrade: %v", err)
	}
	if query.Status != "completed" || query.Secret != "account-secret" {
		t.Fatalf("unexpected query: %+v", query)
	}
}

func TestTGXClientQueryTradeByRequestNo(t *testing.T) {
	server := newTGXTestServer(t, func(r *http.Request) interface{} {
		assertTGXPath(t, r, "/commodity/query")
		if got := r.FormValue("request_no"); got != "local-order-1" {
			t.Fatalf("request_no=%s, want local-order-1", got)
		}
		if r.FormValue("trade_no") != "" {
			t.Fatal("trade_no must not be sent when querying by request_no")
		}
		return map[string]interface{}{"code": 0, "data": map[string]string{"trade_no": "T202607080001", "status": "pending"}}
	})
	defer server.Close()

	client := NewTGXClient(server.URL, "test-app-id", "app-secret")
	query, err := client.QueryTradeByRequestNo(context.Background(), "local-order-1")
	if err != nil {
		t.Fatalf("QueryTradeByRequestNo: %v", err)
	}
	if query.TradeNo != "T202607080001" || query.Status != "pending" {
		t.Fatalf("unexpected query: %+v", query)
	}
}

func TestTGXTargetPrice(t *testing.T) {
	got, err := TGXTargetPrice("100.00")
	if err != nil {
		t.Fatalf("TGXTargetPrice: %v", err)
	}
	if got != "120.00000000" {
		t.Fatalf("target price=%s, want 120.00000000", got)
	}
}

func TestTGXClientRedactsAppKeyInErrors(t *testing.T) {
	server := newTGXTestServer(t, func(r *http.Request) interface{} {
		return map[string]string{"code": "401", "msg": "invalid app key app-secret"}
	})
	defer server.Close()

	client := NewTGXClient(server.URL, "test-app-id", "app-secret")
	_, err := client.Connect(context.Background())
	if !errors.Is(err, ErrTGXAuth) {
		t.Fatalf("expected auth error, got %v", err)
	}
	if strings.Contains(err.Error(), "app-secret") {
		t.Fatalf("error leaked app key: %v", err)
	}
}

func TestTGXClientBadJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`not-json`))
	}))
	defer server.Close()

	client := NewTGXClient(server.URL, "test-app-id", "app-secret")
	_, err := client.Connect(context.Background())
	if !errors.Is(err, ErrTGXBadJSON) {
		t.Fatalf("expected bad json error, got %v", err)
	}
}

func newTGXTestServer(t *testing.T, handler func(*http.Request) interface{}) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("method=%s, want POST", r.Method)
		}
		if err := r.ParseForm(); err != nil {
			t.Fatalf("ParseForm: %v", err)
		}
		if got := r.FormValue("app_id"); got != "test-app-id" {
			t.Fatalf("app_id=%s, want test-app-id", got)
		}
		assertTGXSigned(t, r, "test-app-id", "app-secret")
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(handler(r)); err != nil {
			t.Fatalf("encode response: %v", err)
		}
	}))
}

func assertTGXPath(t *testing.T, r *http.Request, want string) {
	t.Helper()
	if r.URL.Path != want {
		t.Fatalf("path=%s, want %s", r.URL.Path, want)
	}
}

func assertTGXSigned(t *testing.T, r *http.Request, appID, appKey string) {
	t.Helper()
	if got := r.FormValue("app_id"); got != appID {
		t.Fatalf("app_id=%s, want %s", got, appID)
	}
	params := cloneURLValues(r.PostForm)
	got := params.Get("sign")
	if got == "" {
		t.Fatal("missing sign")
	}
	want := SignTGX(params, appKey)
	if got != want {
		t.Fatalf("sign=%s, want %s", got, want)
	}
	if r.FormValue("app_key") != "" {
		t.Fatal("app_key must not be sent in request body")
	}
}
