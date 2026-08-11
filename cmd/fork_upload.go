package cmd

import (
	"context"
	"net/http"

	"github.com/navidrome/navidrome/core/artwork"
	"github.com/navidrome/navidrome/core/metrics"
	"github.com/navidrome/navidrome/core/playlists"
	"github.com/navidrome/navidrome/db"
	"github.com/navidrome/navidrome/persistence"
	"github.com/navidrome/navidrome/scanner"
	"github.com/navidrome/navidrome/server/events"
	"github.com/navidrome/navidrome/server/nativeapi"
)

// CreateUploadRouter assembles the fork-specific music upload router
// (mounted in startServer at /api/upload).
//
// FORK POLICY (see FORK.md): this dependency graph is intentionally wired by
// hand instead of being added to cmd/wire_injectors.go. Keeping fork code out
// of the wire-generated files (cmd/wire_gen.go) means upstream wire
// regeneration never conflicts with it. The construction below mirrors what
// wire generates for the scanner dependency in CreateNativeAPIRouter.
func CreateUploadRouter(ctx context.Context) http.Handler {
	sqlDB := db.Db()
	dataStore := persistence.New(sqlDB)
	broker := events.GetBroker()
	prometheus := metrics.GetPrometheusInstance(dataStore)
	uploader := artwork.NewUploader(dataStore)
	pls := playlists.NewPlaylists(dataStore, uploader)
	sc := scanner.New(ctx, dataStore, broker, pls, prometheus)
	return nativeapi.NewUploadRouter(dataStore, sc)
}
