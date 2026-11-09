package media

import (
	"bytes"
	"image"
	"image/jpeg"
	"testing"
	"time"
)

func jpegOf(w, h int) []byte {
	var buf bytes.Buffer
	jpeg.Encode(&buf, image.NewRGBA(image.Rect(0, 0, w, h)), nil)
	return buf.Bytes()
}

func TestSniff(t *testing.T) {
	ftyp := func(brand string) []byte {
		return append([]byte{0, 0, 0, 0x18}, []byte("ftyp"+brand+"\x00\x00\x00\x00isomiso2")...)
	}
	cases := map[string][]byte{
		JPEG: jpegOf(8, 8), HEIC: ftyp("heic"), MP4: ftyp("isom"),
		"": []byte("\x89PNG\r\n\x1a\n........"),
	}
	for want, b := range cases {
		if got := Sniff(b); got != want {
			t.Errorf("Sniff → %q, want %q", got, want)
		}
	}
	for _, brand := range []string{"heix", "mif1"} {
		if Sniff(ftyp(brand)) != HEIC {
			t.Errorf("%s not HEIC", brand)
		}
	}
	for _, brand := range []string{"mp42", "qt  ", "avc1", "M4V "} {
		if Sniff(ftyp(brand)) != MP4 {
			t.Errorf("%s not MP4", brand)
		}
	}
	for _, brand := range []string{"avif", "M4A ", "\x00\x01\x02\x03"} {
		if Sniff(ftyp(brand)) != "" {
			t.Errorf("%q accepted", brand)
		}
	}
	bad := ftyp("isom")
	bad[3] = 0x05 // box size < 16
	if Sniff(bad) != "" || Sniff(nil) != "" || Sniff([]byte{0xFF, 0xD8}) != "" {
		t.Fatal("malformed accepted")
	}
}

func TestJPEGSize(t *testing.T) {
	for _, d := range [][2]int{{1, 1}, {512, 512}, {1440, 1080}, {7, 3000}} {
		w, h, ok := JPEGSize(jpegOf(d[0], d[1]))
		if !ok || w != d[0] || h != d[1] {
			t.Errorf("%v → %d×%d %v", d, w, h, ok)
		}
	}
	// Progressive SOF2 header, with a leading APP segment.
	prog := []byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x04, 'J', 'F', 0xFF, 0xC2, 0x00, 0x0B, 0x08, 0x02, 0x00, 0x03, 0x20, 0x03, 0x01, 0x11, 0x00}
	if w, h, ok := JPEGSize(prog); !ok || w != 800 || h != 512 {
		t.Fatalf("progressive: %d×%d %v", w, h, ok)
	}
	for _, bad := range [][]byte{nil, {0xFF, 0xD8}, {0xFF, 0xD8, 0xFF, 0xDA, 0, 2}, {0xFF, 0xD8, 0xFF, 0xE0, 0xFF, 0xFF}, {0xFF, 0xD8, 0x00}} {
		if _, _, ok := JPEGSize(bad); ok {
			t.Errorf("accepted %x", bad)
		}
	}
}

func TestSigner(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	s := NewSigner(bytes.Repeat([]byte{1}, 32), func() time.Time { return now })
	id := "01JC3V9Z4X8Q2M7T5R1N6K0B3H"
	u := s.URL(id)
	var e, sig string
	if _, err := fmtSscan(u, &e, &sig); err != nil {
		t.Fatal(u, err)
	}
	if !s.Verify(id, e, sig) {
		t.Fatal("valid signature rejected")
	}
	other := NewSigner(bytes.Repeat([]byte{2}, 32), func() time.Time { return now })
	if other.Verify(id, e, sig) || s.Verify("01JC3V9Z4X8Q2M7T5R1N6K0B3J", e, sig) || s.Verify(id, e+"0", sig) || s.Verify(id, "+"+e, sig) {
		t.Fatal("forged signature accepted")
	}
	// Stable within the hour, valid for at least 24 h.
	now = now.Add(10 * time.Minute)
	if s.URL(id) != u {
		t.Fatal("URL not stable within the hour")
	}
	now = now.Add(24 * time.Hour)
	if !s.Verify(id, e, sig) {
		t.Fatal("expired too early")
	}
	now = now.Add(2 * time.Hour)
	if s.Verify(id, e, sig) {
		t.Fatal("expired signature accepted")
	}
}

// fmtSscan extracts e and s from "/m/<id>?e=..&s=..".
func fmtSscan(u string, e, s *string) (int, error) {
	i := bytes.IndexByte([]byte(u), '?')
	q := u[i+1:]
	for _, kv := range bytes.Split([]byte(q), []byte("&")) {
		k, v, _ := bytes.Cut(kv, []byte("="))
		switch string(k) {
		case "e":
			*e = string(v)
		case "s":
			*s = string(v)
		}
	}
	return 2, nil
}

func FuzzSniff(f *testing.F) {
	f.Add(jpegOf(4, 4))
	f.Add([]byte("\x00\x00\x00\x18ftypheic\x00\x00\x00\x00mif1heic"))
	f.Fuzz(func(t *testing.T, b []byte) {
		switch Sniff(b) {
		case JPEG:
			if b[0] != 0xFF || b[1] != 0xD8 || b[2] != 0xFF {
				t.Fatal("bad jpeg")
			}
		case HEIC, MP4:
			if string(b[4:8]) != "ftyp" {
				t.Fatal("bad ftyp")
			}
		}
	})
}

func FuzzJPEGSize(f *testing.F) {
	f.Add(jpegOf(16, 9))
	f.Fuzz(func(t *testing.T, b []byte) {
		w, h, ok := JPEGSize(b)
		if ok && (w <= 0 || h <= 0) {
			t.Fatal("non-positive size")
		}
	})
}
