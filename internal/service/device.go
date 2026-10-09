package service

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// sessionOrigin is where a new session signs in from, shown in the signed-in
// devices table on Security (0.32.0).
type sessionOrigin struct {
	Device string // "Chrome on Windows"
	IP     string
}

type sessionOriginKey struct{}

// withSessionOrigin attaches the browser and address of a sign-in request, so
// the session it starts can record them.
func withSessionOrigin(ctx context.Context, o sessionOrigin) context.Context {
	return context.WithValue(ctx, sessionOriginKey{}, o)
}

func originFrom(ctx context.Context) sessionOrigin {
	if ctx == nil {
		return sessionOrigin{}
	}
	o, _ := ctx.Value(sessionOriginKey{}).(sessionOrigin)
	return o
}

// deviceLabel names a browser and system from a User-Agent header, such as
// "Chrome on Windows". Only the label is stored, never the header.
func deviceLabel(ua string) string {
	if len(ua) > 1024 {
		ua = ua[:1024]
	}
	has := func(s string) bool { return strings.Contains(ua, s) }
	browser := ""
	switch {
	case has("Edg/") || has("EdgA/") || has("EdgiOS/"):
		browser = "Edge"
	case has("OPR/") || has("OPiOS/"):
		browser = "Opera"
	case has("SamsungBrowser/"):
		browser = "Samsung Internet"
	case has("Firefox/") || has("FxiOS/"):
		browser = "Firefox"
	case has("CriOS/"):
		browser = "Chrome"
	case has("Chrome/") || has("Chromium/"):
		browser = "Chrome"
	case has("Safari/") && has("Version/"):
		browser = "Safari"
	}
	system := ""
	switch {
	case has("iPhone"):
		system = "iPhone"
	case has("iPad"):
		system = "iPad"
	case has("Android"):
		system = "Android"
	case has("Windows"):
		system = "Windows"
	case has("CrOS"):
		system = "ChromeOS"
	case has("Macintosh") || has("Mac OS X"):
		system = "Mac"
	case has("Linux"):
		system = "Linux"
	}
	switch {
	case browser != "" && system != "":
		return browser + " on " + system
	case browser != "":
		return browser
	case system != "":
		return "Browser on " + system
	}
	return ""
}

// sinceText says how long ago t was, for times within a day or so: "just
// now", "5 minutes ago", "3 hours ago". Relative times avoid time zones.
func sinceText(t, now time.Time) string {
	d := now.Sub(t)
	switch {
	case t.IsZero():
		return "unknown"
	case d < 2*time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%d minutes ago", int(d/time.Minute))
	case d < 2*time.Hour:
		return "an hour ago"
	case d < 48*time.Hour:
		return fmt.Sprintf("%d hours ago", int(d/time.Hour))
	}
	return fmt.Sprintf("%d days ago", int(d/(24*time.Hour)))
}
