package main

import (
	"errors"
	"fmt"
	"log"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
)

// amixerOutput is a variable so tests can replace it.
var amixerOutput = func(args ...string) (string, error) {
	out, err := exec.Command("amixer", args...).CombinedOutput()
	return string(out), err
}

// MixerControl is one entry from `amixer -c <card> scontrols`.
type MixerControl struct {
	Name  string
	Index int
}

func (c MixerControl) String() string { return fmt.Sprintf("%q,%d", c.Name, c.Index) }

var scontrolRE = regexp.MustCompile(`^Simple mixer control '(.+)',(\d+)\s*$`)

// ParseMixerControls reads `amixer scontrols` output:
//
//	Simple mixer control 'Mic',0
func ParseMixerControls(out string) []MixerControl {
	var controls []MixerControl
	for _, line := range strings.Split(out, "\n") {
		m := scontrolRE.FindStringSubmatch(strings.TrimRight(line, "\r"))
		if m == nil {
			continue
		}
		idx, err := strconv.Atoi(m[2])
		if err != nil {
			continue
		}
		controls = append(controls, MixerControl{Name: m[1], Index: idx})
	}
	return controls
}

// HasCaptureSwitch reports whether `amixer sget <control>` output shows a
// capture switch ("cswitch"; playback-only controls have "pswitch").
func HasCaptureSwitch(sget string) bool {
	for _, line := range strings.Split(sget, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "Capabilities:") {
			continue
		}
		for _, cap := range strings.Fields(strings.TrimPrefix(line, "Capabilities:")) {
			if cap == "cswitch" || strings.HasPrefix(cap, "cswitch-") {
				return true
			}
		}
	}
	return false
}

// DetectMuteControl finds the card's one control with a capture switch.
// Names vary by device ("Mic", "Capture", "Mic Capture"); more than one
// candidate is an error.
func DetectMuteControl(card string) (string, error) {
	out, err := amixerOutput("-c", card, "scontrols")
	if err != nil {
		return "", fmt.Errorf("list mixer controls: %w (%s)", err, strings.TrimSpace(out))
	}

	controls := ParseMixerControls(out)
	if len(controls) == 0 {
		return "", fmt.Errorf("card %s exposes no mixer controls", card)
	}

	var capable []MixerControl
	for _, c := range controls {
		detail, err := amixerOutput("-c", card, "sget", c.Name)
		if err != nil {
			continue
		}
		if HasCaptureSwitch(detail) {
			capable = append(capable, c)
		}
	}

	switch len(capable) {
	case 1:
		return capable[0].Name, nil
	case 0:
		return "", fmt.Errorf("no control on card %s has a capture switch (found %s)",
			card, describeControls(controls))
	default:
		return "", fmt.Errorf("several controls on card %s have a capture switch (%s); set MUTE_CONTROL",
			card, describeControls(capable))
	}
}

func describeControls(controls []MixerControl) string {
	names := make([]string, 0, len(controls))
	for _, c := range controls {
		names = append(names, c.String())
	}
	return strings.Join(names, "; ")
}

// Muter switches capture at the ALSA level, so the stream carries silence
// without ffmpeg restarting.
type Muter struct {
	card    string
	control string

	mu    sync.Mutex
	muted bool
}

func NewMuter(card, control string) *Muter {
	return &Muter{card: card, control: control}
}

var errNoMuteControl = errors.New("no ALSA capture control configured; set MUTE_CONTROL")

// Set switches capture on (false) or off (true) and remembers the state.
func (m *Muter) Set(mute bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if isAuto(m.control) {
		return errNoMuteControl
	}

	arg := "cap"
	if mute {
		arg = "nocap"
	}
	out, err := amixerOutput("-q", "-c", m.card, "set", m.control, arg)
	if err != nil {
		return fmt.Errorf("amixer -c %s set %s %s: %w (%s)",
			m.card, m.control, arg, err, strings.TrimSpace(out))
	}
	m.muted = mute
	log.Printf("capture %s (card=%s control=%s)",
		map[bool]string{true: "muted", false: "unmuted"}[mute], m.card, m.control)
	return nil
}

func (m *Muter) Muted() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.muted
}
