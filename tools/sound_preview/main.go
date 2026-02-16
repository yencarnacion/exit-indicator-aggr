package main

import (
	"bytes"
	"encoding/binary"
	"errors"
	"flag"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
)

const sampleRate = 44100

type note struct {
	freqHz float64
	startS float64
	durS   float64
	amp    float64
	wave   string
}

type soundDef struct {
	key         string
	title       string
	description string
	notes       []note
	filePath    string
	source      string
}

type playerCmd struct {
	name string
	args []string
}

func waveSample(kind string, phase float64) float64 {
	switch kind {
	case "square":
		if math.Sin(phase) >= 0 {
			return 1.0
		}
		return -1.0
	case "glass":
		// Bright/glassy with stronger upper partials.
		return 0.70*math.Sin(phase) + 0.22*math.Sin(2.0*phase) + 0.08*math.Sin(3.0*phase)
	case "round":
		// Darker/rounder with more fundamental plus mild sub + overtone.
		return 0.84*math.Sin(phase) + 0.10*math.Sin(0.5*phase) + 0.06*math.Sin(1.5*phase)
	default:
		return math.Sin(phase)
	}
}

func envelope(t, durS float64) float64 {
	const attackS = 0.003
	if t < 0.0 || t > durS {
		return 0.0
	}
	if t < attackS {
		return math.Max(0.0, t/attackS)
	}
	remain := math.Max(1e-6, durS-attackS)
	x := (t - attackS) / remain
	// Approximate an exponential fall from 1.0 to ~0.001 by note end.
	return math.Max(0.0, math.Exp(-6.9*x))
}

func synthesize(notes []note) ([]int16, error) {
	if len(notes) == 0 {
		return nil, errors.New("no notes supplied")
	}

	endS := 0.0
	for _, n := range notes {
		e := n.startS + n.durS
		if e > endS {
			endS = e
		}
	}
	endS += 0.05
	frameCount := int(endS * sampleRate)
	buf := make([]float64, frameCount)

	for _, n := range notes {
		startI := int(n.startS * sampleRate)
		nFrames := int(n.durS * sampleRate)
		omega := 2.0 * math.Pi * n.freqHz

		for j := 0; j < nFrames; j++ {
			idx := startI + j
			if idx < 0 || idx >= frameCount {
				break
			}
			t := float64(j) / sampleRate
			v := waveSample(n.wave, omega*t) * envelope(t, n.durS)
			buf[idx] += n.amp * v
		}
	}

	peak := 0.0
	for _, v := range buf {
		if math.Abs(v) > peak {
			peak = math.Abs(v)
		}
	}
	scale := 1.0
	if peak > 0.95 {
		scale = 0.95 / peak
	}

	out := make([]int16, frameCount)
	for i, v := range buf {
		s := v * scale
		if s > 1.0 {
			s = 1.0
		}
		if s < -1.0 {
			s = -1.0
		}
		out[i] = int16(s * 32767.0)
	}
	return out, nil
}

func writeWAV(path string, samples []int16) error {
	const channels = 1
	const bitsPerSample = 16
	byteRate := sampleRate * channels * (bitsPerSample / 8)
	blockAlign := channels * (bitsPerSample / 8)
	dataSize := len(samples) * 2
	riffChunkSize := 36 + dataSize

	var b bytes.Buffer
	b.WriteString("RIFF")
	if err := binary.Write(&b, binary.LittleEndian, uint32(riffChunkSize)); err != nil {
		return err
	}
	b.WriteString("WAVE")

	b.WriteString("fmt ")
	if err := binary.Write(&b, binary.LittleEndian, uint32(16)); err != nil { // PCM fmt chunk size
		return err
	}
	if err := binary.Write(&b, binary.LittleEndian, uint16(1)); err != nil { // PCM format
		return err
	}
	if err := binary.Write(&b, binary.LittleEndian, uint16(channels)); err != nil {
		return err
	}
	if err := binary.Write(&b, binary.LittleEndian, uint32(sampleRate)); err != nil {
		return err
	}
	if err := binary.Write(&b, binary.LittleEndian, uint32(byteRate)); err != nil {
		return err
	}
	if err := binary.Write(&b, binary.LittleEndian, uint16(blockAlign)); err != nil {
		return err
	}
	if err := binary.Write(&b, binary.LittleEndian, uint16(bitsPerSample)); err != nil {
		return err
	}

	b.WriteString("data")
	if err := binary.Write(&b, binary.LittleEndian, uint32(dataSize)); err != nil {
		return err
	}
	for _, s := range samples {
		if err := binary.Write(&b, binary.LittleEndian, s); err != nil {
			return err
		}
	}

	return os.WriteFile(path, b.Bytes(), 0o644)
}

func commandExists(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

func runFirstAvailable(candidates []playerCmd) error {
	available := make([]string, 0, len(candidates))
	var lastErr error

	for _, c := range candidates {
		if !commandExists(c.name) {
			continue
		}
		available = append(available, c.name)
		if err := exec.Command(c.name, c.args...).Run(); err == nil {
			return nil
		} else {
			lastErr = err
		}
	}

	if len(available) == 0 {
		names := make([]string, 0, len(candidates))
		for _, c := range candidates {
			names = append(names, c.name)
		}
		return fmt.Errorf("no supported audio player found (install one of: %s)", strings.Join(names, ", "))
	}
	return fmt.Errorf("audio playback failed via %s: %w", strings.Join(available, ", "), lastErr)
}

func playWAV(path string) error {
	err := runFirstAvailable([]playerCmd{
		{name: "aplay", args: []string{"-q", path}},
		{name: "paplay", args: []string{path}},
		{name: "ffplay", args: []string{"-nodisp", "-autoexit", "-loglevel", "quiet", path}},
		{name: "afplay", args: []string{path}},
	})
	if err == nil {
		return nil
	}

	if runtime.GOOS == "windows" {
		quoted := strings.ReplaceAll(path, "'", "''")
		ps := fmt.Sprintf("(New-Object Media.SoundPlayer '%s').PlaySync();", quoted)
		return exec.Command("powershell", "-NoProfile", "-Command", ps).Run()
	}
	return err
}

func playMP3(path string) error {
	return runFirstAvailable([]playerCmd{
		{name: "ffplay", args: []string{"-nodisp", "-autoexit", "-loglevel", "quiet", path}},
		{name: "afplay", args: []string{path}},
		{name: "mpg123", args: []string{"-q", path}},
		{name: "mpg321", args: []string{"-q", path}},
		{name: "play", args: []string{"-q", path}},
	})
}

func playAudioFile(path string) error {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".wav", ".wave":
		return playWAV(path)
	case ".mp3", ".mpeg":
		return playMP3(path)
	default:
		return fmt.Errorf("unsupported audio file extension %q for %s", ext, path)
	}
}

func playSamples(samples []int16) error {
	tmpName := fmt.Sprintf("ei-sound-%d.wav", time.Now().UnixNano())
	path := filepath.Join(os.TempDir(), tmpName)
	if err := writeWAV(path, samples); err != nil {
		return err
	}
	defer func() {
		_ = os.Remove(path)
	}()
	return playWAV(path)
}

func isSupportedAudioExt(ext string) bool {
	switch strings.ToLower(ext) {
	case ".wav", ".wave", ".mp3", ".mpeg":
		return true
	default:
		return false
	}
}

func discoverFileSounds(soundDir string) ([]soundDef, error) {
	entries, err := os.ReadDir(soundDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}

	fileSounds := make([]soundDef, 0)
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		ext := strings.ToLower(filepath.Ext(name))
		if !isSupportedAudioExt(ext) {
			continue
		}
		fileSounds = append(fileSounds, soundDef{
			key:         name,
			title:       fmt.Sprintf("File sound (%s)", strings.TrimPrefix(ext, ".")),
			description: "Static alert file served by /sounds/<filename>.",
			filePath:    filepath.Join(soundDir, name),
			source:      "file",
		})
	}

	sort.Slice(fileSounds, func(i, j int) bool {
		return strings.ToLower(fileSounds[i].key) < strings.ToLower(fileSounds[j].key)
	})
	return fileSounds, nil
}

func buildSynthSounds() map[string]soundDef {
	askHit := soundDef{
		key:         "ask-hit",
		title:       "Ask hit (existing)",
		description: "Clean bright high ping. Existing at_ask-style hit.",
		source:      "synth",
		notes: []note{
			{freqHz: 659.26, startS: 0.00, durS: 0.20, amp: 0.32, wave: "sine"},
		},
	}
	bidHit := soundDef{
		key:         "bid-hit",
		title:       "Bid hit (existing)",
		description: "Lower darker ping. Existing at_bid-style hit.",
		source:      "synth",
		notes: []note{
			{freqHz: 493.88, startS: 0.00, durS: 0.20, amp: 0.34, wave: "sine"},
		},
	}
	marketCrossedUp := soundDef{
		key:   "market-crossed-up",
		title: "Market crossed up (new)",
		description: "Starts with two jolting ask-hit accents, then 4 glassy bright rising tones " +
			"(crossing upward continuation).",
		source: "synth",
		notes: []note{
			{freqHz: 1318.52, startS: 0.00, durS: 0.08, amp: 0.40, wave: "square"},
			{freqHz: 1567.98, startS: 0.10, durS: 0.08, amp: 0.38, wave: "square"},
			{freqHz: 659.26, startS: 0.00, durS: 0.56, amp: 0.44, wave: "sine"},
			{freqHz: 830.60, startS: 0.36, durS: 0.48, amp: 0.26, wave: "glass"},
			{freqHz: 987.76, startS: 0.60, durS: 0.48, amp: 0.24, wave: "glass"},
			{freqHz: 1174.66, startS: 0.84, durS: 0.51, amp: 0.23, wave: "glass"},
			{freqHz: 1396.91, startS: 1.08, durS: 0.54, amp: 0.22, wave: "glass"},
		},
	}
	marketCrossedDown := soundDef{
		key:   "market-crossed-down",
		title: "Market crossed down (new)",
		description: "Starts with two jolting bid-hit accents, then 4 dark round dropping tones " +
			"(crossing downward continuation).",
		source: "synth",
		notes: []note{
			{freqHz: 246.94, startS: 0.00, durS: 0.08, amp: 0.40, wave: "square"},
			{freqHz: 220.00, startS: 0.10, durS: 0.08, amp: 0.38, wave: "square"},
			{freqHz: 493.88, startS: 0.00, durS: 0.56, amp: 0.46, wave: "sine"},
			{freqHz: 392.00, startS: 0.36, durS: 0.48, amp: 0.30, wave: "round"},
			{freqHz: 329.63, startS: 0.60, durS: 0.48, amp: 0.28, wave: "round"},
			{freqHz: 293.66, startS: 0.84, durS: 0.51, amp: 0.27, wave: "round"},
			{freqHz: 246.94, startS: 1.08, durS: 0.54, amp: 0.26, wave: "round"},
		},
	}
	rvolTickClose := soundDef{
		key:         "rvol-tick-close",
		title:       "RVOL tick (close)",
		description: "Short descending square tick used for RVOL close alerts.",
		source:      "synth",
		notes: []note{
			{freqHz: 1600.00, startS: 0.000, durS: 0.020, amp: 0.06, wave: "square"},
			{freqHz: 1200.00, startS: 0.008, durS: 0.016, amp: 0.05, wave: "square"},
		},
	}
	rvolTickPace := soundDef{
		key:         "rvol-tick-pace",
		title:       "RVOL tick (pace)",
		description: "Higher short descending square tick used for RVOL pace alerts.",
		source:      "synth",
		notes: []note{
			{freqHz: 2400.00, startS: 0.000, durS: 0.020, amp: 0.06, wave: "square"},
			{freqHz: 1800.00, startS: 0.008, durS: 0.016, amp: 0.05, wave: "square"},
		},
	}
	alertFallback := soundDef{
		key:         "alert-fallback-beep",
		title:       "Alert fallback beep",
		description: "Sine beep fallback when file-based alert audio is unavailable.",
		source:      "synth",
		notes: []note{
			{freqHz: 880.00, startS: 0.00, durS: 0.15, amp: 0.10, wave: "sine"},
		},
	}
	return map[string]soundDef{
		askHit.key:            askHit,
		bidHit.key:            bidHit,
		marketCrossedUp.key:   marketCrossedUp,
		marketCrossedDown.key: marketCrossedDown,
		rvolTickClose.key:     rvolTickClose,
		rvolTickPace.key:      rvolTickPace,
		alertFallback.key:     alertFallback,
	}
}

func buildCatalog(soundDir string) (map[string]soundDef, []string, error) {
	sounds := buildSynthSounds()
	order := []string{
		"ask-hit",
		"bid-hit",
		"market-crossed-up",
		"market-crossed-down",
		"rvol-tick-close",
		"rvol-tick-pace",
		"alert-fallback-beep",
	}

	fileSounds, err := discoverFileSounds(soundDir)
	if err != nil {
		return nil, nil, err
	}
	for _, s := range fileSounds {
		if _, exists := sounds[s.key]; exists {
			s.key = "file:" + s.key
		}
		sounds[s.key] = s
		order = append(order, s.key)
	}
	return sounds, order, nil
}

func main() {
	listOnly := flag.Bool("list", false, "List available sounds and exit")
	gapMs := flag.Int("gap-ms", 180, "Silence gap between sounds in milliseconds")
	noPlay := flag.Bool("no-play", false, "Print descriptions but skip audio playback")
	soundsDir := flag.String("sounds-dir", "web/sounds", "Directory to scan for .wav/.mp3 sound files")
	flag.Parse()

	if *gapMs < 0 {
		fmt.Fprintln(os.Stderr, "-gap-ms must be >= 0")
		os.Exit(2)
	}

	sounds, defaultOrder, err := buildCatalog(*soundsDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to build sound catalog: %v\n", err)
		os.Exit(2)
	}
	order := flag.Args()
	if len(order) == 0 {
		order = defaultOrder
	}

	if *listOnly {
		fmt.Println("Available sounds:")
		for _, k := range defaultOrder {
			s := sounds[k]
			fmt.Printf("- %s [%s]: %s\n", s.key, s.source, s.title)
			if s.filePath != "" {
				fmt.Printf("    path: %s\n", s.filePath)
			}
		}
		return
	}

	for _, key := range order {
		s, ok := sounds[key]
		if !ok {
			fmt.Fprintf(os.Stderr, "Unknown sound key: %s (run with -list)\n", key)
			os.Exit(2)
		}

		fmt.Printf("[PLAY] %s :: %s\n", s.key, s.title)
		fmt.Printf("       %s\n", s.description)
		if s.filePath != "" {
			fmt.Printf("       file: %s\n", s.filePath)
		}

		if !*noPlay {
			if s.filePath != "" {
				if err := playAudioFile(s.filePath); err != nil {
					fmt.Fprintf(os.Stderr, "Audio playback failed for %q: %v\n", s.key, err)
					os.Exit(4)
				}
			} else {
				samples, err := synthesize(s.notes)
				if err != nil {
					fmt.Fprintf(os.Stderr, "Failed to synthesize %q: %v\n", s.key, err)
					os.Exit(3)
				}
				if err := playSamples(samples); err != nil {
					fmt.Fprintf(os.Stderr, "Audio playback failed for %q: %v\n", s.key, err)
					os.Exit(4)
				}
			}
		}
		time.Sleep(time.Duration(*gapMs) * time.Millisecond)
	}

	fmt.Println("Done.")
}
