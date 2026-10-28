package web

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

func TestHandlerServesWorkbenchAndAssets(t *testing.T) {
	h := Handler()
	index := httptest.NewRecorder()
	h.ServeHTTP(index, httptest.NewRequest(http.MethodGet, "/", nil))
	if index.Code != http.StatusOK || !strings.Contains(index.Body.String(), "agentloom") {
		t.Fatalf("index: %d %s", index.Code, index.Body.String())
	}
	path := regexp.MustCompile(`src="([^"]+\.js)"`).FindStringSubmatch(index.Body.String())
	if len(path) != 2 {
		t.Fatal("JavaScript asset missing from index")
	}
	asset := httptest.NewRecorder()
	h.ServeHTTP(asset, httptest.NewRequest(http.MethodGet, path[1], nil))
	if asset.Code != http.StatusOK || !strings.Contains(asset.Header().Get("Content-Type"), "javascript") {
		t.Fatalf("asset: %d %s", asset.Code, asset.Header().Get("Content-Type"))
	}
	if !strings.Contains(asset.Body.String(), "访问/管理员令牌") || !strings.Contains(asset.Body.String(), "代理质量") {
		t.Fatal("workbench token or proxy-quality explanation missing from embedded asset")
	}
}
