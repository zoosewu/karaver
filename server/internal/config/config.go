// Package config loads runtime settings from environment variables.
package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
)

type Config struct {
	Listen        string
	PublicURL     string // e.g. https://ktv.example.com, used for QR codes
	AdminPassword string
	MediaDir      string
	DataDir       string

	// Filename parsing: "artist-title", "title-artist" or "title".
	FilenameFormat    string
	FilenameSeparator string
	// Files whose stem ends with this suffix are treated as the original-vocal
	// companion of another song instead of a song of their own. Empty disables it.
	OriginalSuffix string
	Extensions     []string
}

func Load() (*Config, error) {
	c := &Config{
		Listen:            env("LISTEN", ":8080"),
		PublicURL:         strings.TrimRight(strings.TrimSpace(os.Getenv("PUBLIC_URL")), "/"),
		AdminPassword:     os.Getenv("ADMIN_PASSWORD"),
		MediaDir:          env("MEDIA_DIR", "/media"),
		DataDir:           env("DATA_DIR", "/data"),
		FilenameFormat:    env("FILENAME_FORMAT", "artist-title"),
		FilenameSeparator: os.Getenv("FILENAME_SEPARATOR"), // spaces are significant, so no trimming
		OriginalSuffix:    strings.TrimSpace(os.Getenv("ORIGINAL_SUFFIX")),
	}
	if c.FilenameSeparator == "" {
		c.FilenameSeparator = " - "
	}
	for _, e := range strings.Split(env("MEDIA_EXTENSIONS", ".mp4,.m4v,.webm"), ",") {
		e = strings.ToLower(strings.TrimSpace(e))
		if e == "" {
			continue
		}
		if !strings.HasPrefix(e, ".") {
			e = "." + e
		}
		c.Extensions = append(c.Extensions, e)
	}

	if c.PublicURL == "" {
		return nil, errors.New("PUBLIC_URL is required")
	}
	if u, err := url.Parse(c.PublicURL); err != nil || u.Scheme == "" || u.Host == "" {
		return nil, fmt.Errorf("PUBLIC_URL must be an absolute URL, got %q", c.PublicURL)
	}
	if c.AdminPassword == "" {
		return nil, errors.New("ADMIN_PASSWORD is required")
	}
	switch c.FilenameFormat {
	case "artist-title", "title-artist", "title":
	default:
		return nil, fmt.Errorf("FILENAME_FORMAT must be artist-title, title-artist or title, got %q", c.FilenameFormat)
	}
	if len(c.Extensions) == 0 {
		return nil, errors.New("MEDIA_EXTENSIONS is empty")
	}
	return c, nil
}

func env(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}
