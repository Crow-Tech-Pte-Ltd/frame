// Package media validates uploaded media by magic bytes, signs media URLs and
// manages media blobs (metadata in SQLite, bytes in storage). Images are never
// decoded server-side; the only parsing is reading JPEG frame headers for
// dimensions.
package media

import (
	"bytes"
	"encoding/binary"
)

// Content types served by the server.
const (
	JPEG = "image/jpeg"
	HEIC = "image/heic"
	MP4  = "video/mp4"
)

// SniffLen is how many leading bytes Sniff needs.
const SniffLen = 32

var heifImageBrands = map[string]bool{"heic": true, "heix": true, "heim": true, "heis": true, "mif1": true}

// Brands that are ISO-BMFF but never acceptable as video.
var nonVideoBrands = map[string]bool{
	"heic": true, "heix": true, "heim": true, "heis": true, "mif1": true, "msf1": true,
	"hevc": true, "hevx": true, "avif": true, "avis": true, "M4A ": true, "M4B ": true, "crx ": true, "jp2 ": true,
}

// Sniff identifies JPEG, HEIC and MP4 by magic bytes. It returns "" for
// anything else.
func Sniff(head []byte) string {
	if len(head) >= 3 && head[0] == 0xFF && head[1] == 0xD8 && head[2] == 0xFF {
		return JPEG
	}
	if brand, ok := ftypBrand(head); ok {
		if heifImageBrands[brand] {
			return HEIC
		}
		if !nonVideoBrands[brand] && printableBrand(brand) {
			return MP4
		}
	}
	return ""
}

// ftypBrand returns the major brand of an ISO-BMFF file starting with a
// well-formed ftyp box.
func ftypBrand(b []byte) (string, bool) {
	if len(b) < 16 || !bytes.Equal(b[4:8], []byte("ftyp")) {
		return "", false
	}
	size := binary.BigEndian.Uint32(b[0:4])
	if size < 16 || size > 4096 || (size-16)%4 != 0 {
		return "", false
	}
	return string(b[8:12]), true
}

func printableBrand(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < 0x20 || s[i] > 0x7E {
			return false
		}
	}
	return true
}

// JPEGSize reads the dimensions from the first SOFn frame header of a JPEG.
// It walks marker segments only; no entropy-coded data is touched.
func JPEGSize(b []byte) (w, h int, ok bool) {
	if len(b) < 4 || b[0] != 0xFF || b[1] != 0xD8 {
		return 0, 0, false
	}
	i := 2
	for steps := 0; steps < 4096 && i < len(b); steps++ {
		if b[i] != 0xFF {
			return 0, 0, false
		}
		// Skip fill bytes.
		for i < len(b) && b[i] == 0xFF {
			i++
		}
		if i >= len(b) {
			return 0, 0, false
		}
		m := b[i]
		i++
		switch {
		case m == 0xD8 || m == 0x01 || (m >= 0xD0 && m <= 0xD7):
			continue // standalone markers
		case m == 0xD9 || m == 0xDA || m == 0x00:
			return 0, 0, false // EOI/SOS before any frame header
		}
		if i+2 > len(b) {
			return 0, 0, false
		}
		segLen := int(binary.BigEndian.Uint16(b[i:]))
		if segLen < 2 || i+segLen > len(b) {
			return 0, 0, false
		}
		if m >= 0xC0 && m <= 0xCF && m != 0xC4 && m != 0xC8 && m != 0xCC {
			if segLen < 7 {
				return 0, 0, false
			}
			h = int(binary.BigEndian.Uint16(b[i+3:]))
			w = int(binary.BigEndian.Uint16(b[i+5:]))
			if w == 0 || h == 0 {
				return 0, 0, false
			}
			return w, h, true
		}
		i += segLen
	}
	return 0, 0, false
}
