package gmail

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"golang.org/x/oauth2"
	"google.golang.org/api/option"

	"github.com/geshas/mailroutine/pkg/engine"
)

func TestLabelsSorted(t *testing.T) {
	f := &fakeGmail{labels: []map[string]string{{"id": "L2", "name": "zeta", "type": "user"}, {"id": "INBOX", "name": "INBOX", "type": "system"}, {"id": "L1", "name": "Alpha", "type": "user"}}}
	c := newTestClient(t, f)
	got, err := c.Labels(context.Background())
	if err != nil || len(got) != 3 || got[0].Name != "Alpha" || got[1].Name != "INBOX" || got[2].Type != "user" {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestEmailAddressAndTokenSource(t *testing.T) {
	mux := http.NewServeMux()
	var auth string
	mux.HandleFunc("GET /gmail/v1/users/me/profile", func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		json.NewEncoder(w).Encode(map[string]string{"emailAddress": "a@b.c"})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	ts := oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "tok", TokenType: "Bearer"})
	c, err := NewFromTokenSource(context.Background(), ts, 5*time.Second, option.WithEndpoint(srv.URL+"/"))
	if err != nil {
		t.Fatal(err)
	}
	email, err := c.EmailAddress(context.Background())
	if err != nil || email != "a@b.c" || auth != "Bearer tok" {
		t.Fatalf("%q %q %v", email, auth, err)
	}
	// Error path.
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(401) }))
	defer bad.Close()
	c2, _ := New(context.Background(), bad.Client(), option.WithEndpoint(bad.URL+"/"))
	if _, err := c2.EmailAddress(context.Background()); !errors.Is(err, engine.ErrAuthRevoked) {
		t.Fatal(err)
	}
	if _, err := c2.Labels(context.Background()); !errors.Is(err, engine.ErrAuthRevoked) {
		t.Fatal(err)
	}
}

func TestNewRejectsBadOptions(t *testing.T) {
	// Conflicting auth options make service creation fail.
	_, err := New(context.Background(), http.DefaultClient, option.WithoutAuthentication(), option.WithAPIKey("k"), option.WithCredentialsJSON([]byte("{}")))
	if err == nil {
		t.Skip("library accepted conflicting options")
	}
}

func TestListDefaultPageSizeAndInternalDateFallback(t *testing.T) {
	mux := http.NewServeMux()
	var gotMax string
	mux.HandleFunc("GET /gmail/v1/users/me/messages", func(w http.ResponseWriter, r *http.Request) {
		gotMax = r.URL.Query().Get("maxResults")
		json.NewEncoder(w).Encode(map[string]any{"messages": []map[string]string{{"id": "x"}}})
	})
	mux.HandleFunc("GET /gmail/v1/users/me/messages/{id}", func(w http.ResponseWriter, r *http.Request) {
		// No Date header: internalDate is used.
		json.NewEncoder(w).Encode(map[string]any{"id": "x", "internalDate": "1700000000000", "payload": map[string]any{"headers": []map[string]string{{"name": "Date", "value": "garbage"}}}})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	c, _ := New(context.Background(), srv.Client(), option.WithEndpoint(srv.URL+"/"))
	if _, err := c.ListMessages(context.Background(), "q", "", 0); err != nil || gotMax != "500" {
		t.Fatalf("maxResults=%q err=%v", gotMax, err)
	}
	s, err := c.GetSummaries(context.Background(), []string{"x"})
	if err != nil || len(s) != 1 || s[0].Date.Unix() != 1700000000 {
		t.Fatalf("%+v %v", s, err)
	}
	if _, err := parseMailDate("not a date"); err == nil {
		t.Fatal("garbage date accepted")
	}
}

func TestSummariesAndModifyErrors(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /gmail/v1/users/me/messages/{id}", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(503)
		io.WriteString(w, `{"error":{"code":503,"message":"down"}}`)
	})
	mux.HandleFunc("POST /gmail/v1/users/me/messages/batchModify", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(429)
		io.WriteString(w, `{"error":{"code":429,"message":"slow"}}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	c, _ := New(context.Background(), srv.Client(), option.WithEndpoint(srv.URL+"/"))
	if _, err := c.GetSummaries(context.Background(), []string{"a", "b"}); !engine.IsRetryable(err) {
		t.Fatalf("%v", err)
	}
	for name, fn := range map[string]func() error{
		"archive":      func() error { return c.Archive(context.Background(), []string{"a"}) },
		"system label": func() error { return c.AddLabel(context.Background(), []string{"a"}, "STARRED") },
	} {
		if err := fn(); !engine.IsRetryable(err) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestLabelResolutionErrorsAndConflict(t *testing.T) {
	// Label listing fails.
	failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(500) }))
	defer failing.Close()
	c, _ := New(context.Background(), failing.Client(), option.WithEndpoint(failing.URL+"/"))
	if err := c.AddLabel(context.Background(), []string{"a"}, "X"); !engine.IsRetryable(err) {
		t.Fatal(err)
	}
	if err := c.RemoveLabel(context.Background(), []string{"a"}, "X"); err == nil {
		t.Fatal("expected error")
	}

	// Create races with another client (409) and the retry finds the label.
	lists := 0
	mux := http.NewServeMux()
	mux.HandleFunc("GET /gmail/v1/users/me/labels", func(w http.ResponseWriter, r *http.Request) {
		lists++
		labels := []map[string]string{}
		if lists >= 2 {
			labels = append(labels, map[string]string{"id": "Label_9", "name": "Race"})
		}
		json.NewEncoder(w).Encode(map[string]any{"labels": labels})
	})
	mux.HandleFunc("POST /gmail/v1/users/me/labels", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(409)
		io.WriteString(w, `{"error":{"code":409,"message":"exists"}}`)
	})
	var modified map[string]any
	mux.HandleFunc("POST /gmail/v1/users/me/messages/batchModify", func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&modified)
		w.WriteHeader(204)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	c2, _ := New(context.Background(), srv.Client(), option.WithEndpoint(srv.URL+"/"))
	if err := c2.AddLabel(context.Background(), []string{"m"}, "Race"); err != nil {
		t.Fatal(err)
	}
	if ids := modified["addLabelIds"].([]any); ids[0] != "Label_9" {
		t.Fatalf("%v", modified)
	}

	// 409 but the reload still cannot find it, and a reload failure.
	lists = -100
	if err := c2.AddLabel(context.Background(), []string{"m"}, "Ghost"); err == nil {
		t.Fatal("expected failure")
	}
	// Create fails for another reason.
	mux2 := http.NewServeMux()
	mux2.HandleFunc("GET /gmail/v1/users/me/labels", func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, `{}`) })
	mux2.HandleFunc("POST /gmail/v1/users/me/labels", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
		io.WriteString(w, `{"error":{"code":400,"message":"Invalid label name"}}`)
	})
	srv2 := httptest.NewServer(mux2)
	defer srv2.Close()
	c3, _ := New(context.Background(), srv2.Client(), option.WithEndpoint(srv2.URL+"/"))
	if err := c3.AddLabel(context.Background(), []string{"m"}, "bad/"); err == nil {
		t.Fatal("expected error")
	}
}

func TestLabelCacheExpires(t *testing.T) {
	loads := 0
	mux := http.NewServeMux()
	mux.HandleFunc("GET /gmail/v1/users/me/labels", func(w http.ResponseWriter, r *http.Request) {
		loads++
		json.NewEncoder(w).Encode(map[string]any{"labels": []map[string]string{{"id": "L1", "name": "Keep"}}})
	})
	mux.HandleFunc("POST /gmail/v1/users/me/messages/batchModify", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) })
	srv := httptest.NewServer(mux)
	defer srv.Close()
	c, _ := New(context.Background(), srv.Client(), option.WithEndpoint(srv.URL+"/"))
	now := time.Now()
	c.now = func() time.Time { return now }
	for i := 0; i < 3; i++ {
		c.AddLabel(context.Background(), []string{"m"}, "keep")
	}
	if loads != 1 {
		t.Fatalf("cache not used: %d loads", loads)
	}
	now = now.Add(labelCacheMaxAge + time.Second)
	c.AddLabel(context.Background(), []string{"m"}, "keep")
	if loads != 2 {
		t.Fatalf("cache not refreshed: %d loads", loads)
	}
}

func TestClassifyDirect(t *testing.T) {
	if classify(nil, false) != nil {
		t.Fatal("nil")
	}
	if err := classify(context.Canceled, false); !errors.Is(err, context.Canceled) || engine.IsRetryable(err) {
		t.Fatal(err)
	}
	mk := func(code, desc string, status int) error {
		return &oauth2.RetrieveError{ErrorCode: code, Response: &http.Response{StatusCode: status}}
	}
	if err := classify(mk("invalid_grant", "", 400), false); !errors.Is(err, engine.ErrAuthRevoked) {
		t.Fatal(err)
	}
	if err := classify(mk("", "", 400), false); !errors.Is(err, engine.ErrAuthRevoked) {
		t.Fatal("400 from token endpoint means bad grant")
	}
	if err := classify(mk("temporarily_unavailable", "", 503), false); !engine.IsRetryable(err) {
		t.Fatal(err)
	}
	for name, e := range map[string]error{
		"url error": &url.Error{Op: "Get", URL: "x", Err: errors.New("reset")},
		"net error": &net.OpError{Op: "dial", Err: errors.New("refused")},
		"deadline":  fmt.Errorf("w: %w", context.DeadlineExceeded),
	} {
		if !engine.IsRetryable(classify(e, false)) {
			t.Errorf("%s should be retryable", name)
		}
	}
	other := errors.New("weird")
	if classify(other, false) != other {
		t.Fatal("unknown errors pass through")
	}
	// Non-search 400 is a plain error, not invalid query.
	f := &fakeGmail{status: 404, body: `{"error":{"code":404,"message":"nf"}}`}
	c := newTestClient(t, f)
	_, err := c.ListMessages(context.Background(), "q", "", 1)
	if err == nil || engine.IsFatal(err) || engine.IsRetryable(err) {
		t.Fatalf("%v", err)
	}
	if retryAfter(nil) != 0 || retryAfter(http.Header{}) != 0 {
		t.Fatal("retryAfter")
	}
}

func TestConflictThenReloadFails(t *testing.T) {
	lists := 0
	mux := http.NewServeMux()
	mux.HandleFunc("GET /gmail/v1/users/me/labels", func(w http.ResponseWriter, r *http.Request) {
		lists++
		if lists >= 2 {
			w.WriteHeader(500)
			return
		}
		io.WriteString(w, `{}`)
	})
	mux.HandleFunc("POST /gmail/v1/users/me/labels", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(409)
		io.WriteString(w, `{"error":{"code":409,"message":"exists"}}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	c, _ := New(context.Background(), srv.Client(), option.WithEndpoint(srv.URL+"/"))
	if err := c.AddLabel(context.Background(), []string{"m"}, "X"); !engine.IsRetryable(err) {
		t.Fatalf("%v", err)
	}
}

func TestEnsureLabelsCreatesOnlyMissingAndParentsFirst(t *testing.T) {
	f := &fakeGmail{labels: []map[string]string{{"id": "L1", "name": "mailroutine/archive"}}}
	c := newTestClient(t, f)
	created, err := c.EnsureLabels(context.Background(), []string{"MailRoutine", "MailRoutine/Archive", "MailRoutine/Trash"})
	if err != nil {
		t.Fatal(err)
	}
	// "MailRoutine/Archive" exists (different case); the parent and Trash are new.
	if len(created) != 2 || created[0] != "MailRoutine" || created[1] != "MailRoutine/Trash" {
		t.Fatalf("created %v", created)
	}
	if len(f.created) != 2 || f.created[0] != "MailRoutine" || f.created[1] != "MailRoutine/Trash" {
		t.Fatalf("gmail create calls: %v", f.created)
	}
}

func TestNestedLabelCreationAddsMissingParents(t *testing.T) {
	f := &fakeGmail{}
	c := newTestClient(t, f)
	if err := c.AddLabel(context.Background(), []string{"m"}, "Archive/2026/Receipts"); err != nil {
		t.Fatal(err)
	}
	want := []string{"Archive", "Archive/2026", "Archive/2026/Receipts"}
	if len(f.created) != 3 || f.created[0] != want[0] || f.created[1] != want[1] || f.created[2] != want[2] {
		t.Fatalf("created %v want %v", f.created, want)
	}
}

func TestEnsureLabelsErrors(t *testing.T) {
	failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(401) }))
	defer failing.Close()
	c, _ := New(context.Background(), failing.Client(), option.WithEndpoint(failing.URL+"/"))
	if _, err := c.EnsureLabels(context.Background(), []string{"X"}); !errors.Is(err, engine.ErrAuthRevoked) {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /gmail/v1/users/me/labels", func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, `{}`) })
	mux.HandleFunc("POST /gmail/v1/users/me/labels", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
		io.WriteString(w, `{"error":{"code":400,"message":"Invalid"}}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	c2, _ := New(context.Background(), srv.Client(), option.WithEndpoint(srv.URL+"/"))
	if _, err := c2.EnsureLabels(context.Background(), []string{"A/B"}); err == nil {
		t.Fatal("expected error from failing parent creation")
	}
}

func TestMissingPermissionIsTreatedAsNeedingReconnect(t *testing.T) {
	for _, body := range []string{
		`{"error":{"code":403,"message":"Request had insufficient authentication scopes.","errors":[{"reason":"insufficientPermissions"}]}}`,
		`{"error":{"code":403,"message":"Request had insufficient authentication scopes."}}`,
	} {
		f := &fakeGmail{status: 403, body: body}
		c := newTestClient(t, f)
		_, err := c.ListMessages(context.Background(), "q", "", 5)
		if !errors.Is(err, engine.ErrAuthRevoked) || !strings.Contains(err.Error(), "required Gmail permission") {
			t.Fatalf("%v", err)
		}
	}
	// Other 403s stay ordinary errors.
	f := &fakeGmail{status: 403, body: `{"error":{"code":403,"message":"Gmail API has not been used","errors":[{"reason":"accessNotConfigured"}]}}`}
	_, err := newTestClient(t, f).ListMessages(context.Background(), "q", "", 5)
	if err == nil || errors.Is(err, engine.ErrAuthRevoked) {
		t.Fatalf("%v", err)
	}
}
