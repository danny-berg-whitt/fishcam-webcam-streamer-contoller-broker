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

// Device discovery from /proc/asound and /dev. Parsing is kept apart from
// the filesystem so it can be tested without hardware.

// SoundCard is one entry from /proc/asound/cards.
type SoundCard struct {
	Index  int
	ID     string // what ffmpeg's CARD= and amixer -c both take
	Driver string // e.g. "USB-Audio"
	Name   string
}

func (c SoundCard) String() string {
	return fmt.Sprintf("%d:%s (%s, %s)", c.Index, c.ID, c.Driver, c.Name)
}

// /proc/asound/cards has two lines per card; only the first matters:
//
//	1 [<id>           ]: <driver> - <name>
//	                     <long name>
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

// SelectCaptureCard picks the card to record from: cards with a capture PCM
// (hasCapture), narrowed by hint if set (id or name, case-insensitive), else
// preferring USB. Ambiguity is an error: the wrong microphone is worse than
// refusing to start.
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

// procCaptureCheck looks for a capture PCM (pcm<N>c) in /proc/asound/cardN.
func procCaptureCheck(procAsound string) func(int) bool {
	return func(index int) bool {
		matches, err := filepath.Glob(filepath.Join(procAsound, fmt.Sprintf("card%d", index), "pcm*c"))
		return err == nil && len(matches) > 0
	}
}

var videoNodeRE = regexp.MustCompile(`^video(\d+)$`)

// SelectVideoNode returns the lowest-numbered /dev/video node: a UVC camera
// also exposes metadata nodes, numbered after the capture node.
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
