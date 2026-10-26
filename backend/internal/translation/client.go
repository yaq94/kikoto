package translation

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/yexca/kikoto/backend/internal/outbound"
)

const TemplateVersion = "index-user-v1"
const MaxInputBytes = 8 << 10
const MaxResponseBytes = 256 << 10

const (
	// A translation can expand relative to its source and the model tokenizer
	// does not count runes, so budget two output tokens per source rune. The
	// floor keeps short titles from a needlessly small completion; the ceiling
	// bounds generation cost and stays far below MaxResponseBytes.
	outputTokensPerRune = 2
	minOutputTokens     = 256
	maxOutputTokens     = 4096
)

type Input struct {
	Text           string `json:"text"`
	TargetLanguage string `json:"targetLanguage"`
}

type Result struct {
	CacheStatus     string `json:"cacheStatus,omitempty"`
	Text            string `json:"text"`
	Model           string `json:"model"`
	TemplateVersion string `json:"templateVersion"`
	ElapsedMS       int64  `json:"elapsedMs"`
}

type Error struct {
	Code    string
	Message string
}

func (e *Error) Error() string           { return e.Message }
func failure(code, message string) error { return &Error{Code: code, Message: message} }

var languageNames = map[string]string{"en": "英语", "zh-Hans": "简体中文", "ja": "日语", "ko": "韩语"}

func (in Input) Validate() error {
	if strings.TrimSpace(in.Text) == "" {
		return failure("translation_empty_text", "Enter text to translate.")
	}
	if len(in.Text) > MaxInputBytes {
		return failure("translation_text_too_long", "The text is longer than the translation input limit.")
	}
	if languageNames[in.TargetLanguage] == "" {
		return failure("translation_unsupported_language", "Choose a supported target language.")
	}
	return nil
}

// outputTokenBudget sizes one completion from the source text. The factor is a
// conservative estimate, not a tokenizer count: it leaves headroom for target
// language expansion while the ceiling keeps realistic synopsis-length fields
// within one bounded request.
func outputTokenBudget(text string) int {
	tokens := utf8.RuneCountInString(strings.TrimSpace(text)) * outputTokensPerRune
	if tokens < minOutputTokens {
		return minOutputTokens
	}
	if tokens > maxOutputTokens {
		return maxOutputTokens
	}
	return tokens
}

// Translate makes exactly one non-streaming completion request. The caller owns
// the configured-origin policy client; credentials never enter the result.
func Translate(ctx context.Context, client *http.Client, cfg Config, in Input) (Result, error) {
	if err := in.Validate(); err != nil {
		return Result{}, err
	}
	if cfg.BaseURL == "" {
		return Result{}, failure("translation_not_configured", "Configure a translation service first.")
	}
	if err := cfg.Validate(); err != nil {
		return Result{}, err
	}
	payload := map[string]any{
		"model":       cfg.Model,
		"messages":    []map[string]string{{"role": "user", "content": "请将以下文本翻译为" + languageNames[in.TargetLanguage] + "，直接输出翻译结果，不要进行任何解释。\n\n" + strings.TrimSpace(in.Text)}},
		"temperature": 0, "max_tokens": outputTokenBudget(in.Text), "stream": false,
	}
	if cfg.DisableThinking {
		payload["chat_template_kwargs"] = map[string]bool{"enable_thinking": false}
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return Result{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(cfg.TimeoutSeconds)*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(cfg.BaseURL, "/")+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return Result{}, ErrInvalidConfig
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	// A completion may already have generated tokens when its connection fails.
	// Prevent the shared proxy failover transport from replaying the POST body.
	req.GetBody = nil
	if cfg.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+cfg.APIKey)
	}
	start := time.Now()
	res, err := client.Do(req)
	if err != nil {
		return Result{}, requestFailure(ctx, err)
	}
	defer func() { _ = res.Body.Close() }()
	switch {
	case res.StatusCode == http.StatusUnauthorized || res.StatusCode == http.StatusForbidden:
		return Result{}, failure("translation_auth_failed", "The translation service rejected its credentials.")
	case res.StatusCode == http.StatusTooManyRequests:
		return Result{}, failure("translation_rate_limited", "The translation service rate limit was reached.")
	case res.StatusCode >= 300 && res.StatusCode < 400:
		return Result{}, failure("translation_redirect_blocked", "The translation service redirected the request. Configure its final base URL.")
	case res.StatusCode >= 400 && res.StatusCode < 500:
		return Result{}, failure("translation_request_rejected", "The translation service rejected the model or request parameters.")
	}
	if res.StatusCode != http.StatusOK {
		return Result{}, failure("translation_upstream_error", "The translation service request failed.")
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, MaxResponseBytes+1))
	if err != nil {
		if ctx.Err() != nil || isTimeout(err) {
			return Result{}, requestFailure(ctx, err)
		}
		return Result{}, failure("translation_invalid_response", "The translation service returned an invalid response.")
	}
	if len(data) > MaxResponseBytes {
		return Result{}, failure("translation_response_too_large", "The translation response exceeded the size limit.")
	}
	var response struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(data, &response); err != nil || len(response.Choices) != 1 || !utf8.Valid(data) {
		return Result{}, failure("translation_invalid_response", "The translation service returned an invalid response.")
	}
	choice := response.Choices[0]
	if choice.FinishReason == "length" {
		return Result{}, failure("translation_truncated", "The translation reached the output limit and is incomplete.")
	}
	if choice.FinishReason != "stop" || strings.Contains(choice.Message.Content, "<think>") || strings.Contains(choice.Message.Content, "</think>") {
		return Result{}, failure("translation_invalid_response", "The translation service returned an invalid response.")
	}
	if strings.TrimSpace(choice.Message.Content) == "" {
		return Result{}, failure("translation_empty_output", "The translation service returned no translation.")
	}
	return Result{Text: strings.TrimSpace(response.Choices[0].Message.Content), Model: cfg.Model, TemplateVersion: TemplateVersion, ElapsedMS: time.Since(start).Milliseconds()}, nil
}

func isTimeout(err error) bool {
	var networkError net.Error
	return errors.Is(err, context.DeadlineExceeded) || errors.Is(err, outbound.ErrResponseReadTimeout) || (errors.As(err, &networkError) && networkError.Timeout())
}

func requestFailure(ctx context.Context, err error) error {
	if errors.Is(ctx.Err(), context.Canceled) || errors.Is(err, context.Canceled) {
		return failure("translation_cancelled", "The translation request was cancelled.")
	}
	if isTimeout(err) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return failure("translation_timeout", "The translation request timed out. It was not retried.")
	}
	if errors.Is(err, outbound.ErrPolicyViolation) {
		return failure("translation_destination_blocked", "The translation destination was rejected by the outbound policy.")
	}
	return failure("translation_upstream_error", "The translation service request failed.")
}

// PublicError never incorporates an upstream body, URL, or transport error.
func PublicError(err error) *Error {
	var result *Error
	if errors.As(err, &result) {
		return result
	}
	return &Error{Code: "translation_unavailable", Message: "Translation is unavailable."}
}
