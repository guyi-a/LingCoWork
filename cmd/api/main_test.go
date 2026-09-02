package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestHealthz(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	registerHealthz(r)

	request := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	response := httptest.NewRecorder()
	r.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusOK)
	}
	if body := response.Body.String(); body != `{"status":"ok"}` {
		t.Fatalf("body = %q, want health JSON", body)
	}
}

func TestCORSMiddleware(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(corsMiddleware())
	registerHealthz(r)

	for _, test := range []struct {
		name       string
		origin     string
		wantOrigin string
	}{
		{name: "packaged app", origin: "lingcowork://app", wantOrigin: "lingcowork://app"},
		{name: "vite dev", origin: "http://localhost:5173", wantOrigin: "http://localhost:5173"},
		{name: "browser extension", origin: "chrome-extension://ggnaffooacplgigkdjgmakggbbhjdcfj", wantOrigin: "chrome-extension://ggnaffooacplgigkdjgmakggbbhjdcfj"},
		{name: "unknown origin", origin: "https://example.com", wantOrigin: ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "/healthz", nil)
			request.Header.Set("Origin", test.origin)
			response := httptest.NewRecorder()
			r.ServeHTTP(response, request)

			if got := response.Header().Get("Access-Control-Allow-Origin"); got != test.wantOrigin {
				t.Fatalf("allow origin = %q, want %q", got, test.wantOrigin)
			}
		})
	}
}

func TestConfigureRuntimeHome(t *testing.T) {
	originalDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(originalDir); err != nil {
			t.Errorf("restore cwd: %v", err)
		}
	})

	home := filepath.Join(t.TempDir(), "Application Support", "LingCoWork")
	t.Setenv("LINGCOWORK_HOME", home)

	got, err := configureRuntimeHome()
	if err != nil {
		t.Fatal(err)
	}
	if got != home {
		t.Fatalf("runtime home = %q, want %q", got, home)
	}
	canonicalHome, err := filepath.EvalSymlinks(home)
	if err != nil {
		t.Fatal(err)
	}
	if cwd, err := os.Getwd(); err != nil || cwd != canonicalHome {
		t.Fatalf("cwd = %q, err = %v, want %q", cwd, err, canonicalHome)
	}
}
