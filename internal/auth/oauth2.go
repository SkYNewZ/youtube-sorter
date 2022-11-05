package auth

import (
	"context"
	"encoding/gob"
	"fmt"
	"hash/fnv"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sirupsen/logrus"
	"golang.org/x/oauth2"
)

const defaultPort int = 8080

// CacheDirectory wrap string to be used with wire
type CacheDirectory string

// NewTokenSource config oauth2 flow from given config
func NewTokenSource(ctx context.Context, config *oauth2.Config, logger logrus.FieldLogger, cacheDirectory CacheDirectory) oauth2.TokenSource {
	cacheFile := tokenCacheFile(config, logger, string(cacheDirectory))
	token, err := tokenFromFile(cacheFile)
	if err == nil {
		logger.Debugf("using cached token from %q", cacheFile)
		return config.TokenSource(ctx, token)
	}

	token = tokenFromWeb(ctx, config, logger)
	if err := saveToken(cacheFile, token); err != nil {
		logger.WithError(err).Warnln("failed to cache oauth token")
	}

	return config.TokenSource(ctx, token)
}

func saveToken(file string, token *oauth2.Token) error {
	f, err := os.Create(file)
	if err != nil {
		return nil
	}

	defer f.Close()
	return gob.NewEncoder(f).Encode(token)
}

func tokenFromWeb(ctx context.Context, config *oauth2.Config, logger logrus.FieldLogger) *oauth2.Token {
	ch := make(chan string)
	randState := fmt.Sprintf("st%d", time.Now().UnixNano())

	ts := httptest.NewUnstartedServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		if req.URL.Path == "/favicon.ico" {
			http.Error(rw, "", 404)
			return
		}

		if v := req.FormValue("state"); v != randState {
			logger.Warnf("state doesn't match, want [%s] got [%s]", randState, v)
			http.Error(rw, "", 500)
			return
		}

		if code := req.FormValue("code"); code != "" {
			_, _ = fmt.Fprintf(rw, "<h1>Success</h1>Authorized.")
			rw.(http.Flusher).Flush()
			ch <- code
			return
		}

		logger.Warnln("no code received from the request")
		http.Error(rw, "", 500)
	}))

	// configure listener to accept connection from 0.0.0.0
	listener, err := net.Listen("tcp", fmt.Sprintf("0.0.0.0:%d", defaultPort))
	if err != nil {
		logger.WithError(err).Fatalln("cannot start load web server")
		return nil
	}

	ts.Listener = listener

	logger.Debugln("starting local http server")
	ts.Start()
	defer func() {
		logger.Debugln("closing local http server")
		ts.Close()
	}()

	config.RedirectURL = os.Getenv("REDIRECT_URI")
	authURL := config.AuthCodeURL(randState, oauth2.AccessTypeOffline)
	logger.Printf("authorize this app at: %s", authURL)

	code := <-ch
	logger.Debugf("got code: %s", code)

	token, err := config.Exchange(ctx, code)
	if err != nil {
		logger.WithError(err).Fatalln("cannot exchange token")
		return nil
	}

	return token
}

func tokenFromFile(file string) (*oauth2.Token, error) {
	f, err := os.Open(file)
	if err != nil {
		return nil, err
	}

	t := new(oauth2.Token)
	err = gob.NewDecoder(f).Decode(t)
	return t, err
}

func tokenCacheFile(config *oauth2.Config, logger logrus.FieldLogger, cacheDirectory string) string {
	hash := fnv.New32a()
	hash.Write([]byte(config.ClientID))
	hash.Write([]byte(config.ClientSecret))
	hash.Write([]byte(strings.Join(config.Scopes, " ")))
	fn := fmt.Sprintf("youtube-sorter-tok%v", hash.Sum32())

	// if cache directory is specified, use it
	if cacheDirectory != "" {
		return filepath.Join(cacheDirectory, url.QueryEscape(fn))
	}

	return filepath.Join(osUserCacheDir(logger), url.QueryEscape(fn))
}

func osUserCacheDir(logger logrus.FieldLogger) string {
	dir, err := os.UserCacheDir()
	if err != nil {
		logger.WithError(err).Warnln("cannot get user cache dir")
		return "."
	}

	return dir
}
