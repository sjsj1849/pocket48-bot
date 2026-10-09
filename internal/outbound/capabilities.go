package outbound

// RichTextFormat describes a platform-native rich text renderer. HTML is
// intentionally absent: chat platforms generally expose a constrained schema.
type RichTextFormat string

const (
	RichTextNone     RichTextFormat = "none"
	RichTextMarkdown RichTextFormat = "markdown"
	RichTextPost     RichTextFormat = "post"
)

// Capabilities controls rendering and message splitting for one adapter.
type Capabilities struct {
	Platform                  string
	MaxImagesPerMessage       int
	CanMixTextAndImages       bool
	VideoMustBeStandalone     bool
	SupportsCards             bool
	SupportsNativeLinkPreview bool
	RichText                  RichTextFormat
}

type CapabilityProvider interface {
	Capabilities() Capabilities
}
