package outbound

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"pocket48-bot/internal/mediafetch"
	"pocket48-bot/internal/message"
)

const feishuAPIBase = "https://open.feishu.cn/open-apis"

var feishuURLPattern = regexp.MustCompile(`https?://[^\s]+`)

type FeishuOptions struct {
	AppID             string
	AppSecret         string
	QueueSize         int
	RequestTimeout    time.Duration
	MaxMediaBytes     int64
	UploadConcurrency int
}

// Feishu renders neutral messages with Feishu's native card and media APIs.
type Feishu struct {
	appID         string
	appSecret     string
	apiBase       string
	http          *http.Client
	queue         chan feishuDelivery
	tokenMu       sync.Mutex
	token         string
	tokenExpires  time.Time
	cacheMu       sync.RWMutex
	mediaKeys     map[string]string
	maxMediaBytes int64
	// replyMap records source message id -> Feishu message id so later replies
	// can be threaded under their parent message. Nil disables the feature.
	replyMap *ReplyMap
	// upload gate: a dynamic semaphore so the concurrency cap can hot-reload.
	uploadMu     sync.Mutex
	uploadCond   *sync.Cond
	uploadActive int
	uploadLimit  atomic.Int64
}

type feishuDelivery struct {
	target  Target
	content interface{}
}

// FeishuChat is one conversation the bot can reach, used by the admin console
// so operators can copy a chat_id instead of crafting API calls by hand.
type FeishuChat struct {
	ChatID  string `json:"chatId"`
	Name    string `json:"name"`
	Kind    string `json:"kind"`
	OwnerID string `json:"ownerId,omitempty"` // open_id of the group owner
}

// newFeishu builds an adapter without starting the background worker.
func newFeishu(options FeishuOptions) *Feishu {
	queueSize := options.QueueSize
	if queueSize <= 0 {
		queueSize = 1000
	}
	timeout := options.RequestTimeout
	if timeout <= 0 {
		timeout = 45 * time.Second
	}
	maxBytes := options.MaxMediaBytes
	if maxBytes <= 0 {
		maxBytes = 100 << 20
	}
	concurrency := options.UploadConcurrency
	if concurrency <= 0 {
		concurrency = 3
	}
	f := &Feishu{
		appID: options.AppID, appSecret: options.AppSecret, apiBase: feishuAPIBase,
		http: &http.Client{Timeout: timeout}, queue: make(chan feishuDelivery, queueSize),
		mediaKeys: make(map[string]string), maxMediaBytes: maxBytes,
	}
	f.uploadCond = sync.NewCond(&f.uploadMu)
	f.uploadLimit.Store(int64(concurrency))
	return f
}

func NewFeishu(options FeishuOptions) *Feishu {
	f := newFeishu(options)
	go f.worker()
	return f
}

// NewFeishuPanel builds an adapter for one-shot console actions. It does not
// start the background worker, so callers must use SendNow and ListChats.
func NewFeishuPanel(options FeishuOptions) *Feishu {
	return newFeishu(options)
}

// SetReplyMap attaches the source-id → Feishu message-id mapping used to thread
// replies under their parent message. Safe to set before or during operation.
func (f *Feishu) SetReplyMap(m *ReplyMap) {
	if f != nil {
		f.replyMap = m
	}
}

// SetUploadConcurrency adjusts the live media-upload concurrency cap. It is
// safe to call at runtime and is how the admin console hot-reloads the value
// without restarting the bot process.
func (f *Feishu) SetUploadConcurrency(n int) {
	if f == nil || n < 1 {
		return
	}
	if n > 8 {
		n = 8
	}
	f.uploadLimit.Store(int64(n))
}

// SendNow renders and delivers a single message synchronously so the caller can
// report the real API result. The admin console uses it to validate credentials.
func (f *Feishu) SendNow(ctx context.Context, target Target, content interface{}) error {
	if f == nil || strings.TrimSpace(target.Address) == "" {
		return errors.New("missing Feishu target address")
	}
	return f.deliver(ctx, feishuDelivery{target: target, content: message.Normalize(content)})
}

// ListChats returns the conversations the bot has joined.
func (f *Feishu) ListChats(ctx context.Context) ([]FeishuChat, error) {
	if f == nil {
		return nil, errors.New("feishu adapter is not configured")
	}
	chats := make([]FeishuChat, 0, 32)
	pageToken := ""
	for page := 0; page < 10; page++ {
		path := "/im/v1/chats?page_size=100"
		if pageToken != "" {
			path += "&page_token=" + url.QueryEscape(pageToken)
		}
		// Feishu wraps list payloads in "data", so the fields are not top level.
		var page_ struct {
			Data struct {
				HasMore   bool   `json:"has_more"`
				PageToken string `json:"page_token"`
				Items     []struct {
					ChatID   string `json:"chat_id"`
					Name     string `json:"name"`
					ChatMode string `json:"chat_mode"`
					OwnerID  string `json:"owner_id"`
				} `json:"items"`
			} `json:"data"`
		}
		if err := f.authorized(ctx, http.MethodGet, path, "application/json; charset=utf-8", nil, &page_); err != nil {
			return nil, err
		}
		for _, item := range page_.Data.Items {
			kind := "group"
			if strings.EqualFold(item.ChatMode, "p2p") {
				kind = "private"
			}
			name := strings.TrimSpace(item.Name)
			if name == "" {
				name = item.ChatID
			}
			chats = append(chats, FeishuChat{ChatID: item.ChatID, Name: name, Kind: kind, OwnerID: item.OwnerID})
		}
		if !page_.Data.HasMore || page_.Data.PageToken == "" {
			break
		}
		pageToken = page_.Data.PageToken
	}
	return chats, nil
}

func (*Feishu) Capabilities() Capabilities {
	return Capabilities{
		Platform: "feishu", MaxImagesPerMessage: 9, CanMixTextAndImages: true,
		VideoMustBeStandalone: true, SupportsCards: true,
		SupportsNativeLinkPreview: false, RichText: RichTextPost,
	}
}

func (f *Feishu) Send(target Target, content interface{}) {
	if f == nil || strings.TrimSpace(target.Address) == "" {
		return
	}
	select {
	case f.queue <- feishuDelivery{target: target, content: message.Normalize(content)}:
	default:
		log.Printf("[Outbound:feishu] status=queue_full target=%s", target.Address)
	}
}

func (f *Feishu) QueueDepth() int {
	if f == nil {
		return 0
	}
	return len(f.queue)
}

func (f *Feishu) worker() {
	for delivery := range f.queue {
		if err := f.deliver(context.Background(), delivery); err != nil {
			log.Printf("[Outbound:feishu] status=send_failed target=%s error=%v", delivery.target.Address, err)
		} else {
			log.Printf("[Outbound:feishu] status=healthy target=%s", delivery.target.Address)
		}
	}
}

// resolveReplyTarget picks the platform message id to thread under, trying the
// primary source first and then each fallback in order. Empty means none of the
// candidates was ever delivered by us, so the caller sends a standalone message.
func (f *Feishu) resolveReplyTarget(primary string, fallbacks []string) string {
	for _, candidate := range append([]string{primary}, fallbacks...) {
		if candidate == "" {
			continue
		}
		if id, ok := f.replyMap.Lookup(candidate); ok {
			return id
		}
	}
	return ""
}

// shouldDropQuote reports whether the inline quote block is redundant once the
// message is threaded. It is only redundant when we threaded onto the exact
// parent the quote refers to; sources that fall back to a container (Weverse
// replies fall back to the post) set KeepQuoteWhenThreaded because the quoted
// message appears nowhere else.
func shouldDropQuote(replyTarget string, keepQuoteWhenThreaded bool) bool {
	return replyTarget != "" && !keepQuoteWhenThreaded
}

// prefixSpeaker puts "作者：" in front of a reply so it lines up with a quoted
// message rendered by turnParts, which uses the same "作者：内容" shape. The
// prefix goes on the translation (the white, primary line) when there is one,
// leaving the grey original block unprefixed — mirroring turnParts exactly.
func prefixSpeaker(text, original, author string) (string, string) {
	author = strings.TrimSpace(author)
	if author == "" {
		return text, original
	}
	// ★ @全体成员 标记必须留在正文**最前面**。
	//
	//   documentContent 会把 `<at id=all></at>` 加在 text 开头，而这个函数
	//   要在正文前面加「说话人：」。两者叠加会得到「昵称：<at id=all></at>」——
	//   at 标记被夹到行中，飞书不把它当真正的 @，于是「订阅里配了 @ 成员」
	//   在飞书上完全看不到提醒（2026-10-06 用户反馈）。
	//   这里先把它摘下来，加完说话人前缀再放回第一行。
	const atAllMark = "<at id=all></at>"
	body := strings.TrimSpace(text)
	lead := ""
	if strings.HasPrefix(body, atAllMark) {
		lead = atAllMark + "\n"
		body = strings.TrimSpace(strings.TrimPrefix(body, atAllMark))
	}
	if body != "" {
		if lead != "" {
			return lead + author + "：" + body, original
		}
		return author + "：" + body, original
	}
	// No translation: the original is the primary line and carries the prefix.
	// With neither, leave both empty rather than emit a dangling "作者：".
	trimmed := strings.TrimSpace(original)
	if trimmed == "" {
		if lead != "" {
			// 只剩 @ 标记：不要留下尾随换行（后面没有内容了）。
			return strings.TrimSuffix(lead, "\n"), ""
		}
		return "", ""
	}
	if lead != "" {
		return lead + author + "：" + trimmed, ""
	}
	return author + "：" + trimmed, ""
}

func (f *Feishu) deliver(ctx context.Context, delivery feishuDelivery) error {
	var content feishuContent
	var sourceID, replyToSourceID, link string
	var replyFallbackSourceIDs []string
	keepQuoteWhenThreaded := false
	replyAuthorPrefix := ""

	// A structured Document is rendered directly from its fields (never
	// flattened through ToSegments), so quote / original / translation each
	// appear exactly once.
	if doc, ok := message.AsDocument(delivery.content); ok {
		content = f.documentContent(doc)
		sourceID = doc.SourceID
		replyToSourceID = doc.ReplyToSourceID
		replyFallbackSourceIDs = doc.ReplyToFallbackSourceIDs
		keepQuoteWhenThreaded = doc.KeepQuoteWhenThreaded
		replyAuthorPrefix = strings.TrimSpace(doc.ReplyAuthorPrefix)
		link = strings.TrimSpace(doc.Link)
	} else {
		segments := contentSegments(message.ToSegments(delivery.content))
		if len(segments) == 0 {
			return nil
		}
		content = renderFeishuContent(segments)
		link = lastURL(content.text)
	}
	if content.text == "" && content.original == "" && content.quote == nil && len(content.turns) == 0 && len(content.images) == 0 && len(content.media) == 0 {
		return nil
	}

	receiveType := "chat_id"
	if delivery.target.Kind == PrivateChat {
		receiveType = "open_id"
	}
	// Cross-message threading: if this message replies to a source we previously
	// delivered, attach it to that Feishu message as a native reply.
	// The primary target is tried first, then any fallbacks in order — the first
	// one we have a recorded message id for wins. This is what keeps a member's
	// reply under the post when the comment they answered was never delivered.
	replyTarget := f.resolveReplyTarget(replyToSourceID, replyFallbackSourceIDs)
	// When threaded as a native reply, the parent content is already visible
	// above, so drop the inline quote block to avoid repeating it— unless the
	// source opted out, or threading failed and the quote is all we have.
	if shouldDropQuote(replyTarget, keepQuoteWhenThreaded) {
		content.quote = nil
	}
	// When both the quoted message and the reply stay on screen, render them
	// through the same speaker layout so they read as two aligned lines
	// ("粉丝昵称：…" above "成员名：…") rather than a named line over an
	// anonymous one. The prefix rides on the translation when present so the
	// 译文白/原文灰 hierarchy survives.
	if replyAuthorPrefix != "" && content.quote != nil {
		content.text, content.original = prefixSpeaker(content.text, content.original, replyAuthorPrefix)
	}
	// Send useful text immediately when there are no images. Image cards wait for
	// concurrent uploads so users receive one coherent native card.
	cardMessageID := ""
	if content.text != "" || content.original != "" || len(content.turns) > 0 || len(content.images) > 0 || content.quote != nil {
		imageKeys := f.uploadImages(ctx, content.images)
		id, err := f.sendCard(ctx, delivery.target.Address, receiveType, content, imageKeys, link, replyTarget)
		if err != nil {
			fallbackBody := content.text
			if fallbackBody == "" {
				fallbackBody = content.original
			}
			if fallbackErr := f.sendText(ctx, delivery.target.Address, receiveType, plainFallback(content.source, fallbackBody)); fallbackErr != nil {
				return fmt.Errorf("card: %v; fallback: %w", err, fallbackErr)
			}
			// The card silently became a plain-text message. That is exactly the
			// kind of degradation nobody notices until someone reports "样式不对了",
			// so it gets its own log line with the platform's own error.
			log.Printf("[Outbound:feishu] status=card_fallback target=%s error=%v", delivery.target.Address, err)
		} else {
			cardMessageID = id
		}
	}
	// Record this message's own id so a later reply can thread onto it.
	if sourceID != "" && cardMessageID != "" {
		f.replyMap.Record(sourceID, cardMessageID)
	}
	// Large media deliberately follows the card. When we sent a card, attach the
	// media as native replies to it (Feishu renders them as a thread under the
	// card) instead of orphaned standalone messages. A failed reply/upload never
	// hides the notification and original links remain in the card text.
	mediaReplyTo := cardMessageID
	if mediaReplyTo == "" {
		mediaReplyTo = replyTarget
	}
	for _, segment := range content.media {
		if err := f.sendMediaSegment(ctx, delivery.target.Address, receiveType, segment, mediaReplyTo); err != nil {
			log.Printf("[Outbound:feishu] status=media_failed target=%s type=%s error=%v", delivery.target.Address, segment.Type, err)
		}
	}
	return nil
}

// documentContent renders a structured Document into the card-ready view,
// mapping 译文 to the white body and 原文 to a grey block (译文在上、原文在下).
func (f *Feishu) documentContent(doc message.Document) feishuContent {
	content := feishuContent{
		source:      doc.Source,
		sourceLabel: doc.Label,
		sender:      doc.Author,
		quote:       doc.Quote,
		turns:       doc.Turns,
	}
	if content.sender == "" {
		content.sender = doc.Title
	}
	if content.sender == "" {
		content.sender = content.source
	}
	if content.sender == "" {
		content.sender = "Pocket48"
	}
	if len(doc.Turns) == 0 {
		translation := strings.TrimSpace(doc.Translation)
		original := strings.TrimSpace(doc.Body)
		if translation == original {
			translation = ""
		}
		if translation != "" {
			content.text = translation
			content.original = original
		} else {
			// No translation: the original is the primary (white) content.
			content.text = original
			content.original = ""
		}
		if doc.MentionAll {
			if content.text != "" {
				content.text = "<at id=all></at>\n" + content.text
			} else if content.original != "" {
				content.original = "<at id=all></at>\n" + content.original
			}
		}
	} else if doc.MentionAll && len(content.turns) > 0 {
		content.turns[0].Author = "<at id=all></at> " + content.turns[0].Author
	}
	if !doc.CreatedAt.IsZero() {
		content.timestamp = doc.CreatedAt.Format("2006-01-02 15:04:05")
	}
	for _, media := range doc.Media {
		switch media.Kind {
		case "image":
			if media.Source != "" {
				content.images = append(content.images, media.Source)
			}
		case "video":
			content.media = append(content.media, message.Video(media.Source, media.Cover))
		case "audio":
			content.media = append(content.media, message.Audio(media.Source))
		}
	}
	return content
}

func contentSegments(content interface{}) []message.Segment {
	switch value := content.(type) {
	case message.Segment:
		return []message.Segment{value}
	case []message.Segment:
		return value
	case []interface{}:
		result := make([]message.Segment, 0, len(value))
		for _, item := range value {
			if segment, ok := item.(message.Segment); ok {
				result = append(result, segment)
			}
		}
		return result
	case string:
		return []message.Segment{message.Text(value)}
	default:
		return nil
	}
}

// feishuContent is a rendered, card-ready view of a neutral message.
type feishuContent struct {
	source      string // platform, drives button label + card color (e.g. "Melon")
	sourceLabel string // finer source tag shown in the footer (e.g. "Melon 官方文章")
	sender      string // who sent it, used as the card header
	text        string // translation, rendered as the primary (white) body
	original    string // untranslated source text, rendered as a grey note block
	images      []string
	media       []message.Segment
	timestamp   string
	quote       *message.Quote // reply-to context, rendered as a grey note block
	turns       []message.Turn // merged conversation turns (e.g. Melon Music Wave)
}

func renderFeishuContent(segments []message.Segment) feishuContent {
	title, text, images, media := flattenFeishuSegments(segments)
	content := feishuContent{text: text, images: images, media: media}
	// The 【sender|source】 header carries both the sender and the platform.
	// The footer shows only the platform ("|" suffix); the card header prefers
	// the inline sender ("昵称：正文") when present, otherwise the "|" prefix.
	if idx := strings.LastIndex(title, "|"); idx > 0 {
		content.source = strings.TrimSpace(title[idx+1:])
	} else {
		content.source = strings.TrimSpace(title)
	}
	content.text, content.timestamp = extractTimestamp(content.text)
	// extractSender is correct here: this branch only runs for **plain text**
	// messages (the Document branch above already set content.sender).
	//
	// ★ Do not "optimise" this into `if content.sender != ""`. This function
	// builds content without a sender field at all, so such a guard is always
	// true and silently disables the whole thing. That mistake cost one
	// wasted deploy on 2026-10-04.
	content.sender, content.text = extractSender(content.text)
	if content.sender == "" {
		if idx := strings.LastIndex(title, "|"); idx > 0 {
			content.sender = strings.TrimSpace(title[:idx])
		}
	}
	if content.sender == "" {
		// Notices lead with their own subject line ("🤖 机器人已启动"). Promote it
		// to the card title instead of repeating it in the body.
		if head, rest, ok := strings.Cut(content.text, "\n"); ok {
			head = strings.TrimSpace(head)
			if head != "" && len([]rune(head)) <= 30 && !strings.HasSuffix(head, "：") && !strings.HasSuffix(head, ":") {
				content.sender = head
				content.text = strings.TrimSpace(rest)
			}
		}
	}
	if content.sender == "" {
		content.sender = content.source
	}
	if content.sender == "" {
		content.sender = "Pocket48"
	}
	return content
}

func flattenFeishuSegments(segments []message.Segment) (title, text string, images []string, media []message.Segment) {
	var body strings.Builder
	// Fallback provenance. Plain notices (bot start/stop, admin alerts) carry no
	// 【...】 header, so they must not inherit a meaningless placeholder title.
	title = ""
	for _, segment := range segments {
		switch segment.Type {
		case "text":
			body.WriteString(segment.Data["text"])
		case "mention_all":
			body.WriteString("<at id=all></at>\n")
		case "mention":
			body.WriteString("<at id=" + segment.Data["user"] + "></at>")
		case "at":
			// 微博/抖音/X 等平台直接投递 napcat 原始段（不经 Document 转换），
			// 其 @全体成员 段类型是 "at" 而非 "mention_all"。此前缺少该 case，
			// 段被静默丢弃：换行随之消失，正文被提到第一行，标题不再是
			// 首行「【昵称|来源】」，于是卡片标题/来源/发送者全部解析失败
			// （来源回落到 "Pocket48"），正文里还残留未渲染的 at 标记。
			qq := strings.TrimSpace(segment.Data["qq"])
			if qq == "" || qq == "all" || qq == "全体" {
				body.WriteString("<at id=all></at>\n")
			} else {
				body.WriteString("<at id=" + qq + "></at>\n")
			}
		case "image":
			images = append(images, segment.Data["file"])
		case "video", "audio":
			media = append(media, segment)
		}
	}
	raw := strings.TrimSpace(body.String())
	// @ 标记（<at id=all></at>）只是通知，不属于正文也不属于标题。首行若只有
	// @ 标记，必须先剥离，否则「【昵称|来源】」就不是首行、标题提取会失败，
	// 卡片头部/来源随之回落到兜底名。
	raw = strings.TrimSpace(feishuAtPattern.ReplaceAllString(raw, ""))
	if first, rest, ok := strings.Cut(raw, "\n"); ok && strings.HasPrefix(first, "【") && strings.HasSuffix(first, "】") {
		title = strings.TrimSuffix(strings.TrimPrefix(first, "【"), "】")
		raw = strings.TrimSpace(rest)
	}
	return title, raw, images, media
}

var feishuTimestampPattern = regexp.MustCompile(`(?m)^\s*(\d{4}-\d{2}-\d{2}[ T]\d{2}:\d{2}(:\d{2})?)\s*$`)

// feishuAtPattern 匹配渲染出的飞书 @ 标记，如 <at id=all></at>。
// 这些标记必须从「发送者昵称」判断中剔除：<at id=all></at> 里含有冒号，
// 会被extractSender 误当成"昵称：正文"的分隔符，从而把整段标记当成昵称
// 塞进卡片头部，并连带导致首行标题【昵称|来源】解析失败。
var feishuAtPattern = regexp.MustCompile(`<at\s+id=[^>]*>\s*</at>`)

// feishuTrailingTimestampPattern catches a timestamp glued to the end of a text
// line instead of sitting on its own line. 口袋48 图片消息把发言前缀和时间戳直接
// 拼在同一段（"胡晓慧: 2026-10-02 17:52:03"，前缀无换行），只匹配独占行会漏掉，
// 于是时间戳留在正文、底栏又回落到本机当前时间，一条消息出现两个不一致的时间。
var feishuTrailingTimestampPattern = regexp.MustCompile(`[ \t]+(\d{4}-\d{2}-\d{2}[ T]\d{2}:\d{2}(:\d{2})?)[ \t]*$`)

// extractTimestamp pulls a trailing timestamp line out of the body so it can be
// rendered in the card footer instead of dangling after the text. The footer must
// always show the sender's own send time, never our delivery time, so when no
// timestamp can be recovered we return an empty stamp and the footer omits it.
func extractTimestamp(text string) (string, string) {
	if matches := feishuTimestampPattern.FindAllStringSubmatch(text, -1); len(matches) > 0 {
		stamp := strings.TrimSpace(matches[len(matches)-1][1])
		remaining := feishuTimestampPattern.ReplaceAllString(text, "")
		return strings.TrimSpace(remaining), stamp
	}
	if m := feishuTrailingTimestampPattern.FindStringSubmatch(text); m != nil {
		stamp := strings.TrimSpace(m[1])
		remaining := feishuTrailingTimestampPattern.ReplaceAllString(text, "")
		return strings.TrimSpace(strings.TrimRight(remaining, " \t\n\r")), stamp
	}
	return text, ""
}

// extractSender splits "昵称（成员）：正文" into a header name and the body.
// QQ shows the sender inline; a Feishu card looks better with it as the title.
func extractSender(text string) (string, string) {
	if text == "" {
		return "", text
	}
	firstLine := text
	if index := strings.IndexAny(text, "\n"); index >= 0 {
		firstLine = text[:index]
	}
	// 首行若只是 @ 标记（<at id=all></at>），它既不是昵称也不含正文，
	// 应当整体剥离后再继续判断，避免其中的冒号被误当作"昵称：正文"分隔符。
	trimmed := strings.TrimSpace(feishuAtPattern.ReplaceAllString(firstLine, ""))
	if trimmed == "" {
		rest := ""
		if idx := strings.IndexAny(text, "\n"); idx >= 0 {
			rest = text[idx:]
		}
		return "", strings.TrimSpace(rest)
	}
	firstLine = trimmed
	for _, sep := range []string{"：", ":"} {
		index := strings.Index(firstLine, sep)
		if index <= 0 || index > 40 {
			continue
		}
		prefix := strings.TrimSpace(firstLine[:index])
		if prefix == "" || strings.Contains(prefix, "http") {
			continue
		}
		rest := strings.TrimSpace(firstLine[index+len(sep):])
		remainder := rest
		if idx := strings.IndexAny(text, "\n"); idx >= 0 {
			remainder = strings.TrimSpace(rest + text[idx:])
		}
		return prefix, strings.TrimSpace(remainder)
	}
	return "", text
}

func (f *Feishu) uploadImages(ctx context.Context, sources []string) []string {
	keys := make([]string, len(sources))
	var wg sync.WaitGroup
	for i, source := range sources {
		i, source := i, source
		wg.Add(1)
		go func() {
			defer wg.Done()
			f.acquireUploadSlot()
			defer f.releaseUploadSlot()
			key, err := f.uploadResource(ctx, source, "image")
			if err != nil {
				log.Printf("[Outbound:feishu] status=image_failed error=%v", err)
				return
			}
			keys[i] = key
		}()
	}
	wg.Wait()
	result := keys[:0]
	for _, key := range keys {
		if key != "" {
			result = append(result, key)
		}
	}
	return result
}

// acquireUploadSlot blocks until an upload slot is available under the live
// concurrency limit. The limit is an atomic int so it can hot-reload.
func (f *Feishu) acquireUploadSlot() {
	f.uploadMu.Lock()
	for f.uploadActive >= int(f.uploadLimit.Load()) {
		f.uploadCond.Wait()
	}
	f.uploadActive++
	f.uploadMu.Unlock()
}

func (f *Feishu) releaseUploadSlot() {
	f.uploadMu.Lock()
	f.uploadActive--
	f.uploadCond.Signal()
	f.uploadMu.Unlock()
}

func (f *Feishu) sendCard(ctx context.Context, receiver, receiveType string, content feishuContent, imageKeys []string, link, replyTo string) (string, error) {
	// The action button already opens the source, so the raw URL is noise here.
	// QQ keeps it because it has no button to click.
	text := content.text
	if link != "" {
		text = stripURL(text, link)
	}
	text = toFeishuLines(text)
	title := content.source
	header := content.sender
	if header == "" {
		header = title
	}
	elements := make([]map[string]interface{}, 0, len(imageKeys)+4)
	if len(content.turns) > 0 {
		for _, turn := range content.turns {
			head, grey := turnParts(turn.Author, turn.Text, turn.Translation)
			if head == "" && grey == "" {
				continue
			}
			if head != "" {
				elements = append(elements, map[string]interface{}{
					"tag": "div", "text": map[string]string{"tag": "lark_md", "content": toFeishuLines(head)},
				})
			}
			if grey != "" {
				elements = append(elements, originalElement(grey))
			}
		}
	} else {
		if content.quote != nil {
			head, grey := turnParts(content.quote.Author, content.quote.Text, content.quote.Translation)
			if head != "" {
				elements = append(elements, map[string]interface{}{
					"tag": "div", "text": map[string]string{"tag": "lark_md", "content": toFeishuLines(head)},
				})
			}
			if grey != "" {
				elements = append(elements, originalElement(grey))
			}
			// Separate the quoted message from the reply with a divider line.
			if (head != "" || grey != "") && (text != "" || strings.TrimSpace(content.original) != "") {
				elements = append(elements, map[string]interface{}{"tag": "hr"})
			}
		}
		// 正文里可能夹着「----」这类纯横杠行（B站/抖音/TikTok 的原标题常带）。
		// 直接拼进文本会很丑，这里按行拆开：横杠行单独渲染成分隔线，
		// 其余行合成一个 lark_md div。
		elements = append(elements, renderBodyWithDividers(text)...)
		if strings.TrimSpace(content.original) != "" {
			elements = append(elements, originalElement(content.original))
		}
	}
	for _, key := range imageKeys {
		elements = append(elements, map[string]interface{}{
			"tag": "img", "img_key": key,
			"alt": map[string]string{"tag": "plain_text", "content": "消息图片"},
		})
	}
	// Room chatter has nothing worth opening in a browser, so it gets no button.
	if link != "" && wantsLinkButton(title) {
		elements = append(elements, map[string]interface{}{
			"tag": "action", "actions": []map[string]interface{}{{
				"tag": "button", "type": "primary", "url": link,
				"text": map[string]string{"tag": "plain_text", "content": actionLabel(title)},
			}},
		})
	}
	elements = append(elements, map[string]interface{}{"tag": "hr"})
	elements = append(elements, map[string]interface{}{
		"tag": "note", "elements": []map[string]string{{"tag": "plain_text", "content": footerNote(content)}},
	})
	card := map[string]interface{}{
		"config": map[string]bool{"wide_screen_mode": true},
		"header": map[string]interface{}{
			"template": cardColor(title),
			"title":    map[string]string{"tag": "plain_text", "content": header},
		},
		"elements": elements,
	}
	if replyTo != "" {
		if err := f.sendReply(ctx, replyTo, receiver, receiveType, "interactive", card); err == nil {
			return "", nil
		} else {
			// A dead anchor must not cost the user the card. Feishu answers 230002
			// for anchors this app can no longer reach (stale id from a previous
			// app binding, deleted message, chat the bot is not in). Returning the
			// error used to hand the whole message to the caller's plain-text
			// fallback, so a run of replies would silently lose the card styling.
			// Send it as a normal message instead and prune the anchor.
			log.Printf("[Outbound:feishu] status=reply_anchor_failed anchor=%s error=%v action=send_top_level", replyTo, err)
			if f.replyMap != nil {
				if n := f.replyMap.DeleteMessage(replyTo); n > 0 {
					log.Printf("[Outbound:feishu] status=replymap_pruned anchor=%s entries=%d", replyTo, n)
				}
			}
		}
	}
	return f.sendMessage(ctx, receiver, receiveType, "interactive", card)
}

// wantsLinkButton reports whether a source benefits from an outbound button.
// Pocket48 room messages are self-contained: opening the app adds nothing.
func wantsLinkButton(source string) bool {
	lowered := strings.ToLower(source)
	for _, skip := range []string{"包间", "口袋", "pocket48", "房间"} {
		if strings.Contains(lowered, skip) {
			return false
		}
	}
	return true
}

// footerNote carries provenance and time, which is what the note element is
// for. Repeating them in the header made cards noisy. The time is always the
// sender's own send time (recovered from the message body); we never substitute
// our own delivery time, so a message without a recoverable stamp simply shows
// no time instead of a misleading one.
func footerNote(content feishuContent) string {
	parts := make([]string, 0, 3)
	source := strings.TrimSpace(content.sourceLabel)
	if source == "" {
		source = strings.TrimSpace(content.source)
	}
	if source != "" {
		parts = append(parts, source)
	} else {
		parts = append(parts, "Pocket48")
	}
	if stamp := strings.TrimSpace(content.timestamp); stamp != "" {
		parts = append(parts, stamp)
	}
	return strings.Join(parts, " · ")
}

// turnParts splits one speaker's message (or a quoted message) into its white
// head (author + translation) and its grey original line. With no translation
// the original becomes the head so the text stays readable. This lets every
// turn render with the same 译文白 / 原文灰 rule.
func turnParts(author, original, translation string) (head, grey string) {
	author = strings.TrimSpace(author)
	original = strings.TrimSpace(original)
	translation = strings.TrimSpace(translation)
	if translation == original {
		translation = ""
	}
	if author != "" {
		head = author + "："
	}
	if translation != "" {
		head += translation
		grey = original
	} else {
		head += original
	}
	return head, grey
}

// originalElement renders the untranslated source text as a grey note block
// beneath the (white) translation.
func originalElement(original string) map[string]interface{} {
	text := strings.TrimSpace(original)
	return map[string]interface{}{
		"tag": "note",
		"elements": []map[string]string{
			{"tag": "plain_text", "content": text},
		},
	}
}

// toFeishuLines forces real line breaks: a single \n is a soft break in
// lark_md and renders as a space, which glued timestamps to the text.
func toFeishuLines(text string) string {
	lines := make([]string, 0, 8)
	for _, line := range strings.Split(text, "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			lines = append(lines, trimmed)
		}
	}
	return strings.Join(lines, "\n\n")
}

// danglingLinkLabel matches a line that is nothing but a "<来源>链接：" prefix
// once its URL has been lifted into a Feishu button (微博链接：/抖音链接：/
// 小红书链接：...). Kept line-anchored and short so body copy is never touched.
var danglingLinkLabel = regexp.MustCompile(`^.{0,16}链接[：:]\s*$`)

func stripURL(text, link string) string {
	cleaned := strings.ReplaceAll(text, link, "")
	lines := strings.Split(cleaned, "\n")
	kept := lines[:0]
	for _, line := range lines {
		if danglingLinkLabel.MatchString(strings.TrimSpace(line)) {
			continue
		}
		kept = append(kept, line)
	}
	return strings.TrimSpace(strings.Join(kept, "\n"))
}

func lastURL(text string) string {
	urls := feishuURLPattern.FindAllString(text, -1)
	if len(urls) == 0 {
		return ""
	}
	return strings.TrimRight(urls[len(urls)-1], ").,，。；;")
}

func cardColor(title string) string {
	switch {
	case strings.Contains(title, "微博"):
		return "carmine"
	case strings.Contains(title, "小红书"), strings.Contains(title, "抖音"):
		return "red"
	case strings.Contains(title, "Weverse"), strings.Contains(title, "H2H"), strings.Contains(title, "Hearts2Hearts"):
		return "wathet"
	case strings.Contains(title, "Melon"):
		return "green"
	case strings.Contains(title, "Instagram"):
		return "purple"
	case strings.Contains(title, "B站"), strings.Contains(title, "bilibili"), strings.Contains(title, "哔哩哔哩"):
		return "violet"
	case strings.Contains(title, "Pocket48"), strings.Contains(title, "房间"), strings.Contains(title, "包间"):
		return "purple"
	case strings.Contains(title, "|X"), strings.HasSuffix(title, "X"):
		return "grey"
	default:
		return "blue"
	}
}

// actionLabel is the Feishu card button text.
//
// All entries follow one shape —「打开 + 平台名」— so the buttons look like a
// set. The previous mix（「查看原微博」/「在 X 中查看」/「查看小红书」vs
// 「打开抖音」/「打开 Weverse」）read as unrelated buttons to the same users.
func actionLabel(title string) string {
	switch {
	case strings.Contains(title, "微博"):
		return "打开微博"
	case strings.Contains(title, "小红书"):
		return "打开小红书"
	case strings.Contains(title, "抖音"):
		return "打开抖音"
	case strings.Contains(title, "Weverse"):
		return "打开 Weverse"
	case strings.Contains(title, "Instagram"):
		return "打开 Instagram"
	case strings.Contains(title, "B站"), strings.Contains(title, "bilibili"), strings.Contains(title, "哔哩哔哩"):
		return "打开 B站"
	case strings.Contains(title, "|X"), strings.HasSuffix(title, "X"):
		return "打开 X"
	// ★ TikTok 之前落到 default 显示「查看原文」，与其余平台不一致
	//   （用户 2026-10-05 指出）。
	case strings.Contains(title, "TikTok"), strings.HasSuffix(title, "TikTok"):
		return "打开 TikTok"
	case strings.Contains(title, "Melon"):
		return "打开 Melon"
	default:
		// 未知来源保持中性文案（action_label_test 依赖这一点）。
		return "查看原文"
	}
}

func (f *Feishu) sendText(ctx context.Context, receiver, receiveType, text string) error {
	_, err := f.sendMessage(ctx, receiver, receiveType, "text", map[string]string{"text": text})
	return err
}

func plainFallback(title, body string) string {
	if body == "" {
		return title
	}
	return title + "\n" + strings.NewReplacer("**", "", "<at id=all></at>", "@所有人").Replace(body)
}

func (f *Feishu) sendMediaSegment(ctx context.Context, receiver, receiveType string, segment message.Segment, replyTo string) error {
	source := segment.Data["file"]
	fileType := "mp4"
	msgType := "media"
	if segment.Type == "audio" {
		// Feishu renders a native voice bubble only for Opus. Everything else
		// (Pocket48 sends .aac, other sources mp3/m4a) is transcoded to Opus
		// so it arrives as one playable voice message instead of a placeholder
		// card plus a separate audio file.
		key, duration, err := f.uploadVoice(ctx, source)
		if err != nil {
			return err
		}
		content := map[string]string{"file_key": key}
		if duration > 0 {
			content["duration"] = strconv.Itoa(duration)
		}
		if replyTo != "" {
			return f.sendReply(ctx, replyTo, receiver, receiveType, "audio", content)
		}
		_, err = f.sendMessage(ctx, receiver, receiveType, "audio", content)
		return err
	}
	key, err := f.uploadResource(ctx, source, fileType)
	if err != nil {
		return err
	}
	// When a card preceded this media, attach it as a native reply so Feishu
	// renders the media under the card instead of as an orphaned message.
	if replyTo != "" {
		return f.sendReply(ctx, replyTo, receiver, receiveType, msgType, map[string]string{"file_key": key})
	}
	_, err = f.sendMessage(ctx, receiver, receiveType, msgType, map[string]string{"file_key": key})
	return err
}

func (f *Feishu) sendMessage(ctx context.Context, receiver, receiveType, msgType string, content interface{}) (string, error) {
	encoded, err := json.Marshal(content)
	if err != nil {
		return "", err
	}
	body := map[string]string{"receive_id": receiver, "msg_type": msgType, "content": string(encoded)}
	return f.postMessage(ctx, "/im/v1/messages?receive_id_type="+url.QueryEscape(receiveType), body)
}

// sendReply posts a message as a native reply to messageID. Feishu renders the
// reply under the original message so media reads as an attachment of the card.
func (f *Feishu) sendReply(ctx context.Context, messageID, receiver, receiveType, msgType string, content interface{}) error {
	encoded, err := json.Marshal(content)
	if err != nil {
		return err
	}
	body := map[string]string{"content": string(encoded), "msg_type": msgType}
	_, err = f.postMessage(ctx, "/im/v1/messages/"+url.PathEscape(messageID)+"/reply?receive_id_type="+url.QueryEscape(receiveType), body)
	return err
}

// postMessage is the shared message-send path. It returns the created message_id
// (empty when the platform does not return one) and any error.
func (f *Feishu) postMessage(ctx context.Context, path string, body map[string]string) (string, error) {
	var out struct {
		Data struct {
			MessageID string `json:"message_id"`
		} `json:"data"`
	}
	if err := f.authorizedJSON(ctx, http.MethodPost, path, body, &out); err != nil {
		return "", err
	}
	return out.Data.MessageID, nil
}

func (f *Feishu) uploadResource(ctx context.Context, source, resourceType string) (string, error) {
	key, _, err := f.uploadResourceWithDuration(ctx, source, resourceType, 0)
	return key, err
}

// uploadResourceWithDuration uploads media and optionally reports its duration
// in milliseconds. Feishu requires duration for voice/audio uploads — without
// it the bubble renders without a length.
func (f *Feishu) uploadResourceWithDuration(ctx context.Context, source, resourceType string, durationMS int) (string, int, error) {
	if source == "" {
		return "", 0, errors.New("empty media source")
	}
	cacheKey := resourceType + "\x00" + source
	f.cacheMu.RLock()
	if key := f.mediaKeys[cacheKey]; key != "" {
		f.cacheMu.RUnlock()
		return key, durationMS, nil
	}
	f.cacheMu.RUnlock()
	data, name, err := f.readMedia(ctx, source)
	if err != nil {
		return "", 0, err
	}
	var endpoint, keyField, typeField string
	if resourceType == "image" {
		endpoint, keyField, typeField = "/im/v1/images", "image_key", "image_type"
	} else {
		endpoint, keyField, typeField = "/im/v1/files", "file_key", "file_type"
	}
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	fieldValue := resourceType
	if resourceType == "image" {
		fieldValue = "message"
	}
	_ = w.WriteField(typeField, fieldValue)
	if resourceType != "image" {
		_ = w.WriteField("file_name", name)
	}
	// Feishu omits the length on voice bubbles unless duration is supplied.
	if resourceType != "image" && durationMS > 0 {
		_ = w.WriteField("duration", strconv.Itoa(durationMS))
	}
	part, err := w.CreateFormFile("image", name)
	if resourceType != "image" {
		part, err = w.CreateFormFile("file", name)
	}
	if err != nil {
		return "", 0, err
	}
	if _, err = part.Write(data); err != nil {
		return "", 0, err
	}
	_ = w.Close()
	var response struct {
		Code int               `json:"code"`
		Msg  string            `json:"msg"`
		Data map[string]string `json:"data"`
	}
	if err := f.authorized(ctx, http.MethodPost, endpoint, w.FormDataContentType(), &body, &response); err != nil {
		return "", 0, err
	}
	key := response.Data[keyField]
	if key == "" {
		return "", 0, errors.New("Feishu upload returned no resource key")
	}
	f.cacheMu.Lock()
	f.mediaKeys[cacheKey] = key
	f.cacheMu.Unlock()
	return key, durationMS, nil
}

// uploadVoice turns any audio source into a Feishu-native voice message upload
// (file_type=opus). Sources already carrying Opus are uploaded as-is; anything
// else is transcoded with ffmpeg, which is what lets Pocket48's .aac voice
// replies arrive as one playable bubble instead of a file attachment.
func (f *Feishu) uploadVoice(ctx context.Context, source string) (string, int, error) {
	if source == "" {
		return "", 0, errors.New("empty audio source")
	}
	if isOpusAudio(source) {
		return f.uploadResourceWithDuration(ctx, source, "opus", 0)
	}
	data, name, err := f.readMedia(ctx, source)
	if err != nil {
		return "", 0, err
	}
	ctx, cancel := context.WithTimeout(ctx, opusTranscodeTimeout)
	defer cancel()
	encoded, opusName, durationMS, err := transcodeToOpus(ctx, data, name)
	if err != nil {
		return "", 0, err
	}
	return f.uploadBytesAsOpus(ctx, encoded, opusName, durationMS)
}

// uploadBytesAsOpus uploads already-Opus bytes under a stable cache key derived
// from the payload, so repeated deliveries of the same clip skip re-uploading.
func (f *Feishu) uploadBytesAsOpus(ctx context.Context, data []byte, name string, durationMS int) (string, int, error) {
	sum := sha256.Sum256(data)
	cacheKey := "opus\x00" + hex.EncodeToString(sum[:])
	f.cacheMu.RLock()
	if key := f.mediaKeys[cacheKey]; key != "" {
		f.cacheMu.RUnlock()
		return key, durationMS, nil
	}
	f.cacheMu.RUnlock()

	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	_ = w.WriteField("file_type", "opus")
	_ = w.WriteField("file_name", name)
	if durationMS > 0 {
		_ = w.WriteField("duration", strconv.Itoa(durationMS))
	}
	part, err := w.CreateFormFile("file", name)
	if err != nil {
		return "", 0, err
	}
	if _, err = part.Write(data); err != nil {
		return "", 0, err
	}
	_ = w.Close()
	var response struct {
		Code int               `json:"code"`
		Msg  string            `json:"msg"`
		Data map[string]string `json:"data"`
	}
	if err := f.authorized(ctx, http.MethodPost, "/im/v1/files", w.FormDataContentType(), &body, &response); err != nil {
		return "", 0, err
	}
	key := response.Data["file_key"]
	if key == "" {
		return "", 0, errors.New("Feishu upload returned no resource key")
	}
	f.cacheMu.Lock()
	f.mediaKeys[cacheKey] = key
	f.cacheMu.Unlock()
	return key, durationMS, nil
}

func (f *Feishu) readMedia(ctx context.Context, source string) ([]byte, string, error) {
	if strings.HasPrefix(source, "base64://") {
		data, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(source, "base64://"))
		if int64(len(data)) > f.maxMediaBytes {
			return nil, "", errors.New("media exceeds configured size limit")
		}
		return data, "media.bin", err
	}
	if parsed, err := url.Parse(source); err == nil && (parsed.Scheme == "http" || parsed.Scheme == "https") {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, source, nil)
		if err != nil {
			return nil, "", err
		}
		// 抖音 CDN 不带 Referer 一律 403（2026-10-04 实测）。
		mediafetch.ApplyReferer(req, source)
		resp, err := f.http.Do(req)
		if err != nil {
			return nil, "", err
		}
		defer resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return nil, "", fmt.Errorf("media download HTTP %d", resp.StatusCode)
		}
		data, err := io.ReadAll(io.LimitReader(resp.Body, f.maxMediaBytes+1))
		if int64(len(data)) > f.maxMediaBytes {
			return nil, "", errors.New("media exceeds configured size limit")
		}
		name := filepath.Base(parsed.Path)
		if name == "" || name == "." || name == "/" {
			name = "media.bin"
		}
		return data, name, err
	}
	file, err := os.Open(source)
	if err != nil {
		return nil, "", err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, f.maxMediaBytes+1))
	if err != nil {
		return nil, "", err
	}
	if int64(len(data)) > f.maxMediaBytes {
		return nil, "", errors.New("media exceeds configured size limit")
	}
	return data, filepath.Base(source), nil
}

func (f *Feishu) authorizedJSON(ctx context.Context, method, path string, body interface{}, out interface{}) error {
	data, err := json.Marshal(body)
	if err != nil {
		return err
	}
	return f.authorized(ctx, method, path, "application/json; charset=utf-8", bytes.NewReader(data), out)
}

func (f *Feishu) authorized(ctx context.Context, method, path, contentType string, body io.Reader, out interface{}) error {
	token, err := f.accessToken(ctx)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, method, f.apiBase+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", contentType)
	resp, err := f.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("Feishu HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}
	var envelope struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
	}
	_ = json.Unmarshal(data, &envelope)
	if envelope.Code != 0 {
		return fmt.Errorf("Feishu code %d: %s", envelope.Code, envelope.Msg)
	}
	if out != nil {
		return json.Unmarshal(data, out)
	}
	return nil
}

func (f *Feishu) accessToken(ctx context.Context) (string, error) {
	f.tokenMu.Lock()
	defer f.tokenMu.Unlock()
	if f.token != "" && time.Now().Before(f.tokenExpires) {
		return f.token, nil
	}
	payload, _ := json.Marshal(map[string]string{"app_id": f.appID, "app_secret": f.appSecret})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, f.apiBase+"/auth/v3/tenant_access_token/internal", bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	resp, err := f.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var result struct {
		Code   int    `json:"code"`
		Msg    string `json:"msg"`
		Token  string `json:"tenant_access_token"`
		Expire int    `json:"expire"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&result); err != nil {
		return "", err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 || result.Code != 0 || result.Token == "" {
		return "", fmt.Errorf("Feishu token failed: HTTP %d code=%d %s", resp.StatusCode, result.Code, result.Msg)
	}
	f.token = result.Token
	expires := time.Duration(result.Expire) * time.Second
	if expires <= 0 {
		expires = 2 * time.Hour
	}
	f.tokenExpires = time.Now().Add(expires - minDuration(5*time.Minute, expires/4))
	return f.token, nil
}

func minDuration(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}

// 分隔线字符分两类，阈值不同：
//
//   - longDashChars：中文破折号 U+2014、一长音 U+2015、连接号 U+2013、
//     制表线 U+2500/U+2501、双横线 U+2550。这类字符本身就占一个全角宽度，
//     **2 个**就构成视觉分隔（B 站原标题常见「——」这种两字破折号）。
//   - shortDashChars：ASCII 减号、下划线、等号。字符很窄，
//     **3 个**以上才像分隔线，避免把正文里的 "--"、"a_b" 误判。
//
// 为什么分两档：早期版本统一要求 3 个，导致「——」被当正文原样显示，
// 正是用户 2026-10-04 反馈「标题和时长中间有一行只有横杠」的残留场景。
const (
	longDashChars  = "—―–─━═"
	dividerMinRune = 3
	dividerMinLong = 2
)

// dividerLinePattern 只负责「整行是否全由横杠类字符构成」，
// 长度门槛交给 isDividerLine 判断（需要区分长短横线）。
var dividerLinePattern = regexp.MustCompile(`^[\-–—―─━═_=]+$`)

// isDividerLine 判断该行是否为纯横杠装饰行。
func isDividerLine(line string) bool {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		return false
	}
	if !dividerLinePattern.MatchString(trimmed) {
		return false
	}
	if strings.ContainsAny(trimmed, longDashChars) {
		return len([]rune(trimmed)) >= dividerMinLong
	}
	return len([]rune(trimmed)) >= dividerMinRune
}

// renderBodyWithDividers 把正文渲染成飞书卡片元素：
// 纯横杠行 → {"tag":"hr"}，其余行 → 一个 lark_md div。
//
// 为什么需要它：飞书卡片的 lark_md 不把「---」当分隔线渲染，
// 原样显示就是一行难看的横杠字符。真正的分隔线必须用 hr 元素。
//
// 背景：抖音 / TikTok / B 站的原标题常带「----」这类装饰行
// （用户 2026-10-04 反馈「标题和时长两行中间有一行只有横杠」）。
// 放在渲染层处理，所有平台自动受益，无需各自改。
func renderBodyWithDividers(text string) []map[string]interface{} {
	if strings.TrimSpace(text) == "" {
		return nil
	}
	var (
		plain    []string
		elements []map[string]interface{}
		divider  bool
	)
	flush := func() {
		if len(plain) == 0 {
			return
		}
		elements = append(elements, map[string]interface{}{
			"tag": "div",
			"text": map[string]string{
				"tag":     "lark_md",
				"content": strings.Join(plain, "\n\n"),
			},
		})
		plain = plain[:0]
	}
	for _, line := range strings.Split(text, "\n") {
		if isDividerLine(line) {
			flush()
			// 连续两条横杠只保留一条，避免出现双重分隔线。
			if !divider {
				elements = append(elements, map[string]interface{}{"tag": "hr"})
				divider = true
			}
			continue
		}
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			divider = false
			plain = append(plain, trimmed)
		}
	}
	flush()
	return elements
}
