package cmd

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/navidrome/navidrome/core/auth"
	"github.com/navidrome/navidrome/db"
	"github.com/navidrome/navidrome/log"
	"github.com/navidrome/navidrome/model"
	"github.com/navidrome/navidrome/model/request"
	"github.com/navidrome/navidrome/persistence"
	"github.com/spf13/cobra"
)

// The server never runs this: the account only exists where a deployment explicitly invokes the command
// (e.g. the navi-fiesta image, for octo-fiesta) before starting Navidrome.

const serviceAccountPropertyPrefix = "fork.serviceAccount."

var errNoAdminYet = errors.New("no admin user exists yet")

var (
	serviceAccountUsername     string
	serviceAccountPasswordFile string
)

func init() {
	serviceAccountCmd := &cobra.Command{
		Use:   "service-account",
		Short: "Create or rotate an admin service account",
		Long: `Create an admin account for a companion service, or rotate its password if it exists.

A new random password is generated on every run and written to --password-file (mode 0600), so it
never has to be configured by hand and a leaked password stops working on the next run. An existing
account is only taken over if this command created it.

When no admin exists yet, nothing is created (the web UI's first-admin setup must still run) and
the password file is removed.`,
		Run: func(cmd *cobra.Command, _ []string) {
			if err := runServiceAccount(cmd.Context()); err != nil {
				log.Fatal(cmd.Context(), "Could not provision service account", err)
			}
		},
	}
	serviceAccountCmd.Flags().StringVarP(&serviceAccountUsername, "username", "u", "", "service account username")
	serviceAccountCmd.Flags().StringVar(&serviceAccountPasswordFile, "password-file", "", "file to write the generated password to")
	_ = serviceAccountCmd.MarkFlagRequired("username")
	_ = serviceAccountCmd.MarkFlagRequired("password-file")
	rootCmd.AddCommand(serviceAccountCmd)
}

func runServiceAccount(ctx context.Context) error {
	defer db.Init(ctx)()
	ds := persistence.New(db.Db())

	password, err := provisionServiceAccount(ctx, ds, serviceAccountUsername)
	if errors.Is(err, errNoAdminYet) {
		log.Warn(ctx, "No admin user yet, skipping service account. Create your admin in the web UI, then restart",
			"username", serviceAccountUsername)
		if err := os.Remove(serviceAccountPasswordFile); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	if err != nil {
		return err
	}
	if err := writeSecretFile(serviceAccountPasswordFile, password); err != nil {
		return err
	}
	log.Info(ctx, "Service account ready", "username", serviceAccountUsername, "passwordFile", serviceAccountPasswordFile)
	return nil
}

// provisionServiceAccount creates the admin account, or resets its password if this command created it
// before, and returns the new password.
func provisionServiceAccount(ctx context.Context, ds model.DataStore, username string) (string, error) {
	if username == "" {
		return "", errors.New("username is required")
	}
	password, err := randomPassword()
	if err != nil {
		return "", err
	}

	err = ds.WithTx(func(tx model.DataStore) error {
		ctx := auth.WithAdminUser(ctx, tx)
		if admin, _ := request.UserFrom(ctx); admin.ID == "" {
			return errNoAdminYet
		}

		ownedID, err := tx.Property().DefaultGet(ctx, serviceAccountPropertyPrefix+username, "")
		if err != nil {
			return err
		}
		user, err := tx.User().FindByUsername(ctx, username)
		switch {
		case errors.Is(err, model.ErrNotFound):
			user = &model.User{UserName: username, Name: username + " (service account)"}
		case err != nil:
			return fmt.Errorf("looking up user %q: %w", username, err)
		case user.ID != ownedID:
			return fmt.Errorf("user %q exists but was not created by service-account, refusing to take it over", username)
		}

		user.IsAdmin = true
		user.NewPassword = password
		if err := tx.User().Put(ctx, user); err != nil {
			return err
		}
		return tx.Property().Put(ctx, serviceAccountPropertyPrefix+username, user.ID)
	}, "service account")
	if err != nil {
		return "", err
	}
	return password, nil
}

func randomPassword() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// writeSecretFile atomically replaces path with a 0600 file, so a reader never sees a partial password.
func writeSecretFile(path, secret string) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".service-account-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		return err
	}
	if _, err := f.WriteString(secret); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}
