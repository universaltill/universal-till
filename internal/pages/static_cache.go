package pages

import "net/http"

// immutableAssetCacheControl is what a content-versioned static asset
// serves (ut-docs#2224, ADR-0098). Every asset tag in base.html carries
// `?v={{ assetv … }}`, so a changed file gets a new URL and a year-long
// immutable entry can never go stale; it removes the ~12 conditional
// (304) round trips the browser otherwise makes before every page paints.
// The same path requested WITHOUT a version keeps revalidating — that is
// today's effective behaviour, now stated explicitly rather than implied.
const immutableAssetCacheControl = "public, max-age=31536000, immutable"

// assetCacheControl wraps a static handler so that a successful response
// to a `?v=`-versioned request is immutable and everything else is
// `no-cache`. The header is decided at WriteHeader time, not before: a
// 404 for a versioned URL must never be cached for a year (the file may
// appear later — a disk override, a self-update). A handler that has
// already set Cache-Control itself wins.
func assetCacheControl(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(&cacheHeaderWriter{ResponseWriter: w, versioned: r.URL.Query().Get("v") != ""}, r)
	})
}

type cacheHeaderWriter struct {
	http.ResponseWriter
	versioned bool
	wrote     bool
}

func (c *cacheHeaderWriter) WriteHeader(status int) {
	if !c.wrote {
		c.wrote = true
		switch {
		case c.Header().Get("Cache-Control") != "":
			// The handler decided already (a plugin theme, whose file can
			// change without a new URL — see registerThemes).
		case c.versioned && (status == http.StatusOK || status == http.StatusNotModified):
			c.Header().Set("Cache-Control", immutableAssetCacheControl)
		default:
			c.Header().Set("Cache-Control", "no-cache")
		}
	}
	c.ResponseWriter.WriteHeader(status)
}

// Unwrap lets http.ResponseController reach the underlying writer.
func (c *cacheHeaderWriter) Unwrap() http.ResponseWriter { return c.ResponseWriter }

func (c *cacheHeaderWriter) Flush() {
	if !c.wrote {
		c.WriteHeader(http.StatusOK)
	}
	if f, ok := c.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (c *cacheHeaderWriter) Write(b []byte) (int, error) {
	if !c.wrote {
		c.WriteHeader(http.StatusOK)
	}
	return c.ResponseWriter.Write(b)
}
