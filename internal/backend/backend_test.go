package backend

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRejectInvalidRequestsBeforeStorage(t *testing.T) {
	h := (API{}).Handler()
	for _, body := range []string{`{}`, `{"samples":[]}`, `{} {}`, `{"unknown":true}`} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("POST", "/api/v1/telemetry/batches", strings.NewReader(body)))
		if w.Code != 400 {
			t.Fatalf("body %s: status %d", body, w.Code)
		}
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/nodes/n/metrics", nil))
	if w.Code != 400 {
		t.Fatalf("unbounded query: %d", w.Code)
	}
}
