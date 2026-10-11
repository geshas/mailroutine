package accounts

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/oauth2"
	"google.golang.org/api/option"

	"github.com/geshas/mailroutine/internal/oauth"
	"github.com/geshas/mailroutine/internal/security"
	"github.com/geshas/mailroutine/internal/sqlite"
	"github.com/geshas/mailroutine/pkg/models"
)

type fx struct {
	m        *Manager
	st       *sqlite.Store
	revoked  atomic.Int32
	profile  string
	logs     *bytes.Buffer
	failProf atomic.Bool
}

func setup(t *testing.T) *fx {
	t.Helper()
	f := &fx{profile: "me@gmail.com", logs: &bytes.Buffer{}}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /token", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"access_token": "ya29.t", "token_type": "Bearer", "expires_in": 3600})
	})
	mux.HandleFunc("POST /revoke", func(w http.ResponseWriter, r *http.Request) { f.revoked.Add(1) })
	mux.HandleFunc("GET /gmail/v1/users/me/profile", func(w http.ResponseWriter, r *http.Request) {
		if f.failProf.Load() {
			w.WriteHeader(403)
			return
		}
		json.NewEncoder(w).Encode(map[string]string{"emailAddress": f.profile})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	st, err := sqlite.Open(context.Background(), filepath.Join(t.TempDir(), "a.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	g := oauth.New("cid", "secret", "http://localhost/cb")
	g.SetEndpoint(oauth2.Endpoint{TokenURL: srv.URL + "/token"})
	g.RevokeURL = srv.URL + "/revoke"
	key, _ := security.ParseKey(mustSecret())
	f.st = st
	f.m = &Manager{Store: st, Google: g, Key: key, Timeout: 5 * time.Second,
		Logger: slog.New(slog.NewTextHandler(f.logs, nil)), GmailOptions: []option.ClientOption{option.WithEndpoint(srv.URL + "/")}}
	return f
}

func mustSecret() string { s, _ := security.GenerateSecret(); return s }

func TestConnectCreatesThenReconnects(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	tok := &oauth2.Token{AccessToken: "ya29.x", RefreshToken: "1//first", Expiry: time.Now().Add(time.Hour)}
	acc, created, err := f.m.Connect(ctx, tok)
	if err != nil || !created || acc.Email != "me@gmail.com" {
		t.Fatalf("%+v %v %v", acc, created, err)
	}
	if strings.Contains(acc.EncryptedRefreshToken, "first") {
		t.Fatal("token stored in the clear")
	}
	if plain, err := security.Decrypt(f.m.Key, acc.EncryptedRefreshToken); err != nil || plain != "1//first" {
		t.Fatal(plain, err)
	}
	f.m.MarkNeedsReauth(ctx, acc.ID)
	tok.RefreshToken = "1//second"
	again, created, err := f.m.Connect(ctx, tok)
	if err != nil || created || again.ID != acc.ID || again.ConnectionStatus != models.StatusConnected {
		t.Fatalf("%+v %v %v", again, created, err)
	}
}

func TestConnectFailures(t *testing.T) {
	f := setup(t)
	f.failProf.Store(true)
	tok := &oauth2.Token{AccessToken: "x", RefreshToken: "r", Expiry: time.Now().Add(time.Hour)}
	if _, _, err := f.m.Connect(context.Background(), tok); err == nil || !strings.Contains(err.Error(), "mailbox address") {
		t.Fatal(err)
	}
	f.failProf.Store(false)
	f.m.Key = []byte("short") // encryption must fail rather than store plaintext
	if _, _, err := f.m.Connect(context.Background(), tok); err == nil {
		t.Fatal("stored token without a valid key")
	}
}

func TestProvider(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	enc, _ := security.Encrypt(f.m.Key, "1//stored")
	acc, _, _ := f.st.UpsertAccount(ctx, "me@gmail.com", enc)

	client, got, err := f.m.ProviderByID(ctx, acc.ID)
	if err != nil || got.ID != acc.ID {
		t.Fatal(err)
	}
	// The client refreshes its access token through the fake token endpoint.
	if email, err := client.EmailAddress(ctx); err != nil || email != "me@gmail.com" {
		t.Fatal(email, err)
	}

	if _, _, err := f.m.ProviderByID(ctx, "missing"); !errors.Is(err, sqlite.ErrNotFound) {
		t.Fatal(err)
	}
	f.m.MarkNeedsReauth(ctx, acc.ID)
	if _, _, err := f.m.ProviderByID(ctx, acc.ID); !errors.Is(err, ErrNeedsReauth) {
		t.Fatal(err)
	}
	// Wrong key => decryption error that names the account but not the token.
	f.st.SetAccountStatus(ctx, acc.ID, models.StatusConnected)
	f.m.Key, _ = security.ParseKey(mustSecret())
	if _, _, err := f.m.ProviderByID(ctx, acc.ID); err == nil || !strings.Contains(err.Error(), "me@gmail.com") {
		t.Fatal(err)
	}
}

func TestDisconnect(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	enc, _ := security.Encrypt(f.m.Key, "1//stored")
	acc, _, _ := f.st.UpsertAccount(ctx, "me@gmail.com", enc)
	if err := f.m.Disconnect(ctx, acc.ID); err != nil {
		t.Fatal(err)
	}
	if f.revoked.Load() != 1 {
		t.Fatal("token not revoked at Google")
	}
	if _, err := f.st.GetAccount(ctx, acc.ID); !errors.Is(err, sqlite.ErrNotFound) {
		t.Fatal("account not deleted")
	}
	if err := f.m.Disconnect(ctx, acc.ID); !errors.Is(err, sqlite.ErrNotFound) {
		t.Fatal(err)
	}

	// Revocation failure is logged but the account is still removed.
	f.m.Google.RevokeURL = "http://127.0.0.1:1/revoke"
	acc2, _, _ := f.st.UpsertAccount(ctx, "two@gmail.com", enc)
	if err := f.m.Disconnect(ctx, acc2.ID); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(f.logs.String(), "token revocation failed") {
		t.Fatalf("revocation failure not logged: %s", f.logs)
	}
	// Undecryptable token: skip revocation, still delete. No logger is also fine.
	f.m.Logger = nil
	acc3, _, _ := f.st.UpsertAccount(ctx, "three@gmail.com", "v1:garbage")
	if err := f.m.Disconnect(ctx, acc3.ID); err != nil {
		t.Fatal(err)
	}
	f.m.Google.RevokeURL = "http://127.0.0.1:1/revoke"
	acc4, _, _ := f.st.UpsertAccount(ctx, "four@gmail.com", enc)
	if err := f.m.Disconnect(ctx, acc4.ID); err != nil {
		t.Fatal(err)
	}
}
