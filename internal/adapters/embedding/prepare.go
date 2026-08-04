package embedding

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"time"

	"github.com/siro33950/knowbrew/internal/adapters/config"
	"github.com/siro33950/knowbrew/internal/adapters/fsutil"
)

const (
	onnxRuntimeVersion = "1.23.2"
	llamaVersion       = "b9637"
)

type download struct {
	URL    string
	Path   string
	SHA256 string
}

type managedSpec struct {
	Manifest Manifest
	Files    []download
	Runtime  download
}

func Prepare(ctx context.Context, root, model string, progress io.Writer) error {
	model = strings.TrimSpace(model)
	if model == "" || model == config.EmbeddingDisabled {
		return nil
	}
	if model == config.EmbeddingCustom {
		return errors.New("custom embedding models are prepared by the user")
	}
	spec, err := managedModel(model)
	if err != nil {
		return err
	}
	destination := filepath.Join(root, ".knowbrew", "state", "models", model)
	if manifest, err := ReadManifest(destination); err == nil && sameManagedManifest(manifest, spec.Manifest) {
		return nil
	}
	if progress != nil {
		_, _ = fmt.Fprintf(progress, "Preparing embedding model %s...\n", model)
	}
	parent := filepath.Dir(destination)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return err
	}
	temporary, err := os.MkdirTemp(parent, "."+model+"-download-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(temporary) }()
	client := &http.Client{Timeout: 30 * time.Minute}
	for _, file := range spec.Files {
		path := filepath.Join(temporary, filepath.FromSlash(file.Path))
		if err := downloadTo(ctx, client, file.URL, path, file.SHA256); err != nil {
			return fmt.Errorf("download %s: %w", file.Path, err)
		}
	}
	if spec.Runtime.URL != "" {
		if spec.Runtime.SHA256 == "" {
			return errors.New("managed embedding runtime requires a SHA-256 checksum")
		}
		archivePath := filepath.Join(temporary, "runtime.archive")
		if err := downloadTo(ctx, client, spec.Runtime.URL, archivePath, spec.Runtime.SHA256); err != nil {
			return fmt.Errorf("download embedding runtime: %w", err)
		}
		runtimeDirectory := filepath.Join(temporary, "runtime")
		if err := extractArchive(archivePath, runtimeDirectory, spec.Runtime.URL); err != nil {
			return fmt.Errorf("extract embedding runtime: %w", err)
		}
		_ = os.Remove(archivePath)
		if spec.Manifest.Backend == "onnx" {
			library, err := findRuntimeLibrary(runtimeDirectory)
			if err != nil {
				return err
			}
			spec.Manifest.RuntimeFile, err = filepath.Rel(temporary, library)
			if err != nil {
				return err
			}
		} else {
			executable, err := findNamedFile(runtimeDirectory, executableName("llama-server"))
			if err != nil {
				return err
			}
			if err := os.Chmod(executable, 0o755); err != nil {
				return err
			}
			spec.Manifest.ExecutableFile, err = filepath.Rel(temporary, executable)
			if err != nil {
				return err
			}
		}
	}
	manifestData, err := json.MarshalIndent(spec.Manifest, "", "  ")
	if err != nil {
		return err
	}
	manifestData = append(manifestData, '\n')
	if err := fsutil.AtomicWrite(filepath.Join(temporary, "manifest.json"), manifestData, 0o644); err != nil {
		return err
	}
	if _, err := ReadManifest(temporary); err != nil {
		return fmt.Errorf("validate prepared embedding model: %w", err)
	}
	if err := os.RemoveAll(destination); err != nil {
		return err
	}
	if err := os.Rename(temporary, destination); err != nil {
		return err
	}
	if progress != nil {
		_, _ = fmt.Fprintf(progress, "Prepared embedding model %s.\n", model)
	}
	return nil
}

func managedModel(model string) (managedSpec, error) {
	runtimeArchive, err := onnxRuntimeDownload(runtime.GOOS, runtime.GOARCH)
	if err != nil && model != config.EmbeddingQwen {
		return managedSpec{}, err
	}
	switch model {
	case config.EmbeddingRuri:
		return managedSpec{
			Manifest: Manifest{
				ID: model + "@cbd305d39c17cc58375ceb29586996e7d98d48c0+ort-" + onnxRuntimeVersion, Backend: "onnx",
				Dimension: 512, MaxLength: 8192,
				ModelFile: "model.onnx", TokenizerFile: "tokenizer.json",
				InputNames: []string{"input_ids", "attention_mask"},
				OutputName: "last_hidden_state", Pooling: "mean",
				QueryPrefix: "検索クエリ: ", DocumentPrefix: "検索文書: ",
			},
			Files: []download{
				{
					URL:  "https://huggingface.co/onnx-community/ruri-v3-130m-ONNX/resolve/cbd305d39c17cc58375ceb29586996e7d98d48c0/onnx/model_quantized.onnx?download=true",
					Path: "model.onnx", SHA256: "1cef9144b83bce6d858869a4c6d966356811e353229860d4e0e8183f82cf19d6",
				},
				{
					URL:  "https://huggingface.co/cl-nagoya/ruri-v3-130m/resolve/e3114c6ee10dbab8b4b235fbc6dcf9dd4d5ac1a6/tokenizer.json?download=true",
					Path: "tokenizer.json",
				},
			},
			Runtime: runtimeArchive,
		}, nil
	case config.EmbeddingSnowflake:
		return managedSpec{
			Manifest: Manifest{
				ID: model + "@9611734+ort-" + onnxRuntimeVersion, Backend: "onnx",
				Dimension: 768, MaxLength: 512,
				ModelFile: "model.onnx", TokenizerFile: "tokenizer.json",
				InputNames: []string{"input_ids", "attention_mask"},
				OutputName: "sentence_embedding", Pooling: "sentence_embedding",
				QueryPrefix: "query: ",
			},
			Files: []download{
				{
					URL:  "https://huggingface.co/Snowflake/snowflake-arctic-embed-m-v1.5/resolve/9611734/onnx/model_quantized.onnx?download=true",
					Path: "model.onnx", SHA256: "a18f437b2466863901a0bdc14904cf93246f5ecce0b656fc773bc2b7b2f84f6e",
				},
				{
					URL:  "https://huggingface.co/Snowflake/snowflake-arctic-embed-m-v1.5/resolve/9611734/tokenizer.json?download=true",
					Path: "tokenizer.json",
				},
			},
			Runtime: runtimeArchive,
		}, nil
	case config.EmbeddingQwen:
		llamaArchive, err := llamaRuntimeDownload(runtime.GOOS, runtime.GOARCH)
		if err != nil {
			return managedSpec{}, err
		}
		return managedSpec{
			Manifest: Manifest{
				ID: model + "@370f27d7550e0def9b39c1f16d3fbaa13aa67728", Backend: "llama.cpp",
				Dimension: 1024, MaxLength: 32768,
				ModelFile: "model.gguf", Pooling: "last",
			},
			Files: []download{{
				URL:  "https://huggingface.co/Qwen/Qwen3-Embedding-0.6B-GGUF/resolve/370f27d7550e0def9b39c1f16d3fbaa13aa67728/Qwen3-Embedding-0.6B-Q8_0.gguf?download=true",
				Path: "model.gguf", SHA256: "06507c7b42688469c4e7298b0a1e16deff06caf291cf0a5b278c308249c3e439",
			}},
			Runtime: llamaArchive,
		}, nil
	default:
		return managedSpec{}, fmt.Errorf("unsupported managed embedding model %q", model)
	}
}

func sameManagedManifest(existing, expected Manifest) bool {
	existing.RuntimeFile = ""
	existing.ExecutableFile = ""
	expected.RuntimeFile = ""
	expected.ExecutableFile = ""
	return reflect.DeepEqual(existing, expected)
}

func onnxRuntimeDownload(goos, goarch string) (download, error) {
	base := "https://github.com/microsoft/onnxruntime/releases/download/v" + onnxRuntimeVersion + "/"
	switch goos + "/" + goarch {
	case "darwin/arm64":
		return download{
			URL:    base + "onnxruntime-osx-arm64-" + onnxRuntimeVersion + ".tgz",
			SHA256: "b4d513ab2b26f088c66891dbbc1408166708773d7cc4163de7bdca0e9bbb7856",
		}, nil
	case "darwin/amd64":
		return download{
			URL:    base + "onnxruntime-osx-x86_64-" + onnxRuntimeVersion + ".tgz",
			SHA256: "d10359e16347b57d9959f7e80a225a5b4a66ed7d7e007274a15cae86836485a6",
		}, nil
	case "linux/amd64":
		return download{
			URL:    base + "onnxruntime-linux-x64-" + onnxRuntimeVersion + ".tgz",
			SHA256: "1fa4dcaef22f6f7d5cd81b28c2800414350c10116f5fdd46a2160082551c5f9b",
		}, nil
	case "linux/arm64":
		return download{
			URL:    base + "onnxruntime-linux-aarch64-" + onnxRuntimeVersion + ".tgz",
			SHA256: "7c63c73560ed76b1fac6cff8204ffe34fe180e70d6582b5332ec094810241e5c",
		}, nil
	case "windows/amd64":
		return download{
			URL:    base + "onnxruntime-win-x64-" + onnxRuntimeVersion + ".zip",
			SHA256: "0b38df9af21834e41e73d602d90db5cb06dbd1ca618948b8f1d66d607ac9f3cd",
		}, nil
	case "windows/arm64":
		return download{
			URL:    base + "onnxruntime-win-arm64-" + onnxRuntimeVersion + ".zip",
			SHA256: "1cfe88b6435df3b5fb0e9f6bd7d6f5df1e887b6174de7f6e2a47bab956f3f168",
		}, nil
	default:
		return download{}, fmt.Errorf("managed ONNX embedding runtime is unsupported on %s/%s", goos, goarch)
	}
}

func llamaRuntimeDownload(goos, goarch string) (download, error) {
	base := "https://github.com/ggml-org/llama.cpp/releases/download/" + llamaVersion + "/llama-" + llamaVersion + "-bin-"
	switch goos + "/" + goarch {
	case "darwin/arm64":
		return download{
			URL:    base + "macos-arm64.tar.gz",
			SHA256: "72a93f3e68c31de3e438d462669aad1fcdb423b995e9c41033cc7d27a9a3ac69",
		}, nil
	case "darwin/amd64":
		return download{
			URL:    base + "macos-x64.tar.gz",
			SHA256: "71743f8db0958e7c266cceb7add7b16aa418a964667e471094aa6ae65b9c8298",
		}, nil
	case "linux/amd64":
		return download{
			URL:    base + "ubuntu-x64.tar.gz",
			SHA256: "a50ee14f021a9d8e92e30f622f7e3be1318ee1125bb9a9ba8d2025388df48743",
		}, nil
	case "linux/arm64":
		return download{
			URL:    base + "ubuntu-arm64.tar.gz",
			SHA256: "211d9e9ee738698beb7ca271be82661ae2b5da3fbb489cf7d9e4e6ed601be106",
		}, nil
	case "windows/amd64":
		return download{
			URL:    base + "win-cpu-x64.zip",
			SHA256: "f7783c2b8c007f95e710ac40f26a24861a80b603b0b739fc54d7c926a4716c1e",
		}, nil
	case "windows/arm64":
		return download{
			URL:    base + "win-cpu-arm64.zip",
			SHA256: "db1d3f4c13c08b693f539e100bf6d3a435148b0ffc186b044fdd65d490cc6df7",
		}, nil
	default:
		return download{}, fmt.Errorf("managed llama.cpp embedding runtime is unsupported on %s/%s", goos, goarch)
	}
}

func downloadTo(
	ctx context.Context,
	client *http.Client,
	url, path, checksum string,
) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode/100 != 2 {
		return fmt.Errorf("server returned %s", response.Status)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	temporary := path + ".partial"
	file, err := os.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	digest := sha256.New()
	_, copyErr := io.Copy(io.MultiWriter(file, digest), response.Body)
	closeErr := file.Close()
	if copyErr != nil {
		_ = os.Remove(temporary)
		return copyErr
	}
	if closeErr != nil {
		_ = os.Remove(temporary)
		return closeErr
	}
	if checksum != "" && !strings.EqualFold(hex.EncodeToString(digest.Sum(nil)), checksum) {
		_ = os.Remove(temporary)
		return errors.New("SHA-256 checksum mismatch")
	}
	return os.Rename(temporary, path)
}

func extractArchive(path, destination, sourceURL string) error {
	if err := os.MkdirAll(destination, 0o755); err != nil {
		return err
	}
	if strings.HasSuffix(sourceURL, ".zip") {
		return extractZIP(path, destination)
	}
	return extractTarGzip(path, destination)
}

func extractTarGzip(path, destination string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	compressed, err := gzip.NewReader(file)
	if err != nil {
		return err
	}
	defer func() { _ = compressed.Close() }()
	reader := tar.NewReader(compressed)
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		path, err := archivePath(destination, header.Name)
		if err != nil {
			return err
		}
		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(path, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := writeArchiveFile(path, reader, os.FileMode(header.Mode)); err != nil {
				return err
			}
		case tar.TypeSymlink:
			if err := writeArchiveSymlink(destination, path, header.Linkname); err != nil {
				return err
			}
		case tar.TypeLink:
			target, err := archivePath(destination, header.Linkname)
			if err != nil {
				return err
			}
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				return err
			}
			if err := os.Link(target, path); err != nil {
				return err
			}
		}
	}
}

func extractZIP(path, destination string) error {
	reader, err := zip.OpenReader(path)
	if err != nil {
		return err
	}
	defer func() { _ = reader.Close() }()
	for _, entry := range reader.File {
		path, err := archivePath(destination, entry.Name)
		if err != nil {
			return err
		}
		if entry.FileInfo().IsDir() {
			if err := os.MkdirAll(path, 0o755); err != nil {
				return err
			}
			continue
		}
		input, err := entry.Open()
		if err != nil {
			return err
		}
		if entry.Mode()&os.ModeSymlink != 0 {
			link, readErr := io.ReadAll(io.LimitReader(input, 4096))
			if readErr != nil {
				_ = input.Close()
				return readErr
			}
			err = writeArchiveSymlink(destination, path, string(link))
		} else {
			err = writeArchiveFile(path, input, entry.Mode())
		}
		_ = input.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

func writeArchiveSymlink(root, path, target string) error {
	if filepath.IsAbs(target) {
		return fmt.Errorf("archive symlink has absolute target: %s", target)
	}
	resolved := filepath.Clean(filepath.Join(filepath.Dir(path), filepath.FromSlash(target)))
	cleanRoot := filepath.Clean(root)
	if resolved != cleanRoot && !strings.HasPrefix(resolved, cleanRoot+string(filepath.Separator)) {
		return fmt.Errorf("archive symlink escapes destination: %s", target)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.Symlink(target, path)
}

func archivePath(root, name string) (string, error) {
	path := filepath.Join(root, filepath.FromSlash(name))
	if !strings.HasPrefix(filepath.Clean(path), filepath.Clean(root)+string(filepath.Separator)) {
		return "", fmt.Errorf("archive entry escapes destination: %s", name)
	}
	return path, nil
}

func writeArchiveFile(path string, input io.Reader, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode.Perm())
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(file, input)
	closeErr := file.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}

func findRuntimeLibrary(root string) (string, error) {
	var names []string
	switch runtime.GOOS {
	case "darwin":
		names = []string{"libonnxruntime.dylib", "libonnxruntime." + onnxRuntimeVersion + ".dylib"}
	case "linux":
		names = []string{"libonnxruntime.so", "libonnxruntime.so." + onnxRuntimeVersion}
	case "windows":
		names = []string{"onnxruntime.dll"}
	default:
		return "", fmt.Errorf("unsupported ONNX runtime platform %s", runtime.GOOS)
	}
	for _, name := range names {
		if path, err := findNamedFile(root, name); err == nil {
			return path, nil
		}
	}
	return "", errors.New("ONNX Runtime shared library was not found in downloaded archive")
}

func findNamedFile(root, name string) (string, error) {
	var found string
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() && entry.Name() == name {
			found = path
			return filepath.SkipAll
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if found == "" {
		return "", fmt.Errorf("%s was not found in downloaded archive", name)
	}
	return found, nil
}

func executableName(name string) string {
	if runtime.GOOS == "windows" {
		return name + ".exe"
	}
	return name
}
