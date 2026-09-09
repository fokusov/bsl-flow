package main

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
)

type bundleFile struct {
	name string
	data []byte
	hash [32]byte
}

type bundle struct {
	version string
	hash    string
	files   []bundleFile
}

var versionPattern = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.-]+)?$`)
var reservedName = regexp.MustCompile(`(?i)^(CON|PRN|AUX|NUL|COM[1-9]|LPT[1-9])(?:\.|$)`)

func validEntry(name string) bool {
	if name == "" || len(name) > 240 || path.Clean(name) != name || strings.HasPrefix(name, "/") || strings.ContainsAny(name, `\:<>"|?*`+"\x00\r\n") {
		return false
	}
	for _, part := range strings.Split(name, "/") {
		if part == ".." || part == "." || strings.HasSuffix(part, ".") || strings.HasSuffix(part, " ") || reservedName.MatchString(part) {
			return false
		}
		for _, r := range part {
			if r < 32 {
				return false
			}
		}
	}
	return true
}

func readBundle(data []byte, version string) (bundle, error) {
	b := bundle{version: version}
	if !versionPattern.MatchString(version) || len(version) > 64 {
		return b, errors.New("invalid embedded version")
	}
	sum := sha256.Sum256(data)
	b.hash = hex.EncodeToString(sum[:])
	z, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return b, err
	}
	if len(z.File) == 0 || len(z.File) > 20000 {
		return b, errors.New("invalid bundle inventory size")
	}
	seen := map[string]bool{}
	var total uint64
	for _, file := range z.File {
		name := file.Name
		key := strings.ToLower(name)
		if !validEntry(name) || seen[key] || !file.Mode().IsRegular() || (name != "VERSION" && !strings.HasPrefix(name, "global/")) {
			return b, fmt.Errorf("invalid or duplicate bundle file %q", name)
		}
		seen[key] = true
		total += file.UncompressedSize64
		if file.UncompressedSize64 > 64<<20 || total > 256<<20 {
			return b, errors.New("bundle exceeds size limits")
		}
		r, err := file.Open()
		if err != nil {
			return b, err
		}
		content, readErr := io.ReadAll(io.LimitReader(r, int64(file.UncompressedSize64)+1))
		closeErr := r.Close()
		if readErr != nil {
			return b, readErr
		}
		if closeErr != nil {
			return b, closeErr
		}
		if uint64(len(content)) != file.UncompressedSize64 {
			return b, errors.New("bundle file size mismatch")
		}
		if name == "VERSION" && strings.TrimSpace(string(content)) != version {
			return b, errors.New("bundle version mismatch")
		}
		b.files = append(b.files, bundleFile{name, content, sha256.Sum256(content)})
	}
	if !seen["version"] || !seen[strings.ToLower(entrypoint)] {
		return b, errors.New("bundle missing VERSION or controller entrypoint")
	}
	for name := range seen {
		for dir := path.Dir(name); dir != "."; dir = path.Dir(dir) {
			if seen[dir] {
				return b, fmt.Errorf("bundle file/directory collision at %q", dir)
			}
		}
	}
	return b, nil
}

func verifyBundle(root string, b bundle) error {
	if err := checkPath(root); err != nil {
		return err
	}
	expected := map[string][32]byte{}
	dirs := map[string]bool{".": true}
	for _, file := range b.files {
		expected[file.name] = file.hash
		for dir := path.Dir(file.name); dir != "."; dir = path.Dir(dir) {
			dirs[dir] = true
		}
	}
	seen := 0
	err := filepath.WalkDir(root, func(full string, item os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := checkPath(full); err != nil {
			return err
		}
		rel, err := filepath.Rel(root, full)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if item.IsDir() {
			if !dirs[rel] {
				return fmt.Errorf("unexpected bundle directory %q", rel)
			}
			return nil
		}
		want, ok := expected[rel]
		if !ok {
			return fmt.Errorf("unexpected bundle file %q", rel)
		}
		info, err := item.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("non-regular bundle file %q", rel)
		}
		f, err := os.Open(full)
		if err != nil {
			return err
		}
		h := sha256.New()
		_, readErr := io.Copy(h, f)
		closeErr := f.Close()
		if readErr != nil {
			return readErr
		}
		if closeErr != nil {
			return closeErr
		}
		if !bytes.Equal(h.Sum(nil), want[:]) {
			return fmt.Errorf("tampered bundle file %q", rel)
		}
		seen++
		return nil
	})
	if err != nil {
		return err
	}
	if seen != len(expected) {
		return errors.New("bundle files are missing")
	}
	return nil
}

func ensureBundle(base string, b bundle) (string, error) {
	if err := safeMkdir(base); err != nil {
		return "", err
	}
	root := filepath.Join(base, b.version+"-"+b.hash)
	unlock, err := lockCache(filepath.Join(base, ".extract.lock"))
	if err != nil {
		return "", err
	}
	defer unlock()
	if _, err := os.Lstat(root); err == nil {
		return root, verifyBundle(root, b)
	} else if !os.IsNotExist(err) {
		return "", err
	}
	stage, err := os.MkdirTemp(base, ".extract-")
	if err != nil {
		return "", err
	}
	// A crashed extraction is harmless: only a verified, atomically published
	// version directory can be executed. Do not auto-delete another attempt.
	defer removeStage(base, stage)
	for _, file := range b.files {
		full := filepath.Join(stage, filepath.FromSlash(file.name))
		if err := safeMkdir(filepath.Dir(full)); err != nil {
			return "", err
		}
		f, err := os.OpenFile(full, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			return "", err
		}
		_, writeErr := f.Write(file.data)
		syncErr := f.Sync()
		closeErr := f.Close()
		if writeErr != nil {
			return "", writeErr
		}
		if syncErr != nil {
			return "", syncErr
		}
		if closeErr != nil {
			return "", closeErr
		}
	}
	if err := verifyBundle(stage, b); err != nil {
		return "", err
	}
	if err := checkPath(base); err != nil {
		return "", err
	}
	if err := os.Rename(stage, root); err != nil {
		return "", err
	}
	return root, verifyBundle(root, b)
}

func removeStage(base, stage string) {
	// Cleanup must remain below the extraction base and must not traverse a
	// replaced directory. Leave an unsafe/orphan path for operator inspection.
	if filepath.Dir(stage) != base || !strings.HasPrefix(filepath.Base(stage), ".extract-") {
		return
	}
	if err := filepath.WalkDir(stage, func(full string, _ os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		return checkPath(full)
	}); err != nil {
		return
	}
	_ = os.RemoveAll(stage)
}
