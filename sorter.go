package main

import (
	"context"
	"fmt"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/gregdel/pushover"
	"github.com/hashicorp/go-multierror"
	iso8601 "github.com/peterhellberg/duration"
	"github.com/sirupsen/logrus"
	"google.golang.org/api/oauth2/v2"
	"google.golang.org/api/youtube/v3"
)

const maxNbConcurrentGoroutines int = 20
const maxVideoItemsWhenList int = 50

type PlaylistID string

type Sorter struct {
	ticker         *time.Ticker
	youtubeService *youtube.Service
	oauth2Service  *oauth2.Service
	logger         logrus.FieldLogger

	playlistID PlaylistID
}

func NewSorter(
	interval time.Duration,
	youtubeService *youtube.Service,
	oauth2Service *oauth2.Service,
	logger logrus.FieldLogger,
	playlistID PlaylistID,
) *Sorter {
	return &Sorter{
		ticker:         time.NewTicker(interval),
		youtubeService: youtubeService,
		oauth2Service:  oauth2Service,
		logger:         logger,
		playlistID:     playlistID,
	}
}

type Video struct {
	ID             string
	Duration       time.Duration
	Position       int64
	YoutubeVideoID string
}

// Videos is a map of videos items.
// Key is the video ID
// Use to retrieve video details
type Videos map[string]*Video

func (v Videos) ToVideoList() VideoListSortByDuration {
	r := make(VideoListSortByDuration, len(v))
	var i uint
	for _, vv := range v {
		r[i] = vv
		i++
	}
	return r
}

// VideoListSortByDuration is an array of videos
// Implements sort.Interface
type VideoListSortByDuration []*Video

func (v VideoListSortByDuration) Len() int {
	return len(v)
}

func (v VideoListSortByDuration) Less(i, j int) bool {
	return v[i].Duration < v[j].Duration
}

func (v VideoListSortByDuration) Swap(i, j int) {
	v[i], v[j] = v[j], v[i]
}

// Keys returns Video's keys, an array of videos IDs
func (v Videos) Keys() []string {
	ids := make([]string, len(v))
	var i uint
	for k := range v {
		ids[i] = k
		i++
	}
	return ids
}

func (s *Sorter) Run(ctx context.Context, reverse bool, dryRun bool) {
	go func() {
		<-ctx.Done()
		s.logger.Printf("%s: wait for current sorting before exit", ctx.Err())
	}()

	for {
		select {
		case <-s.ticker.C:
			s.run(ctx, reverse, dryRun)
		case <-ctx.Done():
			return
		}
	}
}

func (s *Sorter) run(ctx context.Context, reverse bool, dryRun bool) {
	user, err := s.oauth2Service.Userinfo.Get().Context(ctx).Do()
	if err != nil {
		s.logger.WithError(err).Errorln("cannot get user info")
	}

	s.logger.Debugf("connected has %s", user.Email)
	entry := s.logger.
		WithField("playlist", s.playlistID).
		WithField("reverse", reverse).
		WithField("dry-run", dryRun)

	entry.Debugln("listing playlist items")
	videos := make(Videos, 0)
	if err := s.youtubeService.PlaylistItems.
		List([]string{"id", "snippet"}).
		MaxResults(50).
		PlaylistId(string(s.playlistID)).
		Pages(ctx, func(itemListResponse *youtube.PlaylistItemListResponse) error {
			for _, item := range itemListResponse.Items {
				videos[item.Snippet.ResourceId.VideoId] = &Video{
					ID:             item.Snippet.ResourceId.VideoId,
					Duration:       0,
					Position:       item.Snippet.Position,
					YoutubeVideoID: item.Id,
				}
			}
			return nil
		}); err != nil {
		entry.WithError(err).Errorln("fail to list playlist items")
		s.notifyError(err)
		return
	}

	entry.Printf("found %d items in the playlist", len(videos))

	// list videos only support at 50 batch size
	chunks := chunkVideoIDs(videos.Keys(), maxVideoItemsWhenList)
	for i, chunk := range chunks {
		entry.WithField("chunk", i).Debugln("getting videos details")
		if err := s.youtubeService.Videos.
			List([]string{"id", "contentDetails"}).
			Id(chunk...).
			MaxResults(50).
			Pages(ctx, func(listResponse *youtube.VideoListResponse) error {
				for _, video := range listResponse.Items {
					videos[video.Id].Duration = s.parseVideoDuration(video.ContentDetails.Duration)
				}
				return nil
			}); err != nil {
			entry.WithError(err).Errorln("fail to get video details")
			s.notifyError(err)
			return
		}
	}

	// sort by wanted position
	videoListSortedByDuration := videos.ToVideoList()

	var data sort.Interface = videoListSortedByDuration
	if reverse {
		entry.Println("sorting in reverse order")
		data = sort.Reverse(videoListSortedByDuration)
	}
	sort.Sort(data)

	// update videos position
	var wg sync.WaitGroup
	concurrentGoroutines := make(chan struct{}, maxNbConcurrentGoroutines)

	// collect errors
	var result *multierror.Error
	var errCh = make(chan error)
	go func() {
		for err := range errCh {
			result = multierror.Append(result, err)
		}
	}()

	for i, video := range videoListSortedByDuration {
		videoEntry := entry.WithField("video", video.ID)
		// do not update if already in wanted position
		if video.Position == int64(i) {
			videoEntry.Debugln("is already at the wanted position")
			continue
		}

		if dryRun {
			videoEntry.Printf("[dry run] moving video at position [%d], was at position [%d]", i, video.Position)
			continue
		}

		concurrentGoroutines <- struct{}{}
		video, i := video, i
		wg.Add(1)
		go func() {
			defer wg.Done()
			videoEntry.Printf("moving video to position [%d]", i)
			if err := s.updateVideoPosition(ctx, video, int64(i)); err != nil {
				videoEntry.WithError(err).Errorln("cannot update video in the playlist")
				errCh <- err
			}

			<-concurrentGoroutines
		}()
	}

	wg.Wait()
	close(concurrentGoroutines)
	close(errCh)

	if err := result.ErrorOrNil(); err != nil {
		s.notifyError(err)
		return
	}

	s.notify("Playlist successfully sorted!")
}

func (s *Sorter) parseVideoDuration(durationString string) time.Duration {
	d, err := iso8601.Parse(durationString)
	if err != nil {
		s.logger.WithError(err).Warnln("cannot parse duration string")
		return 0
	}

	return d
}

func (s *Sorter) updateVideoPosition(ctx context.Context, video *Video, wantedPosition int64) error {
	_, err := s.youtubeService.PlaylistItems.Update([]string{"snippet"}, &youtube.PlaylistItem{
		Id: video.YoutubeVideoID,
		Snippet: &youtube.PlaylistItemSnippet{
			PlaylistId: string(s.playlistID),
			ResourceId: &youtube.ResourceId{
				Kind:    "youtube#video",
				VideoId: video.ID,
			},
			Position: wantedPosition,
		},
	}).Context(ctx).Do()
	return err
}

func chunkVideoIDs(slice []string, chunkSize int) [][]string {
	var chunks [][]string
	for {
		if len(slice) == 0 {
			break
		}

		// necessary check to avoid slicing beyond
		// slice capacity
		if len(slice) < chunkSize {
			chunkSize = len(slice)
		}

		chunks = append(chunks, slice[0:chunkSize])
		slice = slice[chunkSize:]
	}

	return chunks
}

func (s *Sorter) notify(data string) {
	if os.Getenv("PUSHOVER_TOKEN") == "" || os.Getenv("PUSHOVER_KEY") == "" {
		s.logger.Debugln("pushover disabled")
		return
	}

	service := pushover.New(os.Getenv("PUSHOVER_TOKEN"))
	recipient := pushover.NewRecipient(os.Getenv("PUSHOVER_KEY"))

	message := &pushover.Message{
		Title:   "youtube-sorter",
		Message: data,
		URL:     fmt.Sprintf("https://www.youtube.com/playlist?list=%s", s.playlistID),
	}

	if _, err := service.SendMessage(message, recipient); err != nil {
		s.logger.WithError(err).Errorln("fail to send notification")
	}
}

func (s *Sorter) notifyError(err error) {
	var message string
	if err != nil {
		message = "Error(s) occurred during last sort:\n"
		message += err.Error()
	}

	s.notify(message)
}
