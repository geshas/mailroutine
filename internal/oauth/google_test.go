package oauth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"golang.org/x/oauth2"
)

func TestAuthURL(t *testing.T) {
	g := New("cid", "csecret", "http://localhost:8080/api/accounts/google/callback")
	u, err := url.Parse(g.AuthURL("STATE123", NewVerifier()))
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	if u.Host != "accounts.google.com" || q.Get("client_id") != "cid" || q.Get("state") != "STATE123" ||
		q.Get("access_type") != "offline" || q.Get("prompt") != "consent" || q.Get("code_challenge_method") != "S256" ||
		q.Get("code_challenge") == "" || q.Get("response_type") != "code" ||
		q.Get("redirect_uri") != "http://localhost:8080/api/accounts/google/callback" {
		t.Fatalf("%s", u)
	}
	if q.Get("scope") != "https://www.googleapis.com/auth/gmail.modify" {
		t.Fatalf("scope must be gmail.modify only, got %q", q.Get("scope"))
	}
	if strings.Contains(u.String(), "csecret") {
		t.Fatal("client secret in URL")
	}
}

var scopeGranted = "https://www.googleapis.com/auth/gmail.modify"

func fakeGoogle(t *testing.T, refresh string, status int) (*Google, *[]url.Values) {
	var seen []url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		seen = append(seen, r.Form)
		if r.URL.Path == "/revoke" {
			w.WriteHeader(status)
			return
		}
		if status != 200 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			json.NewEncoder(w).Encode(map[string]string{"error": "invalid_grant", "error_description": "secret-detail"})
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"access_token": "ya29.x", "token_type": "Bearer", "refresh_token": refresh, "expires_in": 3600, "scope": scopeGranted})
	}))
	t.Cleanup(srv.Close)
	g := New("cid", "csecret", "http://localhost/cb")
	g.SetEndpoint(oauth2.Endpoint{AuthURL: srv.URL + "/auth", TokenURL: srv.URL + "/token"})
	g.RevokeURL = srv.URL + "/revoke"
	return g, &seen
}

func TestExchange(t *testing.T) {
	g, seen := fakeGoogle(t, "1//refresh", 200)
	v := NewVerifier()
	tok, err := g.Exchange(context.Background(), "authcode", v)
	if err != nil || tok.RefreshToken != "1//refresh" {
		t.Fatalf("%v %v", tok, err)
	}
	form := (*seen)[0]
	if form.Get("code") != "authcode" || form.Get("code_verifier") != v || form.Get("grant_type") != "authorization_code" {
		t.Fatalf("%v", form)
	}
}

func TestExchangeWithoutRefreshToken(t *testing.T) {
	g, _ := fakeGoogle(t, "", 200)
	if _, err := g.Exchange(context.Background(), "c", NewVerifier()); err != ErrNoRefreshToken {
		t.Fatalf("%v", err)
	}
}

func TestExchangeErrorDoesNotLeakBody(t *testing.T) {
	g, _ := fakeGoogle(t, "", 400)
	_, err := g.Exchange(context.Background(), "c", NewVerifier())
	if err == nil || strings.Contains(err.Error(), "secret-detail") || !strings.Contains(err.Error(), "invalid_grant") {
		t.Fatalf("%v", err)
	}
}

func TestRevoke(t *testing.T) {
	g, seen := fakeGoogle(t, "r", 200)
	if err := g.Revoke(context.Background(), "tok"); err != nil {
		t.Fatal(err)
	}
	if (*seen)[0].Get("token") != "tok" {
		t.Fatal("token not sent")
	}
	g2, _ := fakeGoogle(t, "r", 400)
	if err := g2.Revoke(context.Background(), "tok"); err == nil {
		t.Fatal("expected error")
	}
}

func TestTokenSourcesAndRedirect(t *testing.T) {
	g, seen := fakeGoogle(t, "r", 200)
	if g.RedirectURL() != "http://localhost/cb" {
		t.Fatal(g.RedirectURL())
	}
	tok, err := g.TokenSource(context.Background(), "stored-refresh").Token()
	if err != nil || tok.AccessToken != "ya29.x" {
		t.Fatalf("%v %v", tok, err)
	}
	if (*seen)[0].Get("grant_type") != "refresh_token" || (*seen)[0].Get("refresh_token") != "stored-refresh" {
		t.Fatalf("%v", (*seen)[0])
	}
	fresh := &oauth2.Token{AccessToken: "live", Expiry: time.Now().Add(time.Hour)}
	got, err := g.StaticTokenSource(context.Background(), fresh).Token()
	if err != nil || got.AccessToken != "live" {
		t.Fatalf("%v %v", got, err)
	}
}

func TestExchangeNetworkErrorAndRevokeFailures(t *testing.T) {
	g := New("c", "s", "http://localhost/cb")
	g.SetEndpoint(oauth2.Endpoint{TokenURL: "http://127.0.0.1:1/token"})
	if _, err := g.Exchange(context.Background(), "c", NewVerifier()); err == nil || strings.Contains(err.Error(), "invalid_grant") {
		t.Fatalf("%v", err)
	}
	g.RevokeURL = "http://127.0.0.1:1/revoke"
	if err := g.Revoke(context.Background(), "t"); err == nil {
		t.Fatal("network failure swallowed")
	}
	g.RevokeURL = "://bad"
	if err := g.Revoke(context.Background(), "t"); err == nil {
		t.Fatal("bad url accepted")
	}
}

func TestExchangeRequiresTheGmailScope(t *testing.T) {
	old := scopeGranted
	defer func() { scopeGranted = old }()
	cases := map[string]bool{
		"https://www.googleapis.com/auth/gmail.modify":              true,
		"openid https://www.googleapis.com/auth/gmail.modify email": true,  // extra scopes are fine
		"https://www.googleapis.com/auth/gmail.readonly":            false, // weaker scope is not enough
		"https://www.googleapis.com/auth/gmail.modifyx":             false, // no prefix matching
		"openid email": false,
		"":             false,
	}
	for granted, ok := range cases {
		scopeGranted = granted
		g, _ := fakeGoogle(t, "1//refresh", 200)
		_, err := g.Exchange(context.Background(), "c", NewVerifier())
		if ok && err != nil {
			t.Errorf("%q rejected: %v", granted, err)
		}
		if !ok && err != ErrInsufficientScope {
			t.Errorf("%q: got %v want ErrInsufficientScope", granted, err)
		}
		if HasRequiredScope(granted) != ok {
			t.Errorf("HasRequiredScope(%q)", granted)
		}
	}
}
