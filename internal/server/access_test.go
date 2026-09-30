package server

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAccessLogRedactsSignedCredentials(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	const token = "1234567890.example-bearer-credential"
	h := logged(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", "https://media.example/api/signed/"+token+"/stream/item?t=10")
		w.WriteHeader(http.StatusFound)
	}), log)
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/signed/"+token+"/stream/item?t=10", nil))
	out := buf.String()
	if strings.Contains(out, token) {
		t.Fatal("access log contains a bearer credential")
	}
	if !strings.Contains(out, "/stream/item") || !strings.Contains(out, "redacted") || !strings.Contains(out, `"status":302`) {
		t.Fatalf("log lost the route or response status: %s", out)
	}
}
