package httpapi

import (
	"path"
	"strings"

	"github.com/yexca/kikoto/backend/internal/workcode"
)

type catalogDirectorySummary struct {
	Audio, FormatDirectories, AuxiliaryDirectories, OtherDirectories int
}

// catalogContainerDirectory reports a category or collection directory. Its
// contents are separate works, so the directory itself is never the identity
// of everything below it. A container may still hold loose playable files;
// those files keep a card of their own.
func catalogContainerDirectory(name string) bool {
	lower := strings.ToLower(strings.TrimSpace(name))
	switch lower {
	case "未分类", "uncategorized", "misc", "miscellaneous", "其他", "其它",
		"合集", "collection", "collections", "部分收集", "大杂烩",
		"tags", "tag", "标签", "分類", "分类":
		return true
	}
	return strings.Contains(lower, "合集") || strings.Contains(lower, "collection") ||
		strings.Contains(lower, "大杂烩") || strings.Contains(lower, "部分收集")
}

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
	if catalogContainerDirectory(path.Base(directoryPath)) {
		// The container's own loose files are still playable; its children are
		// admitted individually because the container is not one work.
		if observed.Audio > 0 {
			return directoryPath
		}
		return ""
	}
	if parentRoot != "" && catalogContainerDirectory(path.Base(parentRoot)) {
		// A category parent does not lend its identity to an album or format
		// directory; this directory decides from its own contents.
		parentRoot = ""
	}
	if _, episode := catalogEpisodeEvidence(directoryPath); episode && catalogDirectoryCode(parentRoot) == "" {
		if _, parentEpisode := catalogEpisodeEvidence(parentRoot); !parentEpisode {
			return directoryPath
		}
	}
	if parentRoot != "" {
		return parentRoot
	}
	// A work root may carry a format directory next to extras, artwork or
	// subtitles. Only a directory that is purely format or auxiliary containers
	// is a helper inside another work; anything else is its own playable root.
	if catalogDirectoryCode(directoryPath) != "" || observed.Audio > 0 ||
		(directoryPath != "/" && observed.FormatDirectories > 0 && observed.OtherDirectories == 0) {
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

// catalogAuxiliaryDirectory reports a directory that accompanies a work
// instead of holding another work. A work root can therefore contain both a
// format directory and extras without turning each format into its own card.
func catalogAuxiliaryDirectory(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "extra", "extras", "omake", "特典", "おまけ", "イラスト", "插图", "插圖",
		"画像", "图片", "圖片", "cover", "covers", "封面", "字幕", "subtitle",
		"subtitles", "lrc", "ass", "srt", "翻译", "翻譯", "translation", "台本",
		"script", "scripts", "文本", "text", "txt", "pdf", "视频", "視頻", "video",
		"pv", "demo", "试听", "試聴", "sample", "wallpaper", "壁纸", "壁紙":
		return true
	}
	return false
}

func catalogDirectoryCode(root string) string {
	name := path.Base(root)
	for _, word := range strings.FieldsFunc(name, func(r rune) bool { return r == '[' || r == ']' || r == ' ' || r == '【' || r == '】' }) {
		if code := normalizeWorkCode(word); code != "" {
			return code
		}
	}
	// Sources also prefix a code directly to a title, such as
	// "RJ01075962妹妹的夜间侍奉". The code ends where the title starts, so a
	// token split alone would miss every one of those works.
	upper := strings.ToUpper(name)
	for _, prefix := range workcode.Prefixes() {
		if !strings.HasPrefix(upper, prefix) {
			continue
		}
		digits := 0
		for digits < len(upper)-len(prefix) && upper[len(prefix)+digits] >= '0' && upper[len(prefix)+digits] <= '9' {
			digits++
		}
		if digits < 5 || digits > 8 {
			return ""
		}
		return normalizeWorkCode(upper[:len(prefix)+digits])
	}
	return normalizeWorkCode(name)
}
