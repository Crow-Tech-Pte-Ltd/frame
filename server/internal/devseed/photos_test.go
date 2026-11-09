package devseed

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestPhotoJPEGUsesDevPhotosDir(t *testing.T) {
	dir := t.TempDir()
	want := []byte{0xFF, 0xD8, 0xFF, 0xE0, 'd', 'e', 'm', 'o'}
	if err := os.WriteFile(filepath.Join(dir, "03-512.jpg"), want, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FRAME_DEV_PHOTOS", dir)
	if got := photoJPEG(3, 512); !bytes.Equal(got, want) {
		t.Fatalf("photoJPEG(3, 512) did not use the override file")
	}
	// No file for this pattern/size: falls back to a generated JPEG.
	if got := photoJPEG(4, 64); !bytes.HasPrefix(got, []byte{0xFF, 0xD8, 0xFF}) || bytes.Equal(got, want) {
		t.Fatalf("photoJPEG(4, 64) should fall back to the generated pattern")
	}
}
