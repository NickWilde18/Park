package docs

import (
	"archive/zip"
	"bytes"
	"compress/flate"
	"io/fs"
	"strings"
	"testing"

	"github.com/NickWilde18/Park/internal/fail"
)

// entry is one entry of a zip a test builds: a file, or a link when link is set.
type entry struct {
	name, body string
	link       bool
}

func zipOf(t *testing.T, entries ...entry) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, e := range entries {
		h := &zip.FileHeader{Name: e.name, Method: zip.Deflate}
		if e.link {
			h.SetMode(fs.ModeSymlink | 0o777)
		}
		w, err := zw.CreateHeader(h)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(e.body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func code(err error) string {
	if f, ok := fail.As(err); ok {
		return f.Code
	}
	return ""
}

// A site at the zip's top is read as it is, sorted by path, each file with the content type Park serves it as.
func TestReadASite(t *testing.T) {
	files, err := Read(zipOf(t, entry{name: "video/demo.MP4", body: "mp4"}, entry{name: "index.html", body: "<h1>Billing</h1>"},
		entry{name: "shots/list.png", body: "png"}, entry{name: "notes.xyz", body: "?"}))
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, f := range files {
		got = append(got, f.Path+" "+f.ContentType+" "+string(f.Content))
	}
	want := []string{"index.html text/html; charset=utf-8 <h1>Billing</h1>", "notes.xyz application/octet-stream ?",
		"shots/list.png image/png png", "video/demo.MP4 video/mp4 mp4"}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("files:\n%s", strings.Join(got, "\n"))
	}
}

// A site zipped as its folder counts the folder as its top. What macOS and Windows add is left out.
func TestReadAFolder(t *testing.T) {
	files, err := Read(zipOf(t, entry{name: "plan/index.html", body: "hi"}, entry{name: "plan/shots/a.png", body: "png"},
		entry{name: "plan/.DS_Store", body: "x"}, entry{name: "__MACOSX/plan/._index.html", body: "x"},
		entry{name: "plan/shots/Thumbs.db", body: "x"}))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 || files[0].Path != "index.html" || files[1].Path != "shots/a.png" {
		t.Fatalf("files %+v", files)
	}
}

// Zips Park refuses, each with the code that says why.
func TestReadRefuses(t *testing.T) {
	many := []entry{{name: "index.html", body: "x"}}
	for i := 0; i < MaxFiles; i++ {
		many = append(many, entry{name: "f/" + strings.Repeat("a", 3) + string(rune('a'+i%26)) + "/" + strings.Repeat("b", i%7) + "x" +
			string(rune('0'+i%10)) + "-" + itoa(i), body: "x"})
	}
	for _, c := range []struct {
		name string
		data []byte
		want string
	}{
		{"not a zip", []byte("plain text"), "not_a_zip"},
		{"larger than the limit", make([]byte, MaxZip+1), "zip_too_large"},
		{"no index.html", zipOf(t, entry{name: "readme.html", body: "x"}), "no_index"},
		{"index.html in one of two folders", zipOf(t, entry{name: "a/index.html", body: "x"}, entry{name: "b/x.png", body: "x"}), "no_index"},
		{"a path that climbs out", zipOf(t, entry{name: "index.html", body: "x"}, entry{name: "../evil.html", body: "x"}), "bad_path"},
		{"an absolute path", zipOf(t, entry{name: "index.html", body: "x"}, entry{name: "/etc/x", body: "x"}), "bad_path"},
		{"a backslash", zipOf(t, entry{name: "index.html", body: "x"}, entry{name: `a\b.png`, body: "x"}), "bad_path"},
		{"a name that is not UTF-8", zipOf(t, entry{name: "index.html", body: "x"}, entry{name: "\xff.png", body: "x"}), "bad_path"},
		{"a path twice", zipOf(t, entry{name: "index.html", body: "x"}, entry{name: "./index.html", body: "y"}), "bad_path"},
		{"a link", zipOf(t, entry{name: "index.html", body: "x"}, entry{name: "secret", body: "/etc/passwd", link: true}), "zip_link"},
		{"too many files", zipOf(t, many...), "too_many_files"},
	} {
		if _, err := Read(c.data); code(err) != c.want {
			t.Errorf("%s: %v, want %s", c.name, err, c.want)
		}
	}
}

func itoa(i int) string {
	var b []byte
	for {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
		if i == 0 {
			return string(b)
		}
	}
}

// A zip cannot lie its way past the limit: a file that says it is small but unpacks to more is refused as a zip Park
// cannot read, and one that says it is larger than what is left of the limit is refused before it is read.
func TestUnpackCounts(t *testing.T) {
	var raw bytes.Buffer
	fw, _ := flate.NewWriter(&raw, flate.BestCompression)
	_, _ = fw.Write(bytes.Repeat([]byte("a"), 100))
	_ = fw.Close()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.CreateRaw(&zip.FileHeader{Name: "index.html", Method: zip.Deflate, CompressedSize64: uint64(raw.Len()),
		UncompressedSize64: 5})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = w.Write(raw.Bytes())
	_ = zw.Close()
	zr, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := unpack(zr.File[0], 1000); code(err) != "not_a_zip" {
		t.Fatalf("a file that says 5 bytes and unpacks to 100: %v", err)
	}
	if _, err := unpack(zr.File[0], 4); code(err) != "unpacks_too_large" {
		t.Fatalf("a file that says 5 bytes, with 4 left: %v", err)
	}
}
