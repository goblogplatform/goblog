package main

import (
	"encoding/json"
	"html/template"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

// Both wizard database handlers used to return nothing when ParseForm failed,
// which gin turns into a bare 200 with an empty body (#632). For /test_db that
// is worse than useless: the wizard's jQuery call has no dataType, so an empty
// 200 takes the success path and the Test Database button goes green on a
// request the server never looked at.

// malformedForm is a body ParseForm rejects: %zz is not valid percent-encoding.
func malformedForm(t *testing.T, path string) *http.Request {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader("database=%zz"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return req
}

func TestTestDB_MalformedFormIsNotReportedAsSuccess(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.POST("/test_db", testDB)

	w := httptest.NewRecorder()
	router.ServeHTTP(w, malformedForm(t, "/test_db"))

	if w.Code == http.StatusOK {
		t.Fatalf("code = 200 for a form the server could not read; the wizard shows that as a passing database test")
	}
	if w.Code != http.StatusBadRequest {
		t.Errorf("code = %d, want 400", w.Code)
	}
	// The wizard reads .error out of the body, so it has to be JSON with that key.
	var body map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("body is not JSON (%v): %q", err, w.Body.String())
	}
	if body["error"] == "" {
		t.Errorf("body has no error to show the user: %q", w.Body.String())
	}
}

// TestUpdateDB_MalformedFormRendersTheWizardAgain: /wizard_db answers with a
// page rather than JSON, and it already has a fail() helper that re-renders
// the wizard with the reason on it. The ParseForm branch skipped it and
// returned a blank 200, so the wizard appeared to do nothing at all.
func TestUpdateDB_MalformedFormRendersTheWizardAgain(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.SetHTMLTemplate(templateWithErrors(t))
	router.POST("/wizard_db", updateDB)

	w := httptest.NewRecorder()
	router.ServeHTTP(w, malformedForm(t, "/wizard_db"))

	if w.Body.Len() == 0 {
		t.Fatal("blank response: the wizard gives the user nothing to act on")
	}
	// html/template escapes the apostrophe, so match on a plain substring.
	if !strings.Contains(w.Body.String(), "read the form") {
		t.Errorf("page does not say why it failed: %q", w.Body.String())
	}
}

// templateWithErrors is a stand-in for wizard_db.html that prints just the
// errors, so this test covers the handler rather than the theme's markup.
func templateWithErrors(t *testing.T) *template.Template {
	t.Helper()
	return template.Must(template.New("wizard_db.html").Parse(`{{ .errors }}`))
}
