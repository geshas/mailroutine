package gmail

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/api/option"

	"github.com/geshas/mailroutine/pkg/engine"
	"github.com/geshas/mailroutine/pkg/models"
)

type fakeGmail struct {
	mu      sync.Mutex
	modify  []map[string]any
	labels  []map[string]string
	created []string
	status  int // forced status for the next messages/list call
	body    string
	headers map[string]string
}

func (f *fakeGmail) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /gmail/v1/users/me/messages", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		status, body := f.status, f.body
		f.mu.Unlock()
		for k, v := range f.headers {
			w.Header().Set(k, v)
		}
		if status != 0 {
			w.WriteHeader(status)
			io.WriteString(w, body)
			return
		}
		q := r.URL.Query()
		if q.Get("q") != "label:news" {
			http.Error(w, "unexpected q", 500)
			return
		}
		if q.Get("pageToken") == "" {
			json.NewEncoder(w).Encode(map[string]any{
				"messages": []map[string]string{{"id": "a"}, {"id": "b"}}, "nextPageToken": "p2", "resultSizeEstimate": 77,
			})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"messages": []map[string]string{{"id": "c"}}})
	})
	mux.HandleFunc("GET /gmail/v1/users/me/messages/{id}", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if r.URL.Query().Get("format") != "metadata" {
			http.Error(w, "bodies must not be requested", 500)
			return
		}
		if id == "gone" {
			w.WriteHeader(404)
			io.WriteString(w, `{"error":{"code":404,"message":"Not Found"}}`)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{
			"id": id, "internalDate": "1700000000000",
			"payload": map[string]any{"headers": []map[string]string{
				{"name": "From", "value": "News <n@example.com>"},
				{"name": "Subject", "value": "Hello " + id},
				{"name": "Date", "value": "Tue, 14 Nov 2023 22:13:20 +0000"},
			}},
		})
	})
	mux.HandleFunc("POST /gmail/v1/users/me/messages/batchModify", func(w http.ResponseWriter, r *http.Request) {
		var m map[string]any
		json.NewDecoder(r.Body).Decode(&m)
		f.mu.Lock()
		f.modify = append(f.modify, m)
		f.mu.Unlock()
		w.WriteHeader(204)
	})
	mux.HandleFunc("GET /gmail/v1/users/me/labels", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		json.NewEncoder(w).Encode(map[string]any{"labels": f.labels})
	})
	mux.HandleFunc("POST /gmail/v1/users/me/labels", func(w http.ResponseWriter, r *http.Request) {
		var l map[string]string
		json.NewDecoder(r.Body).Decode(&l)
		f.mu.Lock()
		f.created = append(f.created, l["name"])
		f.mu.Unlock()
		json.NewEncoder(w).Encode(map[string]string{"id": "Label_new", "name": l["name"]})
	})
	return mux
}

func newTestClient(t *testing.T, f *fakeGmail) *Client {
	t.Helper()
	srv := httptest.NewServer(f.handler())
	t.Cleanup(srv.Close)
	c, err := New(context.Background(), srv.Client(), option.WithEndpoint(srv.URL+"/"))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestListPaginationAndEstimate(t *testing.T) {
	c := newTestClient(t, &fakeGmail{})
	p1, err := c.ListMessages(context.Background(), "label:news", "", 10)
	if err != nil || len(p1.IDs) != 2 || p1.NextPageToken != "p2" || p1.EstimatedTotal != 77 {
		t.Fatalf("%+v %v", p1, err)
	}
	p2, err := c.ListMessages(context.Background(), "label:news", "p2", 10)
	if err != nil || len(p2.IDs) != 1 || p2.NextPageToken != "" {
		t.Fatalf("%+v %v", p2, err)
	}
}

func TestSummariesMetadataOnlyAndMissingSkipped(t *testing.T) {
	c := newTestClient(t, &fakeGmail{})
	got, err := c.GetSummaries(context.Background(), []string{"a", "gone", "b"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].ID != "a" || got[1].ID != "b" {
		t.Fatalf("%+v", got)
	}
	if got[0].From != "News <n@example.com>" || got[0].Subject != "Hello a" || got[0].Date.Year() != 2023 {
		t.Fatalf("%+v", got[0])
	}
}

func TestActionsMapToLabelChanges(t *testing.T) {
	f := &fakeGmail{labels: []map[string]string{{"id": "Label_7", "name": "Receipts"}}}
	c := newTestClient(t, f)
	ctx := context.Background()
	ids := []string{"a", "b"}
	steps := []struct {
		run         func() error
		add, remove []string
	}{
		{func() error { return c.Archive(ctx, ids) }, nil, []string{"INBOX"}},
		{func() error { return c.Trash(ctx, ids) }, []string{"TRASH"}, []string{"INBOX"}},
		{func() error { return c.MarkRead(ctx, ids) }, nil, []string{"UNREAD"}},
		{func() error { return c.MarkUnread(ctx, ids) }, []string{"UNREAD"}, nil},
		{func() error { return c.AddLabel(ctx, ids, "receipts") }, []string{"Label_7"}, nil},
		{func() error { return c.RemoveLabel(ctx, ids, "Receipts") }, nil, []string{"Label_7"}},
		{func() error { return c.AddLabel(ctx, ids, "Brand New") }, []string{"Label_new"}, nil},
		{func() error { return c.AddLabel(ctx, ids, "starred") }, []string{"STARRED"}, nil},
	}
	for i, s := range steps {
		if err := s.run(); err != nil {
			t.Fatalf("step %d: %v", i, err)
		}
		m := f.modify[i]
		if !sameStrings(m["addLabelIds"], s.add) || !sameStrings(m["removeLabelIds"], s.remove) {
			t.Fatalf("step %d: got %v", i, m)
		}
	}
	if len(f.created) != 1 || f.created[0] != "Brand New" {
		t.Fatalf("created labels: %v", f.created)
	}
}

func sameStrings(v any, want []string) bool {
	arr, _ := v.([]any)
	if len(arr) != len(want) {
		return false
	}
	for i := range arr {
		if arr[i].(string) != want[i] {
			return false
		}
	}
	return true
}

func TestRemoveMissingLabelIsNoop(t *testing.T) {
	f := &fakeGmail{}
	c := newTestClient(t, f)
	if err := c.RemoveLabel(context.Background(), []string{"a"}, "Nope"); err != nil {
		t.Fatal(err)
	}
	if len(f.modify) != 0 || len(f.created) != 0 {
		t.Fatal("must not call modify or create a label")
	}
}

func TestLargeBatchesAreChunked(t *testing.T) {
	f := &fakeGmail{}
	c := newTestClient(t, f)
	ids := make([]string, 2500)
	for i := range ids {
		ids[i] = "x"
	}
	if err := c.Archive(context.Background(), ids); err != nil {
		t.Fatal(err)
	}
	if len(f.modify) != 3 {
		t.Fatalf("chunks: %d", len(f.modify))
	}
}

func TestErrorClassification(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		hdr    map[string]string
		check  func(error) bool
	}{
		{"unauthorized", 401, `{"error":{"code":401,"message":"bad"}}`, nil, func(e error) bool { return errors.Is(e, engine.ErrAuthRevoked) }},
		{"bad query", 400, `{"error":{"code":400,"message":"Invalid query"}}`, nil, func(e error) bool { return errors.Is(e, engine.ErrInvalidQuery) }},
		{"rate limit 429", 429, `{"error":{"code":429,"message":"slow"}}`, map[string]string{"Retry-After": "3"}, engine.IsRetryable},
		{"quota 403", 403, `{"error":{"code":403,"message":"q","errors":[{"reason":"userRateLimitExceeded"}]}}`, nil, engine.IsRetryable},
		{"server 503", 503, `{"error":{"code":503,"message":"x"}}`, nil, engine.IsRetryable},
		{"forbidden permanent", 403, `{"error":{"code":403,"message":"no","errors":[{"reason":"accessNotConfigured"}]}}`, nil,
			func(e error) bool { return !engine.IsRetryable(e) && !engine.IsFatal(e) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeGmail{status: tc.status, body: tc.body, headers: tc.hdr}
			c := newTestClient(t, f)
			_, err := c.ListMessages(context.Background(), "label:news", "", 10)
			if err == nil || !tc.check(err) {
				t.Fatalf("unexpected classification: %v", err)
			}
		})
	}
}

func TestRetryAfterHonoured(t *testing.T) {
	f := &fakeGmail{status: 429, body: `{"error":{"code":429,"message":"slow"}}`, headers: map[string]string{"Retry-After": "7"}}
	c := newTestClient(t, f)
	_, err := c.ListMessages(context.Background(), "label:news", "", 10)
	var re *engine.RetryableError
	if !errors.As(err, &re) || re.After < 7*time.Second {
		t.Fatalf("retry-after not propagated: %v", err)
	}
}

func TestContextCancellationNotRetryable(t *testing.T) {
	c := newTestClient(t, &fakeGmail{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := c.ListMessages(ctx, "label:news", "", 10)
	if err == nil || engine.IsRetryable(err) {
		t.Fatalf("cancelled context must not be retryable: %v", err)
	}
}

func TestParseMailDate(t *testing.T) {
	for _, v := range []string{"Tue, 14 Nov 2023 22:13:20 +0000", "Tue, 14 Nov 2023 22:13:20 +0000 (UTC)", "14 Nov 2023 22:13:20 +0000"} {
		if tm, err := parseMailDate(v); err != nil || !strings.HasPrefix(tm.Format(time.RFC3339), "2023-11-14T22:13:20") {
			t.Errorf("%q -> %v %v", v, tm, err)
		}
	}
}

// Compile-time guard that the adapter satisfies the engine contract.
var _ engine.MailProvider = (*Client)(nil)
var _ = models.ProviderGmail
