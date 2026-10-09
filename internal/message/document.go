package message

import (
	"strings"
	"time"
)

// Document describes business meaning before a platform chooses its visual
// representation. Existing Segment messages can coexist during migration.
type Document struct {
	Source     string
	// Label is a finer-grained source tag shown in the card footer instead of
	// Source (e.g. Melon's "Melon 官方文章" / "新专辑"). Source still drives
	// the button label and card color.
	Label       string
	Kind        string
	Title       string
	Author      string
	Room        string
	Body        string
	// Translation holds the machine translation of Body. It renders above the
	// original text (译文在上、原文在下) so a reader sees the translation first.
	// Adapters style the original as a secondary grey block.
	Translation string
	Quote       *Quote
	// Turns carries a merged conversation (e.g. Melon Music Wave): each turn is
	// one speaker's message with its own original + translation.
	Turns      []Turn
	Link       string
	CreatedAt  time.Time
	MentionAll bool
	Media      []Media
	// SourceID is the business identity of this message (e.g. a Weverse
	// comment id). Adapters record it against the platform message id they
	// produce so a later reply can be threaded onto this message.
	SourceID string
	// ReplyToSourceID points at the SourceID of the message this one replies
	// to. When an adapter knows the platform message id for that source, it
	// threads this message under it as a native reply.
	ReplyToSourceID string
	// ReplyToFallbackSourceIDs are lower-priority threading targets, tried in
	// order when ReplyToSourceID has no recorded platform message.
	//
	// Why this exists: Weverse members usually reply to a *fan* comment, and we
	// never forward fan comments — so the parent's message id is unknown and
	// threading silently degrades to an inline quote. Listing the thread's post
	// as a fallback lets the reply still land under the post it belongs to
	// instead of floating loose in the chat.
	ReplyToFallbackSourceIDs []string
	// KeepQuoteWhenThreaded keeps Quote rendered even after the message was
	// threaded natively. Normally a native reply already shows the parent
	// above it, so the quote would be redundant and adapters drop it. Weverse
	// turns it on because the threaded parent is frequently the *post* rather
	// than the comment being answered — the quoted fan comment is then the only
	// place that comment appears.
	KeepQuoteWhenThreaded bool
	// ReplyAuthorPrefix asks the adapter to render the reply body through the
	// same "作者：内容" path it uses for quoted messages. With both Quote and a
	// prefixed body on screen, the two lines line up as parallel speakers
	// (粉丝昵称：… / 成员名：…) instead of an anonymous line under a named one.
	ReplyAuthorPrefix string
}

type Quote struct {
	Author string
	Text   string
	// Translation holds the machine translation of the quoted text, when the
	// quote carries both an original and a translation.
	Translation string
}

// Turn is one speaker's message inside a merged conversation (Turns).
type Turn struct {
	Author      string
	Text        string // 原文
	Translation string // 译文
}

// AsDocument extracts the structured Document if the content carries one, so
// adapters can render reply-to context and per-field styling natively.
func AsDocument(content interface{}) (Document, bool) {
	switch value := content.(type) {
	case Document:
		return value, true
	case *Document:
		if value != nil {
			return *value, true
		}
	}
	return Document{}, false
}

type Media struct {
	Kind    string
	Source  string
	Cover   string
	Caption string
}

// ToSegments provides the stable linear fallback used by QQ and by any future
// adapter that does not yet have a source-specific native renderer.
func ToSegments(content interface{}) interface{} {
	doc, ok := content.(Document)
	if !ok {
		if pointer, pointerOK := content.(*Document); pointerOK && pointer != nil {
			doc, ok = *pointer, true
		}
	}
	if !ok {
		return content
	}
	segments := make([]Segment, 0, len(doc.Media)+3)
	if doc.MentionAll {
		segments = append(segments, MentionAll())
	}
	var lines []string
	header := strings.TrimSpace(doc.Title)
	if header == "" {
		header = strings.TrimSpace(doc.Author)
		if doc.Source != "" {
			header += "|" + doc.Source
		}
	}
	if header != "" {
		lines = append(lines, "【"+header+"】")
	}
	if doc.Quote != nil {
		if author := strings.TrimSpace(doc.Quote.Author); author != "" {
			lines = append(lines, author+"：")
		}
		qtext := strings.TrimSpace(doc.Quote.Text)
		qtr := strings.TrimSpace(doc.Quote.Translation)
		if qtr == qtext {
			qtr = ""
		}
		if qtr != "" {
			lines = append(lines, qtr)
		}
		if qtext != "" {
			lines = append(lines, qtext)
		}
		if len(lines) > 0 {
			lines = append(lines, "")
		}
	}
	if len(doc.Turns) > 0 {
		for _, turn := range doc.Turns {
			author := strings.TrimSpace(turn.Author)
			body := strings.TrimSpace(turn.Text)
			translation := strings.TrimSpace(turn.Translation)
			if translation == body {
				translation = ""
			}
			line := ""
			if author != "" {
				line = author + "："
			}
			if translation != "" {
				line += translation
				if body != "" {
					line += "\n" + body
				}
			} else if body != "" {
				line += body
			}
			if line != "" {
				lines = append(lines, line)
			}
		}
	} else {
		body := strings.TrimSpace(doc.Body)
		translation := strings.TrimSpace(doc.Translation)
		if translation == body {
			translation = ""
		}
		if translation != "" {
			lines = append(lines, translation)
		}
		if body != "" {
			lines = append(lines, body)
		}
	}
	if strings.TrimSpace(doc.Link) != "" {
		lines = append(lines, "", strings.TrimSpace(doc.Link))
	}
	if !doc.CreatedAt.IsZero() {
		lines = append(lines, doc.CreatedAt.Format("2006-01-02 15:04:05"))
	}
	if len(lines) > 0 {
		segments = append(segments, Text(strings.Join(lines, "\n")))
	}
	for _, media := range doc.Media {
		switch media.Kind {
		case "image":
			segments = append(segments, Image(media.Source))
		case "video":
			segments = append(segments, Video(media.Source, media.Cover))
		case "audio":
			segments = append(segments, Audio(media.Source))
		}
	}
	return segments
}
