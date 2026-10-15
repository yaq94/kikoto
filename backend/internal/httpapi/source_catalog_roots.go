package httpapi

import (
	"path"
	"strings"
)

type catalogDirectorySummary struct{ Audio, FormatDirectories, OtherDirectories int }

// catalogAdmissionRoot chooses the playable container from the already-read
// directory and its parent's assignment. This does not create series or merge
// identities; callers retain every real parent/child directory observation.
func catalogAdmissionRoot(directoryPath, parentRoot string, observed catalogDirectorySummary) string {
	if _, episode := catalogEpisodeEvidence(directoryPath); episode && catalogDirectoryCode(parentRoot) == "" {
		if _, parentEpisode := catalogEpisodeEvidence(parentRoot); !parentEpisode {
			return directoryPath
		}
	}
	if parentRoot != "" {
		return parentRoot
	}
	if catalogDirectoryCode(directoryPath) != "" || observed.Audio > 0 || (directoryPath != "/" && observed.FormatDirectories > 0 && observed.OtherDirectories == 0) {
		return directoryPath
	}
	return ""
}

func catalogFormatDirectory(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "mp3", "flac", "wav", "ogg", "opus", "m4a", "aac", "aiff":
		return true
	}
	return false
}

func catalogDirectoryCode(root string) string {
	for _, word := range strings.FieldsFunc(path.Base(root), func(r rune) bool { return r == '[' || r == ']' || r == ' ' || r == '【' || r == '】' }) {
		if code := normalizeWorkCode(word); code != "" {
			return code
		}
	}
	return normalizeWorkCode(path.Base(root))
}
