package cmd

import (
	"context"
	"os"
	"path/filepath"

	"github.com/navidrome/navidrome/model"
	"github.com/navidrome/navidrome/tests"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("provisionServiceAccount", func() {
	var ds *tests.MockDataStore
	var users *tests.MockedUserRepo
	var props *tests.MockedPropertyRepo
	ctx := context.Background()

	BeforeEach(func() {
		users = tests.CreateMockUserRepo()
		props = &tests.MockedPropertyRepo{}
		ds = &tests.MockDataStore{MockedUser: users, MockedProperty: props}
	})

	addAdmin := func() {
		Expect(users.Put(ctx, &model.User{ID: "admin-id", UserName: "admin", IsAdmin: true})).To(Succeed())
	}

	It("does nothing before the first admin exists", func() {
		_, err := provisionServiceAccount(ctx, ds, "octo-fiesta")
		Expect(err).To(MatchError(errNoAdminYet))
		Expect(users.CountAll(ctx)).To(BeZero())
	})

	It("creates an admin account with a random password", func() {
		addAdmin()
		password, err := provisionServiceAccount(ctx, ds, "octo-fiesta")
		Expect(err).ToNot(HaveOccurred())
		Expect(len(password)).To(BeNumerically(">=", 40))

		user, err := users.FindByUsername(ctx, "octo-fiesta")
		Expect(err).ToNot(HaveOccurred())
		Expect(user.IsAdmin).To(BeTrue())
		Expect(user.Password).To(Equal(password))
		Expect(props.Get(ctx, serviceAccountPropertyPrefix+"octo-fiesta")).To(Equal(user.ID))
	})

	It("rotates the password of an account it created", func() {
		addAdmin()
		first, err := provisionServiceAccount(ctx, ds, "octo-fiesta")
		Expect(err).ToNot(HaveOccurred())
		second, err := provisionServiceAccount(ctx, ds, "octo-fiesta")
		Expect(err).ToNot(HaveOccurred())

		Expect(second).ToNot(Equal(first))
		user, _ := users.FindByUsername(ctx, "octo-fiesta")
		Expect(user.Password).To(Equal(second))
	})

	It("refuses to take over an existing account it did not create", func() {
		addAdmin()
		Expect(users.Put(ctx, &model.User{ID: "human", UserName: "octo-fiesta", NewPassword: "mine"})).To(Succeed())

		_, err := provisionServiceAccount(ctx, ds, "octo-fiesta")
		Expect(err).To(MatchError(ContainSubstring("refusing to take it over")))
		user, _ := users.FindByUsername(ctx, "octo-fiesta")
		Expect(user.IsAdmin).To(BeFalse())
		Expect(user.Password).To(Equal("mine"))
	})
})

var _ = Describe("writeSecretFile", func() {
	It("writes the secret readable by the owner only", func() {
		path := filepath.Join(GinkgoT().TempDir(), "pw")
		// Deliberately world-readable, to check that it gets tightened.
		Expect(os.WriteFile(path, []byte("old"), 0o644)).To(Succeed()) //nolint:gosec

		Expect(writeSecretFile(path, "s3cret")).To(Succeed())

		Expect(os.ReadFile(path)).To(Equal([]byte("s3cret")))
		info, err := os.Stat(path)
		Expect(err).ToNot(HaveOccurred())
		Expect(info.Mode().Perm()).To(Equal(os.FileMode(0o600)))
	})
})
