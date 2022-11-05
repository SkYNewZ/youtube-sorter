//go:build wireinject

package main

import (
	"context"
	"os"
	"time"

	"github.com/SkYNewZ/youtube-sorter/internal/auth"
	"github.com/google/wire"
	"github.com/sirupsen/logrus"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	oauth2api "google.golang.org/api/oauth2/v2"
	"google.golang.org/api/option"
	"google.golang.org/api/youtube/v3"
)

var youtubeServiceProviderSet = wire.NewSet(youtube.NewService)

var oauth2ServiceProviderSet = wire.NewSet(oauth2api.NewService)

var tokenSourceProviderSet = wire.NewSet(
	os.ReadFile,
	provideConfig,
	auth.NewTokenSource,
)

func provideOptions(tokenSource oauth2.TokenSource) []option.ClientOption {
	return []option.ClientOption{
		option.WithTokenSource(tokenSource),
	}
}

func provideConfig(jsonKey []byte) (*oauth2.Config, error) {
	return google.ConfigFromJSON(jsonKey, youtube.YoutubeScope, oauth2api.UserinfoEmailScope)
}

func initializeSorter(
	ctx context.Context,
	interval time.Duration,
	logger logrus.FieldLogger,
	filename string,
	playlistID PlaylistID,
	cacheDirectory auth.CacheDirectory,
) (*Sorter, error) {
	panic(wire.Build(
		tokenSourceProviderSet,
		provideOptions,
		youtubeServiceProviderSet,
		oauth2ServiceProviderSet,
		NewSorter,
	))
}
