# Fork maintenance guide

This fork tracks `navidrome/navidrome` while keeping upstream-owned files unchanged. Fork behavior lives only in files
that do not exist upstream, so normal upstream merges have no overlapping edits to resolve.

## Fork-owned files

| File | Purpose |
| --- | --- |
| `.github/README.md` | Fork landing page shown by GitHub |
| `.github/workflows/fork-pipeline.yml` | Fork CI and GHCR publication |
| `.github/workflows/sync-upstream.yml` | Daily upstream synchronization |
| `server/nativeapi/fork_upload.go` | Music upload API (`POST /api/upload`) |
| `server/nativeapi/fork_upload_test.go` | Upload route and authentication tests |
| `cmd/fork_service_account.go` | `navidrome service-account` command for companion services |
| `cmd/fork_service_account_test.go` | Service account provisioning tests |
| `FORK.md` | This guide |

The upstream `.github/workflows/pipeline.yml` remains byte-for-byte unchanged and is disabled in this repository's
Actions settings. The fork pipeline uses a different filename, so both workflows can receive upstream changes without
conflicting.

## Upload integration

The fork adds `ServeHTTP` to the existing `nativeapi.Router` type from a separate file. It intercepts only `/upload`
inside upstream's existing `/api` mount and delegates every other request to the upstream handler. This avoids edits to
`cmd/root.go`, Wire injectors, and generated dependency-injection files.

The endpoint accepts `multipart/form-data` with a required `file` field and optional `libraryId` and `folder` fields.
It requires an admin JWT and runs a selective scan after saving the file.

## Service account

`navidrome service-account --username <name> --password-file <path>` creates an admin account for a companion
service (octo-fiesta in the navi-fiesta image) or rotates its password. Every run generates a new random 256-bit
password and writes it to the file with mode `0600`. The server never runs this on its own; nothing changes unless a
deployment invokes the command, before starting Navidrome.

The command records the account's ID in the `property` table and refuses to touch a same-named account it did not
create. While no admin exists it does nothing and removes the password file, so the web UI's first-admin setup still
runs.

## Automatic synchronization

The sync workflow calls GitHub's native `merge-upstream` API. This handles ordinary upstream files and workflow files
without a personal access token. GitHub's workflow token intentionally suppresses push-triggered workflows, so a
successful non-empty sync explicitly dispatches `fork-pipeline.yml`; a no-op sync starts no build.

Merge conflicts make the sync job fail without committing conflict markers. Resolve one locally with:

```sh
git fetch upstream
git merge upstream/master
```

## Adding fork behavior

1. Put it in a new file with a `fork_` prefix when sharing an upstream directory.
2. Use existing package hooks or methods before editing an upstream-owned file.
3. Add a focused test in another fork-owned file.
4. Update the table above.

An upstream API change can still break compilation without producing a merge conflict. The fork pipeline catches that
after every non-empty sync; fix it forward only in fork-owned files where possible.
