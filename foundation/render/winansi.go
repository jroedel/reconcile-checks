package render

// The PDF names its font's encoding as WinAnsiEncoding, so each character is
// one byte of Windows-1252: ASCII, the Latin-1 accented letters of Western
// European names (ñ, á, é, ü), and in 0x80–0x9F a few typographic marks
// (curly quotes, dashes, €). That is what a check needs, and the simplest kind
// of font a printer's own PDF interpreter can be given
// (docs/02-architecture.md, "Characters").

// winAnsiHigh is 0x80–0x9F, where Windows-1252 differs from Latin-1. A zero is
// a code with no character.
var winAnsiHigh = [32]rune{
	'€', 0, '‚', 'ƒ', '„', '…', '†', '‡', 'ˆ', '‰', 'Š', '‹', 'Œ', 0, 'Ž', 0,
	0, '‘', '’', '“', '”', '•', '–', '—', '˜', '™', 'š', '›', 'œ', 0, 'ž', 'Ÿ',
}

// winAnsiByte is the Windows-1252 byte for r.
func winAnsiByte(r rune) (byte, bool) {
	switch {
	case r >= 0x20 && r <= 0x7e:
		return byte(r), true
	case r >= 0xa0 && r <= 0xff:
		return byte(r), true
	}

	for i, h := range winAnsiHigh {
		if h != 0 && h == r {
			return byte(0x80 + i), true
		}
	}

	return 0, false
}

// winAnsiRunes is every character Windows-1252 can carry, in byte order from
// the space.
func winAnsiRunes() []rune {
	var rs []rune
	for b := 0x20; b <= 0xff; b++ {
		if r := runeAt(byte(b)); r != 0 {
			rs = append(rs, r)
		}
	}

	return rs
}

// runeAt is the character at Windows-1252 byte b, or 0 for none.
func runeAt(b byte) rune {
	switch {
	case b >= 0x20 && b <= 0x7e, b >= 0xa0:
		return rune(b)
	case b >= 0x80 && b <= 0x9f:
		return winAnsiHigh[b-0x80]
	default:
		return 0
	}
}
