package openlist

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"strings"
	"testing"
	"time"

	"github.com/yexca/kikoto/backend/internal/buildinfo"
)

// TestOpenListIdentitySelfCheck is an explicit operator-run probe of ONE
// anonymous source. It verifies that the client's declared identity is accepted
// for a directory listing, one file resolution and two 32-byte range reads, and
// prints only sanitized evidence. It skips unless the operator supplies
// KIKOTO_OPENLIST_SELFCHECK_URL, so routine test runs never contact a network.
// This is a dependency check for the identity fix, not playback acceptance.
func TestOpenListIdentitySelfCheck(t *testing.T) {
	endpoint := strings.TrimSpace(os.Getenv("KIKOTO_OPENLIST_SELFCHECK_URL"))
	if endpoint == "" {
		t.Skip("set KIKOTO_OPENLIST_SELFCHECK_URL to probe one authorized anonymous source")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	client := NewClient(endpoint, &http.Client{Timeout: 30 * time.Second})
	page, err := client.ListPage(ctx, "/", 1, 100)
	if err != nil {
		t.Fatalf("identity=%s stage=list category=%s", buildinfo.UserAgent(), selfCheckCategory(err))
	}
	t.Logf("identity=%s stage=list status=ok total=%d entries=%d", buildinfo.UserAgent(), page.Total, len(page.Content))

	file := strings.TrimSpace(os.Getenv("KIKOTO_OPENLIST_SELFCHECK_FILE"))
	if file == "" {
		file = selfCheckFindAudio(ctx, client, "/", page, 6)
	}
	if file == "" {
		t.Skip("no audio file found within the bounded directory walk")
	}
	object, err := client.Get(ctx, file)
	if err != nil {
		t.Fatalf("stage=get category=%s", selfCheckCategory(err))
	}
	t.Logf("stage=get status=ok size=%d", object.Size)
	if object.Size < 1056 {
		t.Skip("sample too short for the requested offsets")
	}
	for _, header := range []string{"bytes=0-31", "bytes=1024-1055"} {
		status, length, digest, err := selfCheckRange(ctx, object.RawURL, header)
		if err != nil {
			t.Fatalf("stage=media category=%s", selfCheckCategory(err))
		}
		t.Logf("stage=media range=%s status=%d bytes=%d sha256=%s", header, status, length, digest)
		if status != http.StatusPartialContent || length != 32 {
			t.Fatalf("stage=media range=%s status=%d bytes=%d", header, status, length)
		}
	}
}

func selfCheckFindAudio(ctx context.Context, client *Client, root string, page Page, budget int) string {
	for _, entry := range page.Content {
		if Kind(entry.Name, entry.IsDir) == "audio" {
			return path.Join(root, entry.Name)
		}
	}
	if budget <= 0 {
		return ""
	}
	for _, entry := range page.Content {
		if !entry.IsDir || strings.HasPrefix(entry.Name, ".") {
			continue
		}
		next, err := client.ListPage(ctx, path.Join(root, entry.Name), 1, 100)
		if err != nil {
			continue
		}
		if found := selfCheckFindAudio(ctx, client, path.Join(root, entry.Name), next, budget-1); found != "" {
			return found
		}
	}
	return ""
}

func selfCheckRange(ctx context.Context, rawURL, header string) (int, int, string, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return 0, 0, "", err
	}
	request.Header.Set("Range", header)
	request.Header.Set("User-Agent", buildinfo.UserAgent())
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return 0, 0, "", err
	}
	defer func() { _ = response.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(response.Body, 33))
	if err != nil {
		return response.StatusCode, len(body), "", err
	}
	sum := sha256.Sum256(body)
	return response.StatusCode, len(body), hex.EncodeToString(sum[:]), nil
}

func selfCheckCategory(err error) string {
	switch {
	case err == nil:
		return "ok"
	case isErr(err, ErrChallenged):
		return "challenged"
	case isErr(err, ErrRestricted):
		return "restricted"
	case isErr(err, ErrLimit):
		return "limit_exceeded"
	case isErr(err, ErrInvalid):
		return "invalid_response"
	case isErr(err, ErrUnavailable):
		return "unavailable"
	case isErr(err, context.DeadlineExceeded):
		return "timeout"
	default:
		return fmt.Sprintf("transport")
	}
}

func isErr(err, target error) bool {
	for err != nil {
		if err == target {
			return true
		}
		unwrapped, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = unwrapped.Unwrap()
	}
	return false
}
