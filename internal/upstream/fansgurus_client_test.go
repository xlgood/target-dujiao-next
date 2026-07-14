package upstream

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestFansGurusClientGetBalance(t *testing.T) {
	server := newFansGurusTestServer(t, func(r *http.Request) interface{} {
		assertFansGurusAction(t, r, "balance")
		return map[string]string{"balance": "12.34", "currency": "USD"}
	})
	defer server.Close()

	client := NewFansGurusClient(server.URL, "secret-key")
	balance, err := client.GetBalance(context.Background())
	if err != nil {
		t.Fatalf("GetBalance: %v", err)
	}
	if balance.Balance != "12.34" || balance.Currency != "USD" {
		t.Fatalf("unexpected balance: %+v", balance)
	}
}

func TestFansGurusClientListServices(t *testing.T) {
	server := newFansGurusTestServer(t, func(r *http.Request) interface{} {
		assertFansGurusAction(t, r, "services")
		return []map[string]interface{}{
			{
				"service":  16252,
				"name":     "Instagram Followers",
				"type":     "Default",
				"rate":     "0.30",
				"min":      500,
				"max":      100000,
				"dripfeed": false,
				"refill":   true,
				"cancel":   false,
				"category": "Instagram",
			},
		}
	})
	defer server.Close()

	client := NewFansGurusClient(server.URL, "secret-key")
	services, err := client.ListServices(context.Background())
	if err != nil {
		t.Fatalf("ListServices: %v", err)
	}
	if len(services) != 1 {
		t.Fatalf("service count=%d, want 1", len(services))
	}
	if services[0].Service != 16252 || services[0].Rate != "0.30" || services[0].Min != 500 || services[0].Max != 100000 {
		t.Fatalf("unexpected service: %+v", services[0])
	}
}

func TestFansGurusClientAddOrder(t *testing.T) {
	server := newFansGurusTestServer(t, func(r *http.Request) interface{} {
		assertFansGurusAction(t, r, "add")
		if got := r.FormValue("service"); got != "16252" {
			t.Fatalf("service=%s, want 16252", got)
		}
		if got := r.FormValue("link"); got != "https://example.com/post" {
			t.Fatalf("link=%s", got)
		}
		if got := r.FormValue("quantity"); got != "500" {
			t.Fatalf("quantity=%s, want 500", got)
		}
		if got := r.FormValue("runs"); got != "2" {
			t.Fatalf("runs=%s, want 2", got)
		}
		return map[string]interface{}{"order": 123456, "charge": "0.15"}
	})
	defer server.Close()

	client := NewFansGurusClient(server.URL, "secret-key")
	resp, err := client.AddOrder(context.Background(), FansGurusAddOrderRequest{
		Service:  16252,
		Link:     "https://example.com/post",
		Quantity: 500,
		Runs:     2,
	})
	if err != nil {
		t.Fatalf("AddOrder: %v", err)
	}
	if resp.Order != 123456 || resp.Charge != "0.15" {
		t.Fatalf("unexpected add response: %+v", resp)
	}
}

func TestFansGurusClientGetOrderStatus(t *testing.T) {
	server := newFansGurusTestServer(t, func(r *http.Request) interface{} {
		assertFansGurusAction(t, r, "status")
		if got := r.FormValue("order"); got != "123456" {
			t.Fatalf("order=%s, want 123456", got)
		}
		return map[string]string{
			"status":      "Completed",
			"charge":      "0.15",
			"start_count": "100",
			"remains":     "0",
			"currency":    "USD",
		}
	})
	defer server.Close()

	client := NewFansGurusClient(server.URL, "secret-key")
	status, err := client.GetOrderStatus(context.Background(), 123456)
	if err != nil {
		t.Fatalf("GetOrderStatus: %v", err)
	}
	if status.Status != "Completed" || status.Remains != "0" {
		t.Fatalf("unexpected status: %+v", status)
	}
}

func TestFansGurusClientClassifiesAndRedactsAPIError(t *testing.T) {
	server := newFansGurusTestServer(t, func(r *http.Request) interface{} {
		assertFansGurusAction(t, r, "balance")
		return map[string]string{"error": "Invalid API key secret-key"}
	})
	defer server.Close()

	client := NewFansGurusClient(server.URL, "secret-key")
	_, err := client.GetBalance(context.Background())
	if !errors.Is(err, ErrFansGurusAuth) {
		t.Fatalf("expected auth error, got %v", err)
	}
	if strings.Contains(err.Error(), "secret-key") {
		t.Fatalf("error leaked api key: %v", err)
	}
}

func TestFansGurusClientBadJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`not-json`))
	}))
	defer server.Close()

	client := NewFansGurusClient(server.URL, "secret-key")
	_, err := client.GetBalance(context.Background())
	if !errors.Is(err, ErrFansGurusBadJSON) {
		t.Fatalf("expected bad json error, got %v", err)
	}
}

func TestFansGurusClientEmptyServices(t *testing.T) {
	server := newFansGurusTestServer(t, func(r *http.Request) interface{} {
		assertFansGurusAction(t, r, "services")
		return []interface{}{}
	})
	defer server.Close()

	client := NewFansGurusClient(server.URL, "secret-key")
	_, err := client.ListServices(context.Background())
	if !errors.Is(err, ErrFansGurusEmptyResult) {
		t.Fatalf("expected empty result error, got %v", err)
	}
}

func TestFansGurusClientNetworkError(t *testing.T) {
	client := NewFansGurusClient("http://127.0.0.1:1", "secret-key", WithFansGurusHTTPClient(&http.Client{
		Timeout: 10 * time.Millisecond,
	}))
	_, err := client.GetBalance(context.Background())
	if err == nil {
		t.Fatal("expected network error")
	}
}

func newFansGurusTestServer(t *testing.T, handler func(*http.Request) interface{}) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("method=%s, want POST", r.Method)
		}
		if err := r.ParseForm(); err != nil {
			t.Fatalf("ParseForm: %v", err)
		}
		if got := r.FormValue("key"); got != "secret-key" {
			t.Fatalf("key=%s, want secret-key", got)
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(handler(r)); err != nil {
			t.Fatalf("encode response: %v", err)
		}
	}))
}

func assertFansGurusAction(t *testing.T, r *http.Request, want string) {
	t.Helper()
	if got := r.FormValue("action"); got != want {
		t.Fatalf("action=%s, want %s", got, want)
	}
}
