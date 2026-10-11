package httpapi_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/geshas/mailroutine/pkg/models"
)

// TestWriteContractFixtures records the real JSON the server sends so the web UI's
// schema tests (web/src/test/contract.test.ts) validate against reality instead of
// hand-written fixtures. It only runs when CONTRACT_DIR is set (see `make contract`).
func TestWriteContractFixtures(t *testing.T) {
	dir := os.Getenv("CONTRACT_DIR")
	if dir == "" {
		t.Skip("CONTRACT_DIR not set")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	h := newHarness(t, nil)
	save := func(name string, data []byte) {
		if err := os.WriteFile(filepath.Join(dir, name+".json"), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	get := func(name, method, path string, body any, want int) []byte {
		t.Helper()
		resp, data := h.req(method, path, body, "X-CSRF-Token", h.csrf)
		if resp.StatusCode != want {
			t.Fatalf("%s %s: %d %s", method, path, resp.StatusCode, data)
		}
		save(name, data)
		return data
	}

	_, data := h.req("POST", "/api/auth/login", map[string]string{"username": "admin", "password": "hunter2-hunter2"})
	save("login", data)
	h.login()
	get("session", "GET", "/api/auth/session", nil, 200)
	get("renew", "POST", "/api/auth/session/renew", nil, 200)
	get("dashboard_empty", "GET", "/api/dashboard", nil, 200)
	get("accounts", "GET", "/api/accounts", nil, 200)
	get("labels", "GET", "/api/accounts/"+h.acc.ID+"/labels", nil, 200)
	get("settings", "GET", "/api/settings", nil, 200)
	get("trace_labels", "GET", "/api/accounts/"+h.acc.ID+"/labels/mailroutine", nil, 200)
	get("trace_labels_created", "POST", "/api/accounts/"+h.acc.ID+"/labels/mailroutine", nil, 200)
	get("error_validation", "POST", "/api/routines", map[string]any{"account_id": h.acc.ID}, 400)

	body := routineBody(h.acc.ID)
	body["enabled"] = true
	m := h.mustStatus("POST", "/api/routines", body, 201)
	id := m["id"].(string)
	get("routine_new", "GET", "/api/routines/"+id, nil, 200)
	get("preview", "POST", "/api/routines/preview", map[string]any{"account_id": h.acc.ID, "gmail_query": "label:x"}, 200)
	started := get("run_started", "POST", "/api/routines/"+id+"/run", nil, 202)
	_ = started
	var runID string
	for i := 0; i < 200 && runID == ""; i++ {
		items, _, _ := h.store.ListExecutions(context.Background(), sqliteFilter())
		if len(items) > 0 && items[0].Status != models.ExecRunning {
			runID = items[0].ID
		}
		time.Sleep(25 * time.Millisecond)
	}
	if runID == "" {
		t.Fatal("run did not finish")
	}
	// A failed message gives the details sheet something real to show.
	h.store.InsertMessageActions(context.Background(), runID, []models.MessageActionResult{
		{MessageID: "m-failed", Action: models.ActionArchive, Status: models.ActionFailed, Error: "gmail error 404", CreatedAt: time.Now()},
	})
	get("routine", "GET", "/api/routines/"+id, nil, 200)
	get("routines", "GET", "/api/routines", nil, 200)
	get("runs", "GET", "/api/runs", nil, 200)
	get("run", "GET", "/api/runs/"+runID, nil, 200)
	get("dashboard", "GET", "/api/dashboard", nil, 200)
	get("put_settings", "PUT", "/api/settings", map[string]any{"timezone": "Europe/Paris"}, 200)
}
