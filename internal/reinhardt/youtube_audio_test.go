package reinhardt

import (
	"encoding/binary"
	"fmt"
	"math"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestYouTubePeakGain(test *testing.T) {
	installYouTubeTestTools(test)
	for _, scenario := range []struct {
		peak    string
		gain    float64
		invalid bool
	}{
		{peak: "-6.250000", gain: 5.25},
		{peak: "-1.000000", gain: 0},
		{peak: "-0.250000", gain: -0.75},
		{peak: "2.500000", gain: -3.5},
		{peak: "-120.000000", gain: 119},
		{peak: "-inf", gain: 0},
		{peak: "NaN", invalid: true},
		{peak: "inf", invalid: true},
		{peak: "invalid", invalid: true},
		{peak: "", invalid: true},
	} {
		test.Run(scenario.peak, func(test *testing.T) {
			test.Setenv("YTDLP_TEST_PEAK", scenario.peak)
			gain, err := youtubePeakGain(test.Context(), "source.webm")
			if scenario.invalid {
				if err == nil {
					test.Fatal("accepted invalid peak measurement")
				}
				return
			}
			if err != nil || math.Abs(gain-scenario.gain) > 0.000001 {
				test.Fatalf("gain = %v, want %v: %v", gain, scenario.gain, err)
			}
		})
	}
}

func TestRealYouTubePeakNormalization(test *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		test.Skip("optional ffmpeg unavailable")
	}
	for _, amplitude := range []float64{0.2, 1, 0} {
		test.Run(fmt.Sprintf("amplitude=%g", amplitude), func(test *testing.T) {
			root := test.TempDir()
			source := filepath.Join(root, "original audio.wav")
			destination := filepath.Join(root, "normalized audio.mp3")
			// The right channel is four times louder. Both channels double in
			// amplitude halfway through, so compression or separate channel gains
			// would alter the ratios checked after decoding the resulting MP3.
			input := fmt.Sprintf(`aevalsrc=%g*(0.5+0.5*gte(t\,0.5))*sin(2*PI*400*t)|%g*(0.5+0.5*gte(t\,0.5))*sin(2*PI*1000*t):s=48000:d=1`, amplitude/4, amplitude)
			generate := exec.CommandContext(test.Context(), ffmpeg, "-v", "error", "-f", "lavfi", "-i", input, "-c:a", "pcm_f32le", source)
			if output, err := generate.CombinedOutput(); err != nil {
				test.Fatalf("generate fixture: %v: %s", err, output)
			}
			if err := normalizeYouTubeAudio(test.Context(), source, destination); err != nil {
				test.Fatal(err)
			}
			decode := exec.CommandContext(test.Context(), ffmpeg, "-v", "error", "-i", destination, "-f", "f64le", "-c:a", "pcm_f64le", "-ac", "2", "-ar", "48000", "-")
			data, err := decode.Output()
			if err != nil || len(data) < 48000*2*8 {
				test.Fatalf("decode normalized MP3: %d bytes: %v", len(data), err)
			}
			peak := 0.0
			var energy [2][2]float64
			for offset := 0; offset+8 <= len(data); offset += 8 {
				sample := math.Float64frombits(binary.LittleEndian.Uint64(data[offset:]))
				peak = math.Max(peak, math.Abs(sample))
				frame := offset / 16
				// Stay away from codec padding and the amplitude transition.
				if frame >= 4800 && frame < 19200 {
					energy[0][(offset/8)%2] += sample * sample
				} else if frame >= 28800 && frame < 43200 {
					energy[1][(offset/8)%2] += sample * sample
				}
			}
			if amplitude == 0 {
				if peak != 0 {
					test.Fatalf("silence was amplified into noise: peak=%g", peak)
				}
				return
			}
			peakDB := 20 * math.Log10(peak)
			if math.Abs(peakDB-youtubePeakTargetDB) > 0.3 {
				test.Fatalf("decoded MP3 peak=%g dBFS, want approximately %g", peakDB, youtubePeakTargetDB)
			}
			for section := range 2 {
				if ratio := math.Sqrt(energy[section][1] / energy[section][0]); math.Abs(ratio-4) > 0.1 {
					test.Fatalf("stereo balance changed: ratio=%g, want 4", ratio)
				}
			}
			for channel := range 2 {
				if ratio := math.Sqrt(energy[1][channel] / energy[0][channel]); math.Abs(ratio-2) > 0.05 {
					test.Fatalf("dynamics changed: ratio=%g, want 2", ratio)
				}
			}
		})
	}
}
