package main

import (
	"bufio"
	"encoding/json"
	"html/template"
	"log/slog"
	"net/http"
	"os"
	"strings"

	"github.com/gorilla/mux"
)

func main() {
	slog.SetLogLoggerLevel(slog.LevelDebug)

	cfg := loadConfig()

	rttClient := NewRTTClient(cfg.dataURL, cfg.rttToken)

	tmpl, err := template.New("").Funcs(template.FuncMap{
		"nowTime": func() string { return nowHHMM() },
		"stationsJSON": func(ss []Station) template.JS {
			b, _ := json.Marshal(ss)
			return template.JS(b)
		},
	}).ParseGlob("templates/*.html")
	if err != nil {
		slog.Error("parse templates", "err", err)
		os.Exit(1)
	}

	srv := &server{rtt: rttClient, tmpl: tmpl}

	r := mux.NewRouter()
	r.HandleFunc("/", srv.handleIndex).Methods(http.MethodGet)
	r.HandleFunc("/departures", srv.handleDepartures).Methods(http.MethodGet)
	r.HandleFunc("/calling-points/{uid}/{date}", srv.handleCallingPoints).Methods(http.MethodGet)
	r.HandleFunc("/journey", srv.handleJourney).Methods(http.MethodGet)
	r.HandleFunc("/leg/{n}", srv.handleLeg).Methods(http.MethodGet)
	r.PathPrefix("/static/").Handler(
		http.StripPrefix("/static/", http.FileServer(http.Dir("static"))),
	)

	slog.Info("starting", "addr", cfg.addr)
	if err := http.ListenAndServe(cfg.addr, r); err != nil {
		slog.Error("server", "err", err)
		os.Exit(1)
	}
}

type config struct {
	addr     string
	rttToken string
	dataURL  string
}

func loadConfig() config {
	loadDotEnv(".env")
	port := os.Getenv("PORT")
	if port == "" {
		port = "8089"
	}
	dataURL := os.Getenv("DATA_URL")
	if dataURL == "" {
		dataURL = "https://data.rtt.io"
	}
	return config{
		addr:     ":" + port,
		rttToken: os.Getenv("RTT_TOKEN"),
		dataURL:  dataURL,
	}
}

func loadDotEnv(path string) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(v)
		if os.Getenv(k) == "" {
			os.Setenv(k, v)
		}
	}
}
