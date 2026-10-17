package tiefs

// ReadmeText is the help file served at /query/README by every transport.
const ReadmeText = `tie tag-query filesystem
=========================

Navigate to a directory whose name is a tag query to list the matching files:

    ls "query/jazz"                 files tagged jazz
    ls "query/jazz mellow"          tagged jazz AND mellow
    ls "query/jazz mellow -live"    tagged jazz AND mellow, but NOT live

Space separates terms (all ANDed). A leading "-" excludes a tag.

Scope to a tie-type with a "type:" token (default: all types). This accepts a
media type, a full type name, or a custom directory label:

    ls "query/mellow type:audio"    audio files tagged mellow
    ls "query/type:image"           all image files
    ls "query/type:live-album"      dirs carrying the custom "live-album" label

List every known tag, or every known dir-type label:

    cat query/tags
    cat query/types

Saved queries from your config's [Queries] table appear here as ready-made
directories, e.g. a "chill-jazz = jazz mellow -live" entry gives:

    ls query/chill-jazz

The other top-level directory, files/<path>/, is the path-based virtual
directory tree.
`

// Names of the three helper files inside /query.
const (
	ReadmeName   = "README"
	TagListName  = "tags"
	TypeListName = "types"
)
