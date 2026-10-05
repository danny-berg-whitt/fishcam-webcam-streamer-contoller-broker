package main

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

// A webcam whose one control is named "Mic", not "Capture".
const c922Scontrols = `Simple mixer control 'Mic',0
`

const c922MicSget = `Simple mixer control 'Mic',0
  Capabilities: cvolume cvolume-joined cswitch cswitch-joined
  Capture channels: Mono
  Limits: Capture 0 - 3072
  Mono: Capture 2462 [80%] [10.03dB] [on]
`

// A card whose only control is playback-only — nothing to mute.
const playbackOnlyScontrols = `Simple mixer control 'Headphone',0
`

const headphoneSget = `Simple mixer control 'Headphone',0
  Capabilities: pvolume pswitch
  Playback channels: Front Left - Front Right
`

const twoCaptureScontrols = `Simple mixer control 'Mic',0
Simple mixer control 'Line',0
`

// fakeAmixer swaps in canned output keyed by the sget control name.
func fakeAmixer(t *testing.T, scontrols string, sget map[string]string) {
	t.Helper()
	orig := amixerOutput
	t.Cleanup(func() { amixerOutput = orig })

	amixerOutput = func(args ...string) (string, error) {
		for i, a := range args {
			if a == "scontrols" {
				return scontrols, nil
			}
			if a == "sget" && i+1 < len(args) {
				out, ok := sget[args[i+1]]
				if !ok {
					return "", fmt.Errorf("amixer: unable to find simple control %q", args[i+1])
				}
				return out, nil
			}
		}
		return "", nil
	}
}

func TestParseMixerControls(t *testing.T) {
	got := ParseMixerControls(twoCaptureScontrols)
	if len(got) != 2 {
		t.Fatalf("parsed %d controls, want 2: %+v", len(got), got)
	}
	if got[0] != (MixerControl{Name: "Mic", Index: 0}) {
		t.Errorf("controls[0] = %+v, want Mic,0", got[0])
	}
}

func TestParseMixerControlsHandlesSpacesInNames(t *testing.T) {
	got := ParseMixerControls("Simple mixer control 'Auto Gain Control',0\n")
	if len(got) != 1 || got[0].Name != "Auto Gain Control" {
		t.Errorf("got %+v, want a single control named 'Auto Gain Control'", got)
	}
}

func TestHasCaptureSwitch(t *testing.T) {
	if !HasCaptureSwitch(c922MicSget) {
		t.Error("C922 Mic reported as having no capture switch")
	}
	if HasCaptureSwitch(headphoneSget) {
		t.Error("a playback-only control reported as mutable")
	}
}

// The control is discovered, not assumed to be "Capture".
func TestDetectMuteControlFindsMic(t *testing.T) {
	fakeAmixer(t, c922Scontrols, map[string]string{"Mic": c922MicSget})

	got, err := DetectMuteControl("Webcam")
	if err != nil {
		t.Fatalf("DetectMuteControl: %v", err)
	}
	if got != "Mic" {
		t.Errorf("detected %q, want Mic", got)
	}
}

func TestDetectMuteControlNoCaptureSwitch(t *testing.T) {
	fakeAmixer(t, playbackOnlyScontrols, map[string]string{"Headphone": headphoneSget})

	if _, err := DetectMuteControl("Headphones"); err == nil {
		t.Fatal("playback-only card returned a mute control, want error")
	}
}

func TestDetectMuteControlAmbiguous(t *testing.T) {
	fakeAmixer(t, twoCaptureScontrols, map[string]string{
		"Mic":  c922MicSget,
		"Line": strings.Replace(c922MicSget, "'Mic'", "'Line'", 1),
	})

	_, err := DetectMuteControl("Webcam")
	if err == nil {
		t.Fatal("two capture controls returned a choice, want ambiguity error")
	}
	if !strings.Contains(err.Error(), "MUTE_CONTROL") {
		t.Errorf("error %q does not suggest MUTE_CONTROL", err)
	}
}

func TestMuterSetUsesDetectedControl(t *testing.T) {
	var got []string
	orig := amixerOutput
	t.Cleanup(func() { amixerOutput = orig })
	amixerOutput = func(args ...string) (string, error) {
		got = args
		return "", nil
	}

	m := NewMuter("Webcam", "Mic")
	if err := m.Set(true); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if !m.Muted() {
		t.Error("Muted() = false after Set(true)")
	}

	joined := strings.Join(got, " ")
	for _, want := range []string{"-c Webcam", "set Mic nocap"} {
		if !strings.Contains(joined, want) {
			t.Errorf("amixer args %q missing %q", joined, want)
		}
	}
}

// With no control detected, mute must fail rather than appear to work.
func TestMuterSetWithoutControl(t *testing.T) {
	m := NewMuter("Webcam", autoValue)
	if err := m.Set(true); !errors.Is(err, errNoMuteControl) {
		t.Fatalf("Set with no control returned %v, want errNoMuteControl", err)
	}
	if m.Muted() {
		t.Error("Muted() = true after a failed Set")
	}
}
