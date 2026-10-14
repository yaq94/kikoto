package httpapi

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yexca/kikoto/backend/internal/config"
)

// Translation tests exercise the production routes over HTTP with real sessions
// and the migrated SQLite database. Only the external model is replaced.
func translationHTTPFixture(t *testing.T) (*Server, *httptest.Server, *http.Cookie, *http.Cookie) {
	t.Helper()
	s := NewServer(openMigratedTestDB(t), config.Config{Mode: config.ModeProduction, RootUsername: "root"})
	createTestAdministrator(t, s, "root", "root-password")
	createAccountManagedTestUser(t, s, "listener", "listener-password")
	host := httptest.NewServer(s.Routes())
	t.Cleanup(host.Close)
	login := func(username, password string) *http.Cookie {
		res, err := host.Client().Post(host.URL+"/api/auth/login", "application/json", strings.NewReader(`{"username":"`+username+`","password":"`+password+`"}`))
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = res.Body.Close() }()
		if res.StatusCode != http.StatusOK {
			t.Fatalf("login status = %d", res.StatusCode)
		}
		for _, cookie := range res.Cookies() {
			if cookie.Value != "" {
				return cookie
			}
		}
		t.Fatal("login returned no cookie")
		return nil
	}
	return s, host, login("root", "root-password"), login("listener", "listener-password")
}

func translationRequest(t *testing.T, host *httptest.Server, cookie *http.Cookie, method, path, body string, status int) map[string]any {
	t.Helper()
	req, err := http.NewRequest(method, host.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if cookie != nil {
		req.AddCookie(cookie)
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := host.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	data, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != status {
		t.Fatalf("%s %s status = %d, want %d; body = %s", method, path, res.StatusCode, status, data)
	}
	var result map[string]any
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatalf("invalid JSON response: %s", data)
	}
	return result
}

func TestTranslationAdminConfigurationIsPrivateAndPreservesModel(t *testing.T) {
	_, host, admin, listener := translationHTTPFixture(t)
	const path = "/api/settings/translation"
	initial := translationRequest(t, host, admin, "GET", path, "", 200)
	if initial["model"] != "Index-Translate-35B-A3B" || initial["hasApiKey"] != false {
		t.Fatalf("unexpected defaults: %#v", initial)
	}
	translationRequest(t, host, admin, "PATCH", path, `{"baseUrl":"https://translation.example/v1","apiKey":"synthetic-translation-secret","model":"custom/Exact-Model"}`, 200)
	saved := translationRequest(t, host, admin, "GET", path, "", 200)
	if saved["model"] != "custom/Exact-Model" || saved["baseUrl"] != "https://translation.example/v1" || saved["hasApiKey"] != true || saved["apiKeyMask"] != "********" {
		t.Fatalf("unexpected saved configuration: %#v", saved)
	}
	if _, exists := saved["apiKey"]; exists {
		t.Fatal("configuration exposed API key field")
	}
	encoded, _ := json.Marshal(saved)
	if strings.Contains(string(encoded), "synthetic-translation-secret") {
		t.Fatal("configuration exposed secret")
	}
	translationRequest(t, host, listener, "GET", path, "", 403)
	translationRequest(t, host, listener, "PATCH", path, `{"model":"unauthorized"}`, 403)
	translationRequest(t, host, nil, "GET", path, "", 401)
	retained := translationRequest(t, host, admin, "PATCH", path, `{"model":"another-model"}`, 200)
	if retained["hasApiKey"] != true {
		t.Fatal("omitting the key cleared it")
	}
	cleared := translationRequest(t, host, admin, "PATCH", path, `{"apiKey":""}`, 200)
	if cleared["hasApiKey"] != false || cleared["apiKeyMask"] != "" {
		t.Fatal("explicit key clearing failed")
	}
}

func TestTranslationTrialUsesSavedModelKeyAndOfficialSingleUserTemplate(t *testing.T) {
	requests := make(chan map[string]any, 4)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Error(err)
		}
		payload["authorization"] = r.Header.Get("Authorization")
		payload["path"] = r.URL.Path
		payload["method"] = r.Method
		requests <- payload
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"model":"custom/Exact-Model","choices":[{"message":{"role":"assistant","content":"今天天气真好。"},"finish_reason":"stop"}]}`)
	}))
	defer upstream.Close()
	_, host, admin, listener := translationHTTPFixture(t)
	settings, _ := json.Marshal(map[string]any{"baseUrl": upstream.URL + "/v1", "apiKey": "synthetic-translation-secret", "model": "custom/Exact-Model"})
	translationRequest(t, host, admin, "PATCH", "/api/settings/translation", string(settings), 200)
	const trial = "/api/settings/translation/test"
	translationRequest(t, host, listener, "POST", trial, `{"text":"The weather is nice today.","targetLanguage":"zh-Hans"}`, 403)
	result := translationRequest(t, host, admin, "POST", trial, `{"text":"The weather is nice today.","targetLanguage":"zh-Hans"}`, 200)
	if result["text"] != "今天天气真好。" || result["model"] != "custom/Exact-Model" {
		t.Fatalf("trial response: %#v", result)
	}
	sent := <-requests
	if sent["model"] != "custom/Exact-Model" || sent["authorization"] != "Bearer synthetic-translation-secret" || sent["path"] != "/v1/chat/completions" || sent["method"] != "POST" {
		t.Fatalf("unexpected upstream request: %#v", sent)
	}
	messages := sent["messages"].([]any)
	if len(messages) != 1 || messages[0].(map[string]any)["role"] != "user" || messages[0].(map[string]any)["content"] != "请将以下文本翻译为简体中文，直接输出翻译结果，不要进行任何解释。\n\nThe weather is nice today." {
		t.Fatalf("unexpected prompt: %#v", messages)
	}
	if sent["temperature"] != float64(0) || sent["max_tokens"] != float64(1024) || sent["stream"] != false || sent["chat_template_kwargs"].(map[string]any)["enable_thinking"] != false {
		t.Fatalf("unexpected model parameters: %#v", sent)
	}
	translationRequest(t, host, admin, "PATCH", "/api/settings/translation", `{"disableThinking":false}`, 200)
	translationRequest(t, host, admin, "POST", trial, `{"text":"Hello.","targetLanguage":"en"}`, 200)
	sent = <-requests
	if sent["authorization"] != "Bearer synthetic-translation-secret" {
		t.Fatal("omitted key was not retained for translation")
	}
	if _, ok := sent["chat_template_kwargs"]; ok {
		t.Fatal("disabled extension was still sent")
	}
	translationRequest(t, host, admin, "PATCH", "/api/settings/translation", `{"apiKey":""}`, 200)
	translationRequest(t, host, admin, "POST", trial, `{"text":"Hello.","targetLanguage":"en"}`, 200)
	if sent = <-requests; sent["authorization"] != "" {
		t.Fatal("cleared key was still sent")
	}
}
