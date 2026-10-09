package outbound

import "pocket48-bot/internal/message"

// Plan splits a normalized message according to a platform's delivery rules.
// It preserves segment order and returns platform-neutral batches.
func Plan(content interface{}, caps Capabilities) []interface{} {
	segments, ok := content.([]message.Segment)
	if !ok {
		if segment, single := content.(message.Segment); single {
			segments = []message.Segment{segment}
		} else {
			return []interface{}{content}
		}
	}
	if len(segments) == 0 {
		return nil
	}

	var batches []interface{}
	current := make([]message.Segment, 0, len(segments))
	images := 0
	flush := func() {
		if len(current) == 0 {
			return
		}
		batches = append(batches, current)
		current = nil
		images = 0
	}

	for _, segment := range segments {
		if segment.Type == "video" && caps.VideoMustBeStandalone {
			flush()
			batches = append(batches, []message.Segment{segment})
			continue
		}
		if segment.Type == "image" {
			if !caps.CanMixTextAndImages && len(current) > 0 {
				flush()
			}
			if caps.MaxImagesPerMessage > 0 && images >= caps.MaxImagesPerMessage {
				flush()
			}
			images++
		} else if !caps.CanMixTextAndImages && images > 0 {
			flush()
		}
		current = append(current, segment)
	}
	flush()
	return batches
}
