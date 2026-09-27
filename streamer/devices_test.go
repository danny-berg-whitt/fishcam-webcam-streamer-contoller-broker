package main

import (
	"strings"
	"testing"
)

// Real /proc/asound/cards content from a Pi with a C922 attached: the
// built-in headphone and HDMI outputs, plus the webcam.
const pi5WithC922 = ` 0 [Headphones     ]: bcm2835_headpho - bcm2835 Headphones
                      bcm2835 Headphones
 1 [vc4hdmi0       ]: vc4-hdmi - vc4-hdmi-0
                      vc4-hdmi-0
 2 [C922           ]: USB-Audio - C922 Pro Stream Webcam
                      Generic C922 Pro Stream Webcam at usb-xhci-hcd.1-1.3, high speed
`

// The same machine with a C270 instead — note the id is "Webcam", derived
// from that model's USB product string.
const pi4WithC270 = ` 0 [Headphones     ]: bcm2835_headpho - bcm2835 Headphones
                      bcm2835 Headphones
 1 [Webcam         ]: USB-Audio - Webcam C270
                      Webcam C270 at usb-0000:01:00.0-1.2, high speed
`

const twoWebcams = ` 0 [Headphones     ]: bcm2835_headpho - bcm2835 Headphones
                      bcm2835 Headphones
 1 [C922           ]: USB-Audio - C922 Pro Stream Webcam
                      Generic C922 Pro Stream Webcam at usb-xhci-hcd.1-1.3
 2 [Webcam         ]: USB-Audio - Webcam C270
                      Webcam C270 at usb-xhci-hcd.1-1.4
`

func TestParseSoundCards(t *testing.T) {
	cards, err := ParseSoundCards(strings.NewReader(pi5WithC922))
	if err != nil {
		t.Fatalf("ParseSoundCards: %v", err)
	}
	if len(cards) != 3 {
		t.Fatalf("parsed %d cards, want 3: %+v", len(cards), cards)
	}

	got := cards[2]
	want := SoundCard{Index: 2, ID: "C922", Driver: "USB-Audio", Name: "C922 Pro Stream Webcam"}
	if got != want {
		t.Errorf("card[2] = %+v, want %+v", got, want)
	}
}

func TestParseSoundCardsEmpty(t *testing.T) {
	cards, err := ParseSoundCards(strings.NewReader("--- no soundcards ---\n"))
	if err != nil {
		t.Fatalf("ParseSoundCards: %v", err)
	}
	if len(cards) != 0 {
		t.Errorf("parsed %d cards from an empty list, want 0", len(cards))
	}
}

// captureOn returns a hasCapture func treating the listed indexes as
// capture-capable — on a real Pi the HDMI and headphone cards are not.
func captureOn(indexes ...int) func(int) bool {
	set := make(map[int]bool, len(indexes))
	for _, i := range indexes {
		set[i] = true
	}
	return func(i int) bool { return set[i] }
}

// The point of the whole exercise: the same code, no config change, picks
// the right card on two machines with different webcams.
func TestSelectCaptureCardAcrossModels(t *testing.T) {
	cases := []struct {
		name    string
		procs   string
		capture []int
		wantID  string
	}{
		{"C922 on the Pi 5", pi5WithC922, []int{2}, "C922"},
		{"C270 on the Pi 4b", pi4WithC270, []int{1}, "Webcam"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cards, err := ParseSoundCards(strings.NewReader(tc.procs))
			if err != nil {
				t.Fatalf("ParseSoundCards: %v", err)
			}
			card, err := SelectCaptureCard(cards, "", captureOn(tc.capture...))
			if err != nil {
				t.Fatalf("SelectCaptureCard: %v", err)
			}
			if card.ID != tc.wantID {
				t.Errorf("selected %q, want %q", card.ID, tc.wantID)
			}
		})
	}
}

// A playback-only card must never be chosen, even when it sorts first.
func TestSelectCaptureCardIgnoresPlaybackOnly(t *testing.T) {
	cards, _ := ParseSoundCards(strings.NewReader(pi5WithC922))
	card, err := SelectCaptureCard(cards, "", captureOn(2))
	if err != nil {
		t.Fatalf("SelectCaptureCard: %v", err)
	}
	if card.ID == "Headphones" || card.ID == "vc4hdmi0" {
		t.Errorf("selected the output-only card %q", card.ID)
	}
}

// Two microphones is genuinely ambiguous — refuse rather than guess wrong.
func TestSelectCaptureCardAmbiguous(t *testing.T) {
	cards, _ := ParseSoundCards(strings.NewReader(twoWebcams))
	_, err := SelectCaptureCard(cards, "", captureOn(1, 2))
	if err == nil {
		t.Fatal("two capture cards selected without error, want ambiguity error")
	}
	for _, want := range []string{"C922", "Webcam", "ALSA_CARD"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

func TestSelectCaptureCardHintResolvesAmbiguity(t *testing.T) {
	cards, _ := ParseSoundCards(strings.NewReader(twoWebcams))

	card, err := SelectCaptureCard(cards, "c270", captureOn(1, 2))
	if err != nil {
		t.Fatalf("SelectCaptureCard with hint: %v", err)
	}
	if card.ID != "Webcam" {
		t.Errorf("hint %q selected %q, want Webcam", "c270", card.ID)
	}
}

func TestSelectCaptureCardHintNoMatch(t *testing.T) {
	cards, _ := ParseSoundCards(strings.NewReader(pi5WithC922))
	if _, err := SelectCaptureCard(cards, "brio", captureOn(2)); err == nil {
		t.Fatal("unmatched hint returned a card, want error")
	}
}

func TestSelectCaptureCardNoneAvailable(t *testing.T) {
	cards, _ := ParseSoundCards(strings.NewReader(pi5WithC922))
	if _, err := SelectCaptureCard(cards, "", captureOn()); err == nil {
		t.Fatal("no capture-capable card returned success, want error")
	}
}

func TestSelectVideoNode(t *testing.T) {
	cases := []struct {
		name    string
		entries []string
		want    string
		wantErr bool
	}{
		{"single node", []string{"video0", "snd", "null"}, "/dev/video0", false},
		// A UVC camera exposes capture then metadata; capture sorts lowest.
		{"capture and metadata", []string{"video1", "video0"}, "/dev/video0", false},
		// Numeric, not lexical: video2 must beat video10.
		{"double digits", []string{"video10", "video2"}, "/dev/video2", false},
		{"none present", []string{"snd", "null"}, "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := SelectVideoNode(tc.entries)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("got %q, want error", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("SelectVideoNode: %v", err)
			}
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}
