package httpapi

import (
	"fmt"
	"testing"
)

func TestContentTranslationReusesAuthoredTraditionalTitleWithoutModel(t *testing.T) {
	s, host, admin, listener := translationHTTPFixture(t)
	if _, err := s.db.Exec(`INSERT INTO work (id,primary_code,title) VALUES (1,'RJ00000000','Example title')`); err != nil {
		t.Fatal(err)
	}
	translationRequest(t, host, admin, "PATCH", "/api/works/1/manual-overrides", `{"titles":{"zh-tw":"範例標題"}}`, 200)
	result := translationRequest(t, host, listener, "POST", "/api/content-translations", `{"workId":1,"field":"title","text":"Example title","language":"zh-tw"}`, 200)
	if result["status"] != "existing" || result["text"] != "範例標題" || result["original"] != "Example title" {
		t.Fatalf("authored Traditional title was not reused: %#v", result)
	}
}

func TestContentTranslationReturnsOriginalTagWithEarlierTargetName(t *testing.T) {
	s, host, admin, listener := translationHTTPFixture(t)
	if _, err := s.db.Exec(`INSERT INTO work (id,primary_code,title) VALUES (1,'RJ00000000','Example title')`); err != nil {
		t.Fatal(err)
	}
	tags := translationRequest(t, host, admin, "PUT", "/api/works/1/metadata-tags", `{"newTags":["Example tag"]}`, 200)
	id := tags["tags"].([]any)[0].(map[string]any)["id"].(float64)
	translationRequest(t, host, admin, "PATCH", fmt.Sprintf("/api/metadata/tags/%.0f", id), `{"names":{"":"","ja-jp":"例のタグ","en-us":"Example translated tag","zh-tw":"範例標籤"}}`, 200)
	result := translationRequest(t, host, listener, "POST", "/api/content-translations", `{"workId":1,"field":"tag","text":"Example translated tag","language":"en-us"}`, 200)
	if result["status"] != "existing" || result["text"] != "Example translated tag" || result["original"] != "例のタグ" {
		t.Fatalf("tag lost its original: %#v", result)
	}
	traditional := translationRequest(t, host, listener, "POST", "/api/content-translations", `{"workId":1,"field":"tag","text":"Example translated tag","language":"zh-tw"}`, 200)
	if traditional["status"] != "existing" || traditional["text"] != "範例標籤" || traditional["original"] != "例のタグ" {
		t.Fatalf("authored Traditional tag was not reused: %#v", traditional)
	}
}
