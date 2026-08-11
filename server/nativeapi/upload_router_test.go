package nativeapi

import (
	"context"
	"net/http"
	"net/http/httptest"

	"github.com/go-chi/chi/v5"
	"github.com/navidrome/navidrome/conf/configtest"
	"github.com/navidrome/navidrome/core/auth"
	"github.com/navidrome/navidrome/model"
	"github.com/navidrome/navidrome/server"
	"github.com/navidrome/navidrome/tests"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// FORK test: verifies the standalone upload router is reachable at the same
// path as before the refactor (/api/upload) and that its auth chain
// (Authenticator -> adminOnlyMiddleware) behaves correctly.
var _ = Describe("Upload API (fork)", func() {
	var ds *tests.MockDataStore
	var router http.Handler
	var adminToken, userToken string

	BeforeEach(func() {
		DeferCleanup(configtest.SetupConfig())
		ds = &tests.MockDataStore{}
		auth.Init(ds)

		root := chi.NewRouter()
		// Mounted exactly as in cmd/root.go
		root.Mount("/api/upload", NewUploadRouter(ds, nil))
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

	It("rejects unauthenticated requests", func() {
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
		// Empty (non-multipart) body -> the handler itself rejects the form.
		// Any of these proves the request reached uploadAndScan instead of
		// being a 404 (bad mount) or 401/403 (auth chain broken).
		Expect(w.Code).To(BeElementOf(http.StatusBadRequest, http.StatusUnprocessableEntity))
	})
})
