package metabase

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// datasetServer возвращает заданный ответ на POST /api/dataset.
// На /api/session отдаёт фиктивный токен.
func datasetServer(payload string, status int) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/session" && r.Method == http.MethodPost {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]string{"id": "sess"})
			return
		}
		if r.URL.Path == "/api/dataset" && r.Method == http.MethodPost {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(payload))
			return
		}
		http.NotFound(w, r)
	}))
}

func TestDataset_Completed(t *testing.T) {
	body := `{
		"status": "completed",
		"running_time": 42,
		"data": {
			"cols": [{"name": "id", "base_type": "type/Integer"}],
			"rows": [[1], [2]]
		}
	}`
	srv := datasetServer(body, http.StatusOK)
	defer srv.Close()
	c := newTestClient(srv)

	res, err := c.Dataset(context.Background(), 3, "SELECT id FROM t", 100)
	if err != nil {
		t.Fatalf("Dataset: %v", err)
	}
	if res.Status != "completed" {
		t.Errorf("Status: %q", res.Status)
	}
	if len(res.Data.Rows) != 2 {
		t.Errorf("rows: %d", len(res.Data.Rows))
	}
	if res.RunningTime != 42 {
		t.Errorf("running_time: %d", res.RunningTime)
	}
}

func TestDataset_FailedStringError(t *testing.T) {
	body := `{"status": "failed", "error": "syntax error at line 1"}`
	srv := datasetServer(body, http.StatusOK)
	defer srv.Close()
	c := newTestClient(srv)

	_, err := c.Dataset(context.Background(), 3, "SELECT 1", 100)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "syntax error at line 1") {
		t.Errorf("err should contain message: %v", err)
	}
	if !errors.Is(err, ErrQueryFailed) {
		t.Errorf("err should wrap ErrQueryFailed: %v", err)
	}
}

func TestDataset_FailedObjectError(t *testing.T) {
	body := `{"status": "failed", "error": {"message": "table x not found", "cause": "missing"}}`
	srv := datasetServer(body, http.StatusOK)
	defer srv.Close()
	c := newTestClient(srv)

	_, err := c.Dataset(context.Background(), 3, "SELECT 1", 100)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "table x not found") {
		t.Errorf("err should contain message field: %v", err)
	}
}

func TestDataset_FailedViaChain(t *testing.T) {
	body := `{"status": "failed", "via": [{"message": "wrapped failure"}]}`
	srv := datasetServer(body, http.StatusOK)
	defer srv.Close()
	c := newTestClient(srv)

	_, err := c.Dataset(context.Background(), 3, "SELECT 1", 100)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "wrapped failure") {
		t.Errorf("err should contain via[0].message: %v", err)
	}
}

func TestDataset_FailedNoExtractableError(t *testing.T) {
	body := `{"status": "failed"}`
	srv := datasetServer(body, http.StatusOK)
	defer srv.Close()
	c := newTestClient(srv)

	_, err := c.Dataset(context.Background(), 3, "SELECT 1", 100)
	if err == nil {
		t.Fatal("expected error")
	}
	// Должны получить fallback с упоминанием статуса.
	if !strings.Contains(err.Error(), "failed") {
		t.Errorf("fallback should mention status: %v", err)
	}
}

func TestDataset_AcceptsAny2xx(t *testing.T) {
	body := `{"status": "completed", "data": {"cols": [], "rows": []}}`
	srv := datasetServer(body, http.StatusAccepted) // 202, не 200
	defer srv.Close()
	c := newTestClient(srv)

	res, err := c.Dataset(context.Background(), 3, "SELECT 1", 100)
	if err != nil {
		t.Fatalf("Dataset: %v", err)
	}
	if res.Status != "completed" {
		t.Errorf("expected completed, got %q", res.Status)
	}
}

func TestDataset_BodyShape(t *testing.T) {
	var captured map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/session" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]string{"id": "sess"})
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&captured)
		_, _ = w.Write([]byte(`{"status": "completed", "data": {"cols": [], "rows": []}}`))
	}))
	defer srv.Close()
	c := newTestClient(srv)

	if _, err := c.Dataset(context.Background(), 5, "SELECT 1", 1234); err != nil {
		t.Fatalf("Dataset: %v", err)
	}

	if got := captured["database"]; got != float64(5) {
		t.Errorf("database: %v", got)
	}
	if got := captured["type"]; got != "native" {
		t.Errorf("type: %v", got)
	}
	native, _ := captured["native"].(map[string]any)
	if got := native["query"]; got != "SELECT 1" {
		t.Errorf("native.query: %v", got)
	}
	if _, ok := native["template-tags"].(map[string]any); !ok {
		t.Errorf("native.template-tags should be empty object, got %#v", native["template-tags"])
	}
	constraints, _ := captured["constraints"].(map[string]any)
	if got := constraints["max-results"]; got != float64(1234) {
		t.Errorf("constraints.max-results: %v", got)
	}
	if got := constraints["max-results-bare-rows"]; got != float64(1234) {
		t.Errorf("constraints.max-results-bare-rows: %v", got)
	}
}
