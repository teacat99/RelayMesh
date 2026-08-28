package store

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/teacat99/RelayMesh/internal/model"
)

func ValidatePathReference(ref model.PathReference) error {
	p := ref.Path
	if p == "" {
		return fmt.Errorf("path reference cannot be empty")
	}
	if strings.Contains(p, "\\") {
		return fmt.Errorf("path must use forward slashes: %q", p)
	}
	if strings.ContainsRune(p, 0) {
		return fmt.Errorf("path contains null byte")
	}
	if filepath.IsAbs(p) || strings.HasPrefix(p, "/") {
		return fmt.Errorf("path must be project-relative, not absolute: %q", p)
	}
	if strings.Contains(p, ":") {
		return fmt.Errorf("path contains forbidden drive or protocol separator: %q", p)
	}

	clean := filepath.ToSlash(filepath.Clean(p))
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return fmt.Errorf("path escapes project boundary: %q", p)
	}
	return nil
}

func ValidatePathReferences(refs []model.PathReference) error {
	if len(refs) > model.MaxPathReferences {
		return fmt.Errorf("too many path references: %d > %d", len(refs), model.MaxPathReferences)
	}
	for i, ref := range refs {
		if err := ValidatePathReference(ref); err != nil {
			return fmt.Errorf("invalid reference [%d]: %w", i, err)
		}
	}
	return nil
}

func ValidateSegments(segments []model.Segment) error {
	if len(segments) > model.MaxSegments {
		return fmt.Errorf("too many segments: %d > %d", len(segments), model.MaxSegments)
	}
	names := make(map[string]bool)
	for i, seg := range segments {
		name := strings.TrimSpace(seg.Name)
		if name == "" {
			return fmt.Errorf("segment [%d] name is required", i)
		}
		if names[name] {
			return fmt.Errorf("duplicate segment name %q", name)
		}
		names[name] = true

		if len(seg.Content) > model.MaxSegmentBytes {
			return fmt.Errorf("segment %q exceeds max bytes (%d > %d)", name, len(seg.Content), model.MaxSegmentBytes)
		}
	}
	return nil
}
