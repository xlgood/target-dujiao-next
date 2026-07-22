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
	wantSign := hex.EncodeToString(sum[:])
	if got := SignTGX(params, "app-secret"); got != wantSign {
		t.Fatalf("sign=%s, want %s", got, wantSign)
	}
}

func TestTGXSignerPreservesLiteralPlusAfterDocumentedQueryDecoding(t *testing.T) {
	params := url.Values{
		"note":   []string{"A+B"},
		"app_id": []string{"test-app-id"},
	}
	if got, want := BuildTGXSignString(params, "app+secret"), "app_id=test-app-id&note=A+B&key=app+secret"; got != want {
		t.Fatalf("sign string=%q, want %q", got, want)
	}
}

func TestTGXClientConnectAndSignedRequest(t *testing.T) {
	server := newTGXTestServer(t, func(r *http.Request) interface{} {
		assertTGXPath(t, r, "/authentication/connect")
		assertTGXSigned(t, r, "test-app-id", "app-secret")
		return map[string]interface{}{
			"code": 200,
			"data": map[string]string{"shopName": "TGX Shop", "balance": "88.00"},
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

func TestTGXConnectResponseAcceptsNumericBalance(t *testing.T) {
	var response TGXConnectResponse
	if err := json.Unmarshal([]byte(`{"shopName":"TGX Shop","balance":88.5}`), &response); err != nil {
		t.Fatalf("unmarshal numeric balance: %v", err)
	}
	if response.ShopName != "TGX Shop" || response.Balance != "88.5" {
		t.Fatalf("unexpected connect response: %+v", response)
	}
}

func TestTGXClientListItems(t *testing.T) {
	server := newTGXTestServer(t, func(r *http.Request) interface{} {
		assertTGXPath(t, r, "/commodity/items")
		return map[string]interface{}{
			"code": 200,
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

func TestTGXClientGetItemUsesDocumentedSharedCodeParameter(t *testing.T) {
	server := newTGXTestServer(t, func(r *http.Request) interface{} {
		assertTGXPath(t, r, "/commodity/item")
		if got := r.FormValue("shared_code"); got != "IG-001" {
			t.Fatalf("shared_code=%q, want IG-001", got)
		}
		if got := r.FormValue("sharedCode"); got != "" {
			t.Fatalf("legacy sharedCode unexpectedly sent: %q", got)
		}
		return map[string]interface{}{
			"code": 200,
			"data": map[string]interface{}{
				"code": "IG-001", "name": "Aged account", "price": "100.00",
			},
		}
	})
	defer server.Close()

	item, err := NewTGXClient(server.URL, "test-app-id", "app-secret").GetItem(context.Background(), "IG-001")
	if err != nil {
		t.Fatalf("GetItem: %v", err)
	}
	if item.Code != "IG-001" {
		t.Fatalf("item=%+v", item)
	}
}

func TestDecodeTGXCatalogCategoriesFlattensChildren(t *testing.T) {
	payload := []byte(`[{"name":"Instagram","children":[{"id":101,"code":"IG-001","shared_code":"SHARED-001","name":"Aged account","price":100,"user_price":95.5,"factory_price":90,"delivery_way":0,"contact_type":0,"password_status":0,"draft_status":0,"inventory_hidden":0,"minimum":1,"config":"category[Standard]=100.00","widget":"[]"}]}]`)
	items, categories, ok := decodeTGXCatalogCategories(payload)
	if !ok || len(categories) == 0 {
		t.Fatalf("catalog was not recognized: ok=%v categories=%s", ok, categories)
	}
	if len(items) != 1 || items[0].Code != "IG-001" || items[0].Category != "Instagram" {
		t.Fatalf("unexpected flattened items: %+v", items)
	}
	if items[0].Price != "100" || items[0].UserPrice != "95.5" || items[0].DeliveryWay != "0" {
		t.Fatalf("numeric fields were not normalized: %+v", items[0])
	}
}

func TestTGXClientInventoryTradeAndQuery(t *testing.T) {
	server := newTGXTestServer(t, func(r *http.Request) interface{} {
		switch r.URL.Path {
		case "/commodity/inventory":
			if got := r.FormValue("sharedCode"); got != "IG-001" {
				t.Fatalf("sharedCode=%s, want IG-001", got)
			}
			if got := r.FormValue("race"); got != "普通" {
				t.Fatalf("race=%s, want 普通", got)
			}
			return map[string]interface{}{"code": 200, "data": map[string]interface{}{"count": 5, "price": 100}}
		case "/commodity/inventoryState":
			if got := r.FormValue("shared_code"); got != "IG-001" {
				t.Fatalf("shared_code=%s, want IG-001", got)
			}
			if got := r.FormValue("num"); got != "2" {
				t.Fatalf("num=%s, want 2", got)
			}
			return map[string]interface{}{"code": 200, "data": map[string]interface{}{}}
		case "/commodity/trade":
			if got := r.FormValue("num"); got != "2" {
				t.Fatalf("num=%s, want 2", got)
			}
			if got := r.FormValue("request_no"); got != "local-order-1" {
				t.Fatalf("request_no=%s, want local-order-1", got)
			}
			if got := r.FormValue("email"); got != "buyer@example.com" {
				t.Fatalf("email=%s, want buyer@example.com", got)
			}
			return map[string]interface{}{"code": 200, "data": map[string]interface{}{"trade_no": "T202607080001", "secret": "account-secret", "status": 1}}
		case "/commodity/query":
			if got := r.FormValue("tradeNo"); got != "T202607080001" {
				t.Fatalf("tradeNo=%s, want T202607080001", got)
			}
			return map[string]interface{}{"code": 200, "data": map[string]interface{}{"secret": "account-secret", "status": 1, "delivery_status": 1}}
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
	if inventory.Count != 5 || inventory.Price != "100" {
		t.Fatalf("unexpected inventory: %+v", inventory)
	}

	state, err := client.GetInventoryState(context.Background(), "IG-001", "普通", 2)
	if err != nil {
		t.Fatalf("GetInventoryState: %v", err)
	}
	if !state.Available || state.Quantity != 0 {
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
	if query.Status != "1" || query.DeliveryStatus != "1" || query.Secret != "account-secret" {
		t.Fatalf("unexpected query: %+v", query)
	}
}

func TestTGXClientInventoryStateMapsDocumentedInsufficientStock(t *testing.T) {
	server := newTGXTestServer(t, func(r *http.Request) interface{} {
		assertTGXPath(t, r, "/commodity/inventoryState")
		if got := r.FormValue("shared_code"); got != "IG-001" {
			t.Fatalf("shared_code=%q, want IG-001", got)
		}
		if got := r.FormValue("num"); got != "2" {
			t.Fatalf("num=%q, want 2", got)
		}
		return map[string]interface{}{"code": 500, "msg": "库存不足"}
	})
	defer server.Close()

	_, err := NewTGXClient(server.URL, "test-app-id", "app-secret").GetInventoryState(context.Background(), "IG-001", "", 2)
	if !errors.Is(err, ErrTGXStock) {
		t.Fatalf("error=%v, want ErrTGXStock", err)
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

func TestTGXClientRejectsDocumentedCodeZeroError(t *testing.T) {
	server := newTGXTestServer(t, func(r *http.Request) interface{} {
		return map[string]interface{}{"code": 0, "msg": "密钥错误"}
	})
	defer server.Close()

	_, err := NewTGXClient(server.URL, "test-app-id", "app-secret").ListItems(context.Background())
	if !errors.Is(err, ErrTGXBusiness) {
		t.Fatalf("expected business error for code 0, got %v", err)
	}
}

func TestTGXClientClassifiesUnavailableCommodity(t *testing.T) {
	server := newTGXTestServer(t, func(r *http.Request) interface{} {
		assertTGXPath(t, r, "/commodity/inventory")
		return map[string]interface{}{"code": 0, "msg": "该商品暂时缺货，请稍后再来"}
	})
	defer server.Close()

	_, err := NewTGXClient(server.URL, "test-app-id", "app-secret").GetInventory(context.Background(), "TGX-001", "")
	if !errors.Is(err, ErrTGXUnavailable) {
		t.Fatalf("error=%v, want ErrTGXUnavailable", err)
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
	if got := r.FormValue("app_key"); got != appKey {
		t.Fatalf("app_key=%q, want configured key", got)
	}
}
