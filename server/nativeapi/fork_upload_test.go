package nativeapi

import (
	"bytes"
	"context"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"

	"github.com/go-chi/chi/v5"
	"github.com/navidrome/navidrome/conf/configtest"
	"github.com/navidrome/navidrome/core/auth"
	"github.com/navidrome/navidrome/model"
	"github.com/navidrome/navidrome/server"
	"github.com/navidrome/navidrome/tests"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Upload API (fork)", func() {
	var ds *tests.MockDataStore
	var router http.Handler
	var adminToken, userToken string

	BeforeEach(func() {
		DeferCleanup(configtest.SetupConfig())
		ds = &tests.MockDataStore{}
		auth.Init(ds)

		fallback := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTeapot) })
		root := chi.NewRouter()
		root.Mount("/api", &Router{Handler: fallback, ds: ds})
		router = server.JWTVerifier(root)

		adminUser := model.User{ID: "admin-1", UserName: "admin", IsAdmin: true, NewPassword: "adminpass"}
		regularUser := model.User{ID: "user-1", UserName: "regular", IsAdmin: false, NewPassword: "userpass"}
		Expect(ds.User(context.TODO()).Put(&adminUser)).To(Succeed())
		Expect(ds.User(context.TODO()).Put(&regularUser)).To(Succeed())

		var err error
		adminToken, err = auth.CreateToken(&adminUser)
		Expect(err).ToNot(HaveOccurred())
		userToken, err = auth.CreateToken(&regularUser)
		Expect(err).ToNot(HaveOccurred())
	})

	It("preserves upstream routes", func() {
		req := createUnauthenticatedRequest("GET", "/api/song", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		Expect(w.Code).To(Equal(http.StatusTeapot))
	})

	It("rejects unauthenticated uploads", func() {
		req := createUnauthenticatedRequest("POST", "/api/upload", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		Expect(w.Code).To(Equal(http.StatusUnauthorized))
	})

	It("rejects non-admin users", func() {
		req := createAuthenticatedRequest("POST", "/api/upload", nil, userToken)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		Expect(w.Code).To(Equal(http.StatusForbidden))
	})

	It("routes admin requests to the upload handler", func() {
		req := createAuthenticatedRequest("POST", "/api/upload", nil, adminToken)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		Expect(w.Code).To(Equal(http.StatusBadRequest))
	})

	It("does not overwrite an existing file", func() {
		libraryPath := GinkgoT().TempDir()
		library := model.Library{ID: 1, Name: "Music", Path: libraryPath}
		Expect(ds.Library(context.TODO()).Put(&library)).To(Succeed())
		target := filepath.Join(libraryPath, "song.mp3")
		Expect(os.WriteFile(target, []byte("original"), 0o600)).To(Succeed())

		var body bytes.Buffer
		form := multipart.NewWriter(&body)
		file, err := form.CreateFormFile("file", "song.mp3")
		Expect(err).ToNot(HaveOccurred())
		_, err = file.Write([]byte("replacement"))
		Expect(err).ToNot(HaveOccurred())
		Expect(form.Close()).To(Succeed())

		req := createAuthenticatedRequest("POST", "/upload", &body, adminToken)
		req.Header.Set("Content-Type", form.FormDataContentType())
		w := httptest.NewRecorder()
		server.JWTVerifier(newUploadRouter(ds, tests.NewMockScanner())).ServeHTTP(w, req)

		Expect(w.Code).To(Equal(http.StatusConflict))
		content, err := os.ReadFile(target)
		Expect(err).ToNot(HaveOccurred())
		Expect(content).To(Equal([]byte("original")))
	})
})
