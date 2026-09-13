package release

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"time"
)

// writeArchive writes a deterministic zip archive containing only the binary
// at archive root: fixed entry name, Deflate method, fixed timestamp and
// fixed mode, with no other metadata or comments.
func writeArchive(dst, binaryPath, entryName string, modTime time.Time) error {
	f, err := os.Create(dst)
	if err != nil {
		return err
	}
	if err := writeZipArchive(f, binaryPath, entryName, modTime); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// hashArchive streams the same deterministic archive bytes as writeArchive
// into a SHA-256 digest without touching the filesystem.
func hashArchive(binaryPath, entryName string, modTime time.Time) (string, error) {
	digest := sha256.New()
	if err := writeZipArchive(digest, binaryPath, entryName, modTime); err != nil {
		return "", err
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

func writeZipArchive(w io.Writer, binaryPath, entryName string, modTime time.Time) error {
	zipWriter := zip.NewWriter(w)
	header := &zip.FileHeader{Name: entryName, Method: zip.Deflate, Modified: modTime}
	header.SetMode(0o755)
	entry, err := zipWriter.CreateHeader(header)
	if err != nil {
		_ = zipWriter.Close()
		return err
	}
	src, err := os.Open(binaryPath)
	if err != nil {
		_ = zipWriter.Close()
		return err
	}
	_, copyErr := io.Copy(entry, src)
	closeErr := src.Close()
	zipErr := zipWriter.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	return zipErr
}

// hashFile returns the lowercase SHA-256 of a file's bytes, streaming the
// content so release binaries are never held in memory whole.
func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	digest := sha256.New()
	_, readErr := io.Copy(digest, f)
	closeErr := f.Close()
	if readErr != nil {
		return "", readErr
	}
	if closeErr != nil {
		return "", closeErr
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}
