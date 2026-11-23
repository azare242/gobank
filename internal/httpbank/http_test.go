package httpbank

import (
	"github.com/azare242/gobank/internal/observability"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMetricsUseRouteTemplate(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /accounts/{id}/balance", func(w http.ResponseWriter, r *http.Request) {
		JSON(w, 202, map[string]any{"pending": true, "decision": "COMMIT"})
	})
	handler := Log("", "", mux)
	for _, id := range []string{"A0001", "A0002"} {
		handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/accounts/"+id+"/balance", nil))
	}
	recorder := httptest.NewRecorder()
	observability.Handler().ServeHTTP(recorder, httptest.NewRequest("GET", "/metrics", nil))
	text := recorder.Body.String()
	if strings.Contains(text, "A0001") || strings.Contains(text, "A0002") || !strings.Contains(text, `route="GET /accounts/{id}/balance",status="202"} 2`) {
		t.Fatal(text)
	}
}
