package main

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"os"
	"time"
)

//go:embed page.html
var pageSource string

var page = template.Must(template.New("demo").Parse(pageSource))

func main() {
	hostname, _ := os.Hostname()
	http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "ready") })
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		rev := os.Getenv("REVISION")
		w.Header().Set("X-App-Revision", rev)
		if os.Getenv("FAIL") == "true" {
			http.Error(w, "Deliberately broken revision: readiness passes, application requests fail", http.StatusServiceUnavailable)
			return
		}
		data := map[string]string{"Revision": rev, "Tenant": r.Header.Get("X-Harbor-Tenant"), "Deployment": r.Header.Get("X-Harbor-Deployment"), "Hostname": hostname, "Cell": os.Getenv("CELL"), "Host": r.Host, "Time": time.Now().UTC().Format(time.RFC3339Nano)}
		if r.URL.Path == "/api/info" {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(data)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		page.Execute(w, data)
	})
	log.Fatal(http.ListenAndServe(":8080", nil))
}
