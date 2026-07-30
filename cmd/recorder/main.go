package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/pkg/browser"

	"video-recorder/internal/api"
	"video-recorder/internal/config"
	"video-recorder/internal/service"
	"video-recorder/internal/tray"
	"video-recorder/pkg/logger"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "video-recorder:", err)
		os.Exit(1)
	}
}

func run() error {
	configPath := flag.String("config", defaultConfigPath(), "path to the JSON configuration file")
	debug := flag.Bool("debug", false, "enable debug logging")
	flag.Parse()

	log := logger.New(*debug)
	log.Info("loading configuration", "path", *configPath)
	store, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	cfg := store.Get()

	root, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()

	buffer, err := service.NewCaptureBuffer(time.Duration(cfg.Capture.BufferSeconds) * time.Second)
	if err != nil {
		return err
	}
	recording, err := service.NewRecordingSession(buffer, time.Duration(cfg.Recording.MaxDurationMinutes)*time.Minute)
	if err != nil {
		_ = buffer.Close()
		return err
	}
	liveTranscoder := service.NewLiveTranscoder(store.Get, log)
	recording.SetTranscoder(liveTranscoder)
	hub := service.NewFrameHub()
	capture := service.NewCaptureService(recording, hub, log)
	if err := capture.Start(root, cfg.Capture); err != nil {
		recording.Close()
		_ = buffer.Close()
		return fmt.Errorf("start capture service: %w", err)
	}
	exporter := service.NewExporter(buffer, store.Get, log)
	listener, listenAddress, err := listenWithFallback(cfg.Server.Address, net.Listen)
	if err != nil {
		capture.Stop()
		recording.Close()
		exporter.Close()
		_ = buffer.Close()
		return fmt.Errorf("listen on %s: %w", cfg.Server.Address, err)
	}
	if listenAddress != cfg.Server.Address {
		log.Warn("configured port is occupied; using next available port", "configured", cfg.Server.Address, "listening", listenAddress)
	}
	handler := api.NewServer(store, capture, recording, buffer, hub, exporter, log).Handler()
	httpServer := &http.Server{
		Addr:              cfg.Server.Address,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      0,
		IdleTimeout:       60 * time.Second,
	}
	errCh := make(chan error, 1)
	go func() {
		log.Info("local video recorder started", "url", "http://"+listenAddress+"/console")
		if err := httpServer.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	appCtx, cancel := context.WithCancel(root)
	defer cancel()
	go func() {
		select {
		case err := <-errCh:
			log.Error("HTTP server stopped", "error", err)
			cancel()
		case <-appCtx.Done():
		}
	}()

	trayManager := tray.NewTrayManager()
	trayManager.Init(
		func() {
			_ = browser.OpenURL("http://" + listenAddress + "/console")
		},
		func() {
			current := store.Get()
			directory, err := filepath.Abs(current.Storage.Directory)
			if err != nil {
				log.Error("resolve storage directory", "error", err)
				return
			}
			_ = os.MkdirAll(directory, 0o755)
			folderURL := (&url.URL{Scheme: "file", Path: filepath.ToSlash(directory)}).String()
			if err := browser.OpenURL(folderURL); err != nil {
				log.Error("open storage directory", "error", err)
			}
		},
		cancel,
	)

	shutdownDone := make(chan struct{})
	go func() {
		defer close(shutdownDone)
		<-appCtx.Done()
		log.Info("shutting down video recorder")
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
		_ = httpServer.Shutdown(shutdownCtx)
		shutdownCancel()
		capture.Stop()
		recording.Close()
		exporter.Close()
		if err := buffer.Close(); err != nil {
			log.Error("close capture buffer", "error", err)
		}
		trayManager.Quit()
	}()

	trayManager.Run()
	cancel()
	<-shutdownDone
	return nil
}

func defaultConfigPath() string {
	developmentPath := filepath.Join("configs", "config.json")
	if _, err := os.Stat(developmentPath); err == nil {
		return developmentPath
	}
	directory, err := os.UserConfigDir()
	if err != nil {
		return developmentPath
	}
	return filepath.Join(directory, "video-recorder", "config.json")
}
