package nativeapi

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/navidrome/navidrome/log"
	"github.com/navidrome/navidrome/model"
)

// maxUploadSize is the maximum size for an uploaded music file.
const maxUploadSize = 1 << 30 // 1 GiB

// addUploadRoute registers the music-file upload endpoint.
//
// POST /api/upload
//
//	multipart/form-data fields:
//	  file       (required)  - the music file to upload
//	  libraryId  (optional)  - target library ID (defaults to first library)
//	  folder     (optional)  - relative folder path inside the library
//	                           (created if missing). No path traversal.
//
// On success, performs a selective scan limited to the target folder and
// returns JSON `{"id": "<mediaFileId>", "path": "...", "libraryId": N, "title": "..."}`.
func (api *Router) addUploadRoute(r chi.Router) {
	r.Post("/upload", api.uploadAndScan)
}

func (api *Router) uploadAndScan(w http.ResponseWriter, r *http.Request) {
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

	// Trigger a selective scan limited to the target folder
	scanFolder := relFolder
	if scanFolder == "" {
		scanFolder = "."
	}
	target := model.ScanTarget{LibraryID: lib.ID, FolderPath: scanFolder}
	log.Info(ctx, "Upload: triggering selective scan", "library", lib.ID, "folder", scanFolder, "file", fileName)
	if _, err := api.scanner.ScanFolders(ctx, false, []model.ScanTarget{target}); err != nil {
		http.Error(w, "scan failed: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Locate the new media file by library-qualified path (path is stored relative to library)
	relInLib := filepath.Join(relFolder, fileName)
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
