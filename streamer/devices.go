package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Device discovery, so one image and one ConfigMap can serve hosts with
// different webcams. ALSA card ids are derived by snd-usb-audio from each
// device's USB product string — a C922 Pro Stream registers as "C922", a
// C270 as "Webcam" — so hardcoding one breaks the other.
//
// Everything here reads plain text from /proc and /dev. No cgo, no external
// dependencies, and the parsing is separated from the filesystem so it can
// be tested without hardware.

// SoundCard is one entry from /proc/asound/cards.
type SoundCard struct {
	Index  int
	ID     string // ALSA id: what ffmpeg's CARD= and amixer -c both want
	Driver string // e.g. "USB-Audio"
	Name   string
}

func (c SoundCard) String() string {
	return fmt.Sprintf("%d:%s (%s, %s)", c.Index, c.ID, c.Driver, c.Name)
}

// /proc/asound/cards holds two lines per card:
//
//	1 [C922           ]: USB-Audio - C922 Pro Stream Webcam
//	                     Generic C922 Pro Stream Webcam at usb-xhci-hcd.1-1.3
//
// Only the first carries the fields we need; continuation lines are skipped.
var cardLineRE = regexp.MustCompile(`^\s*(\d+)\s+\[([^\]]+)\]\s*:\s*(\S+)\s*-\s*(.*)$`)

// ParseSoundCards reads the contents of /proc/asound/cards.
func ParseSoundCards(r io.Reader) ([]SoundCard, error) {
	var cards []SoundCard
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		m := cardLineRE.FindStringSubmatch(sc.Text())
		if m == nil {
			continue
		}
		idx, err := strconv.Atoi(m[1])
		if err != nil {
			continue
		}
		cards = append(cards, SoundCard{
			Index:  idx,
			ID:     strings.TrimSpace(m[2]),
			Driver: strings.TrimSpace(m[3]),
			Name:   strings.TrimSpace(m[4]),
		})
	}
	return cards, sc.Err()
}

// SelectCaptureCard picks the card to record from.
//
// hasCapture reports whether a card index offers a capture PCM, which filters
// out playback-only devices such as the Pi's HDMI and headphone outputs.
// When hint is set, only cards whose id or name contain it (case-insensitively)
// are considered; otherwise USB cards are preferred, a webcam mic always being
// one. An ambiguous result is an error rather than a guess — picking the wrong
// microphone is worse than refusing to start.
func SelectCaptureCard(cards []SoundCard, hint string, hasCapture func(int) bool) (SoundCard, error) {
	var candidates []SoundCard
	for _, c := range cards {
		if hasCapture != nil && !hasCapture(c.Index) {
			continue
		}
		candidates = append(candidates, c)
	}
	if len(candidates) == 0 {
		return SoundCard{}, errors.New("no sound card offers a capture device")
	}

	if hint != "" {
		var matched []SoundCard
		for _, c := range candidates {
			if containsFold(c.ID, hint) || containsFold(c.Name, hint) {
				matched = append(matched, c)
			}
		}
		if len(matched) == 0 {
			return SoundCard{}, fmt.Errorf("no capture card matches %q; found %s", hint, describeCards(candidates))
		}
		candidates = matched
	} else if usb := filterUSB(candidates); len(usb) > 0 {
		candidates = usb
	}

	if len(candidates) > 1 {
		return SoundCard{}, fmt.Errorf(
			"several capture cards found (%s); set ALSA_CARD to one explicitly, or ALSA_CARD_MATCH to narrow it",
			describeCards(candidates))
	}
	return candidates[0], nil
}

func filterUSB(cards []SoundCard) []SoundCard {
	var out []SoundCard
	for _, c := range cards {
		if strings.EqualFold(c.Driver, "USB-Audio") {
			out = append(out, c)
		}
	}
	return out
}

func containsFold(haystack, needle string) bool {
	return strings.Contains(strings.ToLower(haystack), strings.ToLower(needle))
}

func describeCards(cards []SoundCard) string {
	names := make([]string, 0, len(cards))
	for _, c := range cards {
		names = append(names, c.String())
	}
	return strings.Join(names, "; ")
}

// DetectCaptureCard resolves the capture card from a live /proc/asound.
func DetectCaptureCard(procAsound, hint string) (SoundCard, error) {
	f, err := os.Open(filepath.Join(procAsound, "cards"))
	if err != nil {
		return SoundCard{}, fmt.Errorf("read sound cards: %w", err)
	}
	defer f.Close()

	cards, err := ParseSoundCards(f)
	if err != nil {
		return SoundCard{}, fmt.Errorf("parse sound cards: %w", err)
	}
	return SelectCaptureCard(cards, hint, procCaptureCheck(procAsound))
}

// procCaptureCheck reports whether /proc/asound/cardN contains a capture PCM.
// ALSA names those pcm<N>c; playback devices are pcm<N>p.
func procCaptureCheck(procAsound string) func(int) bool {
	return func(index int) bool {
		matches, err := filepath.Glob(filepath.Join(procAsound, fmt.Sprintf("card%d", index), "pcm*c"))
		return err == nil && len(matches) > 0
	}
}

var videoNodeRE = regexp.MustCompile(`^video(\d+)$`)

// SelectVideoNode returns the lowest-numbered V4L2 node from a list of
// /dev entry names. A UVC camera may expose several nodes (capture first,
// then metadata), and the capture node is conventionally the lowest.
func SelectVideoNode(entries []string) (string, error) {
	var nums []int
	for _, e := range entries {
		if m := videoNodeRE.FindStringSubmatch(e); m != nil {
			n, err := strconv.Atoi(m[1])
			if err == nil {
				nums = append(nums, n)
			}
		}
	}
	if len(nums) == 0 {
		return "", errors.New("no /dev/video* node found")
	}
	sort.Ints(nums)
	return fmt.Sprintf("/dev/video%d", nums[0]), nil
}

// DetectVideoDevice resolves the video node from a live /dev.
func DetectVideoDevice(devDir string) (string, error) {
	f, err := os.Open(devDir)
	if err != nil {
		return "", fmt.Errorf("open %s: %w", devDir, err)
	}
	defer f.Close()

	names, err := f.Readdirnames(-1)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", devDir, err)
	}
	return SelectVideoNode(names)
}
