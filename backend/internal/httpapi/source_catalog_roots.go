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
	// A directory named with its own product code is an authored work, not a
	// format or auxiliary directory inside the parent work. Keeping the parent
	// root here would fold several coded works into one card and make each one
	// unreachable by the code the source publishes.
	if catalogDirectoryCode(directoryPath) != "" {
		return directoryPath
	}
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
