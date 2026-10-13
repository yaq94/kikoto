package localfs

import (
	"path/filepath"
	"testing"
)

// A remote lyrics download names its folder after the edition it read, so the
// folder carries another work's code while lying inside a work folder. It is
// part of that work folder and must not be discovered as a work of its own,
// which would also drop the folder that holds it.
func TestDiscoverKeepsADownloadedLyricsFolderInsideItsWorkFolder(t *testing.T) {
	root := t.TempDir()
	lyrics := "Lyrics - CHI_HANS - RJ00000002 - 20261008-153012"
	writeFile(t, filepath.Join(root, "RJ00000001", "mp3", "01.mp3"))
	writeFile(t, filepath.Join(root, "RJ00000001", lyrics, "Translated", "01.mp3.vtt"))
	writeFile(t, filepath.Join(root, "RJ00000001", lyrics+" (2)", "01.mp3.vtt"))

	folders, summary, err := Discover(root, Options{ScanDepth: 3})
	if err != nil {
		t.Fatal(err)
	}
	if len(folders) != 1 || folders[0].Code != "RJ00000001" || folders[0].RelPath != "RJ00000001" {
		t.Fatalf("discovered %+v, want only the work folder", folders)
	}
	if len(folders[0].Files) != 3 || len(summary.DuplicateGroups) != 0 || len(summary.AmbiguousFolders) != 0 {
		t.Fatalf("files = %d, summary = %+v; want the lyrics among the work's three files", len(folders[0].Files), summary)
	}

	changed, _, err := DiscoverChangedFolders(root, Options{ScanDepth: 3}, []string{"RJ00000001/" + lyrics + "/Translated/01.mp3.vtt"})
	if err != nil {
		t.Fatal(err)
	}
	if len(changed) != 1 || changed[0].RelPath != "RJ00000001" {
		t.Fatalf("a change inside the lyrics folder resolved to %+v, want its work folder", changed)
	}
}

func TestExtractWorkCodeReadsNoCodeFromADownloadedLyricsFolder(t *testing.T) {
	for _, name := range []string{
		"Lyrics - CHI_HANS - RJ00000002 - 20261008-153012",
		"Lyrics - RJ00000002 - 20261008-153012",
		"Lyrics - JPN - RJ00000002 - 20261008-153012 (3)",
	} {
		if code, _ := ExtractWorkCode(name); code != "" {
			t.Fatalf("%q reads as work code %q", name, code)
		}
	}
	// Only the exact download name is exempt; a folder a user names is a work folder.
	for _, name := range []string{
		"Lyrics RJ00000002",
		"Lyrics - CHI_HANS - RJ00000002",
		"RJ00000002 Lyrics - 20261008-153012",
	} {
		if code, _ := ExtractWorkCode(name); code != "RJ00000002" {
			t.Fatalf("%q reads as work code %q, want RJ00000002", name, code)
		}
	}
}
