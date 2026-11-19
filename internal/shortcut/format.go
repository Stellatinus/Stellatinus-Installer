package shortcut

import (
	"bytes"
	"encoding/binary"
	"strings"
	"unicode/utf16"
)

// desktopEntry is a Desktop Entry file (freedesktop.org). The program is
// quoted as the specification asks for the Exec key, then the whole value is
// escaped as a string.
func desktopEntry(s Spec) []byte {
	var b strings.Builder
	b.WriteString("[Desktop Entry]\nType=Application\nVersion=1.0\n")
	b.WriteString("Name=" + entryString(s.Name) + "\n")
	if s.Description != "" {
		b.WriteString("Comment=" + entryString(s.Description) + "\n")
	}
	b.WriteString("Exec=" + entryString(execQuote(s.Target)) + "\n")
	b.WriteString("Path=" + entryString(s.Dir) + "\n")
	b.WriteString("Terminal=false\n")
	return []byte(b.String())
}

// execQuote quotes one argument for the Exec key: in double quotes, with the
// characters the specification names escaped, and a literal percent doubled.
func execQuote(arg string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range arg {
		switch r {
		case '"', '`', '$', '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		case '%':
			b.WriteString("%%")
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// entryString escapes a value of type string in a Desktop Entry file.
func entryString(v string) string {
	return strings.ReplaceAll(v, `\`, `\\`)
}

// Shell link constants (MS-SHLLINK).
const (
	lnkHasLinkInfo  = 0x00000002
	lnkHasName      = 0x00000004
	lnkHasWorkDir   = 0x00000010
	lnkIsUnicode    = 0x00000080
	lnkNoLinkTrack  = 0x00100000
	lnkLocalBase    = 0x00000001 // LinkInfoFlags: VolumeIDAndLocalBasePath
	lnkDriveFixed   = 3
	lnkShowNormal   = 1
	lnkFileArchive  = 0x20
	lnkMaxStringLen = 0xFFFF
)

// lnk builds a Windows shell link (.lnk) with the program's path, its working
// folder and a description, written by hand so no Windows call and no helper
// program is needed. It is checked here by reading the bytes back, not by
// Windows: whether the Start menu and Explorer accept and show it is tracked
// in the [Test] issue for shortcuts.
func lnk(s Spec) []byte {
	var out bytes.Buffer
	le := binary.LittleEndian
	put32 := func(b *bytes.Buffer, v uint32) { _ = binary.Write(b, le, v) }

	// Header, 76 bytes.
	put32(&out, 0x4C)
	out.Write([]byte{0x01, 0x14, 0x02, 0x00, 0x00, 0x00, 0x00, 0x00, 0xC0, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x46})
	flags := uint32(lnkHasLinkInfo | lnkIsUnicode | lnkNoLinkTrack | lnkHasWorkDir)
	if s.Description != "" {
		flags |= lnkHasName
	}
	put32(&out, flags)
	put32(&out, lnkFileArchive)
	out.Write(make([]byte, 24)) // creation, access and write times
	put32(&out, 0)              // file size
	put32(&out, 0)              // icon index
	put32(&out, lnkShowNormal)
	out.Write(make([]byte, 12)) // hot key and the reserved fields

	// LinkInfo: the local path of the target, in ANSI and in Unicode.
	ansi := toANSI(s.Target)
	uni := utf16Z(s.Target)
	const headerSize = 0x24
	volume := make([]byte, 0, 17)
	vb := bytes.NewBuffer(volume)
	put32(vb, 0x11)          // VolumeIDSize
	put32(vb, lnkDriveFixed) // DriveType
	put32(vb, 0)             // DriveSerialNumber
	put32(vb, 0x10)          // VolumeLabelOffset
	vb.WriteByte(0)          // empty label
	volumeOff := uint32(headerSize)
	baseOff := volumeOff + uint32(vb.Len())
	suffixOff := baseOff + uint32(len(ansi)) + 1
	baseUniOff := suffixOff + 1
	suffixUniOff := baseUniOff + uint32(len(uni))
	size := suffixUniOff + 2

	var li bytes.Buffer
	put32(&li, size)
	put32(&li, headerSize)
	put32(&li, lnkLocalBase)
	put32(&li, volumeOff)
	put32(&li, baseOff)
	put32(&li, 0) // CommonNetworkRelativeLinkOffset
	put32(&li, suffixOff)
	put32(&li, baseUniOff)
	put32(&li, suffixUniOff)
	li.Write(vb.Bytes())
	li.Write(ansi)
	li.WriteByte(0)
	li.WriteByte(0) // empty CommonPathSuffix
	li.Write(uni)
	li.Write([]byte{0, 0}) // empty Unicode CommonPathSuffix
	out.Write(li.Bytes())

	// String data: the description, then the working folder.
	sd := func(v string) {
		u := utf16.Encode([]rune(v))
		if len(u) > lnkMaxStringLen {
			u = u[:lnkMaxStringLen]
		}
		_ = binary.Write(&out, le, uint16(len(u)))
		_ = binary.Write(&out, le, u)
	}
	if s.Description != "" {
		sd(s.Description)
	}
	sd(s.Dir)

	put32(&out, 0) // terminal block
	return out.Bytes()
}

// utf16Z is the text as null-terminated UTF-16, little endian.
func utf16Z(v string) []byte {
	u := append(utf16.Encode([]rune(v)), 0)
	b := make([]byte, 2*len(u))
	for i, c := range u {
		binary.LittleEndian.PutUint16(b[2*i:], c)
	}
	return b
}

// toANSI keeps ASCII and puts '?' for anything else; Windows reads the Unicode
// copy when there is one.
func toANSI(v string) []byte {
	b := make([]byte, 0, len(v))
	for _, r := range v {
		if r < 0x80 {
			b = append(b, byte(r))
		} else {
			b = append(b, '?')
		}
	}
	return b
}
