// Command cake-proxy relays OpenAI Chat Completions and Responses API requests
// to one upstream provider and records every exchange for inspection in a
// local HTML interface.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/travisennis/cake-proxy/internal/capture"
	"github.com/travisennis/cake-proxy/internal/proxy"
	"github.com/travisennis/cake-proxy/internal/version"
	"github.com/travisennis/cake-proxy/internal/web"
)

const defaultListen = "127.0.0.1:8787"

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if len(args) > 0 {
		switch args[0] {
		case "version", "-version", "--version", "-V":
			fmt.Println(version.String())
			return 0
		case "help", "-h", "--help":
			usage(os.Stdout)
			return 0
		}
	}

	flags := flag.NewFlagSet("cake-proxy", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	flags.Usage = func() { usage(os.Stderr) }
	listen := flags.String("listen", envOr("CAKE_PROXY_LISTEN", defaultListen), "listen address (host:port)")
	upstream := flags.String("upstream", os.Getenv("CAKE_PROXY_UPSTREAM"), "upstream base URL, for example https://opencode.ai/inference/go/openai/v1")
	keyEnv := flags.String("upstream-key-env", "", "environment variable holding the upstream credential; replaces the client's credential")
	keyHeader := flags.String("upstream-key-header", "Authorization", "header that carries the upstream credential")
	dataDir := flags.String("data-dir", envOr("CAKE_PROXY_DATA_DIR", defaultDataDir()), "capture store directory; empty keeps records in memory")
	keep := flags.Int("keep", 200, "number of finished exchanges to retain (0 keeps all)")
	maxBodyMB := flags.Int64("max-body-mb", 64, "largest request or captured response body, in MiB")
	allowRemote := flags.Bool("allow-remote", false, "allow binding a non-loopback address (the UI has no authentication)")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() > 0 {
		fmt.Fprintf(os.Stderr, "cake-proxy: unexpected argument %q\n\n", flags.Arg(0))
		usage(os.Stderr)
		return 2
	}
	if strings.TrimSpace(*upstream) == "" {
		fmt.Fprintln(os.Stderr, "cake-proxy: -upstream is required")
		usage(os.Stderr)
		return 2
	}
	if *maxBodyMB <= 0 {
		fmt.Fprintln(os.Stderr, "cake-proxy: -max-body-mb must be positive")
		return 2
	}
	if *keep < 0 {
		fmt.Fprintln(os.Stderr, "cake-proxy: -keep must be zero or positive")
		return 2
	}
	if err := checkListen(*listen, *allowRemote); err != nil {
		fmt.Fprintln(os.Stderr, "cake-proxy:", err)
		return 2
	}

	upstreamURL, err := url.Parse(*upstream)
	if err != nil {
		fmt.Fprintf(os.Stderr, "cake-proxy: parse upstream: %v\n", err)
		return 2
	}

	upstreamKey := ""
	if *keyEnv != "" {
		upstreamKey = os.Getenv(*keyEnv)
		if upstreamKey == "" {
			fmt.Fprintf(os.Stderr, "cake-proxy: environment variable %s is unset or empty\n", *keyEnv)
			return 2
		}
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	store, err := capture.New(*dataDir, *keep, logger)
	if err != nil {
		fmt.Fprintln(os.Stderr, "cake-proxy:", err)
		return 1
	}

	relay, err := proxy.New(proxy.Options{
		Upstream:          upstreamURL,
		UpstreamKey:       upstreamKey,
		UpstreamKeyHeader: *keyHeader,
		Store:             store,
		MaxBodyBytes:      *maxBodyMB << 20,
		Logger:            logger,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "cake-proxy:", err)
		return 2
	}

	ui, err := web.New(store, web.Info{
		Upstream: relay.Upstream(),
		Listen:   *listen,
		DataDir:  *dataDir,
		Keep:     *keep,
	}, logger)
	if err != nil {
		fmt.Fprintln(os.Stderr, "cake-proxy:", err)
		return 1
	}

	mux := http.NewServeMux()
	mux.Handle("/v1/", relay)
	mux.Handle("/v1", relay)
	mux.Handle("/chat/completions", relay)
	mux.Handle("/responses", relay)
	mux.Handle("/", ui)

	var handler http.Handler = mux
	if host, _, err := net.SplitHostPort(*listen); err == nil && isLoopbackHost(host) {
		handler = hostGuard(mux, loopbackHosts(*listen))
	}

	server := &http.Server{
		Addr:              *listen,
		Handler:           handler,
		ReadHeaderTimeout: 15 * time.Second,
		IdleTimeout:       2 * time.Minute,
		MaxHeaderBytes:    1 << 20,
	}

	printStartup(*listen, relay.Upstream(), *dataDir, *keep, *keyEnv, *keyHeader, upstreamKey != "")

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	errs := make(chan error, 1)
	go func() {
		errs <- server.ListenAndServe()
	}()

	select {
	case err := <-errs:
		if !errors.Is(err, http.ErrServerClosed) {
			fmt.Fprintln(os.Stderr, "cake-proxy:", err)
			return 1
		}
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}
	return 0
}

func checkListen(listen string, allowRemote bool) error {
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return fmt.Errorf("invalid -listen %q: %w", listen, err)
	}
	if port == "" {
		return fmt.Errorf("invalid -listen %q: missing port", listen)
	}
	if !isLoopbackHost(host) && !allowRemote {
		return fmt.Errorf("refusing to bind %q: the inspector has no authentication; use -allow-remote to override", listen)
	}
	return nil
}

func isLoopbackHost(host string) bool {
	ip := net.ParseIP(host)
	return host != "" &&
		(strings.EqualFold(host, "localhost") || (ip != nil && ip.IsLoopback()))
}

// loopbackHosts is the Host header allowlist for a loopback listener. It
// blocks DNS-rebinding pages from reading the inspector over 127.0.0.1.
func loopbackHosts(listen string) map[string]bool {
	allowed := map[string]bool{
		"127.0.0.1": true,
		"localhost": true,
		"::1":       true,
	}
	if host, _, err := net.SplitHostPort(listen); err == nil {
		allowed[strings.ToLower(strings.Trim(host, "[]"))] = true
	}
	return allowed
}

func hostGuard(next http.Handler, allowed map[string]bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := r.Host
		if parsed, _, err := net.SplitHostPort(r.Host); err == nil {
			host = parsed
		}
		host = strings.ToLower(strings.Trim(host, "[]"))
		if !allowed[host] {
			http.Error(w, "cake-proxy: unexpected Host header", http.StatusMisdirectedRequest)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func printStartup(listen, upstream, dataDir string, keep int, keyEnv, keyHeader string, keySet bool) {
	base := publicBase(listen)
	retention := "all exchanges kept"
	if keep > 0 {
		retention = fmt.Sprintf("keep %d", keep)
	}
	data := dataDir
	if data == "" {
		data = "(memory only)"
	}
	fmt.Printf("cake-proxy %s\n", version.Version)
	fmt.Printf("  inspector  %s/\n", base)
	fmt.Printf("  upstream   %s\n", upstream)
	fmt.Printf("  data       %s (%s)\n", data, retention)
	if keySet {
		fmt.Printf("  credential $%s substituted into %s\n", keyEnv, keyHeader)
	} else {
		fmt.Println("  credential forwarded from the client")
	}
	fmt.Println()
	fmt.Println("Point cake at it:")
	if keySet {
		fmt.Println("  export CAKE_PROXY_TOKEN=proxy   # placeholder; the proxy substitutes the real key")
	} else {
		fmt.Println("  export CAKE_PROXY_TOKEN=<your-upstream-api-key>")
	}
	fmt.Println("  # ~/.config/cake/settings.toml")
	fmt.Println("  [[models]]")
	fmt.Println("  name = \"proxied\"")
	fmt.Println("  model = \"<provider-model-id>\"")
	fmt.Printf("  base_url = \"%s/v1\"\n", base)
	fmt.Println("  api_key_env = \"CAKE_PROXY_TOKEN\"")
	fmt.Println("  api_type = \"chat_completions\"   # or \"responses\"")
	if provider := providerForUpstream(upstream); provider != "" {
		fmt.Printf("  provider = \"%s\"\n", provider)
	}
	fmt.Println()
}

// providerForUpstream suggests the provider strategy cake would have inferred
// from the real base URL; pointing cake at the proxy hides that signal.
func providerForUpstream(upstream string) string {
	switch {
	case strings.Contains(upstream, "opencode.ai"):
		return "opencode"
	case strings.Contains(upstream, "openrouter.ai"):
		return "openrouter"
	default:
		return ""
	}
}

func publicBase(listen string) string {
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return "http://" + listen
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port)
}

func defaultDataDir() string {
	if dir := os.Getenv("XDG_DATA_HOME"); dir != "" {
		return filepath.Join(dir, "cake-proxy")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "cake-proxy-data"
	}
	return filepath.Join(home, ".local", "share", "cake-proxy")
}

func envOr(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func usage(out io.Writer) {
	fmt.Fprintf(out, `cake-proxy - record OpenAI-compatible requests for inspection

Usage:
  cake-proxy -upstream <url> [flags]
  cake-proxy version | -version | --version
  cake-proxy help | -h | --help

The proxy forwards /v1/chat/completions and /v1/responses to the upstream base
URL, streaming server-sent events unchanged, and records every exchange for the
HTML inspector. Point cake at http://127.0.0.1:8787/v1.

Flags:
  -listen <host:port>          listen address (default %s, $CAKE_PROXY_LISTEN)
  -upstream <url>              upstream base URL ($CAKE_PROXY_UPSTREAM)
  -upstream-key-env <name>     replace the client credential with $<name>
  -upstream-key-header <name>  credential header (default Authorization)
  -data-dir <path>             capture store (default $CAKE_PROXY_DATA_DIR or
                               ~/.local/share/cake-proxy; empty is memory-only)
  -keep <n>                    finished exchanges to retain (default 200; 0 keeps all)
  -max-body-mb <n>             body capture limit in MiB (default 64)
  -allow-remote                allow a non-loopback bind (no UI authentication)

The client credential is forwarded unless -upstream-key-env is set. The real
credential is never written to the capture store; credential headers are
redacted.
`, defaultListen)
}
