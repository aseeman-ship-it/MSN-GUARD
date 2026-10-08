//go:build stealth_novpn

// Command msnlanternd runs Lantern's radiance backend as a local SOCKS5 proxy
// for the MsnGuard Android app.
//
// Why this exists instead of the stock `lanternd`:
//   - lanternd cannot be given a device id on Android (radiance reads it from
//     backend.Options.DeviceID there), so every config request fails with
//     "request missing Device ID".
//   - lanternd only starts the proxy when told to connect over its IPC socket,
//     and that socket refuses non-privileged peers, which an app UID is.
//
// This binary embeds the backend as a library, passes the device id itself and
// connects without IPC. It never starts the IPC server.
//
// Protocol with the app (stdout, one line each):
//
//	MSN_READY socks=127.0.0.1:1841
//	MSN_FATAL <reason>
//
// Build (see the notes next to this file):
//
//	CGO_ENABLED=1 GOOS=android GOARCH=arm64 CC=<ndk>/aarch64-linux-android24-clang \
//	  go build -tags stealth_novpn -buildmode=pie -trimpath -ldflags="-s -w" \
//	  -o liblantern_msn.so ./cmd/msnlanternd
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/getlantern/radiance/backend"
	"github.com/getlantern/radiance/common/env"
)

// connectTag selects the server to connect to. Empty means "best available"
// (what `lantern connect` does without --tag).
//
// VERIFY against your radiance revision: if ConnectVPN rejects "", use the
// auto-select tag your servers list reports.
const connectTag = ""

func main() {
	if len(os.Args) < 2 || os.Args[1] != "run" {
		fmt.Fprintln(os.Stderr, "usage: msnlanternd run --data-path D --log-path L --device-id ID [--socks ADDR] [--locale TAG]")
		os.Exit(2)
	}

	fs := flag.NewFlagSet("run", flag.ExitOnError)
	dataPath := fs.String("data-path", "", "directory for radiance state")
	logPath := fs.String("log-path", "", "directory for radiance logs")
	socksAddr := fs.String("socks", "127.0.0.1:1841", "SOCKS5/HTTP listen address")
	deviceID := fs.String("device-id", "", "stable per-install device id (required)")
	locale := fs.String("locale", "en-US", "locale sent in config requests")
	logLevel := fs.String("log-level", "info", "trace|debug|info|warn|error")
	_ = fs.Parse(os.Args[2:])

	if *dataPath == "" || *deviceID == "" {
		fatal("--data-path and --device-id are required")
	}
	if *logPath == "" {
		*logPath = filepath.Join(*dataPath, "logs")
	}
	for _, dir := range []string{*dataPath, *logPath} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			fatal("mkdir %s: %v", dir, err)
		}
	}

	// radiance reads these during common.Init, so they must be set before the
	// backend is constructed.
	os.Setenv(string(env.DataPath), *dataPath)
	os.Setenv(string(env.LogPath), *logPath)
	os.Setenv(string(env.SocksAddress), *socksAddr)
	os.Setenv(string(env.LogToStdout), "true")

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// VERIFY (1): the field is documented as backend.Options.DeviceID. Check the
	// exact name with: go doc github.com/getlantern/radiance/backend Options
	be, err := backend.NewLocalBackend(ctx, backend.Options{
		DataDir:  *dataPath,
		LogDir:   *logPath,
		Locale:   *locale,
		LogLevel: *logLevel,
		DeviceID: *deviceID,
		// No PlatformInterface: the stealth_novpn build has no TUN.
	})
	if err != nil {
		fatal("new backend: %v", err)
	}

	// The config fetcher needs a registered user before it can ask for servers.
	// Non-fatal on failure: the fetcher retries ensureUser on its own.
	if user, _ := be.UserData(); user == nil {
		if _, err := be.NewUser(ctx); err != nil {
			slog.Warn("msnlanternd: initial NewUser failed, fetcher will retry", "error", err)
		}
	}

	be.Start()

	// VERIFY (2): ConnectVPN is the LocalBackend method behind `lantern connect`.
	// In the stealth_novpn build it is what opens the SOCKS inbound. Check the
	// signature with: go doc github.com/getlantern/radiance/backend LocalBackend.ConnectVPN
	// If your revision's novpn build opens the inbound from Start() alone, delete
	// this call.
	go func() {
		for ctx.Err() == nil {
			if err := be.ConnectVPN(connectTag); err != nil {
				slog.Warn("msnlanternd: connect failed, retrying", "error", err)
				select {
				case <-ctx.Done():
					return
				case <-time.After(3 * time.Second):
				}
				continue
			}
			return
		}
	}()

	// Report ready only once the inbound really accepts connections; the app
	// additionally verifies a SOCKS5 handshake before it routes traffic.
	if !waitListening(ctx, *socksAddr, 120*time.Second) {
		if ctx.Err() == nil {
			fatal("SOCKS inbound %s did not come up within 120s", *socksAddr)
		}
	} else {
		fmt.Printf("MSN_READY socks=%s\n", *socksAddr)
	}

	<-ctx.Done()
	be.Close()
}

func waitListening(ctx context.Context, addr string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if ctx.Err() != nil {
			return false
		}
		if c, err := net.DialTimeout("tcp", addr, 400*time.Millisecond); err == nil {
			c.Close()
			return true
		}
		time.Sleep(300 * time.Millisecond)
	}
	return false
}

func fatal(format string, args ...any) {
	fmt.Printf("MSN_FATAL %s\n", fmt.Sprintf(format, args...))
	os.Exit(1)
}
