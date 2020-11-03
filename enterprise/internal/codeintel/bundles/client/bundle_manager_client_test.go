package client

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/inconshreveable/log15"
	"github.com/sourcegraph/sourcegraph/enterprise/internal/codeintel/upload_store/mocks"
)

func TestMain(m *testing.M) {
	flag.Parse()
	if !testing.Verbose() {
		log15.Root().SetHandler(log15.DiscardHandler())
	}
	os.Exit(m.Run())
}

func TestGetUpload(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer ts.Close()

	var fullContents []byte
	for i := 0; i < 1000; i++ {
		fullContents = append(fullContents, []byte(fmt.Sprintf("payload %d\n", i))...)
	}

	uploadStore := mocks.NewMockStore()
	uploadStore.GetFunc.SetDefaultReturn(ioutil.NopCloser(bytes.NewReader(compress(fullContents))), nil)

	client := &bundleManagerClientImpl{bundleManagerURL: ts.URL, uploadStore: uploadStore, ioCopy: io.Copy}
	r, err := client.GetUpload(context.Background(), 42)
	if err != nil {
		t.Fatalf("unexpected error getting upload: %s", err)
	}

	contents, err := ioutil.ReadAll(r)
	if err != nil {
		t.Fatalf("unexpected error reading file: %s", err)
	}

	if diff := cmp.Diff(fullContents, contents); diff != "" {
		t.Errorf("unexpected payload (-want +got):\n%s", diff)
	}
}

func TestGetUploadTransientErrors(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer ts.Close()

	var fullContents []byte
	for i := 0; i < 1000; i++ {
		fullContents = append(fullContents, []byte(fmt.Sprintf("payload %d\n", i))...)
	}

	// mockCopy is like io.Copy but it will read 50 bytes and return an error
	// that appears to be a transient connection error.
	mockCopy := func(w io.Writer, r io.Reader) (int64, error) {
		var buf bytes.Buffer
		_, readErr := io.CopyN(&buf, r, 50)
		if readErr != nil && readErr != io.EOF {
			return 0, readErr
		}

		n, writeErr := io.Copy(w, bytes.NewReader(buf.Bytes()))
		if writeErr != nil {
			return 0, writeErr
		}

		if readErr == io.EOF {
			readErr = nil
		} else {
			readErr = errors.New("read: connection reset by peer")
		}
		return n, readErr
	}

	uploadStore := mocks.NewMockStore()
	uploadStore.GetFunc.SetDefaultHook(func(ctx context.Context, key string, seek int64) (io.ReadCloser, error) {
		return ioutil.NopCloser(bytes.NewReader(compress(fullContents)[seek:])), nil
	})

	client := &bundleManagerClientImpl{bundleManagerURL: ts.URL, uploadStore: uploadStore, ioCopy: mockCopy}
	r, err := client.GetUpload(context.Background(), 42)
	if err != nil {
		t.Fatalf("unexpected error getting upload: %s", err)
	}

	contents, err := ioutil.ReadAll(r)
	if err != nil {
		t.Fatalf("unexpected error reading file: %s", err)
	}

	if diff := cmp.Diff(fullContents, contents); diff != "" {
		t.Errorf("unexpected payload (-want +got):\n%s", diff)
	}
}

func TestGetUploadReadNothingLoop(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer ts.Close()

	var fullContents []byte
	for i := 0; i < 1000; i++ {
		fullContents = append(fullContents, []byte(fmt.Sprintf("payload %d\n", i))...)
	}

	uploadStore := mocks.NewMockStore()
	uploadStore.GetFunc.SetDefaultHook(func(ctx context.Context, key string, seek int64) (io.ReadCloser, error) {
		return ioutil.NopCloser(bytes.NewReader(compress(fullContents)[seek:])), nil
	})

	// Ensure that no progress transient errors do not cause an infinite loop
	mockCopy := func(w io.Writer, r io.Reader) (int64, error) {
		return 0, errors.New("read: connection reset by peer")
	}

	client := &bundleManagerClientImpl{bundleManagerURL: ts.URL, uploadStore: uploadStore, ioCopy: mockCopy}
	if _, err := client.GetUpload(context.Background(), 42); err != ErrNoDownloadProgress {
		t.Fatalf("unexpected error getting upload. want=%q have=%q", ErrNoDownloadProgress, err)
	}
}

func TestSendDB(t *testing.T) {
	tempDir, err := ioutil.TempDir("", "")
	if err != nil {
		t.Fatalf("unexpected error creating temp dir: %s", err)
	}
	defer os.RemoveAll(tempDir)

	var paths []string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		if r.URL.Path == "/dbs/42/stitch" {
			return
		}

		if r.URL.Path != "/dbs/42/0" {
			t.Errorf("unexpected path. want=%s have=%s", "/dbs/42/0", r.URL.Path)
		}

		gzipReader, err := gzip.NewReader(r.Body)
		if err != nil {
			t.Fatalf("unexpected error decompressing payload: %s", err)
		}
		defer gzipReader.Close()

		contents, err := ioutil.ReadAll(gzipReader)
		if err != nil {
			t.Fatalf("unexpected error reading decompressed payload: %s", err)
		}

		if diff := cmp.Diff([]byte("payload\n"), contents); diff != "" {
			t.Errorf("unexpected contents (-want +got):\n%s", diff)
		}

		w.Write([]byte(`{"size": 100}`))
	}))
	defer ts.Close()

	if err := ioutil.WriteFile(filepath.Join(tempDir, "test"), []byte("payload\n"), os.ModePerm); err != nil {
		t.Fatalf("unexpected error writing temp file: %s", err)
	}

	client := &bundleManagerClientImpl{bundleManagerURL: ts.URL, maxPayloadSizeBytes: 10000}
	if err := client.SendDB(context.Background(), 42, filepath.Join(tempDir, "test")); err != nil {
		t.Fatalf("unexpected error sending db: %s", err)
	}
}

func TestSendDBMultipart(t *testing.T) {
	tempDir, err := ioutil.TempDir("", "")
	if err != nil {
		t.Fatalf("unexpected error creating temp dir: %s", err)
	}
	defer os.RemoveAll(tempDir)

	const maxPayloadSizeBytes = 1000

	var fullContents []byte
	for i := 0; i < maxPayloadSizeBytes/10*5; i++ {
		fullContents = append(fullContents, []byte(fmt.Sprintf("payload %02d\n", i%10))...)
	}

	var paths []string
	var sentContent []byte
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		if r.URL.Path == "/dbs/42/stitch" {
			return
		}

		rawContent, err := ioutil.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("unexpected error reading payload: %s", err)
		}

		if len(rawContent) > maxPayloadSizeBytes {
			t.Errorf("oversized payload. want<%d have=%d", maxPayloadSizeBytes, len(rawContent))
		}

		gzipReader, err := gzip.NewReader(bytes.NewReader(rawContent))
		if err != nil {
			t.Fatalf("unexpected error decompressing payload: %s", err)
		}
		defer gzipReader.Close()

		content, err := ioutil.ReadAll(gzipReader)
		if err != nil {
			t.Fatalf("unexpected error reading decompressed payload: %s", err)
		}

		sentContent = append(sentContent, content...)

		w.Write([]byte(`{"size": 100}`))
	}))
	defer ts.Close()

	filename := filepath.Join(tempDir, "test")
	if err := ioutil.WriteFile(filename, fullContents, os.ModePerm); err != nil {
		t.Fatalf("unexpected error writing temp file: %s", err)
	}

	client := &bundleManagerClientImpl{bundleManagerURL: ts.URL, maxPayloadSizeBytes: maxPayloadSizeBytes}
	if err := client.SendDB(context.Background(), 42, filename); err != nil {
		t.Fatalf("unexpected error sending db: %s", err)
	}

	if len(paths) < 5 {
		t.Errorf("unexpected number of requests. want>=%d have=%d", 5, len(paths))
	}
	if paths[len(paths)-1] != "/dbs/42/stitch" {
		t.Errorf("unexpected final request path. want=%s have=%s", "/dbs/42/stitch", paths[len(paths)-1])
	}

	if diff := cmp.Diff(sentContent, fullContents); diff != "" {
		t.Errorf("unexpected contents (-want +got):\n%s", diff)
	}
}

func TestSendDBBadResponse(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer ts.Close()

	tempDir, err := ioutil.TempDir("", "")
	if err != nil {
		t.Fatalf("unexpected error creating temp dir: %s", err)
	}
	defer os.RemoveAll(tempDir)

	client := &bundleManagerClientImpl{bundleManagerURL: ts.URL, maxPayloadSizeBytes: 1000}
	if err := client.SendDB(context.Background(), 42, tempDir); err == nil {
		t.Fatalf("unexpected nil error sending db")
	}
}

func TestBulkExists(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			t.Errorf("unexpected method. want=%s have=%s", "GET", r.Method)
		}
		if r.URL.Path != "/exists" {
			t.Errorf("unexpected method. want=%s have=%s", "/exists", r.URL.Path)
		}

		if diff := cmp.Diff("1,2,3,4,5", r.URL.Query().Get("ids")); diff != "" {
			t.Errorf("unexpected ids (-want +got):\n%s", diff)
		}

		_, _ = w.Write([]byte(`{"1": false, "2": true, "3": false, "4": true, "5": true}`))
	}))
	defer ts.Close()

	client := &bundleManagerClientImpl{bundleManagerURL: ts.URL}
	existsMap, err := client.Exists(context.Background(), []int{1, 2, 3, 4, 5})
	if err != nil {
		t.Fatalf("unexpected error checking bulk exists: %s", err)
	}

	expected := map[int]bool{
		1: false,
		2: true,
		3: false,
		4: true,
		5: true,
	}
	if diff := cmp.Diff(expected, existsMap); diff != "" {
		t.Errorf("unexpected exists map (-want +got):\n%s", diff)
	}
}

func TestBulkExistsBadResponse(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer ts.Close()

	client := &bundleManagerClientImpl{bundleManagerURL: ts.URL}
	_, err := client.Exists(context.Background(), []int{1, 2, 3, 4, 5})
	if err == nil {
		t.Fatalf("unexpected nil error checking bulk exists")
	}
}

func compress(payload []byte) []byte {
	var buf bytes.Buffer
	gzipWriter := gzip.NewWriter(&buf)
	_, _ = io.Copy(gzipWriter, bytes.NewReader(payload))
	_ = gzipWriter.Close()
	return buf.Bytes()
}
