package melon

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"pocket48-bot/internal/hearts2hearts"
	"strings"
	"sync/atomic"
	"testing"
)

type translationRoundTripFunc func(*http.Request) (*http.Response, error)

func (f translationRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func TestTranslateGoogleBatch(t *testing.T) {
	oldTransport := http.DefaultTransport
	defer func() { http.DefaultTransport = oldTransport }()
	http.DefaultTransport = translationRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Host != "translation.googleapis.com" || req.URL.Query().Get("key") != "google-secret" {
			t.Fatalf("request URL = %s", req.URL.Redacted())
		}
		body, _ := io.ReadAll(req.Body)
		if !strings.Contains(string(body), `"q":["안녕","고마워"]`) || strings.Contains(string(body), `"source"`) {
			t.Fatalf("request body = %s", body)
		}
		return translationResponse(`{"data":{"translations":[{"translatedText":"你好"},{"translatedText":"谢谢&amp;再见"}]}}`), nil
	})

	got, err := TranslateBatch(context.Background(), "google", "google-secret", []string{"안녕", "고마워"})
	if err != nil || len(got) != 2 || got[0] != "你好" || got[1] != "谢谢&再见" {
		t.Fatalf("translations=%#v err=%v", got, err)
	}
}

func TestTranslateDeepLFreeBatch(t *testing.T) {
	oldTransport := http.DefaultTransport
	defer func() { http.DefaultTransport = oldTransport }()
	http.DefaultTransport = translationRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Host != "api-free.deepl.com" || req.Header.Get("Authorization") != "DeepL-Auth-Key secret:fx" {
			t.Fatalf("request host=%s auth=%q", req.URL.Host, req.Header.Get("Authorization"))
		}
		return translationResponse(`{"translations":[{"detected_source_language":"KO","text":"你好"}]}`), nil
	})

	got, err := TranslateBatch(context.Background(), "deepl", "secret:fx", []string{"안녕"})
	if err != nil || len(got) != 1 || got[0] != "你好" {
		t.Fatalf("translations=%#v err=%v", got, err)
	}
}

func TestTranslateSiliconFlowBatch(t *testing.T) {
	oldTransport := http.DefaultTransport
	defer func() { http.DefaultTransport = oldTransport }()
	var requests atomic.Int32
	http.DefaultTransport = translationRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		requests.Add(1)
		if req.URL.String() != "https://api.siliconflow.cn/v1/chat/completions" || req.Header.Get("Authorization") != "Bearer silicon-secret" {
			return translationResponse(`{"error":"bad request"}`), nil
		}
		var payload struct {
			Model    string `json:"model"`
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(req.Body).Decode(&payload); err != nil || payload.Model != "tencent/Hunyuan-MT-7B" || len(payload.Messages) != 1 {
			return translationResponse(`{"error":"bad payload"}`), nil
		}
		content := payload.Messages[0].Content
		if !strings.Contains(content, "[[0]] 이안 (IAN): 太子殿下 옷") || !strings.Contains(content, "[[1]] 에이나 (A-NA): 고마워") || !strings.Contains(content, "태자님 => 太子殿下") {
			return translationResponse(`{"error":"missing context"}`), nil
		}
		return translationResponse(`{"choices":[{"message":{"content":"[[0]] IAN: 太子殿下的衣服，哈哈。\n[[1]] A-NA: 谢谢。"}}]}`), nil
	})

	glossary := hearts2hearts.Glossary{Terms: []hearts2hearts.Term{{Source: "태자님", Target: "太子殿下"}}}
	lines := []TranslationLine{{Speaker: "이안 (IAN)", Text: "태자님 옷 ㅋㅋ"}, {Speaker: "에이나 (A-NA)", Text: "고마워"}}
	got, err := TranslateConversation(context.Background(), "siliconflow", "silicon-secret", lines, glossary)
	if err != nil || len(got) != 2 || got[0] != "太子殿下的衣服，哈哈。" || got[1] != "谢谢。" || requests.Load() != 1 {
		t.Fatalf("translations=%#v requests=%d err=%v", got, requests.Load(), err)
	}
}

func TestParseSiliconFlowTranslationsRejectsMissingOrDuplicateRows(t *testing.T) {
	lines := []TranslationLine{{Text: "one"}, {Text: "two"}}
	for _, body := range []string{"[[0]] 第一条", "[[0]] 第一条\n[[0]] 重复\n[[1]] 第二条", "[[2]] 越界"} {
		if result, err := parseSiliconFlowTranslations(body, lines); err == nil || result != nil {
			t.Fatalf("accepted invalid result %q: %#v", body, result)
		}
	}
}

func translationResponse(body string) *http.Response {
	return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}
