package selfupdate

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"testing"
)

func makeArchive(t *testing.T, name string, content []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(content)), Typeflag: tar.TypeReg})
	tw.Write(content)
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

func makeZip(t *testing.T, name string, content []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, _ := zw.Create(name)
	w.Write(content)
	zw.Close()
	return buf.Bytes()
}

func TestExtractBinaryZip(t *testing.T) {
	want := []byte("fake-exe-bytes")
	archive := makeZip(t, "furo.exe", want)
	got, err := extractBinary(archive, "furo.exe")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("got %q", got)
	}
	if _, err := extractBinary(archive, "missing.exe"); err == nil {
		t.Fatal("missing file did not error")
	}
}

func TestExtractBinary(t *testing.T) {
	want := []byte("fake-binary-bytes")
	archive := makeArchive(t, "furo", want)
	got, err := extractBinary(archive, "furo")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("got %q", got)
	}
	if _, err := extractBinary(archive, "missing"); err == nil {
		t.Fatal("missing file did not error")
	}
	if _, err := extractBinary([]byte("not a gzip"), "furo"); err == nil {
		t.Fatal("bad archive did not error")
	}
}
