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
	"github.com/navidrome/navidrome/core/metrics"
	"github.com/navidrome/navidrome/log"
	"github.com/navidrome/navidrome/model"
	"github.com/navidrome/navidrome/scanner"
	"github.com/navidrome/navidrome/server"
	"github.com/navidrome/navidrome/server/events"
)

const maxUploadSize = 1 << 30 // 1 GiB

type uploadRouter struct {
	ds      model.DataStore
	scanner model.Scanner
}

// ServeHTTP adds the fork's upload endpoint without changing the upstream
// native router. Every other route is delegated unchanged.
func (api *Router) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	routePath := r.URL.Path
	if routeContext := chi.RouteContext(r.Context()); routeContext != nil && routeContext.RoutePath != "" {
		routePath = routeContext.RoutePath
	}
	if routePath != "/upload" && routePath != "/upload/" {
		api.Handler.ServeHTTP(w, r)
		return
	}

	sc := scanner.New(r.Context(), api.ds, events.GetBroker(), api.playlists, metrics.GetPrometheusInstance(api.ds))
	newUploadRouter(api.ds, sc).ServeHTTP(w, r)
}

func newUploadRouter(ds model.DataStore, scanner model.Scanner) http.Handler {
	api := &uploadRouter{ds: ds, scanner: scanner}
	r := chi.NewRouter()
	r.Use(server.Authenticator(ds))
	r.Use(server.JWTRefresher)
	r.Use(server.UpdateLastAccessMiddleware(ds))
	r.With(adminOnlyMiddleware).Post("/upload", api.uploadAndScan)
	r.With(adminOnlyMiddleware).Post("/upload/", api.uploadAndScan)
	return r
}

func (api *uploadRouter) uploadAndScan(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

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

	root, err := os.OpenRoot(lib.Path)
	if err != nil {
		http.Error(w, "cannot open library: "+err.Error(), http.StatusInternalServerError)
		return
	}
	defer root.Close()

	targetDir := relFolder
	if targetDir == "" {
		targetDir = "."
	}
	if err := root.MkdirAll(targetDir, 0o755); err != nil {
		http.Error(w, "cannot create folder: "+err.Error(), http.StatusInternalServerError)
		return
	}
	targetPath := filepath.Join(relFolder, fileName)

	out, err := root.OpenFile(targetPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			http.Error(w, "file already exists", http.StatusConflict)
			return
		}
		http.Error(w, "cannot save file: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if _, err := io.Copy(out, file); err != nil {
		_ = out.Close()
		_ = root.Remove(targetPath)
		http.Error(w, "error writing file: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if err := out.Close(); err != nil {
		_ = root.Remove(targetPath)
		http.Error(w, "error closing file: "+err.Error(), http.StatusInternalServerError)
		return
	}

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

func sanitizeRelPath(p string) (string, error) {
	p = strings.TrimSpace(p)
	if p == "" {
		return "", nil
	}
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
