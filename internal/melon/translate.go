package melon

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"pocket48-bot/internal/hearts2hearts"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const TranslationTimeout = 20 * time.Second

type TranslationLine struct {
	Speaker string
	Text    string
}

// TranslateBatch uses a purpose-built machine translation service. It is kept
// separate from the Weverse AI pool so slow model requests cannot delay Melon.
func TranslateBatch(ctx context.Context, provider, apiKey string, texts []string) ([]string, error) {
	lines := make([]TranslationLine, len(texts))
	for i, value := range texts {
		lines[i].Text = value
	}
	return TranslateConversation(ctx, provider, apiKey, lines, hearts2hearts.Glossary{})
}

// TranslateConversation translates a chronological chat batch. SiliconFlow's
// MT model receives the whole conversation so Korean fragments can resolve
// their omitted subjects and objects from adjacent messages.
func TranslateConversation(ctx context.Context, provider, apiKey string, lines []TranslationLine, glossary hearts2hearts.Glossary) ([]string, error) {
	texts := make([]string, len(lines))
	for i, line := range lines {
		texts[i] = glossary.Apply(line.Text)
	}
	if len(texts) == 0 {
		return nil, nil
	}
	provider = strings.ToLower(strings.TrimSpace(provider))
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return nil, fmt.Errorf("翻译 API Key 未配置")
	}
	callCtx, cancel := context.WithTimeout(ctx, TranslationTimeout)
	defer cancel()
	switch provider {
	case "google":
		return translateGoogle(callCtx, apiKey, texts)
	case "deepl":
		return translateDeepL(callCtx, apiKey, texts)
	case "siliconflow":
		return translateSiliconFlow(callCtx, apiKey, lines, texts, glossary)
	default:
		return nil, fmt.Errorf("不支持的翻译服务")
	}
}

var translationMarkerRE = regexp.MustCompile(`(?m)^\s*\[\[(\d+)\]\]\s*(.*?)\s*$`)

func translateSiliconFlow(ctx context.Context, apiKey string, lines []TranslationLine, texts []string, glossary hearts2hearts.Glossary) ([]string, error) {
	var source strings.Builder
	hasSpeaker := false
	for i, text := range texts {
		fmt.Fprintf(&source, "[[%d]]", i)
		if speaker := strings.TrimSpace(lines[i].Speaker); speaker != "" {
			hasSpeaker = true
			fmt.Fprintf(&source, " %s:", speaker)
		}
		fmt.Fprintf(&source, " %s\n", text)
	}
	prompt := `把下面各条韩文内容翻译成自然、简洁、忠实的简体中文。
严格保留每行开头的 [[数字]]，输出行数、编号和顺序必须与原文完全一致。
每个编号只输出该条内容的译文，不要解释、注释、补充背景或自行扩写。
已有中文和指定术语必须原样保留。专名或网络用语不确定时保留原文，绝对不要猜测或编造含义。
表情符号原样保留。只输出带编号的逐行译文。`
	if hasSpeaker {
		prompt = `把下面这段连续的韩国偶像群聊翻译成自然、简洁、忠实的简体中文。
必须结合前后消息理解承接关系、省略的主语和宾语。
严格保留每行开头的 [[数字]]，输出行数、编号和顺序必须与原文完全一致。
每个编号只输出该条消息的译文，不要重复说话人，不要解释、注释、补充背景或自行扩写。
已有中文和指定术语必须原样保留。专名、昵称、团内梗或网络用语不确定时保留原文，绝对不要猜测或编造含义。
ㅋㅋ/ㅎㅎ 按语气译成“哈哈”，表情符号原样保留。只输出带编号的逐行译文。`
	}
	if instructions := glossary.Instructions(); instructions != "" {
		prompt += "\n\n" + instructions
	}
	prompt += "\n\n待翻译对话：\n" + strings.TrimSpace(source.String())
	payload, err := json.Marshal(map[string]any{
		"model": "tencent/Hunyuan-MT-7B",
		"messages": []map[string]string{{
			"role":    "user",
			"content": prompt,
		}},
		"temperature": 0,
		"max_tokens":  1200,
	})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.siliconflow.cn/v1/chat/completions", bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("硅基流动请求配置无效")
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")
	var response struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := doTranslationRequest(req, &response); err != nil {
		return nil, fmt.Errorf("硅基流动翻译失败: %w", err)
	}
	if len(response.Choices) == 0 || strings.TrimSpace(response.Choices[0].Message.Content) == "" {
		return nil, fmt.Errorf("硅基流动未返回有效译文")
	}
	return parseSiliconFlowTranslations(response.Choices[0].Message.Content, lines)
}

func parseSiliconFlowTranslations(content string, lines []TranslationLine) ([]string, error) {
	result := make([]string, len(lines))
	seen := make([]bool, len(lines))
	for _, match := range translationMarkerRE.FindAllStringSubmatch(content, -1) {
		index, err := strconv.Atoi(match[1])
		if err != nil || index < 0 || index >= len(lines) || seen[index] {
			return nil, fmt.Errorf("硅基流动返回的译文编号无效")
		}
		value := stripTranslationSpeaker(match[2], lines[index].Speaker)
		if value == "" {
			return nil, fmt.Errorf("硅基流动返回了空译文")
		}
		result[index], seen[index] = value, true
	}
	for i, ok := range seen {
		if !ok {
			return nil, fmt.Errorf("硅基流动缺少第 %d 条译文", i+1)
		}
	}
	return validateTranslations(result, len(lines))
}

func stripTranslationSpeaker(value, speaker string) string {
	value = strings.TrimSpace(value)
	candidates := []string{strings.TrimSpace(speaker)}
	if start, end := strings.LastIndex(speaker, "("), strings.LastIndex(speaker, ")"); start >= 0 && end > start {
		candidates = append(candidates, strings.TrimSpace(speaker[start+1:end]))
	}
	for _, candidate := range candidates {
		if candidate == "" {
			continue
		}
		for _, separator := range []string{":", "："} {
			prefix := candidate + separator
			if len(value) >= len(prefix) && strings.EqualFold(value[:len(prefix)], prefix) {
				return strings.TrimSpace(value[len(prefix):])
			}
		}
	}
	return value
}

func translationHTTPClient() *http.Client {
	return &http.Client{
		Timeout:       TranslationTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

func translateGoogle(ctx context.Context, apiKey string, texts []string) ([]string, error) {
	payload, err := json.Marshal(map[string]any{"q": texts, "target": "zh-CN", "format": "text"})
	if err != nil {
		return nil, err
	}
	endpoint := "https://translation.googleapis.com/language/translate/v2?key=" + url.QueryEscape(apiKey)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("Google 翻译请求配置无效")
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	var response struct {
		Data struct {
			Translations []struct {
				Text string `json:"translatedText"`
			} `json:"translations"`
		} `json:"data"`
	}
	if err := doTranslationRequest(req, &response); err != nil {
		return nil, fmt.Errorf("Google 翻译失败: %w", err)
	}
	result := make([]string, len(response.Data.Translations))
	for i, item := range response.Data.Translations {
		result[i] = strings.TrimSpace(html.UnescapeString(item.Text))
	}
	return validateTranslations(result, len(texts))
}

func translateDeepL(ctx context.Context, apiKey string, texts []string) ([]string, error) {
	payload, err := json.Marshal(map[string]any{"text": texts, "target_lang": "ZH-HANS"})
	if err != nil {
		return nil, err
	}
	host := "https://api.deepl.com"
	if strings.HasSuffix(apiKey, ":fx") {
		host = "https://api-free.deepl.com"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, host+"/v2/translate", bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("DeepL 翻译请求配置无效")
	}
	req.Header.Set("Authorization", "DeepL-Auth-Key "+apiKey)
	req.Header.Set("Content-Type", "application/json")
	var response struct {
		Translations []struct {
			Text string `json:"text"`
		} `json:"translations"`
	}
	if err := doTranslationRequest(req, &response); err != nil {
		return nil, fmt.Errorf("DeepL 翻译失败: %w", err)
	}
	result := make([]string, len(response.Translations))
	for i, item := range response.Translations {
		result[i] = strings.TrimSpace(item.Text)
	}
	return validateTranslations(result, len(texts))
}

func doTranslationRequest(req *http.Request, output any) error {
	resp, err := translationHTTPClient().Do(req)
	if err != nil {
		return fmt.Errorf("请求超时或网络不可用")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("接口返回 HTTP %d", resp.StatusCode)
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(output); err != nil {
		return fmt.Errorf("接口返回内容无效")
	}
	return nil
}

func validateTranslations(result []string, expected int) ([]string, error) {
	if len(result) != expected {
		return nil, fmt.Errorf("接口返回 %d 条译文，预期 %d 条", len(result), expected)
	}
	for _, value := range result {
		if value == "" {
			return nil, fmt.Errorf("接口返回了空译文")
		}
	}
	return result, nil
}
