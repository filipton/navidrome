# Navidrome fork

This fork tracks [upstream Navidrome](https://github.com/navidrome/navidrome) and adds an admin-only
`POST /api/upload` endpoint for uploading music directly into a configured library.

```sh
curl -X POST https://your-navidrome.example/api/upload \
  -H "Authorization: Bearer $TOKEN" \
  -F "file=@album.flac" \
  -F "libraryId=1" \
  -F "folder=Artist/Album"
```

Container images are published at `ghcr.io/filipton/navidrome`. See [FORK.md](../FORK.md) for fork maintenance and
the [upstream README](../README.md) for Navidrome documentation.
