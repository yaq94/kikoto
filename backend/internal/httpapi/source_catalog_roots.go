package httpapi

import (
	"path"
	"regexp"
	"strings"

	"github.com/yexca/kikoto/backend/internal/workcode"
)

type catalogDirectorySummary struct {
	Audio, FormatDirectories, AuxiliaryDirectories, TrackContainers, OtherDirectories int
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
	// A Chinese collection label is a suffix, not a substring of an arbitrary
	// work title ("合集部屋" is still a collection, "My Collection" is a work).
	// English titles legitimately contain the word, so only the exact names
	// above count for English.
	return strings.HasSuffix(lower, "合集") || strings.HasSuffix(lower, "合辑") ||
		strings.HasSuffix(lower, "大杂烩") || strings.HasSuffix(lower, "部分收集")
}

// catalogAdmissionRoot chooses the playable container from the already-read
// directory and its parent's assignment. This does not create series or merge
// identities; callers retain every real parent/child directory observation.
func catalogAdmissionRoot(directoryPath, parentRoot string, observed catalogDirectorySummary) string {
	// A child directory that repeats its parent's release code is a format or
	// packaging copy of the same work (RJ147133/…/RJ147133 MP3+lrc). It must
	// not become a second card with the same number.
	if parent := path.Dir(directoryPath); parent != directoryPath {
		if code := catalogDirectoryCode(directoryPath); code != "" && code == catalogDirectoryCode(parent) {
			return parent
		}
	}
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
		// A child with its own audio below an uncoded grouping folder is a
		// work, not another track of the card above it. A coded parent is one
		// release, so its discs, bonus folders and per-track directories stay
		// inside it.
		if catalogDirectoryCode(parentRoot) == "" && !catalogWorkPartDirectory(path.Base(directoryPath)) {
			if catalogSelfWorkRoot(directoryPath, observed) {
				return directoryPath
			}
		}
		return parentRoot
	}
	// A work root may carry a format directory next to extras, artwork or
	// subtitles. Only a directory that is purely format or auxiliary containers
	// is a helper inside another work; anything else is its own playable root.
	if catalogSelfWorkRoot(directoryPath, observed) {
		return directoryPath
	}
	return ""
}

// catalogSelfWorkRoot reports a directory whose own contents make it playable
// without a parent: a work code, direct audio, or nothing but format copies and
// track containers. A release that only ships Disc 1/Disc 2 or MP3 files still
// owns one identity; admitting the discs separately would create one card per
// disc and break the "tracks do not become works" rule.
//
// Unknown subdirectories do not veto admission. A directory with audio-bearing
// children is handled child by child, so admitting the parent only gives its own
// files a card; it cannot swallow those children. The source root is admitted
// too: audio placed directly at the configured root is still playable, and the
// source display name supplies the card title.
func catalogSelfWorkRoot(directoryPath string, observed catalogDirectorySummary) bool {
	return catalogDirectoryCode(directoryPath) != "" || observed.Audio > 0 ||
		(observed.FormatDirectories > 0 || observed.TrackContainers > 0)
}

func catalogFormatDirectory(name string) bool {
	lower := catalogDirectoryFormatName(name)
	// A format directory often carries a quality marker, such as
	// "FLAC 24bit" or "MP3 320kbps". Match the leading format token so those
	// copies stay part of the work instead of becoming a second card.
	token := lower
	rest := ""
	if index := strings.IndexFunc(lower, func(r rune) bool {
		return r == ' ' || r == '_' || r == '-' || r == '(' || r == ')' || r == '['
	}); index > 0 {
		token = lower[:index]
		rest = lower[index:]
	}
	switch token {
	case "mp3", "flac", "wav", "ogg", "opus", "m4a", "aac", "aiff", "aif",
		"wma", "dsd", "dsf", "dff", "ape", "alac", "wv", "tta":
		// Only a quality marker may follow the format word. "MP3 Collection"
		// is a title; "MP3 320kbps" and "FLAC 24bit" are copies.
		return strings.TrimSpace(rest) == "" || catalogFormatQuality.MatchString(rest)
	}
	// A Japanese release names its audio copy "音声" or "ボイス". These words
	// are also ordinary title words ("Audio Drama", "音声剧"), so only the
	// complete name counts; an ordinal prefix such as "1.音声" was removed
	// above.
	switch lower {
	case "音声", "音聲", "音频", "音頻", "ボイス", "voice", "audio":
		return true
	}
	// "hi-res" contains a dash, so the token split above would only see "hi".
	for _, prefix := range []string{"lossless", "hires", "hi-res", "无损", "無損"} {
		if strings.HasPrefix(lower, prefix) {
			return true
		}
	}
	return false
}

var catalogFormatQuality = regexp.MustCompile(`[\s._\-()\[\]]*(?:[0-9]{1,3}\s*(?:k|kbps|bps|bit|khz|hz)?[\s._\-()\[\]]*)+$`)

// catalogDirectoryFormatName removes a leading order marker from a directory
// name. Releases order their copies as "2-mp3", "01_mp3" or "1.音声"; without
// this step the token split sees the number and the format directory becomes
// a work of its own. The marker is only removed when it is a short ordinal:
// a title such as "24.歡迎光臨閃亮銀" keeps its number and is not a format.
var catalogLeadingOrdinal = regexp.MustCompile(`^\s*(?:[0-9]{1,3}\s*[._\-、:：]+\s*|[\[(（【]\s*[0-9]{1,3}\s*[\])）】]\s*)`)

func catalogDirectoryFormatName(name string) string {
	lower := strings.ToLower(strings.TrimSpace(name))
	if stripped := catalogLeadingOrdinal.ReplaceAllString(lower, ""); stripped != "" {
		lower = stripped
	}
	return lower
}

// catalogWorkPartDirectory reports a directory that holds part of a work:
// a format copy, an auxiliary attachment, or a track/disc container. A child
// with its own audio that does not match keeps its own card.
func catalogWorkPartDirectory(name string) bool {
	return catalogFormatDirectory(name) || catalogAuxiliaryDirectory(name) || catalogTrackContainerDirectory(name)
}

// catalogTrackContainerDirectory reports a directory that groups the tracks of
// one work. The names are the ones a release uses for discs, volumes and
// format copies; they are not work titles.
// catalogNumberedTrackContainer matches a numbered part of one work:
// "Chapter 03", "Track 12", "Part 2". A bare word without a number stays a
// title. Episode markers keep their own series path. Volumes are deliberately
// absent: "Vol 1"/"Vol 2" names separate releases as often as parts of one, and
// treating only the dotted spelling as separate was inconsistent.
var catalogNumberedTrackContainer = regexp.MustCompile(`(?i)^(?:chapter|ch|track|scene|part)\s*[0-9]+(?:\b|$)`)

func catalogTrackContainerDirectory(name string) bool {
	lower := catalogDirectoryFormatName(name)
	if catalogNonEpisode.MatchString(lower) || catalogNumberedTrackContainer.MatchString(lower) {
		return true
	}
	switch lower {
	case "本編", "本篇", "特典映像", "映像特典", "音声特典", "ボイス", "se", "bgm",
		"bonus", "bonus track", "bonus tracks", "おまけ", "おまけ音声":
		return true
	}
	return false
}

// catalogAuxiliaryDirectory reports a directory that accompanies a work
// instead of holding another work. A work root can therefore contain both a
// format directory and extras without turning each format into its own card.
func catalogAuxiliaryDirectory(name string) bool {
	switch catalogDirectoryFormatName(name) {
	case "extra", "extras", "omake", "特典", "おまけ", "イラスト", "插图", "插圖",
		"画像", "图片", "圖片", "cover", "covers", "封面", "字幕", "subtitle",
		"subtitles", "lrc", "ass", "srt", "翻译", "翻譯", "translation", "台本",
		"script", "scripts", "文本", "text", "txt", "pdf", "视频", "視頻", "video",
		"pv", "demo", "试听", "試聴", "sample", "wallpaper", "壁纸", "壁紙",
		"art", "arts", "artwork", "photo", "photos", "picture", "pictures",
		"image", "images", "img", "scans", "scan", "写真", "照片":
		return true
	}
	return false
}

// catalogCodePattern finds a work code anywhere in a directory name, whatever
// brackets or separators surround it: "[RJ00000001] Title", "(RJ00000001)
// Title", "（RJ00000001）Title" and "RJ00000001Title" all name the same work.
// The digit group is bounded so a longer number is not silently truncated.
var catalogCodePattern = func() *regexp.Regexp {
	prefixes := make([]string, 0, 8)
	for _, prefix := range workcode.Prefixes() {
		prefixes = append(prefixes, regexp.QuoteMeta(prefix))
	}
	return regexp.MustCompile(`(?i)((?:` + strings.Join(prefixes, "|") + `)[0-9]{5,8})(?:[^0-9]|$)`)
}()

func catalogDirectoryCode(root string) string {
	name := path.Base(root)
	if match := catalogCodePattern.FindStringSubmatch(name); match != nil {
		if code := normalizeWorkCode(match[1]); code != "" {
			return code
		}
	}
	// A bare code without a title still resolves through the shared parser.
	return normalizeWorkCode(name)
}
