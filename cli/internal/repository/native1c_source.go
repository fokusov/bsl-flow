package repository

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// This file ports Get-BFNativeSource and Copy-BFNativeSnapshot from
// Task.Runtime.ps1: the byte-addressed extension source snapshot with the
// Configuration.xml identity and the owned-metadata UUID scan. It is a
// read-only binding shared by the controller and the native stage host.

var native1CIdentityUUIDPattern = regexp.MustCompile(`^[0-9a-f]{8}(-[0-9a-f]{4}){3}-[0-9a-f]{12}$`)

const native1CZeroUUID = "00000000-0000-0000-0000-000000000000"

// Native1CSourceSnapshot mirrors Get-BFNativeSource.
func Native1CSourceSnapshot(root string) (map[string]any, error) {
	resolved, err := SafePath(root)
	if err != nil {
		return nil, err
	}
	if !isNativeDir(resolved) {
		return nil, blocked("native source root is missing.")
	}
	rows := []map[string]any{}
	// Get-ChildItem -File -Recurse yields every file of a directory before it
	// descends into subdirectories, in the filesystem's own enumeration order.
	// Replicating that exact order keeps the row hash byte-identical with the
	// legacy snapshot; a name-sorted walk would not.
	var walk func(dir string) error
	walk = func(dir string) error {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return err
		}
		files := []os.DirEntry{}
		subdirs := []os.DirEntry{}
		for _, entry := range entries {
			if entry.IsDir() {
				subdirs = append(subdirs, entry)
			} else {
				files = append(files, entry)
			}
		}
		for _, entry := range files {
			path := filepath.Join(dir, entry.Name())
			if _, err := SafePath(path); err != nil {
				return err
			}
			if !entry.Type().IsRegular() {
				return fmt.Errorf("native source tree contains a non-regular file: %s", path)
			}
			data, err := ReadFileBytes(path)
			if err != nil {
				return blocked("native source file cannot be read: %v", err)
			}
			relative, err := filepath.Rel(resolved, path)
			if err != nil {
				return err
			}
			rows = append(rows, map[string]any{"path": filepath.ToSlash(relative), "sha256": fileSHA256(data)})
		}
		for _, entry := range subdirs {
			if err := walk(filepath.Join(dir, entry.Name())); err != nil {
				return err
			}
		}
		return nil
	}
	if err := walk(resolved); err != nil {
		return nil, blocked("%v", err)
	}
	if len(rows) == 0 {
		return nil, blocked("native source snapshot would be empty.")
	}
	configPath := filepath.Join(resolved, "Configuration.xml")
	configData, err := ReadFileBytes(configPath)
	if err != nil || !nativeDependencyRegularFile(configPath) {
		return nil, blocked("Configuration.xml is missing.")
	}
	name, version, uuidValue, found, err := native1CConfigurationIdentity(configData)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, blocked("extension Configuration node is missing.")
	}
	if !native1CIdentityUUIDPattern.MatchString(strings.ToLower(uuidValue)) || strings.ToLower(uuidValue) == native1CZeroUUID {
		return nil, blocked("extension UUID is missing or zero.")
	}
	uuidValue = strings.ToLower(uuidValue)
	owned := map[string]bool{}
	files := make([]string, 0, len(rows))
	for _, row := range rows {
		if !strings.HasSuffix(strings.ToLower(asStringOr(row["path"])), ".xml") {
			continue
		}
		if strings.EqualFold(filepath.Base(asStringOr(row["path"])), "ConfigDumpInfo.xml") {
			continue
		}
		files = append(files, filepath.Join(resolved, filepath.FromSlash(asStringOr(row["path"]))))
	}
	for _, path := range files {
		data, err := ReadFileBytes(path)
		if err != nil {
			return nil, blocked("native source file cannot be read: %v", err)
		}
		uuids, extended, err := native1CMetadataIdentityScan(data)
		if err != nil {
			return nil, err
		}
		// ClassId and borrowed-object references are not owned identities; the
		// scan above only collects ContainedObject/ObjectId and uuid attributes.
		for _, id := range uuids {
			if !native1CIdentityUUIDPattern.MatchString(id) || strings.HasPrefix(id, "00000000-0000-0000-0000-") || owned[id] {
				return nil, blocked("owned metadata UUIDs must be unique and free of scaffold placeholders.")
			}
			owned[id] = true
		}
		for _, reference := range extended {
			if strings.TrimSpace(reference) == native1CZeroUUID {
				return nil, blocked("ExtendedConfigurationObject UUID is zero.")
			}
		}
	}
	fileRows := make([]any, 0, len(rows))
	for _, row := range rows {
		fileRows = append(fileRows, row)
	}
	hash, err := Hash(fileRows)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"root":      resolved,
		"extension": name,
		"version":   version,
		"uuid":      uuidValue,
		"files":     fileRows,
		"sha256":    hash,
	}, nil
}

// Native1CSnapshotCopy mirrors Copy-BFNativeSnapshot: the snapshot is copied
// into the owned evidence directory and its identity must re-bind.
func Native1CSnapshotCopy(source map[string]any, destination string) (map[string]any, error) {
	resolved, err := SafePath(destination)
	if err != nil {
		return nil, err
	}
	if err := SafeMkdir(resolved); err != nil {
		return nil, blocked("%v", err)
	}
	root := asStringOr(source["root"])
	for _, raw := range anyItems(source["files"]) {
		file := asMap(raw)
		relative := asStringOr(file["path"])
		if validateRelativeNativePath(relative, false) != nil {
			return nil, invalid("unsafe native snapshot file path: %s", relative)
		}
		to, err := SafePath(filepath.Join(resolved, filepath.FromSlash(relative)))
		if err != nil {
			return nil, err
		}
		if err := SafeMkdir(filepath.Dir(to)); err != nil {
			return nil, blocked("%v", err)
		}
		data, err := ReadFileBytes(filepath.Join(root, filepath.FromSlash(relative)))
		if err != nil {
			return nil, blocked("native source file cannot be read: %v", err)
		}
		if err := os.WriteFile(to, data, 0o644); err != nil {
			return nil, blocked("%v", err)
		}
	}
	copy, err := Native1CSourceSnapshot(resolved)
	if err != nil {
		return nil, err
	}
	if asStringOr(copy["sha256"]) != asStringOr(source["sha256"]) {
		return nil, blocked("native source snapshot identity mismatch.")
	}
	return copy, nil
}

// native1CConfigurationIdentity parses the Configuration.xml identity node
// exactly like the legacy XmlDocument projection: the root MetaDataObject, its
// direct Configuration child, Properties/Name, Properties/Version and the raw
// uuid attribute.
func native1CConfigurationIdentity(data []byte) (name, version, uuidValue string, found bool, err error) {
	decoder := xml.NewDecoder(bytes.NewReader(data))
	depth := 0
	rootOK := false
	inConfiguration := false
	configurationFound := false
	inProperties := false
	inName := false
	inVersion := false
	var nameText, versionText strings.Builder
	for {
		token, tokenErr := decoder.Token()
		if tokenErr == io.EOF {
			break
		}
		if tokenErr != nil {
			return "", "", "", false, fmt.Errorf("native source XML is malformed: %v", tokenErr)
		}
		switch typed := token.(type) {
		case xml.StartElement:
			switch {
			case depth == 0 && typed.Name.Local == "MetaDataObject":
				rootOK = true
			case depth == 1 && rootOK && !configurationFound && typed.Name.Local == "Configuration":
				inConfiguration = true
				configurationFound = true
				for _, attr := range typed.Attr {
					if attr.Name.Space == "" && attr.Name.Local == "uuid" {
						uuidValue = attr.Value
					}
				}
			case depth == 2 && inConfiguration && typed.Name.Local == "Properties":
				inProperties = true
			case depth == 3 && inProperties && typed.Name.Local == "Name":
				inName = true
			case depth == 3 && inProperties && typed.Name.Local == "Version":
				inVersion = true
			}
			depth++
		case xml.CharData:
			if inName {
				nameText.Write(typed)
			} else if inVersion {
				versionText.Write(typed)
			}
		case xml.EndElement:
			depth--
			switch {
			case depth == 0:
			case depth == 1 && typed.Name.Local == "Configuration":
				inConfiguration = false
			case depth == 2 && typed.Name.Local == "Properties":
				inProperties = false
			case depth == 3 && typed.Name.Local == "Name":
				inName = false
			case depth == 3 && typed.Name.Local == "Version":
				inVersion = false
			}
		}
	}
	if !configurationFound {
		return "", "", "", false, nil
	}
	return nameText.String(), versionText.String(), uuidValue, true, nil
}

// native1CMetadataIdentityScan collects the owned identity surface of one
// metadata document: every uuid attribute and every ContainedObject/ObjectId
// text, plus the ExtendedConfigurationObject references.
func native1CMetadataIdentityScan(data []byte) (uuids, extended []string, err error) {
	decoder := xml.NewDecoder(bytes.NewReader(data))
	type frame struct {
		name string
		text *strings.Builder
	}
	stack := []frame{}
	for {
		token, tokenErr := decoder.Token()
		if tokenErr == io.EOF {
			break
		}
		if tokenErr != nil {
			return nil, nil, fmt.Errorf("native source XML is malformed: %v", tokenErr)
		}
		switch typed := token.(type) {
		case xml.StartElement:
			for _, attr := range typed.Attr {
				if attr.Name.Space == "" && attr.Name.Local == "uuid" {
					uuids = append(uuids, strings.ToLower(attr.Value))
				}
			}
			stack = append(stack, frame{name: typed.Name.Local, text: &strings.Builder{}})
		case xml.CharData:
			if len(stack) > 0 {
				stack[len(stack)-1].text.Write(typed)
			}
		case xml.EndElement:
			top := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			if top.name == "ObjectId" && len(stack) > 0 && stack[len(stack)-1].name == "ContainedObject" {
				uuids = append(uuids, strings.ToLower(top.text.String()))
			}
			if top.name == "ExtendedConfigurationObject" {
				extended = append(extended, top.text.String())
			}
		}
	}
	return uuids, extended, nil
}

func isNativeDir(path string) bool {
	info, err := os.Lstat(path)
	return err == nil && info.IsDir()
}
