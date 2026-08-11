package nativeapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/navidrome/navidrome/log"
	"github.com/navidrome/navidrome/model"
	"github.com/navidrome/navidrome/scanner"
	"github.com/navidrome/navidrome/server"
)

// maxUploadSize is the maximum size for an uploaded music file.
const maxUploadSize = 1 << 30 // 1 GiB

// uploadRouter serves the fork-specific music upload API.
//
// FORK POLICY (see FORK.md): it is a standalone router, mounted separately in
// cmd/root.go, instead of being part of nativeapi.Router. This way upstream
// changes to native_api.go / wire_gen.go never conflict with this feature.
type uploadRouter struct {
	ds      model.DataStore
	scanner model.Scanner
}

// NewUploadRouter returns the music-file upload handler.
//
// It is meant to be mounted at /api/upload (see cmd/root.go), serving:
//
//	POST /api/upload
//
//	multipart/form-data fields:
//	  file       (required)  - the music file to upload
//	  libraryId  (optional)  - target library ID (defaults to first library)
//	  folder     (optional)  - relative folder path inside the library
//	                           (created if missing). No path traversal.
//
// On success, performs a selective scan limited to the target folder and
// returns JSON `{"id": "<mediaFileId>", "path": "...", "libraryId": N, "title": "..."}`.
func NewUploadRouter(ds model.DataStore, scanner model.Scanner) http.Handler {
	api := &uploadRouter{ds: ds, scanner: scanner}
	r := chi.NewRouter()
	r.Use(server.Authenticator(ds))
	r.Use(server.JWTRefresher)
	r.Use(server.UpdateLastAccessMiddleware(ds))
	r.With(adminOnlyMiddleware).Post("/", api.uploadAndScan)
	return r
}

func (api *uploadRouter) uploadAndScan(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Limit the size of the request body up-front
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadSize)
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		http.Error(w, "invalid multipart form: "+err.Error(), http.StatusBadRequest)
		return
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "missing 'file' form field: "+err.Error(), http.StatusBadRequest)
		return
	}
	defer file.Close()

	// Resolve target library
	libRepo := api.ds.Library(ctx)
	var lib *model.Library
	if libIDStr := strings.TrimSpace(r.FormValue("libraryId")); libIDStr != "" {
		id, err := strconv.Atoi(libIDStr)
		if err != nil {
			http.Error(w, "invalid libraryId", http.StatusBadRequest)
			return
		}
		lib, err = libRepo.Get(id)
		if err != nil {
			http.Error(w, "library not found", http.StatusNotFound)
			return
		}
	} else {
		libs, err := libRepo.GetAll()
		if err != nil {
			http.Error(w, "cannot list libraries: "+err.Error(), http.StatusInternalServerError)
			return
		}
		if len(libs) == 0 {
			http.Error(w, "no libraries configured", http.StatusInternalServerError)
			return
		}
		lib = &libs[0]
	}

	// Validate / sanitise relative folder path
	relFolder, err := sanitizeRelPath(r.FormValue("folder"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	fileName := filepath.Base(header.Filename)
	if fileName == "" || fileName == "." || fileName == string(filepath.Separator) {
		http.Error(w, "invalid file name", http.StatusBadRequest)
		return
	}

	targetDir := filepath.Join(lib.Path, relFolder)
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		http.Error(w, "cannot create folder: "+err.Error(), http.StatusInternalServerError)
		return
	}
	targetPath := filepath.Join(targetDir, fileName)

	// Write the file to disk
	out, err := os.Create(targetPath)
	if err != nil {
		http.Error(w, "cannot save file: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if _, err := io.Copy(out, file); err != nil {
		_ = out.Close()
		_ = os.Remove(targetPath)
		http.Error(w, "error writing file: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(targetPath)
		http.Error(w, "error closing file: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Trigger a selective scan limited to the target folder.
	// Retry with back-off if the scanner is already busy (e.g. another upload or
	// a scheduled scan is in progress) rather than failing the whole upload.
	scanFolder := relFolder
	if scanFolder == "" {
		scanFolder = "."
	}
	target := model.ScanTarget{LibraryID: lib.ID, FolderPath: scanFolder}
	log.Info(ctx, "Upload: triggering selective scan", "library", lib.ID, "folder", scanFolder, "file", fileName)

	const maxRetries = 10
	var scanErr error
	for i := range maxRetries {
		_, scanErr = api.scanner.ScanFolders(ctx, false, []model.ScanTarget{target})
		if scanErr == nil {
			break
		}
		if !errors.Is(scanErr, scanner.ErrAlreadyScanning) {
			http.Error(w, "scan failed: "+scanErr.Error(), http.StatusInternalServerError)
			return
		}
		if i == maxRetries-1 {
			http.Error(w, "scan failed: scanner busy, try again later", http.StatusServiceUnavailable)
			return
		}
		wait := time.Duration(i+1) * 500 * time.Millisecond
		log.Debug(ctx, "Upload: scanner busy, retrying", "attempt", i+1, "wait", wait)
		time.Sleep(wait)
	}

	// Locate the new media file by library-qualified path.
	// Use path.Join (forward slashes) because the DB always stores paths with '/'.
	relInLib := path.Join(relFolder, fileName)
	mfs, err := api.ds.MediaFile(ctx).FindByPaths([]string{fmt.Sprintf("%d:%s", lib.ID, relInLib)})
	if err != nil {
		http.Error(w, "lookup failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if len(mfs) == 0 {
		http.Error(w, "file uploaded but not indexed (unsupported format or scan skipped it)", http.StatusUnprocessableEntity)
		return
	}

	mf := mfs[0]
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"id":        mf.ID,
		"path":      mf.Path,
		"libraryId": mf.LibraryID,
		"title":     mf.Title,
	})
}

// sanitizeRelPath cleans a user-supplied relative folder path and rejects any
// attempt to escape the library root.
func sanitizeRelPath(p string) (string, error) {
	p = strings.TrimSpace(p)
	if p == "" {
		return "", nil
	}
	// Reject absolute paths
	if filepath.IsAbs(p) || strings.HasPrefix(p, "/") || strings.HasPrefix(p, `\`) {
		return "", fmt.Errorf("folder must be a relative path")
	}
	cleaned := filepath.Clean(p)
	if cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("invalid folder path: traversal not allowed")
	}
	if cleaned == "." {
		return "", nil
	}
	return cleaned, nil
}
