package outbound

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Feishu renders a native voice bubble only for Opus audio. Its upload API
// documents file_type=opus and explicitly requires other formats to be
// converted first:
//
//	ffmpeg -i SourceFile.mp3 -acodec libopus -ac 1 -ar 16000 TargetFile.opus
//
// Pocket48 hands us AAC (and Weibo/other sources ship mp3/m4a), which used to
// degrade into a "file" message — the user saw a placeholder card plus a
// separate .aac attachment instead of one playable voice bubble. Transcoding
// lets every audio source deliver as msg_type=audio.
//
// Feishu also wants the duration on upload; without it the bubble shows no
// length, so we read it back from the transcoded file.

const (
	opusSampleRate = "16000"
	// opusBitrate keeps voice intelligible while staying small; Feishu's own
	// docs use a single mono stream and do not require a specific bitrate.
	opusBitrate = "32k"
	// Transcoding is CPU-bound but cheap for short voice clips. Cap it so a
	// corrupt or hostile source cannot wedge the outbound worker.
	opusTranscodeTimeout = 30 * time.Second
)

// opusToolReady caches whether ffmpeg exists so we probe the filesystem once
// instead of on every audio message.
var opusToolReady struct {
	sync.Once
	ok bool
}

func opusToolAvailable() bool {
	opusToolReady.Do(func() {
		if _, err := exec.LookPath("ffmpeg"); err == nil {
			opusToolReady.ok = true
		}
	})
	return opusToolReady.ok
}

// isOpusAudio reports whether the source is already Opus, in which case no
// transcode is needed. Feishu accepts .opus (and .ogg carrying Opus).
func isOpusAudio(source string) bool {
	switch strings.ToLower(strings.TrimPrefix(filepath.Ext(strings.Split(source, "?")[0]), ".")) {
	case "opus", "ogg":
		return true
	}
	return false
}

// audioDurationMS probes the duration of an audio file. Feishu shows no length
// on the voice bubble unless duration is supplied at upload time.
func audioDurationMS(data []byte, name string) int {
	tmp, err := os.CreateTemp("", "feishu-audio-probe-*")
	if err != nil {
		return 0
	}
	defer os.Remove(tmp.Name())
	if _, err = tmp.Write(data); err != nil {
		tmp.Close()
		return 0
	}
	if err = tmp.Close(); err != nil {
		return 0
	}
	ctx, cancel := context.WithTimeout(context.Background(), opusTranscodeTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "ffprobe",
		"-v", "error",
		"-show_entries", "format=duration",
		"-of", "default=noprint_wrappers=1:nokey=1",
		tmp.Name(),
	).Output()
	if err != nil {
		return 0
	}
	seconds, err := strconv.ParseFloat(strings.TrimSpace(string(out)), 64)
	if err != nil || seconds <= 0 {
		return 0
	}
	return int(seconds * 1000)
}

// transcodeToOpus converts any audio the bot can read into the single-channel
// Opus stream Feishu requires for voice messages. It returns the Opus bytes,
// the file name to upload under, and the duration in milliseconds.
func transcodeToOpus(ctx context.Context, data []byte, name string) ([]byte, string, int, error) {
	if len(data) == 0 {
		return nil, "", 0, errors.New("empty audio payload")
	}
	if !opusToolAvailable() {
		return nil, "", 0, errors.New("ffmpeg unavailable: cannot produce Opus for Feishu voice message")
	}
	// Extension drives ffmpeg's demuxer. Keep the original one when it is a
	// real audio extension, otherwise let ffmpeg probe the bytes.
	ext := strings.ToLower(filepath.Ext(name))
	inPath, err := writeTempAudio(data, "feishu-in-*"+ext)
	if err != nil {
		return nil, "", 0, err
	}
	defer os.Remove(inPath)

	outDir, err := os.MkdirTemp("", "feishu-opus-")
	if err != nil {
		return nil, "", 0, err
	}
	defer os.RemoveAll(outDir)
	outPath := filepath.Join(outDir, "voice.opus")

	cmd := exec.CommandContext(ctx, "ffmpeg",
		"-hide_banner", "-loglevel", "error", "-nostdin", "-y",
		"-i", inPath,
		"-acodec", "libopus",
		"-ac", "1",
		"-ar", opusSampleRate,
		"-b:a", opusBitrate,
		outPath,
	)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		reason := strings.TrimSpace(stderr.String())
		if len(reason) > 200 {
			reason = reason[:200]
		}
		if reason != "" {
			return nil, "", 0, fmt.Errorf("ffmpeg opus transcode failed: %v: %s", err, reason)
		}
		return nil, "", 0, fmt.Errorf("ffmpeg opus transcode failed: %w", err)
	}
	encoded, err := os.ReadFile(outPath)
	if err != nil {
		return nil, "", 0, err
	}
	if len(encoded) == 0 {
		return nil, "", 0, errors.New("ffmpeg produced an empty Opus file")
	}
	return encoded, "voice.opus", audioDurationMS(encoded, outPath), nil
}

func writeTempAudio(data []byte, pattern string) (string, error) {
	tmp, err := os.CreateTemp("", pattern)
	if err != nil {
		return "", err
	}
	defer tmp.Close()
	if _, err = tmp.Write(data); err != nil {
		os.Remove(tmp.Name())
		return "", err
	}
	return tmp.Name(), nil
}