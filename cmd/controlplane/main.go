package main

import (
	"context"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/rosswilson/harbor/internal/platform"
	"github.com/rosswilson/harbor/web"
)

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	store, err := platform.NewStore(env("STATE_PATH", "/data/state.json"))
	if err != nil {
		log.Fatal(err)
	}
	docker := platform.NewDocker(env("DOCKER_SOCKET", "/var/run/docker.sock"), env("DOCKER_NETWORK", "harbor-runtime"), "harbor-lab")
	xds := platform.NewXDS()
	controller := &platform.Controller{Store: store, Runtime: docker, XDS: xds}
	go func() {
		if err := xds.Serve(ctx, ":18000"); err != nil && ctx.Err() == nil {
			log.Fatal(err)
		}
	}()
	go controller.Run(ctx)
	mux := http.NewServeMux()
	mux.Handle("/api/", &platform.API{Controller: controller, Docker: docker, Token: env("HARBOR_TOKEN", "harbor-local-dev")})
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) })
	files, _ := fs.Sub(web.Files, ".")
	mux.Handle("/", http.FileServer(http.FS(files)))
	srv := &http.Server{Addr: ":9090", Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "same-origin")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'self'; script-src 'self'; connect-src 'self'; img-src 'self' data:; frame-ancestors 'none'")
		mux.ServeHTTP(w, r)
	}), ReadHeaderTimeout: 5 * time.Second}
	go func() {
		<-ctx.Done()
		shutdown, c := context.WithTimeout(context.Background(), 5*time.Second)
		defer c()
		srv.Shutdown(shutdown)
	}()
	log.Println("Harbor UI/API :9090; xDS gRPC :18000")
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}
