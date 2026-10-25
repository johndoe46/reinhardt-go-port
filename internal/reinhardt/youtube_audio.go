package reinhardt

import (
	"context"
	"fmt"
	"math"
	"os/exec"
	"strconv"
	"strings"
)

const youtubePeakTargetDB = -1.0

func youtubePeakGain(ctx context.Context, source string) (float64, error) {
	// astats measures decoded samples without volumedetect's 16-bit rounding.
	// The overall peak spans all channels; one gain preserves their balance.
	cmd := exec.CommandContext(ctx, "ffmpeg", "-hide_banner", "-nostdin", "-nostats", "-v", "info",
		"-i", source, "-map", "0:a:0", "-af", "astats=measure_perchannel=none:measure_overall=Peak_level", "-f", "null", "-")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return 0, fmt.Errorf("scan YouTube audio peak: %w: %.1000s", err, output)
	}
	const label = "Peak level dB:"
	index := strings.LastIndex(string(output), label)
	if index < 0 {
		return 0, fmt.Errorf("scan YouTube audio peak: ffmpeg did not report a peak")
	}
	fields := strings.Fields(string(output[index+len(label):]))
	if len(fields) == 0 {
		return 0, fmt.Errorf("scan YouTube audio peak: ffmpeg reported an empty peak")
	}
	peak, err := strconv.ParseFloat(fields[0], 64)
	if err != nil || math.IsNaN(peak) || math.IsInf(peak, 1) {
		return 0, fmt.Errorf("scan YouTube audio peak: invalid peak %q", fields[0])
	}
	if math.IsInf(peak, -1) {
		return 0, nil // Silence has no finite peak and needs no amplification.
	}
	return youtubePeakTargetDB - peak, nil
}

func normalizeYouTubeAudio(ctx context.Context, source, destination string) error {
	gain, err := youtubePeakGain(ctx, source)
	if err != nil {
		return err
	}
	// Apply a constant gain during the only lossy encode. The preceding scan
	// writes no audio file; the downloaded source is never transcoded first.
	filter := "volume=" + strconv.FormatFloat(gain, 'f', 6, 64) + "dB:precision=double"
	cmd := exec.CommandContext(ctx, "ffmpeg", "-hide_banner", "-nostdin", "-nostats", "-v", "error",
		"-i", source, "-map", "0:a:0", "-af", filter, "-c:a", "libmp3lame", "-b:a", "320k", destination)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("encode peak-normalized YouTube audio: %w: %.1000s", err, output)
	}
	return nil
}
