package validate

import "testing"

// TestTextKeepsComplexEmoji guards the list/category icon path: the emoji
// picker (static/js/emoji-picker.js) and the free-text icon field can send
// any emoji sequence, and Text's control-character stripping must leave
// every one of them intact — ZWJ (U+200D), VS16 (U+FE0F), skin-tone
// modifiers, keycaps and tag sequences are all outside the ranges it
// removes — while MaxIconLen stays large enough for the longest of them.
func TestTextKeepsComplexEmoji(t *testing.T) {
	cases := []struct {
		name  string
		emoji string
	}{
		{"skin tone modifier", "\U0001F44D\U0001F3FD"},
		{"ZWJ family", "\U0001F468\u200d\U0001F469\u200d\U0001F467\u200d\U0001F466"},
		{"ZWJ with two skin tones", "\U0001F469\U0001F3FB\u200d❤\ufe0f\u200d\U0001F48B\u200d\U0001F468\U0001F3FC"},
		{"toned ZWJ profession", "\U0001F9D1\U0001F3FE\u200d\U0001F4BB"},
		{"VS16 + ZWJ flag", "\U0001F3F3\ufe0f\u200d\U0001F308"},
		{"tag sequence flag", "\U0001F3F4\U000E0067\U000E0062\U000E0065\U000E006E\U000E0067\U000E007F"},
		{"keycap", "1\ufe0f\u20e3"},
		{"regional indicator flag", "\U0001F1EB\U0001F1F7"},
		{"Emoji 15.1 ZWJ", "\U0001F642\u200d↔\ufe0f"},
		{"Emoji 17", "\U0001FAEA"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Text(tc.emoji); got != tc.emoji {
				t.Fatalf("Text altered %q (%+q) into %q (%+q)", tc.emoji, tc.emoji, got, got)
			}
			if !MaxLen(tc.emoji, MaxIconLen) {
				t.Fatalf("%q is rejected by MaxIconLen (%d)", tc.emoji, MaxIconLen)
			}
		})
	}
}
