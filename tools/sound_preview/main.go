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

func playWAV(path string) error {
	switch {
	case commandExists("aplay"):
		return exec.Command("aplay", "-q", path).Run()
	case commandExists("paplay"):
		return exec.Command("paplay", path).Run()
	case commandExists("ffplay"):
		return exec.Command("ffplay", "-nodisp", "-autoexit", "-loglevel", "quiet", path).Run()
	case commandExists("afplay"):
		return exec.Command("afplay", path).Run()
	case runtime.GOOS == "windows":
		quoted := strings.ReplaceAll(path, "'", "''")
		ps := fmt.Sprintf("(New-Object Media.SoundPlayer '%s').PlaySync();", quoted)
		return exec.Command("powershell", "-NoProfile", "-Command", ps).Run()
	default:
		return errors.New("no supported audio player found (install one of: aplay, paplay, ffplay, afplay)")
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

func buildSounds() map[string]soundDef {
	askHit := soundDef{
		key:         "ask-hit",
		title:       "Ask hit (existing)",
		description: "Clean bright high ping. Existing at_ask-style hit.",
		notes: []note{
			{freqHz: 659.26, startS: 0.00, durS: 0.20, amp: 0.32, wave: "sine"},
		},
	}
	bidHit := soundDef{
		key:         "bid-hit",
		title:       "Bid hit (existing)",
		description: "Lower darker ping. Existing at_bid-style hit.",
		notes: []note{
			{freqHz: 493.88, startS: 0.00, durS: 0.20, amp: 0.34, wave: "sine"},
		},
	}
	marketCrossedUp := soundDef{
		key:   "market-crossed-up",
		title: "Market crossed up (new)",
		description: "Starts with two jolting ask-hit accents, then 4 glassy bright rising tones " +
			"(crossing upward continuation).",
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
	return map[string]soundDef{
		askHit.key:            askHit,
		bidHit.key:            bidHit,
		marketCrossedUp.key:   marketCrossedUp,
		marketCrossedDown.key: marketCrossedDown,
	}
}

func main() {
	listOnly := flag.Bool("list", false, "List available sounds and exit")
	gapMs := flag.Int("gap-ms", 180, "Silence gap between sounds in milliseconds")
	noPlay := flag.Bool("no-play", false, "Print descriptions but skip audio playback")
	flag.Parse()

	sounds := buildSounds()
	defaultOrder := []string{
		"ask-hit",
		"bid-hit",
		"market-crossed-up",
		"market-crossed-down",
	}
	order := flag.Args()
	if len(order) == 0 {
		order = defaultOrder
	}

	if *listOnly {
		fmt.Println("Available sounds:")
		for _, k := range defaultOrder {
			s := sounds[k]
			fmt.Printf("- %s: %s\n", s.key, s.title)
		}
		return
	}

	for _, key := range order {
		s, ok := sounds[key]
		if !ok {
			fmt.Fprintf(os.Stderr, "Unknown sound key: %s\n", key)
			os.Exit(2)
		}

		fmt.Printf("[PLAY] %s :: %s\n", s.key, s.title)
		fmt.Printf("       %s\n", s.description)

		samples, err := synthesize(s.notes)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to synthesize %q: %v\n", s.key, err)
			os.Exit(3)
		}
		if !*noPlay {
			if err := playSamples(samples); err != nil {
				fmt.Fprintf(os.Stderr, "Audio playback failed for %q: %v\n", s.key, err)
				os.Exit(4)
			}
		}
		time.Sleep(time.Duration(*gapMs) * time.Millisecond)
	}

	fmt.Println("Done.")
}
