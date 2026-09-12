package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/scottlaird/drivelist/internal/server"
	"github.com/scottlaird/drivelist/internal/store"
)

func newServeCmd() *cobra.Command {
	var (
		dbPath, listen, agentTokenFile, operatorTokenFile, tlsCert, tlsKey string
		interval                                                           time.Duration
	)
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Run the fleet server",
		Long: `serve runs the server that agents report to and the query commands
read from. It needs two bearer tokens: one for agents, one for
operators. Give each as a file with --agent-token-file and
--operator-token-file, or in DRIVELIST_AGENT_TOKEN and
DRIVELIST_OPERATOR_TOKEN.

Without --tls-cert and --tls-key the server speaks plain HTTP, which
carries the Connect protocol drivelist's own agent and CLI use; gRPC
clients need TLS.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			agentToken, err := tokenFrom(agentTokenFile, "DRIVELIST_AGENT_TOKEN")
			if err != nil {
				return err
			}
			operatorToken, err := tokenFrom(operatorTokenFile, "DRIVELIST_OPERATOR_TOKEN")
			if err != nil {
				return err
			}
			if (tlsCert == "") != (tlsKey == "") {
				return errors.New("--tls-cert and --tls-key go together")
			}
			return runServe(cmd.Context(), dbPath, listen, server.Config{AgentToken: agentToken, OperatorToken: operatorToken, Interval: interval}, tlsCert, tlsKey)
		},
	}
	f := cmd.Flags()
	f.StringVar(&dbPath, "db", "drivelist.db", "SQLite database path (created if missing)")
	f.StringVar(&listen, "listen", ":9450", "address to listen on")
	f.StringVar(&agentTokenFile, "agent-token-file", "", "file holding the token agents present")
	f.StringVar(&operatorTokenFile, "operator-token-file", "", "file holding the token the query commands present")
	f.DurationVar(&interval, "interval", 5*time.Minute, "how often agents report; hosts are stale after three intervals")
	f.StringVar(&tlsCert, "tls-cert", "", "TLS certificate file; with --tls-key, serve HTTPS")
	f.StringVar(&tlsKey, "tls-key", "", "TLS private key file")
	return cmd
}

// tokenFrom reads a token from the file if given, else the environment
// variable. Surrounding whitespace is dropped so a file with a trailing
// newline works.
func tokenFrom(file, env string) (string, error) {
	if file != "" {
		b, err := os.ReadFile(file)
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(string(b)), nil
	}
	if v := strings.TrimSpace(os.Getenv(env)); v != "" {
		return v, nil
	}
	return "", fmt.Errorf("no token: give a token file or set %s", env)
}

func runServe(ctx context.Context, dbPath, listen string, cfg server.Config, tlsCert, tlsKey string) error {
	st, err := store.Open(dbPath)
	if err != nil {
		return fmt.Errorf("open %s: %w", dbPath, err)
	}
	defer st.Close()
	log := slog.Default()
	srv, err := server.New(st, cfg, log)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	go srv.RunSweeper(ctx)

	hs := &http.Server{Addr: listen, Handler: srv.Handler(), ReadHeaderTimeout: 10 * time.Second}
	errc := make(chan error, 1)
	go func() {
		log.Info("serving", "listen", listen, "db", dbPath, "tls", tlsCert != "")
		if tlsCert != "" {
			errc <- hs.ListenAndServeTLS(tlsCert, tlsKey)
		} else {
			errc <- hs.ListenAndServe()
		}
	}()
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	log.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return hs.Shutdown(shutdownCtx)
}
