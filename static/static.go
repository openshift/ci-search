package static

import (
	"embed"
	"net/http"
)

// staticContent holds our static web server content.
//go:embed *
var staticContent embed.FS

// Handler returns a file server with the contents of files in the `static` directory.
// `prefix` is used for nested static files ex: /static/
// An empty string can be passed in if contents are desired to be served at the root of the path.
func Handler(prefix string) http.Handler {
	return http.StripPrefix(prefix, http.FileServer(http.FS(staticContent)))
}
