# Unreleased

<details>
<summary><strong>Library and work detail</strong></summary>

- The List button on a work's detail page names the list that holds the work.
  When several do, it names the first and counts the rest, and its tooltip
  names them all.
- **Manage lyrics** keeps one lyrics file per track and language. A language
  strip switches between the lyrics for every language and Japanese,
  Simplified Chinese, Traditional Chinese, English, and Korean. Assignments
  saved by earlier versions apply to every language.
- **Add lyrics** in **Manage lyrics** replaces **From remote**. It also copies
  lyrics from another edition of the work that is in the library, asks which
  language the lyrics are in, and saves them to a folder named after that
  language, such as `lyrics_zh_hans`, inside the work folder. A file that is
  already there is never replaced.
- **Manage files** has **Find identical files**, which compares the local
  files of every edition and folder of the work by content, marks the files
  that have an identical copy, and can select the copies that also exist in
  another folder or are repeated in the listed one.
- A work whose files lie in several local folders shows each folder in its
  directory, lists one folder at a time in **Manage files**, and tells a
  library manager how to bring the copies into one. The same notice appears
  when another edition of the work is in the library.

</details>

<details>
<summary><strong>Metadata and scans</strong></summary>

- A local scan keeps every folder in which it finds a work's code as a folder
  of that work and indexes all of them. It no longer raises duplicate folders
  for review in Activity.
- A local scan no longer takes a lyrics folder that **From remote** created
  inside a work folder, such as
  `Lyrics - CHI_HANS - RJ00000001 - 20261008-153012`, for a separate work.
  When such a folder lay within the scan depth, the scan attached it to the
  edition it names and lost the work folder that holds it. The next scan
  restores that work folder.

</details>

<details>
<summary><strong>Playback</strong></summary>

- Lyrics follow your metadata languages: a track uses the first of them that
  has lyrics, then the lyrics for every language, then another language, and
  the lyrics list names each file's language.

</details>

<details>
<summary><strong>Settings and interface</strong></summary>

- About lists its reference projects as compact credits, one line of project
  names per area. **Show details** adds what each project informed.

</details>

<details>
<summary><strong>Demo</strong></summary>

- Insights in Demo shows a simulated listening history over the admitted
  works, with a notice that it is simulated. Demo records no listening, so the
  dashboard was empty there.

</details>
