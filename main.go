package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"time"

	"github.com/SkYNewZ/youtube-sorter/internal/auth"
	"github.com/SkYNewZ/youtube-sorter/internal/logger"
	flag "github.com/spf13/pflag"
)

func main() {
	var (
		interval       = flag.Duration("interval", time.Hour*12, "Run sort process at this interval")
		playlistID     = flag.String("playlist", "", "Which playlist to sort")
		secretFilePath = flag.String("client-credentials-file", "client_credentials.json", "Google Developers Console client_credentials.json file")
		reverse        = flag.Bool("reverse", false, "Sort videos by duration in reverse order")

		dryRun   = flag.Bool("dry-run", false, "Only show sort without sorting playlist")
		cacheDir = flag.String("cache-dir", "", "Custom cache directory")
		logLevel = new(logger.Level)
	)
	flag.Var(logLevel, "log-level", "Log level")
	flag.Parse()

	required := []string{"playlist"}
	seen := make(map[string]bool)
	flag.Visit(func(f *flag.Flag) { seen[f.Name] = true })
	for _, req := range required {
		if !seen[req] {
			_, _ = fmt.Fprintf(os.Stderr, "missing required --%s flag\n", req)
			os.Exit(2)
		}
	}

	log := logger.New(*logLevel)
	sorter, err := initializeSorter(
		context.Background(),
		*interval,
		log,
		*secretFilePath,
		PlaylistID(*playlistID),
		auth.CacheDirectory(*cacheDir),
	)
	if err != nil {
		log.WithError(err).Fatalln("fail to initialize app")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	sorter.Run(ctx, *reverse, *dryRun)
}
