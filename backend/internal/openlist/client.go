// Package openlist reads a bounded anonymous file catalog. It neither registers
// works nor downloads media; HTTP policy and pacing are supplied by the caller.
package openlist

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/yexca/kikoto/backend/internal/buildinfo"
)

const MaxResponseBytes = 2 << 20

var (
	ErrUnavailable = errors.New("OpenList directory is unavailable")
	ErrRestricted  = errors.New("OpenList directory requires access permission")
	// ErrChallenged is a browser-interactive anti-bot challenge, not a
	// permission decision. Callers must not treat it as a business response and
	// must not retry it tightly; the origin gate applies its normal backoff.
	ErrChallenged  = errors.New("OpenList directory answered with an interactive challenge")
	ErrLimit       = errors.New("OpenList directory exceeds the finite scan limit")
	ErrInvalid     = errors.New("OpenList returned invalid directory data")
	ErrMaintenance = errors.New("OpenList source is under maintenance")
	// ErrNotFound is a definitive "this object is not there" answer. It is
	// separate from ErrUnavailable so a caller can stop retrying a file that
	// the release deleted instead of treating it like a temporary outage.
	ErrNotFound = errors.New("OpenList object was not found")
)

type Client struct {
	endpoint         string
	http             *http.Client
	maxResponseBytes int64
	userAgent        string
	requestHeaders   map[string]string
}
type Object struct {
	Name     string          `json:"name"`
	Size     int64           `json:"size"`
	IsDir    bool            `json:"is_dir"`
	Type     int             `json:"type"`
	Modified string          `json:"modified"`
	RawURL   string          `json:"raw_url"`
	HashInfo json.RawMessage `json:"hash_info"`
	// NameDamaged marks a listed name whose JSON string was not valid UTF-8.
	// Name preserves those bytes unchanged; callers must not build a source
	// path from it, because the character the origin intended is unknown.
	NameDamaged bool `json:"-"`
}
type File struct {
	Path  string
	Title string
	Kind  string
	Size  int64
}

func NewClient(endpoint string, client *http.Client) *Client {
	copyClient := *client
	// Metadata POST bodies contain source paths; do not forward them elsewhere.
	copyClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	// An explicit identity is required: the Go default User-Agent is rejected by
	// common anonymous-gateway bot rules even when the request is legitimate and
	// no other header changes. The transport itself stays caller-supplied; the
	// observed refusal was agent-identity based, not a connection or HTTP/2
	// issue, so no transport setting is changed here.
	return &Client{
		endpoint:         strings.TrimRight(endpoint, "/"),
		http:             &copyClient,
		maxResponseBytes: MaxResponseBytes,
		userAgent:        buildinfo.UserAgent(),
	}
}

// WithResponseLimit sets a smaller or larger operator-selected metadata bound.
// Callers validate the setting before constructing a client.
func (c *Client) WithResponseLimit(n int64) *Client { c.maxResponseBytes = n; return c }

// WithUserAgent replaces the declared identity for this client. An empty value
// keeps the default rather than falling back to the Go default identity.
func (c *Client) WithUserAgent(value string) *Client {
	if trimmed := strings.TrimSpace(value); trimmed != "" && ValidHeaderValue(trimmed) {
		c.userAgent = trimmed
	}
	return c
}

// WithRequestHeaders adds operator-configured request headers. Entries that
// could change transport semantics (connection handling, framing, credentials,
// the declared identity) are dropped here as a second line of defense; the
// configuration layer rejects them with a readable error first.
func (c *Client) WithRequestHeaders(headers map[string]string) *Client {
	for name, value := range headers {
		canonical := http.CanonicalHeaderKey(strings.TrimSpace(name))
		if !ValidHeaderName(canonical) || !AllowedConfiguredHeader(canonical) || !ValidHeaderValue(value) {
			continue
		}
		if c.requestHeaders == nil {
			c.requestHeaders = map[string]string{}
		}
		c.requestHeaders[canonical] = value
	}
	return c
}

// ForbiddenHeader explains that a header a source configuration can never set.
// User-Agent has a dedicated configuration field; connection, framing, proxy,
// cookie and encoding headers belong to the transport.
func ForbiddenHeader(name string) bool {
	switch name {
	case "Host", "Content-Length", "Connection", "Transfer-Encoding", "Upgrade",
		"Proxy-Authorization", "Proxy-Connection", "Te", "Trailer", "Expect",
		"Cookie", "Set-Cookie", "User-Agent", "Content-Type", "Accept-Encoding",
		"Accept", "Authorization":
		return true
	}
	return false
}

// AllowedConfiguredHeader is the whitelist for operator-configurable headers:
// custom X- headers plus Accept-Language, and nothing the client owns.
func AllowedConfiguredHeader(name string) bool {
	if ForbiddenHeader(name) {
		return false
	}
	return strings.HasPrefix(name, "X-") || name == "Accept-Language"
}

// ValidHeaderName accepts an RFC 7230 token within the configured length bound.
func ValidHeaderName(name string) bool {
	if name == "" || len(name) > 64 {
		return false
	}
	for index := 0; index < len(name); index++ {
		character := name[index]
		switch {
		case character >= 'a' && character <= 'z',
			character >= 'A' && character <= 'Z',
			character >= '0' && character <= '9':
		case strings.ContainsRune("!#$%&'*+-.^_`|~", rune(character)):
		default:
			return false
		}
	}
	return true
}

// ValidHeaderValue accepts visible ASCII plus horizontal tab, and refuses line
// breaks or control characters that could inject additional headers.
func ValidHeaderValue(value string) bool {
	if value == "" || len(value) > 512 {
		return false
	}
	for _, character := range value {
		if character == '\t' {
			continue
		}
		if character < 0x20 || character > 0x7e {
			return false
		}
	}
	return true
}

const challengeProbeBytes = 8 << 10

// isChallengeStatus limits challenge inspection to the two refusal statuses an
// anonymous gateway uses. Other statuses keep their existing classification.
func isChallengeStatus(status int) bool {
	return status == http.StatusForbidden || status == http.StatusTooManyRequests
}

// isChallengeResponse reports an interactive anti-bot challenge rather than a
// permission decision. The explicit Cf-Mitigated marker is authoritative; when
// it is absent, a bounded prefix of an HTML refusal is inspected for the
// documented Cloudflare interstitial markers. The challenge page is never
// parsed as business JSON, and detection never retries.
func isChallengeResponse(resp *http.Response) bool {
	if strings.EqualFold(strings.TrimSpace(resp.Header.Get("Cf-Mitigated")), "challenge") {
		return true
	}
	if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(resp.Header.Get("Content-Type"))), "text/html") {
		return false
	}
	peek, err := io.ReadAll(io.LimitReader(resp.Body, challengeProbeBytes))
	if err != nil && len(peek) == 0 {
		return false
	}
	lowercase := strings.ToLower(string(peek))
	for _, marker := range []string{
		"challenge-platform",
		"cf-chl",
		"cf-mitigated",
		"just a moment",
		"attention required",
		"enable javascript and cookies",
	} {
		if strings.Contains(lowercase, marker) {
			return true
		}
	}
	return false
}

type Page struct {
	Content []Object `json:"content"`
	Total   int      `json:"total"`
}

// listObject keeps the name as raw JSON so an invalid byte survives the
// decode. encoding/json would silently replace it with U+FFFD, which is
// indistinguishable from a replacement character the source actually sent.
type listObject struct {
	Name     json.RawMessage `json:"name"`
	Size     int64           `json:"size"`
	IsDir    bool            `json:"is_dir"`
	Type     int             `json:"type"`
	Modified string          `json:"modified"`
	RawURL   string          `json:"raw_url"`
	HashInfo json.RawMessage `json:"hash_info"`
}

type listPage struct {
	Content []listObject `json:"content"`
	Total   *int         `json:"total"`
}

// ListPage reads exactly one anonymous page, leaving traversal/checkpoints to
// the caller. A missing total or invalid name cannot prove directory coverage.
func (c *Client) ListPage(ctx context.Context, root string, page, pageSize int) (Page, error) {
	data := Page{Total: -1}
	if (root != "/" && !ValidRoot(root)) || page < 1 || pageSize < 1 || pageSize > 500 {
		return data, ErrInvalid
	}
	var raw listPage
	err := c.request(ctx, "list", map[string]any{"path": root, "page": page, "per_page": pageSize, "password": "", "refresh": false}, &raw)
	if err != nil {
		return data, err
	}
	if raw.Total == nil || *raw.Total < 0 || len(raw.Content) > pageSize {
		return data, ErrInvalid
	}
	data.Total = *raw.Total
	data.Content = make([]Object, 0, len(raw.Content))
	for _, entry := range raw.Content {
		name, valid, decodeErr := decodeJSONName(entry.Name)
		if decodeErr != nil {
			return Page{Total: -1}, ErrInvalid
		}
		data.Content = append(data.Content, Object{
			Name: name, Size: entry.Size, IsDir: entry.IsDir, Type: entry.Type,
			Modified: entry.Modified, RawURL: entry.RawURL, HashInfo: entry.HashInfo, NameDamaged: !valid,
		})
	}
	for _, obj := range data.Content {
		if obj.Name == "" || obj.Name == "." || obj.Name == ".." || len(obj.Name) > 1024 || strings.ContainsAny(obj.Name, "/\\\x00\r\n") || obj.Size < 0 {
			return Page{Total: -1}, ErrInvalid
		}
	}
	return data, nil
}

// decodeJSONName unquotes one JSON string without passing it through
// encoding/json's UTF-8 repair. The returned flag is false when the string
// held an invalid byte or a lone surrogate; the bytes themselves are kept.
func decodeJSONName(raw []byte) (string, bool, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) < 2 || raw[0] != '"' || raw[len(raw)-1] != '"' {
		return "", false, ErrInvalid
	}
	src := raw[1 : len(raw)-1]
	out := make([]byte, 0, len(src))
	valid := true
	for index := 0; index < len(src); index++ {
		if src[index] != '\\' {
			out = append(out, src[index])
			continue
		}
		index++
		if index >= len(src) {
			return "", false, ErrInvalid
		}
		switch src[index] {
		case '"', '\\', '/':
			out = append(out, src[index])
		case 'b':
			out = append(out, '\b')
		case 'f':
			out = append(out, '\f')
		case 'n':
			out = append(out, '\n')
		case 'r':
			out = append(out, '\r')
		case 't':
			out = append(out, '\t')
		case 'u':
			if index+4 >= len(src) {
				return "", false, ErrInvalid
			}
			r, ok := decodeHexRune(src[index+1 : index+5])
			if !ok {
				return "", false, ErrInvalid
			}
			index += 4
			if utf16.IsSurrogate(r) {
				if index+6 < len(src) && src[index+1] == '\\' && src[index+2] == 'u' {
					if second, secondOK := decodeHexRune(src[index+3 : index+7]); secondOK {
						if pair := utf16.DecodeRune(r, second); pair != utf8.RuneError {
							out = utf8.AppendRune(out, pair)
							index += 6
							continue
						}
					}
				}
				// A lone surrogate cannot be represented in UTF-8.
				valid = false
				out = utf8.AppendRune(out, utf8.RuneError)
				continue
			}
			out = utf8.AppendRune(out, r)
		default:
			return "", false, ErrInvalid
		}
	}
	if !utf8.Valid(out) {
		valid = false
	}
	return string(out), valid, nil
}

func decodeHexRune(raw []byte) (rune, bool) {
	if len(raw) != 4 {
		return 0, false
	}
	var value rune
	for _, digit := range raw {
		value <<= 4
		switch {
		case digit >= '0' && digit <= '9':
			value |= rune(digit - '0')
		case digit >= 'a' && digit <= 'f':
			value |= rune(digit-'a') + 10
		case digit >= 'A' && digit <= 'F':
			value |= rune(digit-'A') + 10
		default:
			return 0, false
		}
	}
	return value, true
}

func ValidRoot(root string) bool {
	return strings.HasPrefix(root, "/") && root != "/" && path.Clean(root) == root && len(root) <= 4096 && !strings.ContainsAny(root, "\\\x00\r\n")
}

func (c *Client) request(ctx context.Context, operation string, input, output any) error {
	endpoint, err := url.Parse(c.endpoint)
	if err != nil || endpoint.User != nil || (endpoint.Scheme != "https" && endpoint.Scheme != "http") || endpoint.Host == "" {
		return ErrInvalid
	}
	endpoint.Path = strings.TrimRight(endpoint.Path, "/") + "/api/fs/" + operation
	endpoint.RawQuery, endpoint.Fragment = "", ""
	body, err := json.Marshal(input)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), bytes.NewReader(body))
	if err != nil {
		return ErrInvalid
	}
	req.Header.Set("User-Agent", c.userAgent)
	req.Header.Set("Accept", "application/json")
	for name, value := range c.requestHeaders {
		req.Header.Set(name, value)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if isChallengeStatus(resp.StatusCode) && isChallengeResponse(resp) {
		return ErrChallenged
	}
	if resp.StatusCode == 401 || resp.StatusCode == 403 {
		return ErrRestricted
	}
	if resp.StatusCode == http.StatusServiceUnavailable {
		return errors.Join(ErrUnavailable, ErrMaintenance)
	}
	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone {
		return ErrNotFound
	}
	if resp.StatusCode != http.StatusOK {
		return ErrUnavailable
	}
	if resp.ContentLength > c.maxResponseBytes {
		return ErrLimit
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, c.maxResponseBytes+1))
	if err != nil {
		return err
	}
	if int64(len(raw)) > c.maxResponseBytes {
		return ErrLimit
	}
	var envelope struct {
		Code int             `json:"code"`
		Data json.RawMessage `json:"data"`
	}
	if json.Unmarshal(raw, &envelope) != nil {
		return ErrInvalid
	}
	if envelope.Code == 401 || envelope.Code == 403 {
		return ErrRestricted
	}
	if envelope.Code != 200 {
		return ErrUnavailable
	}
	if json.Unmarshal(envelope.Data, output) != nil {
		return ErrInvalid
	}
	return nil
}

func (c *Client) Get(ctx context.Context, fullPath string) (Object, error) {
	var object Object
	err := c.request(ctx, "get", map[string]any{"path": fullPath, "password": ""}, &object)
	if err == nil && (object.IsDir || object.Name != path.Base(fullPath) || object.RawURL == "") {
		return Object{}, ErrInvalid
	}
	return object, err
}

// HTTP exposes the configured client for a bounded companion fetch, such as a
// text sidecar reached through a raw URL returned by Get. The transport still
// carries the caller's outbound policy, redirect rules and request pacing.
func (c *Client) HTTP() *http.Client {
	return c.http
}

// GetText reads a bounded text body from a URL the source itself returned. It
// applies the same declared identity and operator headers as metadata calls;
// the caller keeps the URL inside the configured outbound policy.
func (c *Client) GetText(ctx context.Context, rawURL string, limit int64) (string, error) {
	if rawURL == "" || limit <= 0 {
		return "", ErrInvalid
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", ErrInvalid
	}
	request.Header.Set("User-Agent", c.userAgent)
	for name, value := range c.requestHeaders {
		request.Header.Set(name, value)
	}
	response, err := c.http.Do(request)
	if err != nil {
		return "", err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode == http.StatusNotFound || response.StatusCode == http.StatusGone {
		return "", ErrNotFound
	}
	if response.StatusCode != http.StatusOK {
		return "", ErrUnavailable
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		return "", err
	}
	if int64(len(body)) > limit {
		return "", ErrLimit
	}
	return string(body), nil
}

// ScanRoot treats one explicitly configured directory as one work. Format and
// track directories below it cannot accidentally become additional works.
func (c *Client) ScanRoot(ctx context.Context, root string) ([]File, error) {
	if !ValidRoot(root) {
		return nil, ErrInvalid
	}
	type directory struct {
		path  string
		depth int
	}
	queue := []directory{{root, 0}}
	files := []File{}
	seen := map[string]bool{}
	pages := 0
	for index := 0; index < len(queue); index++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if len(queue) > 32 || queue[index].depth > 8 {
			return nil, ErrLimit
		}
		dir := queue[index]
		count, expected := 0, -1
		for page := 1; ; page++ {
			pages++
			if pages > 100 {
				return nil, ErrLimit
			}
			var data struct {
				Content []Object `json:"content"`
				Total   int      `json:"total"`
			}
			if err := c.request(ctx, "list", map[string]any{"path": dir.path, "page": page, "per_page": 100, "password": "", "refresh": false}, &data); err != nil {
				return nil, err
			}
			if data.Total < 0 || data.Total > 2000 || (expected >= 0 && expected != data.Total) {
				return nil, ErrLimit
			}
			expected = data.Total
			if len(data.Content) == 0 && count < expected {
				return nil, ErrInvalid
			}
			for _, obj := range data.Content {
				if obj.Name == "" || obj.Name == "." || obj.Name == ".." || strings.ContainsAny(obj.Name, "/\\\x00\r\n") || len(obj.Name) > 1024 || obj.Size < 0 {
					return nil, ErrInvalid
				}
				fullPath := path.Join(dir.path, obj.Name)
				if seen[fullPath] {
					return nil, ErrInvalid
				}
				seen[fullPath] = true
				if len(seen) > 2000 {
					return nil, ErrLimit
				}
				kind := Kind(obj.Name, obj.IsDir)
				files = append(files, File{Path: strings.TrimPrefix(fullPath, root+"/"), Title: obj.Name, Kind: kind, Size: obj.Size})
				if obj.IsDir {
					queue = append(queue, directory{fullPath, dir.depth + 1})
				}
			}
			count += len(data.Content)
			if count >= expected {
				break
			}
		}
	}
	return files, nil
}

func Kind(name string, directory bool) string {
	if directory {
		return "folder"
	}
	switch strings.ToLower(path.Ext(name)) {
	case ".mp3", ".wav", ".flac", ".m4a", ".aac", ".ogg", ".opus", ".wma", ".aiff", ".aif":
		return "audio"
	case ".srt", ".vtt", ".lrc", ".ass", ".txt":
		return "text"
	default:
		return "other"
	}
}

// Dirs returns the direct subdirectories of one directory. The OpenList
// endpoint is intentionally shallow: callers that need a whole tree walk it
// one level at a time and treat each response as the metadata for exactly
// that parent. Modified is the child's own mtime; it is not inherited from
// anything below the child.
func (c *Client) Dirs(ctx context.Context, root string) ([]Object, error) {
	if (root != "/" && !ValidRoot(root)) || root == "" {
		return nil, ErrInvalid
	}
	var data json.RawMessage
	if err := c.request(ctx, "dirs", map[string]any{"path": root, "password": ""}, &data); err != nil {
		return nil, err
	}
	objects, err := decodeDirObjects(data)
	if err != nil {
		return nil, err
	}
	for i := range objects {
		obj := &objects[i]
		if obj.Name == "" || !ValidRoot("/"+obj.Name) || obj.Size < 0 {
			return nil, ErrInvalid
		}
		// Some OpenList builds omit is_dir on this endpoint because the
		// endpoint only returns directories. The path validation above still
		// proves the name is one direct child.
		obj.IsDir = true
	}
	return objects, nil
}

func decodeDirObjects(raw json.RawMessage) ([]Object, error) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return []Object{}, nil
	}
	var objects []Object
	if strings.HasPrefix(trimmed, "[") {
		if json.Unmarshal(raw, &objects) != nil {
			return nil, ErrInvalid
		}
		return objects, nil
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return nil, ErrInvalid
	}
	if _, ok := fields["content"]; !ok {
		// An unrecognized object is not an empty directory. Treating it as
		// empty would let the incremental sync mark every child directory as
		// deleted.
		return nil, ErrInvalid
	}
	var envelope struct {
		Content []Object `json:"content"`
	}
	if json.Unmarshal(raw, &envelope) != nil || envelope.Content == nil {
		return nil, ErrInvalid
	}
	return envelope.Content, nil
}
