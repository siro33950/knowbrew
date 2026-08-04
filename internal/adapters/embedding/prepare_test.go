package embedding

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDownloadToVerifiesChecksum(t *testing.T) {
	content := []byte("verified model")
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK, Status: "200 OK",
			Body: io.NopCloser(bytes.NewReader(content)), Request: request,
		}, nil
	})}
	digest := sha256.Sum256(content)
	path := filepath.Join(t.TempDir(), "model.bin")
	if err := downloadTo(
		context.Background(), client, "https://models.example/model", path,
		hex.EncodeToString(digest[:]),
	); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(path); err != nil || !bytes.Equal(data, content) {
		t.Fatalf("downloaded data = %q, %v", data, err)
	}
	if err := downloadTo(
		context.Background(), client, "https://models.example/model", path, strings.Repeat("0", 64),
	); err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("checksum error = %v", err)
	}
}

func TestManagedRuntimeDownloadsCoverReleasePlatformsWithChecksums(t *testing.T) {
	platforms := []struct {
		goos, goarch string
	}{
		{goos: "darwin", goarch: "amd64"},
		{goos: "darwin", goarch: "arm64"},
		{goos: "linux", goarch: "amd64"},
		{goos: "linux", goarch: "arm64"},
		{goos: "windows", goarch: "amd64"},
		{goos: "windows", goarch: "arm64"},
	}
	for _, platform := range platforms {
		name := platform.goos + "/" + platform.goarch
		t.Run(name, func(t *testing.T) {
			for runtimeName, resolve := range map[string]func(string, string) (download, error){
				"onnxruntime": onnxRuntimeDownload,
				"llama.cpp":   llamaRuntimeDownload,
			} {
				artifact, err := resolve(platform.goos, platform.goarch)
				if err != nil {
					t.Fatalf("%s download: %v", runtimeName, err)
				}
				if artifact.URL == "" || len(artifact.SHA256) != sha256.Size*2 {
					t.Fatalf("%s artifact = %#v", runtimeName, artifact)
				}
				if platform.goos == "windows" && !strings.HasSuffix(artifact.URL, ".zip") {
					t.Fatalf("%s Windows URL = %q", runtimeName, artifact.URL)
				}
			}
		})
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func TestExtractTarGzipPreservesSafeSymlinks(t *testing.T) {
	archive := filepath.Join(t.TempDir(), "runtime.tar.gz")
	writeTarGzip(t, archive, []tar.Header{
		{Name: "runtime/library.1.dylib", Mode: 0o644, Size: 7, Typeflag: tar.TypeReg},
		{Name: "runtime/library.dylib", Linkname: "library.1.dylib", Typeflag: tar.TypeSymlink},
	}, [][]byte{[]byte("runtime"), nil})
	destination := t.TempDir()
	if err := extractTarGzip(archive, destination); err != nil {
		t.Fatal(err)
	}
	target, err := os.Readlink(filepath.Join(destination, "runtime", "library.dylib"))
	if err != nil {
		t.Fatal(err)
	}
	if target != "library.1.dylib" {
		t.Fatalf("symlink target = %q", target)
	}
}

func TestExtractTarGzipRejectsEscapingSymlink(t *testing.T) {
	archive := filepath.Join(t.TempDir(), "runtime.tar.gz")
	writeTarGzip(t, archive, []tar.Header{{
		Name: "runtime/library.dylib", Linkname: "../../outside", Typeflag: tar.TypeSymlink,
	}}, [][]byte{nil})
	err := extractTarGzip(archive, t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "escapes destination") {
		t.Fatalf("escaping symlink error = %v", err)
	}
}

func writeTarGzip(t *testing.T, path string, headers []tar.Header, bodies [][]byte) {
	t.Helper()
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	compressed := gzip.NewWriter(file)
	archive := tar.NewWriter(compressed)
	for index := range headers {
		header := headers[index]
		if err := archive.WriteHeader(&header); err != nil {
			t.Fatal(err)
		}
		if len(bodies[index]) > 0 {
			if _, err := archive.Write(bodies[index]); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	if err := compressed.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}
