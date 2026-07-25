package privateweb

import (
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

func TestWriteDashboardPreview(t *testing.T) {
	dir := os.Getenv("PREVIEW_DIR")
	if dir == "" {
		t.Skip("PREVIEW_DIR is not set")
	}
	deps, _ := testDependencies(Session{UserID: "admin-1", DisplayName: "Dan Guns", Role: RoleOwner, Active: true})
	w := httptest.NewRecorder()
	newHandler(t, deps).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/admin", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	if err := os.WriteFile(dir+"/m-dash.html", w.Body.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}
