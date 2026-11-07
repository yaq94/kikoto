package httpapi

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestContentTranslationVersionFollowsInFlightConfiguration(t *testing.T) {
	for _, batch := range []bool{false, true} {
		t.Run(map[bool]string{false: "single", true: "batch"}[batch], func(t *testing.T) {
			started, release := make(chan string, 1), make(chan struct{})
			model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var in struct{ Model string }
				if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
					t.Error(err)
					return
				}
				if in.Model == "Model-A" {
					started <- in.Model
					<-release
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": "译文 " + in.Model}, "finish_reason": "stop"}}})
			}))
			defer model.Close()
			defer func() {
				select {
				case <-release:
				default:
					close(release)
				}
			}()
			s, host, admin, listener := translationHTTPFixture(t)
			metadataReviewExec(t, s.db, `INSERT INTO work(id,primary_code,title) VALUES(1,'RJ00000000','Example title')`)
			settings := mustJSON(map[string]any{"baseUrl": model.URL, "model": "Model-A", "minFreeBytes": 0})
			translationRequest(t, host, admin, "PATCH", "/api/settings/translation", settings, 200)
			versionA := translationRequest(t, host, listener, "GET", "/api/content-translations/version", "", 200)["version"]
			path, body := "/api/content-translations", `{"workId":1,"field":"title","text":"Example title","language":"zh-cn"}`
			if batch {
				path += "/batch"
				body = `{"language":"zh-cn","items":[{"key":"a","workId":1,"field":"title","text":"Example title"}]}`
			}
			type response struct {
				status int
				body   []byte
				err    error
			}
			done := make(chan response, 1)
			go func() {
				req, _ := http.NewRequest("POST", host.URL+path, strings.NewReader(body))
				req.AddCookie(listener)
				req.Header.Set("Content-Type", "application/json")
				res, err := host.Client().Do(req)
				if err != nil {
					done <- response{err: err}
					return
				}
				defer func() { _ = res.Body.Close() }()
				data, err := io.ReadAll(res.Body)
				done <- response{res.StatusCode, data, err}
			}()
			select {
			case <-started:
			case <-time.After(5 * time.Second):
				t.Fatal("model request did not start")
			}
			translationRequest(t, host, admin, "PATCH", "/api/settings/translation", `{"model":"Model-B"}`, 200)
			versionB := translationRequest(t, host, listener, "GET", "/api/content-translations/version", "", 200)["version"]
			if versionA == versionB {
				t.Fatal("changing the model did not change its version")
			}
			close(release)
			var old map[string]any
			select {
			case result := <-done:
				if result.err != nil || result.status != 200 {
					t.Fatalf("in-flight response: %+v", result)
				}
				if err := json.Unmarshal(result.body, &old); err != nil {
					t.Fatal(err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("in-flight response did not finish")
			}
			unwrap := func(result map[string]any) map[string]any {
				if batch {
					return result["items"].([]any)[0].(map[string]any)
				}
				return result
			}
			if result := unwrap(old); result["version"] != versionA || result["text"] != "译文 Model-A" {
				t.Errorf("Model-A response mislabeled: %#v; want version %v", result, versionA)
			}
			for range 2 {
				result := unwrap(translationRequest(t, host, listener, "POST", path, body, 200))
				if result["version"] != versionB || result["text"] != "译文 Model-B" {
					t.Fatalf("current generation or cache returned old text: %#v", result)
				}
			}
		})
	}
}
