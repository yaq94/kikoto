package httpapi

import (
	"fmt"
	"testing"
)

func duplicateAudioSet(prefix string, size int64) []catalogAudioFile {
	files := make([]catalogAudioFile, 0, 20)
	for index := 1; index <= 20; index++ {
		files = append(files, catalogAudioFile{
			Name: fmt.Sprintf("%s%02d.mp3", prefix, index),
			Size: size,
		})
	}
	return files
}

// Equal counts and sizes are not evidence that two large, differently named
// sets are copies when the provider supplies no hashes.
func TestCatalogDuplicateMatchesRequiresNamesWithoutHashes(t *testing.T) {
	child := duplicateAudioSet("B", 1024*1024)
	parent := duplicateAudioSet("A", 1024*1024)
	matched, duplicate := catalogDuplicateMatches(child, parent, false)
	if duplicate {
		t.Fatalf("size-only match marked different names as a copy: %v", matched)
	}
}

func TestCatalogDuplicateMatchesUsesHashesForRenamedCopies(t *testing.T) {
	child := duplicateAudioSet("B", 1024*1024)
	parent := duplicateAudioSet("A", 1024*1024)
	for index := range child {
		child[index].Hash = fmt.Sprintf("hash-%02d", index)
		parent[index].Hash = child[index].Hash
	}
	if _, duplicate := catalogDuplicateMatches(child, parent, false); !duplicate {
		t.Fatal("complete matching hashes did not identify a renamed copy")
	}
}

// The nested-package shortcut must not hide a differently named work when the
// only evidence is reused names and sizes.
func TestCatalogDuplicateAncestorEvidenceRequiresPartOrHashes(t *testing.T) {
	child := duplicateAudioSet("A", 1024)
	ancestor := duplicateAudioSet("A", 1024)
	if catalogDuplicateAncestorEvidence(child, ancestor, "/Creator/Ordinary/WorkA", "/Creator/Ordinary") {
		t.Fatal("a plain work folder was accepted as a nested package copy")
	}
	if !catalogDuplicateAncestorEvidence(child, ancestor, "/Creator/Ordinary/FLAC", "/Creator/Ordinary") {
		t.Fatal("a format directory was not accepted as a nested package copy")
	}
	if !catalogDuplicateAncestorEvidence(child, ancestor, "/Creator/Package/WorkA", "/Creator/Package") {
		t.Fatal("a named package container was not accepted as a nested copy")
	}
	for index := range child {
		child[index].Hash = fmt.Sprintf("hash-%02d", index)
		ancestor[index].Hash = child[index].Hash
	}
	if !catalogDuplicateAncestorEvidence(child, ancestor, "/Creator/Ordinary/WorkA", "/Creator/Ordinary") {
		t.Fatal("complete matching hashes were not accepted as copy evidence")
	}
}

func TestCatalogPackageDirectoryRequiresExplicitPackageEvidence(t *testing.T) {
	for _, name := range []string{"Work", "My Work", "WorkA", "Ordinary"} {
		if catalogPackageDirectory(name) {
			t.Fatalf("ordinary work directory %q was treated as a package", name)
		}
	}
	for _, name := range []string{"Package", "24 works 14G", "Collection 14G", "作品集合集"} {
		if !catalogPackageDirectory(name) {
			t.Fatalf("package directory %q was not recognized", name)
		}
	}
}
