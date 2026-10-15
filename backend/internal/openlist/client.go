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
)

const MaxResponseBytes = 2 << 20

var (
	ErrUnavailable = errors.New("OpenList directory is unavailable")
	ErrRestricted  = errors.New("OpenList directory requires access permission")
	ErrLimit       = errors.New("OpenList directory exceeds the finite scan limit")
	ErrInvalid     = errors.New("OpenList returned invalid directory data")
	ErrMaintenance = errors.New("OpenList source is under maintenance")
)

type Client struct {
	endpoint         string
	http             *http.Client
	maxResponseBytes int64
}
type Object struct {
	Name     string          `json:"name"`
	Size     int64           `json:"size"`
	IsDir    bool            `json:"is_dir"`
	Type     int             `json:"type"`
	Modified string          `json:"modified"`
	RawURL   string          `json:"raw_url"`
	HashInfo json.RawMessage `json:"hash_info"`
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
	return &Client{endpoint: strings.TrimRight(endpoint, "/"), http: &copyClient, maxResponseBytes: MaxResponseBytes}
}

// WithResponseLimit sets a smaller or larger operator-selected metadata bound.
// Callers validate the setting before constructing a client.
func (c *Client) WithResponseLimit(n int64) *Client { c.maxResponseBytes = n; return c }

type Page struct {
	Content []Object `json:"content"`
	Total   int      `json:"total"`
}

// ListPage reads exactly one anonymous page, leaving traversal/checkpoints to
// the caller. A missing total or invalid name cannot prove directory coverage.
func (c *Client) ListPage(ctx context.Context, root string, page, pageSize int) (Page, error) {
	data := Page{Total: -1}
	if (root != "/" && !ValidRoot(root)) || page < 1 || pageSize < 1 || pageSize > 500 {
		return data, ErrInvalid
	}
	err := c.request(ctx, "list", map[string]any{"path": root, "page": page, "per_page": pageSize, "password": "", "refresh": false}, &data)
	if err != nil {
		return data, err
	}
	if data.Total < 0 || len(data.Content) > pageSize {
		return data, ErrInvalid
	}
	for _, obj := range data.Content {
		if obj.Name == "" || obj.Name == "." || obj.Name == ".." || len(obj.Name) > 1024 || strings.ContainsAny(obj.Name, "/\\\x00\r\n") || obj.Size < 0 {
			return data, ErrInvalid
		}
	}
	return data, nil
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
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == 401 || resp.StatusCode == 403 {
		return ErrRestricted
	}
	if resp.StatusCode == http.StatusServiceUnavailable {
		return errors.Join(ErrUnavailable, ErrMaintenance)
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
