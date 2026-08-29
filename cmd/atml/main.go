package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"atml/internal/client"
	clientconfig "atml/internal/config"
	"atml/internal/hosting"
)

const version = "0.1.0"

func main() {
	if err := run(os.Args[1:]); err != nil {
		if !errors.Is(err, flag.ErrHelp) {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
	}
}

func run(args []string) error {
	if len(args) == 0 {
		printUsage()
		return flag.ErrHelp
	}
	switch args[0] {
	case "configure":
		return runConfigure(args[1:])
	case "publish":
		return runPublish(args[1:])
	case "serve":
		return runServe(args[1:])
	case "version", "--version", "-version":
		fmt.Println("atml", version)
		return nil
	case "help", "--help", "-h":
		printUsage()
		return nil
	default:
		printUsage()
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func printUsage() {
	fmt.Fprintln(os.Stderr, `ATML publishes PIN-protected static HTML sites.

Usage:
  atml configure --server URL --token TOKEN
  atml publish [--title TITLE] [--json] PATH
  atml serve [options]
  atml version

Run "atml <command> -h" for command-specific options.`)
}

func runConfigure(args []string) error {
	flags := flag.NewFlagSet("configure", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	server := flags.String("server", os.Getenv("ATML_SERVER"), "ATML service base URL")
	token := flags.String("token", firstNonEmpty(os.Getenv("ATML_TOKEN"), os.Getenv("ATML_API_TOKEN")), "publish API token")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("configure does not accept positional arguments")
	}
	path, err := clientconfig.Save(clientconfig.Config{Server: *server, Token: *token})
	if err != nil {
		return err
	}
	fmt.Printf("Configured ATML server %s in %s\n", strings.TrimRight(*server, "/"), path)
	return nil
}

func runPublish(args []string) error {
	flags := flag.NewFlagSet("publish", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	title := flags.String("title", "", "display title shown on the PIN screen")
	jsonOutput := flags.Bool("json", false, "emit machine-readable JSON")
	serverOverride := flags.String("server", os.Getenv("ATML_SERVER"), "override configured server URL")
	tokenOverride := flags.String("token", os.Getenv("ATML_TOKEN"), "override configured publish token")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 1 {
		return errors.New("publish requires exactly one HTML file or directory")
	}
	server, token, err := resolveClientCredentials(*serverOverride, *tokenOverride)
	if err != nil {
		return err
	}
	source := flags.Arg(0)
	if *title == "" {
		*title = defaultTitle(source)
	}
	archive, err := client.CreateArchive(source)
	if err != nil {
		return err
	}
	defer archive.Remove()
	result, err := client.Publish(server, token, *title, archive)
	if err != nil {
		return err
	}
	if *jsonOutput {
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(result)
	}
	fmt.Printf("Published %d files (%d bytes)\nURL: %s\nPIN: %s\n", result.Files, result.Bytes, result.URL, result.PIN)
	return nil
}

func runServe(args []string) error {
	flags := flag.NewFlagSet("serve", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	addr := flags.String("addr", envOr("ATML_ADDR", ":8080"), "HTTP listen address")
	dataDir := flags.String("data", envOr("ATML_DATA_DIR", "./atml-data"), "persistent data directory")
	token := flags.String("token", firstNonEmpty(os.Getenv("ATML_API_TOKEN"), os.Getenv("ATML_TOKEN")), "required publish API token")
	publicURL := flags.String("public-url", os.Getenv("ATML_PUBLIC_URL"), "externally visible base URL")
	maxUpload := flags.Int64("max-upload-bytes", 25<<20, "maximum compressed upload size")
	maxSite := flags.Int64("max-site-bytes", 100<<20, "maximum expanded site size")
	maxFiles := flags.Int("max-files", 500, "maximum files per site")
	trustProxy := flags.Bool("trust-proxy", envBool("ATML_TRUST_PROXY"), "trust X-Forwarded-For/X-Real-IP for PIN rate limits")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("serve does not accept positional arguments")
	}
	logger := log.New(os.Stderr, "atml: ", log.LstdFlags)
	handler, err := hosting.New(hosting.Config{
		DataDir:        *dataDir,
		APIToken:       *token,
		PublicURL:      *publicURL,
		MaxUploadBytes: *maxUpload,
		MaxSiteBytes:   *maxSite,
		MaxFiles:       *maxFiles,
		TrustProxy:     *trustProxy,
		Logger:         logger,
	})
	if err != nil {
		return err
	}
	httpServer := &http.Server{
		Addr:              *addr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       2 * time.Minute,
		WriteTimeout:      2 * time.Minute,
		IdleTimeout:       2 * time.Minute,
		MaxHeaderBytes:    32 << 10,
	}

	stopContext, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	errCh := make(chan error, 1)
	go func() {
		logger.Printf("listening on %s; data directory %s", *addr, *dataDir)
		errCh <- httpServer.ListenAndServe()
	}()
	select {
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-stopContext.Done():
		logger.Print("shutting down")
		shutdownContext, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return httpServer.Shutdown(shutdownContext)
	}
}

func resolveClientCredentials(serverOverride, tokenOverride string) (string, string, error) {
	server := strings.TrimSpace(serverOverride)
	token := strings.TrimSpace(tokenOverride)
	if server == "" || token == "" {
		cfg, err := clientconfig.Load()
		if err != nil {
			return "", "", err
		}
		if server == "" {
			server = cfg.Server
		}
		if token == "" {
			token = cfg.Token
		}
	}
	normalized, err := clientconfig.NormalizeServer(server)
	if err != nil {
		return "", "", err
	}
	if token == "" {
		return "", "", errors.New("publish token cannot be empty")
	}
	return normalized, token, nil
}

func defaultTitle(source string) string {
	clean := filepath.Clean(source)
	if clean == "." {
		if cwd, err := os.Getwd(); err == nil {
			return filepath.Base(cwd)
		}
	}
	base := filepath.Base(clean)
	if extension := filepath.Ext(base); extension != "" {
		base = strings.TrimSuffix(base, extension)
	}
	if base == "" || base == "." || base == string(filepath.Separator) {
		return "Published site"
	}
	return base
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func envOr(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

func envBool(name string) bool {
	value := strings.ToLower(strings.TrimSpace(os.Getenv(name)))
	return value == "1" || value == "true" || value == "yes" || value == "on"
}
