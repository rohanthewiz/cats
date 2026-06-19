package wire

import "fmt"

// ansi16 maps herdr's named-color indices (wire.rs color_to_u32: 0=Reset,
// 1=Black .. 16=White) to CSS. Index 0 is empty, meaning "terminal default".
var ansi16 = [17]string{
	"", // Reset / default
	"#000000", "#aa0000", "#00aa00", "#aa5500",
	"#0000aa", "#aa00aa", "#00aaaa", "#aaaaaa",
	"#555555", "#ff5555", "#55ff55", "#ffff55",
	"#5555ff", "#ff55ff", "#55ffff", "#ffffff",
}

// ColorToCSS converts a packed wire color (wire.rs color_to_u32 encoding) to a
// CSS color string. An empty string means "terminal default" so the browser can
// apply its own theme foreground/background.
//
// Encoding (high byte = tag):
//
//	0x00 → named color, low byte 0..16
//	0x01 → 256-color palette index in low byte
//	0x02 → RGB in the low 3 bytes
func ColorToCSS(v uint32) string {
	switch v >> 24 {
	case 0x00:
		if idx := v & 0xff; idx <= 16 {
			return ansi16[idx]
		}
		return ""
	case 0x01:
		return indexedToCSS(uint8(v & 0xff))
	case 0x02:
		return fmt.Sprintf("#%02x%02x%02x", (v>>16)&0xff, (v>>8)&0xff, v&0xff)
	default:
		return ""
	}
}

// indexedToCSS resolves an xterm 256-color palette index to CSS.
func indexedToCSS(i uint8) string {
	switch {
	case i < 16:
		return ansi16[int(i)+1]
	case i >= 232:
		c := 8 + int(i-232)*10
		return fmt.Sprintf("#%02x%02x%02x", c, c, c)
	default:
		n := int(i) - 16
		conv := func(v int) int {
			if v == 0 {
				return 0
			}
			return 55 + v*40
		}
		return fmt.Sprintf("#%02x%02x%02x", conv((n/36)%6), conv((n/6)%6), conv(n%6))
	}
}
