// Package gmail implements engine.MailProvider on top of the official Gmail API.
//
// Only message metadata (From/Subject/Date) is ever requested; bodies are never
// fetched. Retries are performed by the engine: this package only classifies
// failures into permanent, retryable, and authorization errors.
package gmail

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/sync/errgroup"
	gmailapi "google.golang.org/api/gmail/v1"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/option"

	"github.com/geshas/mailroutine/pkg/engine"
	"github.com/geshas/mailroutine/pkg/models"
)

// Scope is the OAuth scope MailRoutine needs: read, label, archive and trash
// messages, but not permanently delete them or send mail.
const Scope = gmailapi.GmailModifyScope

const (
	user             = "me"
	maxBatchModify   = 1000 // Gmail's documented limit for batchModify
	maxListPageSize  = 500
	metadataWorkers  = 5
	labelCacheMaxAge = 2 * time.Minute
)

// systemLabels are the Gmail label IDs that equal their names.
var systemLabels = map[string]bool{
	"INBOX": true, "UNREAD": true, "STARRED": true, "IMPORTANT": true,
	"CATEGORY_PERSONAL": true, "CATEGORY_SOCIAL": true, "CATEGORY_PROMOTIONS": true,
	"CATEGORY_UPDATES": true, "CATEGORY_FORUMS": true, "TRASH": true, "SPAM": true,
}

// Label describes a Gmail label.
type Label struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Type string `json:"type"` // "system" or "user"
}

// Client talks to Gmail for one account.
type Client struct {
	svc *gmailapi.Service

	mu       sync.Mutex
	labels   map[string]string // lower-case name -> id
	loadedAt time.Time
	now      func() time.Time
}

var _ engine.MailProvider = (*Client)(nil)

// New builds a client. httpClient must already attach OAuth credentials (for
// example from oauth2.NewClient) and should carry the request timeout. Extra
// options (such as option.WithEndpoint for tests) are passed through.
func New(ctx context.Context, httpClient *http.Client, opts ...option.ClientOption) (*Client, error) {
	all := append([]option.ClientOption{option.WithHTTPClient(httpClient)}, opts...)
	svc, err := gmailapi.NewService(ctx, all...)
	if err != nil {
		return nil, fmt.Errorf("create gmail service: %w", err)
	}
	return &Client{svc: svc, now: time.Now}, nil
}

// NewFromTokenSource is a convenience that wraps ts in an authorizing client.
func NewFromTokenSource(ctx context.Context, ts oauth2.TokenSource, timeout time.Duration, opts ...option.ClientOption) (*Client, error) {
	hc := oauth2.NewClient(ctx, ts)
	hc.Timeout = timeout
	return New(ctx, hc, opts...)
}

// EmailAddress returns the authenticated mailbox address.
func (c *Client) EmailAddress(ctx context.Context) (string, error) {
	p, err := c.svc.Users.GetProfile(user).Context(ctx).Do()
	if err != nil {
		return "", classify(err, false)
	}
	return p.EmailAddress, nil
}

// ListMessages implements engine.MailProvider using Gmail's native search syntax.
func (c *Client) ListMessages(ctx context.Context, query, pageToken string, pageSize int) (engine.MessagePage, error) {
	if pageSize < 1 || pageSize > maxListPageSize {
		pageSize = maxListPageSize
	}
	call := c.svc.Users.Messages.List(user).Q(query).MaxResults(int64(pageSize)).Context(ctx)
	if pageToken != "" {
		call = call.PageToken(pageToken)
	}
	resp, err := call.Do()
	if err != nil {
		return engine.MessagePage{}, classify(err, true)
	}
	page := engine.MessagePage{NextPageToken: resp.NextPageToken, EstimatedTotal: int64(resp.ResultSizeEstimate)}
	for _, m := range resp.Messages {
		page.IDs = append(page.IDs, m.Id)
	}
	return page, nil
}

// GetSummaries fetches From/Subject/Date for each id. Messages that vanished
// since the search (404) are skipped. Order follows ids.
func (c *Client) GetSummaries(ctx context.Context, ids []string) ([]models.MessageSummary, error) {
	out := make([]*models.MessageSummary, len(ids))
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(metadataWorkers)
	for i, id := range ids {
		g.Go(func() error {
			m, err := c.svc.Users.Messages.Get(user, id).Format("metadata").
				MetadataHeaders("From", "Subject", "Date").Fields("id", "internalDate", "payload/headers").Context(gctx).Do()
			if err != nil {
				var ge *googleapi.Error
				if errors.As(err, &ge) && ge.Code == http.StatusNotFound {
					return nil
				}
				return classify(err, false)
			}
			s := models.MessageSummary{ID: m.Id}
			if m.Payload != nil {
				for _, h := range m.Payload.Headers {
					switch strings.ToLower(h.Name) {
					case "from":
						s.From = h.Value
					case "subject":
						s.Subject = h.Value
					case "date":
						if t, err := parseMailDate(h.Value); err == nil {
							s.Date = t
						}
					}
				}
			}
			if s.Date.IsZero() && m.InternalDate > 0 {
				s.Date = time.UnixMilli(m.InternalDate).UTC()
			}
			out[i] = &s
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}
	res := make([]models.MessageSummary, 0, len(ids))
	for _, s := range out {
		if s != nil {
			res = append(res, *s)
		}
	}
	return res, nil
}

func parseMailDate(v string) (time.Time, error) {
	v = strings.TrimSpace(v)
	// Some senders append a "(UTC)" style comment.
	if i := strings.Index(v, " ("); i > 0 {
		v = v[:i]
	}
	for _, layout := range []string{time.RFC1123Z, time.RFC1123, "Mon, 2 Jan 2006 15:04:05 -0700", "2 Jan 2006 15:04:05 -0700"} {
		if t, err := time.Parse(layout, v); err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("unrecognised date %q", v)
}

// Archive removes the INBOX label.
func (c *Client) Archive(ctx context.Context, ids []string) error {
	return c.modify(ctx, ids, nil, []string{"INBOX"})
}

// Trash moves messages to Trash (they are not permanently deleted).
func (c *Client) Trash(ctx context.Context, ids []string) error {
	return c.modify(ctx, ids, []string{"TRASH"}, []string{"INBOX"})
}

// MarkRead removes the UNREAD label.
func (c *Client) MarkRead(ctx context.Context, ids []string) error {
	return c.modify(ctx, ids, nil, []string{"UNREAD"})
}

// MarkUnread adds the UNREAD label.
func (c *Client) MarkUnread(ctx context.Context, ids []string) error {
	return c.modify(ctx, ids, []string{"UNREAD"}, nil)
}

// AddLabel adds a label by name, creating it if it does not exist.
func (c *Client) AddLabel(ctx context.Context, ids []string, label string) error {
	id, err := c.resolveLabel(ctx, label, true)
	if err != nil {
		return err
	}
	return c.modify(ctx, ids, []string{id}, nil)
}

// RemoveLabel removes a label by name. A label that does not exist is a no-op
// because no message can carry it.
func (c *Client) RemoveLabel(ctx context.Context, ids []string, label string) error {
	id, err := c.resolveLabel(ctx, label, false)
	if err != nil {
		return err
	}
	if id == "" {
		return nil
	}
	return c.modify(ctx, ids, nil, []string{id})
}

func (c *Client) modify(ctx context.Context, ids, add, remove []string) error {
	for start := 0; start < len(ids); start += maxBatchModify {
		chunk := ids[start:min(start+maxBatchModify, len(ids))]
		err := c.svc.Users.Messages.BatchModify(user, &gmailapi.BatchModifyMessagesRequest{
			Ids: chunk, AddLabelIds: add, RemoveLabelIds: remove,
		}).Context(ctx).Do()
		if err != nil {
			return classify(err, false)
		}
	}
	return nil
}

// Labels lists system and user labels sorted by name.
func (c *Client) Labels(ctx context.Context) ([]Label, error) {
	resp, err := c.svc.Users.Labels.List(user).Context(ctx).Do()
	if err != nil {
		return nil, classify(err, false)
	}
	out := make([]Label, 0, len(resp.Labels))
	for _, l := range resp.Labels {
		out = append(out, Label{ID: l.Id, Name: l.Name, Type: strings.ToLower(l.Type)})
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	return out, nil
}

// resolveLabel maps a label name to its ID. With create it makes the label when
// missing; otherwise it returns "" for unknown labels.
func (c *Client) resolveLabel(ctx context.Context, name string, create bool) (string, error) {
	name = strings.TrimSpace(name)
	if systemLabels[strings.ToUpper(name)] {
		return strings.ToUpper(name), nil
	}
	key := strings.ToLower(name)
	if id := c.cachedLabel(key); id != "" {
		return id, nil
	}
	if err := c.reloadLabels(ctx); err != nil {
		return "", err
	}
	if id := c.cachedLabel(key); id != "" {
		return id, nil
	}
	if !create {
		return "", nil
	}
	return c.createLabel(ctx, name)
}

// createLabel makes a label, creating missing parents of nested names ("A/B/C") first so
// Gmail shows the hierarchy. A concurrent creation by someone else counts as success.
func (c *Client) createLabel(ctx context.Context, name string) (string, error) {
	if i := strings.LastIndex(name, "/"); i > 0 {
		parent := name[:i]
		if c.cachedLabel(strings.ToLower(parent)) == "" {
			if _, err := c.createLabel(ctx, parent); err != nil {
				return "", err
			}
		}
	}
	key := strings.ToLower(name)
	l, err := c.svc.Users.Labels.Create(user, &gmailapi.Label{
		Name: name, LabelListVisibility: "labelShow", MessageListVisibility: "show",
	}).Context(ctx).Do()
	if err != nil {
		var ge *googleapi.Error
		if errors.As(err, &ge) && ge.Code == http.StatusConflict {
			// Created concurrently elsewhere; pick it up.
			if rerr := c.reloadLabels(ctx); rerr != nil {
				return "", rerr
			}
			if id := c.cachedLabel(key); id != "" {
				return id, nil
			}
		}
		return "", classify(err, false)
	}
	c.mu.Lock()
	if c.labels == nil {
		c.labels = map[string]string{}
	}
	c.labels[key] = l.Id
	c.mu.Unlock()
	return l.Id, nil
}

// EnsureLabels creates every label in names that does not exist yet (names are matched
// case-insensitively, as Gmail does) and returns the ones it created. Existing labels are
// left untouched, so the call is safe to repeat.
func (c *Client) EnsureLabels(ctx context.Context, names []string) ([]string, error) {
	if err := c.reloadLabels(ctx); err != nil {
		return nil, err
	}
	var created []string
	for _, name := range names {
		if c.cachedLabel(strings.ToLower(name)) != "" {
			continue
		}
		if _, err := c.createLabel(ctx, name); err != nil {
			return created, err
		}
		created = append(created, name)
	}
	return created, nil
}

func (c *Client) cachedLabel(key string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.labels == nil || c.now().Sub(c.loadedAt) > labelCacheMaxAge {
		return ""
	}
	return c.labels[key]
}

func (c *Client) reloadLabels(ctx context.Context) error {
	resp, err := c.svc.Users.Labels.List(user).Context(ctx).Do()
	if err != nil {
		return classify(err, false)
	}
	m := make(map[string]string, len(resp.Labels))
	for _, l := range resp.Labels {
		m[strings.ToLower(l.Name)] = l.Id
	}
	c.mu.Lock()
	c.labels, c.loadedAt = m, c.now()
	c.mu.Unlock()
	return nil
}

// classify converts Gmail/OAuth/network errors into the engine's error taxonomy.
func classify(err error, isSearch bool) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) {
		return err
	}
	var re *oauth2.RetrieveError
	if errors.As(err, &re) {
		if re.ErrorCode == "invalid_grant" || re.ErrorCode == "unauthorized_client" || re.Response != nil && re.Response.StatusCode == http.StatusBadRequest {
			return fmt.Errorf("%w: %s", engine.ErrAuthRevoked, re.ErrorCode)
		}
		return engine.Retryable(err, 0)
	}
	var ge *googleapi.Error
	if errors.As(err, &ge) {
		reason := ""
		if len(ge.Errors) > 0 {
			reason = ge.Errors[0].Reason
		}
		after := retryAfter(ge.Header)
		switch {
		case ge.Code == http.StatusUnauthorized:
			return fmt.Errorf("%w: gmail rejected credentials", engine.ErrAuthRevoked)
		case ge.Code == http.StatusTooManyRequests,
			ge.Code == http.StatusForbidden && (strings.Contains(reason, "ateLimit") || strings.Contains(reason, "uota")):
			return engine.Retryable(fmt.Errorf("gmail rate limited (%s)", reason), max(after, 5*time.Second))
		case ge.Code >= 500:
			return engine.Retryable(fmt.Errorf("gmail server error %d", ge.Code), after)
		case ge.Code == http.StatusBadRequest && isSearch:
			return fmt.Errorf("%w: %s", engine.ErrInvalidQuery, ge.Message)
		case ge.Code == http.StatusForbidden && (reason == "insufficientPermissions" || strings.Contains(strings.ToLower(ge.Message), "insufficient authentication scopes")):
			// The token lacks the Gmail permission (for example it was granted partially or reduced
			// later): only reconnecting can fix it, so treat it like a revoked authorization.
			return fmt.Errorf("%w: the connection is missing the required Gmail permission", engine.ErrAuthRevoked)
		case ge.Code == http.StatusForbidden:
			return fmt.Errorf("gmail denied the request (%s): %s", reason, ge.Message)
		}
		return fmt.Errorf("gmail error %d: %s", ge.Code, ge.Message)
	}
	var ue *url.Error
	var ne net.Error
	if errors.As(err, &ue) || errors.As(err, &ne) || errors.Is(err, context.DeadlineExceeded) {
		return engine.Retryable(err, 0)
	}
	return err
}

func retryAfter(h http.Header) time.Duration {
	if h == nil {
		return 0
	}
	if secs, err := strconv.Atoi(h.Get("Retry-After")); err == nil && secs > 0 {
		return time.Duration(secs) * time.Second
	}
	return 0
}
