package handler

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFileImageHandlerReturnsBinarySafeImage(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pixel.png")
	var encoded bytes.Buffer
	img := image.NewRGBA(image.Rect(0, 0, 2, 3))
	img.Set(1, 2, color.RGBA{R: 255, A: 255})
	if err := png.Encode(&encoded, img); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, encoded.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}

	body, _ := json.Marshal(FileImageRequest{Path: path})
	request := httptest.NewRequest("POST", "/file/image", bytes.NewReader(body))
	response := httptest.NewRecorder()
	(&FileImageHandler{}).ServeHTTP(response, request)

	var result FileImageResponse
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatalf("response is not JSON: %v: %s", err, response.Body.String())
	}
	if result.MimeType != "image/png" {
		t.Fatalf("mimeType = %q", result.MimeType)
	}
	decoded, err := base64.StdEncoding.DecodeString(result.Data)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(decoded, encoded.Bytes()) {
		t.Fatal("image bytes changed during transport")
	}
}

func TestFileImageHandlerRejectsNonImage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "note.txt")
	if err := os.WriteFile(path, []byte("not an image"), 0600); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(FileImageRequest{Path: path})
	request := httptest.NewRequest("POST", "/file/image", bytes.NewReader(body))
	response := httptest.NewRecorder()
	(&FileImageHandler{}).ServeHTTP(response, request)

	if !strings.Contains(response.Body.String(), "unsupported image type") {
		t.Fatalf("unexpected response: %s", response.Body.String())
	}
}
