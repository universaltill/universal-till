package pages

import "net/http"

func registerStatic(mux *http.ServeMux) {
	mux.Handle("/public/", http.StripPrefix("/public/", http.FileServer(http.Dir("web/public"))))
}
