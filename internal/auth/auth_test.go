package auth

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/geshas/mailroutine/internal/security"
	"github.com/geshas/mailroutine/internal/sqlite"
)

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

var testHash, _ = security.HashPassword("s3cret-pw")

func setup(t *testing.T, mod func(*Settings)) (*Service, *sqlite.Store, *clock, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "a.db")
	st, err := sqlite.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	cfg := Settings{Username: "admin", PasswordHash: testHash, Secret: []byte("0123456789abcdef0123456789abcdef"),
		IdleTimeout: 30 * time.Minute, RenewThreshold: 5 * time.Minute}
	if mod != nil {
		mod(&cfg)
	}
	clk := &clock{t: time.Now().UTC().Truncate(time.Millisecond)}
	svc := New(st, cfg)
	svc.Now = clk.now
	return svc, st, clk, path
}

func TestCheckCredentials(t *testing.T) {
	svc, _, _, _ := setup(t, nil)
	if !svc.CheckCredentials("admin", "s3cret-pw") {
		t.Fatal("valid login rejected")
	}
	for _, c := range [][2]string{{"admin", "wrong"}, {"root", "s3cret-pw"}, {"", ""}, {"admin", ""}} {
		if svc.CheckCredentials(c[0], c[1]) {
			t.Errorf("accepted %v", c)
		}
	}
	bad, _, _, _ := setup(t, func(s *Settings) { s.PasswordHash = "garbage" })
	if bad.CheckCredentials("admin", "s3cret-pw") {
		t.Fatal("malformed hash must never authenticate")
	}
}

func TestSessionStoresOnlyHashedToken(t *testing.T) {
	svc, st, _, _ := setup(t, nil)
	ctx := context.Background()
	token, sess, err := svc.CreateSession(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if sess.TokenHash == token || len(token) < 40 {
		t.Fatal("token must be random, long and not stored raw")
	}
	if _, err := st.SessionByHash(ctx, token); !errors.Is(err, sqlite.ErrNotFound) {
		t.Fatal("raw token found in database")
	}
	got, err := svc.Validate(ctx, token)
	if err != nil || got.ID != sess.ID {
		t.Fatalf("%v", err)
	}
	if _, err := svc.Validate(ctx, token+"x"); !errors.Is(err, ErrUnauthenticated) {
		t.Fatal(err)
	}
	if _, err := svc.Validate(ctx, ""); !errors.Is(err, ErrUnauthenticated) {
		t.Fatal(err)
	}
	// Two logins -> two distinct tokens (rotation).
	t2, _, _ := svc.CreateSession(ctx)
	if t2 == token {
		t.Fatal("token reused")
	}
}

func TestInactivityExpiryAndNoImplicitExtension(t *testing.T) {
	svc, _, clk, _ := setup(t, nil)
	ctx := context.Background()
	token, _, _ := svc.CreateSession(ctx)

	// Passive checks (what dashboard polling does) never extend the session.
	for i := 0; i < 5; i++ {
		clk.advance(5 * time.Minute)
		if _, err := svc.Validate(ctx, token); err != nil {
			t.Fatalf("minute %d: %v", (i+1)*5, err)
		}
	}
	clk.advance(5*time.Minute + time.Second) // 30m since login, only polled
	if _, err := svc.Validate(ctx, token); !errors.Is(err, ErrExpired) {
		t.Fatalf("polling must not keep the session alive, got %v", err)
	}
	// Expired session is deleted.
	if _, err := svc.Validate(ctx, token); !errors.Is(err, ErrUnauthenticated) {
		t.Fatal(err)
	}
}

func TestRenewSlidesWindow(t *testing.T) {
	svc, _, clk, _ := setup(t, nil)
	ctx := context.Background()
	token, sess, _ := svc.CreateSession(ctx)

	// Below the threshold: no write, expiry unchanged.
	clk.advance(time.Minute)
	got, renewed, err := svc.Renew(ctx, sess)
	if err != nil || renewed || !got.ExpiresAt.Equal(sess.ExpiresAt) {
		t.Fatalf("renewed too early: %v %v", renewed, err)
	}
	// Active user: renew every 6 minutes for 2 hours, far past the 30m idle timeout.
	cur := sess
	for i := 0; i < 20; i++ {
		clk.advance(6 * time.Minute)
		cur, err = svc.Validate(ctx, token)
		if err != nil {
			t.Fatalf("iteration %d: %v", i, err)
		}
		cur, renewed, err = svc.Renew(ctx, cur)
		if err != nil || !renewed {
			t.Fatalf("iteration %d: renewed=%v err=%v", i, renewed, err)
		}
		if want := clk.now().Add(30 * time.Minute); !cur.ExpiresAt.Equal(want) {
			t.Fatalf("expiry %v want %v", cur.ExpiresAt, want)
		}
	}
	// Then idle: expires 30 minutes after the last activity.
	clk.advance(29 * time.Minute)
	if _, err := svc.Validate(ctx, token); err != nil {
		t.Fatal(err)
	}
	clk.advance(2 * time.Minute)
	if _, err := svc.Validate(ctx, token); !errors.Is(err, ErrExpired) {
		t.Fatal(err)
	}
}

func TestAbsoluteTimeoutCapsSliding(t *testing.T) {
	svc, _, clk, _ := setup(t, func(s *Settings) { s.AbsoluteTimeout = time.Hour })
	ctx := context.Background()
	token, sess, _ := svc.CreateSession(ctx)
	cur := sess
	for i := 0; i < 9; i++ {
		clk.advance(6 * time.Minute)
		var err error
		if cur, err = svc.Validate(ctx, token); err != nil {
			t.Fatal(err)
		}
		if cur, _, err = svc.Renew(ctx, cur); err != nil {
			t.Fatal(err)
		}
		if cur.ExpiresAt.After(*sess.AbsoluteExpiresAt) {
			t.Fatal("expiry beyond absolute limit")
		}
	}
	clk.advance(10 * time.Minute) // 64 minutes since login
	if _, err := svc.Validate(ctx, token); !errors.Is(err, ErrExpired) {
		t.Fatalf("absolute timeout not enforced: %v", err)
	}
}

func TestSurvivesRestartAndCredentialChangeInvalidates(t *testing.T) {
	svc, st, clk, path := setup(t, nil)
	ctx := context.Background()
	token, _, _ := svc.CreateSession(ctx)
	st.Close()

	st2, err := sqlite.Open(ctx, path) // "restart"
	if err != nil {
		t.Fatal(err)
	}
	defer st2.Close()
	same := New(st2, svc.cfg)
	same.Now = clk.now
	if n, _ := same.InvalidateStaleSessions(ctx); n != 0 {
		t.Fatalf("valid session purged: %d", n)
	}
	if _, err := same.Validate(ctx, token); err != nil {
		t.Fatalf("session did not survive restart: %v", err)
	}

	newHash, _ := security.HashPassword("different")
	changed := svc.cfg
	changed.PasswordHash = newHash
	svc2 := New(st2, changed)
	svc2.Now = clk.now
	// Even before the startup purge, validation rejects the old session.
	if _, err := svc2.Validate(ctx, token); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("session survived password change: %v", err)
	}
	token2, _, _ := same.CreateSession(ctx)
	if n, _ := svc2.InvalidateStaleSessions(ctx); n != 1 {
		t.Fatalf("purged %d", n)
	}
	if _, err := svc2.Validate(ctx, token2); !errors.Is(err, ErrUnauthenticated) {
		t.Fatal("old-credential session accepted")
	}
	// Changing the secret also invalidates.
	changed2 := svc.cfg
	changed2.Secret = []byte("ffffffffffffffffffffffffffffffff")
	svc3 := New(st2, changed2)
	if _, err := svc3.Validate(ctx, token); !errors.Is(err, ErrUnauthenticated) {
		t.Fatal("session survived secret change")
	}
}

func TestLogoutInvalidates(t *testing.T) {
	svc, _, _, _ := setup(t, nil)
	ctx := context.Background()
	token, sess, _ := svc.CreateSession(ctx)
	if err := svc.Destroy(ctx, sess); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Validate(ctx, token); !errors.Is(err, ErrUnauthenticated) {
		t.Fatal(err)
	}
}

func TestConcurrentRenewal(t *testing.T) {
	svc, _, clk, _ := setup(t, nil)
	ctx := context.Background()
	token, sess, _ := svc.CreateSession(ctx)
	clk.advance(10 * time.Minute)
	var renewals atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 15; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, renewed, err := svc.Renew(ctx, sess)
			if err != nil {
				t.Error(err)
			}
			if renewed {
				renewals.Add(1)
			}
		}()
	}
	wg.Wait()
	if renewals.Load() != 1 {
		t.Fatalf("%d concurrent renewals applied", renewals.Load())
	}
	if _, err := svc.Validate(ctx, token); err != nil {
		t.Fatal(err)
	}
}

func TestCSRFTokenPerSession(t *testing.T) {
	svc, _, _, _ := setup(t, nil)
	ctx := context.Background()
	_, a, _ := svc.CreateSession(ctx)
	_, b, _ := svc.CreateSession(ctx)
	if svc.CSRFToken(a) == svc.CSRFToken(b) || svc.CSRFToken(a) != svc.CSRFToken(a) || len(svc.CSRFToken(a)) < 32 {
		t.Fatal("csrf tokens must be stable per session and distinct across sessions")
	}
}

func TestLoginLimiter(t *testing.T) {
	now := time.Now()
	l := NewLoginLimiter(3, 5, 10*time.Minute)
	l.Now = func() time.Time { return now }
	for i := 0; i < 3; i++ {
		if ok, _ := l.Allow("1.1.1.1"); !ok {
			t.Fatal("blocked early")
		}
		l.Fail("1.1.1.1")
	}
	ok, wait := l.Allow("1.1.1.1")
	if ok || wait <= 0 || wait > 10*time.Minute {
		t.Fatalf("ok=%v wait=%v", ok, wait)
	}
	if ok, _ := l.Allow("2.2.2.2"); !ok {
		t.Fatal("other clients must not be blocked by per-key limit")
	}
	// Global ceiling across many clients.
	l.Fail("2.2.2.2")
	l.Fail("3.3.3.3")
	if ok, _ := l.Allow("4.4.4.4"); ok {
		t.Fatal("global limit not enforced")
	}
	// Window expiry unblocks.
	now = now.Add(11 * time.Minute)
	if ok, _ := l.Allow("1.1.1.1"); !ok {
		t.Fatal("still blocked after window")
	}
	l.Fail("1.1.1.1")
	l.Succeed("1.1.1.1")
	if len(l.fails["1.1.1.1"]) != 0 {
		t.Fatal("success should reset client counter")
	}
}

func TestConfigPruneAndAbsoluteSession(t *testing.T) {
	svc, _, clk, _ := setup(t, func(s *Settings) { s.AbsoluteTimeout = 10 * time.Minute })
	ctx := context.Background()
	if svc.Config().Username != "admin" {
		t.Fatal("Config")
	}
	_, sess, err := svc.CreateSession(ctx)
	if err != nil || sess.AbsoluteExpiresAt == nil || !sess.ExpiresAt.Equal(*sess.AbsoluteExpiresAt) {
		t.Fatalf("absolute limit must cap the first expiry: %+v %v", sess, err)
	}
	clk.advance(11 * time.Minute)
	if n, err := svc.Prune(ctx); err != nil || n != 1 {
		t.Fatalf("pruned %d %v", n, err)
	}
}

func TestStoreFailuresSurface(t *testing.T) {
	svc, st, clk, _ := setup(t, nil)
	ctx := context.Background()
	token, sess, _ := svc.CreateSession(ctx)
	clk.advance(10 * time.Minute)
	st.Close()
	if _, _, err := svc.CreateSession(ctx); err == nil {
		t.Fatal("create")
	}
	if _, err := svc.Validate(ctx, token); err == nil || errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("db errors must not look like logout: %v", err)
	}
	if _, _, err := svc.Renew(ctx, sess); err == nil {
		t.Fatal("renew")
	}
	if _, err := svc.Prune(ctx); err == nil {
		t.Fatal("prune")
	}
	if _, err := svc.InvalidateStaleSessions(ctx); err == nil {
		t.Fatal("invalidate")
	}
}

func TestRenewAfterConcurrentDeletion(t *testing.T) {
	svc, st, clk, _ := setup(t, nil)
	ctx := context.Background()
	_, sess, _ := svc.CreateSession(ctx)
	clk.advance(10 * time.Minute)
	st.DeleteSession(ctx, sess.ID) // logged out elsewhere between validate and renew
	if _, _, err := svc.Renew(ctx, sess); !errors.Is(err, ErrExpired) {
		t.Fatalf("got %v", err)
	}
}

func TestLimiterBoundsMemory(t *testing.T) {
	l := NewLoginLimiter(3, 1000000, time.Minute)
	now := time.Now()
	l.Now = func() time.Time { return now }
	for i := 0; i < 10001; i++ {
		l.Fail(string(rune(i)) + "k")
	}
	now = now.Add(2 * time.Minute)
	l.Fail("trigger")
	if len(l.fails) > 10 {
		t.Fatalf("stale entries not pruned: %d", len(l.fails))
	}
}

func TestPasswordVerificationConcurrencyIsBounded(t *testing.T) {
	svc, _, _, _ := setup(t, nil)
	var running, peak atomic.Int32
	svc.hashSlots = make(chan struct{}, 2)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			svc.hashSlots <- struct{}{} // same acquire discipline CheckCredentials uses
			n := running.Add(1)
			for {
				p := peak.Load()
				if n <= p || peak.CompareAndSwap(p, n) {
					break
				}
			}
			time.Sleep(5 * time.Millisecond)
			running.Add(-1)
			<-svc.hashSlots
		}()
	}
	wg.Wait()
	if peak.Load() > 2 {
		t.Fatalf("peak concurrency %d exceeds the cap", peak.Load())
	}
	// And the real path still works (and releases its slot).
	svc2, _, _, _ := setup(t, nil)
	for i := 0; i < 3*maxConcurrentHashes; i++ {
		if !svc2.CheckCredentials("admin", "s3cret-pw") {
			t.Fatal("valid login rejected")
		}
	}
}
