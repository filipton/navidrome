# Fork maintenance guide (filipton/navidrome)

This fork tracks `navidrome/navidrome` (remote `upstream`) and syncs `master`
daily via `.github/workflows/sync-upstream.yml`. The rules below keep those
syncs conflict-free. **Read this before adding any new feature to the fork.**

## The one rule

> Never modify an upstream-owned file unless there is no alternative.
> Conflicts only happen when **both** sides change the same file, so fork
> features live in fork-owned files.

### Fork-owned files (safe to change freely)

| File | Purpose |
| --- | --- |
| `server/nativeapi/upload.go` | Music upload API handler (`POST /api/upload`) |
| `server/nativeapi/upload_router_test.go` | Tests for the upload router |
| `cmd/fork_upload.go` | Hand-written DI for the upload router |
| `.github/workflows/sync-upstream.yml` | Daily upstream sync workflow |
| `.github/workflows/pipeline.yml` | Fork's own CI (pinned, see below) |
| `.gitattributes` | Merge policies (the `merge=ours` rules) |
| `FORK.md` | This file |

### The single upstream edit

`cmd/root.go` contains **one** fork line in `startServer()`:

```go
a.MountRouter("Upload API", consts.URLPathNativeAPI+"/upload", CreateUploadRouter(ctx))
```

Keep it the only change to upstream-owned Go files. If a sync ever conflicts
in `root.go`, re-add just this line.

## Why not wire?

The upload router is wired by hand in `cmd/fork_upload.go` instead of being
added to `cmd/wire_injectors.go`. Wire regeneration (which upstream does
regularly) would otherwise rewrite `cmd/wire_gen.go` with the fork's
injector missing, or conflict with it on every DI change.

## pipeline.yml policy: `merge=ours`

`.gitattributes` marks `.github/workflows/pipeline.yml` with `merge=ours`:
when a sync finds that both sides changed it, **our version wins
automatically** — no conflict, and upstream's pipeline changes are
intentionally discarded (we maintain our own trimmed pipeline).

The merge driver must be enabled for it to work:

```sh
git config merge.ours.driver true   # one-time, per clone
```

The sync workflow sets this itself; you only need it for manual merges.

## Merging upstream manually

```sh
git config merge.ours.driver true   # once per clone
git fetch upstream
git merge upstream/master
```

Expected result in the current layout: clean merge, or at worst a trivial
conflict in `cmd/root.go` (keep the fork line, take upstream's rest).

## Adding a new fork feature

1. Put the code in **new files** (never edit upstream files).
2. If it needs HTTP routes, build a standalone `http.Handler` and mount it
   with one extra `MountRouter` line in `cmd/root.go` (see upload API).
3. If it needs dependency injection, wire it by hand in a new `cmd/fork_*.go`
   file (mirror what `cmd/wire_gen.go` does for similar dependencies).
4. Add tests in new `*_test.go` files.
5. Update the file table above.
