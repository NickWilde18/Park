package docs

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
	"io/fs"
	"path"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/NickWilde18/Park/internal/fail"
)

// File is one file of a description: its path from the site's top, its content type, and its bytes.
type File struct {
	Path        string
	ContentType string
	Content     []byte
}

var (
	errTooLarge        = fail.New(fail.Invalid, "zip_too_large", fmt.Sprintf("The zip is larger than %d MB.", MaxZip>>20))
	errUnpacksTooLarge = fail.New(fail.Invalid, "unpacks_too_large", fmt.Sprintf("The zip unpacks to more than %d MB.", MaxUnpacked>>20))
	errTooManyFiles    = fail.New(fail.Invalid, "too_many_files", fmt.Sprintf("The zip holds more than %d files.", MaxFiles))
	errNotAZip         = fail.New(fail.Invalid, "not_a_zip", "The upload is not a zip Park can read.")
	errNoIndex         = fail.New(fail.Invalid, "no_index", "The zip has no index.html at its top, or in the one folder at its top.")
)

// Read takes a description's files out of its zip, sorted by path. It refuses a zip larger than the limits; a path
// that is absolute, climbs out of the zip, or is not UTF-8; a link; a path twice; and a zip without an index.html at
// its top, or in the one folder at its top, which then counts as the top. Files a system adds, such as __MACOSX and
// .DS_Store, are left out. A file that says it unpacks to more than is left of the limit is refused before it is
// read, and one that unpacks to more than it says is refused as a zip Park cannot read, so that a zip cannot pass the
// limit by lying.
func Read(data []byte) ([]File, error) {
	if len(data) > MaxZip {
		return nil, errTooLarge
	}
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, errNotAZip
	}
	var files []File
	seen := map[string]bool{}
	left := int64(MaxUnpacked)
	for _, f := range zr.File {
		if f.FileInfo().IsDir() || junk(f.Name) {
			continue
		}
		p, ok := clean(f.Name)
		if !ok {
			return nil, fail.New(fail.Invalid, "bad_path", "The zip holds a path Park will not serve, "+strconv.Quote(f.Name)+
				": paths must be relative, stay inside the zip, and be UTF-8.")
		}
		if f.Mode()&fs.ModeSymlink != 0 {
			return nil, fail.New(fail.Invalid, "zip_link", "The zip holds a link, "+strconv.Quote(p)+".")
		}
		if seen[p] {
			return nil, fail.New(fail.Invalid, "bad_path", "The zip holds "+strconv.Quote(p)+" twice.")
		}
		seen[p] = true
		if len(files) == MaxFiles {
			return nil, errTooManyFiles
		}
		content, err := unpack(f, left)
		if err != nil {
			return nil, err
		}
		left -= int64(len(content))
		files = append(files, File{Path: p, Content: content})
	}
	files, err = top(files)
	if err != nil {
		return nil, err
	}
	for i := range files {
		files[i].ContentType = contentType(files[i].Path)
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return files, nil
}

// unpack reads one file, refusing it once it passes what is left of the limit.
func unpack(f *zip.File, left int64) ([]byte, error) {
	if f.UncompressedSize64 > uint64(left) {
		return nil, errUnpacksTooLarge
	}
	rc, err := f.Open()
	if err != nil {
		return nil, errNotAZip
	}
	defer func() { _ = rc.Close() }()
	b, err := io.ReadAll(io.LimitReader(rc, left+1))
	if err != nil {
		return nil, errNotAZip
	}
	if int64(len(b)) > left {
		return nil, errUnpacksTooLarge
	}
	return b, nil
}

// clean is a zip entry's path from the zip's top, if Park will serve it: relative, inside the zip, and UTF-8.
func clean(name string) (string, bool) {
	if !utf8.ValidString(name) || strings.ContainsAny(name, "\\\x00") || strings.HasPrefix(name, "/") {
		return "", false
	}
	p := path.Clean(name)
	if p == "." || p == ".." || strings.HasPrefix(p, "../") {
		return "", false
	}
	return p, true
}

// junk is what a system adds to a zip: macOS's resource forks and folder settings, and Windows' thumbnails.
func junk(name string) bool {
	for _, segment := range strings.Split(name, "/") {
		if segment == "__MACOSX" {
			return true
		}
	}
	base := path.Base(name)
	return base == ".DS_Store" || base == "Thumbs.db" || strings.HasPrefix(base, "._")
}

// top makes index.html the site's top: where it is already, or under the one folder that holds every file.
func top(files []File) ([]File, error) {
	for _, f := range files {
		if f.Path == "index.html" {
			return files, nil
		}
	}
	if len(files) == 0 {
		return nil, errNoIndex
	}
	folder, _, _ := strings.Cut(files[0].Path, "/")
	prefix := folder + "/"
	index := false
	for _, f := range files {
		if !strings.HasPrefix(f.Path, prefix) {
			return nil, errNoIndex
		}
		index = index || f.Path == prefix+"index.html"
	}
	if !index {
		return nil, errNoIndex
	}
	for i := range files {
		files[i].Path = strings.TrimPrefix(files[i].Path, prefix)
	}
	return files, nil
}

// types are the content types Park serves a description's files as, by extension; any other file is served as bytes
// to download. They are Park's own, so that what a page gets does not depend on the host's MIME tables.
var types = map[string]string{
	".html": "text/html; charset=utf-8", ".htm": "text/html; charset=utf-8", ".css": "text/css; charset=utf-8",
	".js": "text/javascript; charset=utf-8", ".mjs": "text/javascript; charset=utf-8", ".json": "application/json",
	".map": "application/json", ".txt": "text/plain; charset=utf-8", ".md": "text/markdown; charset=utf-8",
	".csv": "text/csv; charset=utf-8", ".xml": "application/xml", ".svg": "image/svg+xml", ".png": "image/png",
	".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".gif": "image/gif", ".webp": "image/webp", ".avif": "image/avif",
	".ico": "image/x-icon", ".bmp": "image/bmp", ".mp4": "video/mp4", ".m4v": "video/mp4", ".webm": "video/webm",
	".mov": "video/quicktime", ".ogv": "video/ogg", ".mp3": "audio/mpeg", ".m4a": "audio/mp4", ".wav": "audio/wav",
	".ogg": "audio/ogg", ".oga": "audio/ogg", ".woff": "font/woff", ".woff2": "font/woff2", ".ttf": "font/ttf",
	".otf": "font/otf", ".pdf": "application/pdf", ".wasm": "application/wasm",
}

func contentType(p string) string {
	if t, ok := types[strings.ToLower(path.Ext(p))]; ok {
		return t
	}
	return "application/octet-stream"
}
