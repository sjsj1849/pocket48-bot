package logic

import (
	"bytes"
	"encoding/binary"
	"image/png"
)

// pngDimensions 只读 PNG 头部取宽高（避免整个解码）。
func pngDimensions(data []byte) (int, int) {
	if len(data) < 24 {
		return 0, 0
	}
	if !bytes.Equal(data[1:4], []byte("PNG")) {
		// 兜底：完整解码
		img, err := png.Decode(bytes.NewReader(data))
		if err != nil {
			return 0, 0
		}
		b := img.Bounds()
		return b.Dx(), b.Dy()
	}
	w := int(binary.BigEndian.Uint32(data[16:20]))
	h := int(binary.BigEndian.Uint32(data[20:24]))
	return w, h
}
