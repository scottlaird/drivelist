package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"github.com/scottlaird/drivelist/internal/pb/drivelistv1/drivelistv1connect"
	"github.com/scottlaird/drivelist/internal/report"
)

// clientConfig is how the fleet commands find the server. Each value comes
// from the flag, else the environment, else ~/.config/drivelist/config.
type clientConfig struct {
	server        string
	operatorToken string
	agentToken    string
	actor         string
	json          bool
}

// configFile is $XDG_CONFIG_HOME/drivelist/config or ~/.config/drivelist/config:
// one "key = value" per line, # comments. Keys: server, operator_token,
// agent_token, operator_token_file, agent_token_file, actor.
func configFile() string {
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "drivelist", "config")
}

func readConfigFile(path string) map[string]string {
	m := map[string]string{}
	f, err := os.Open(path)
	if err != nil {
		return m
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if k, v, ok := strings.Cut(line, "="); ok {
			m[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
	}
	return m
}

// resolve fills empty fields from the environment and the config file.
func (c *clientConfig) resolve() {
	file := readConfigFile(configFile())
	pick := func(field *string, env, key string) {
		if *field != "" {
			return
		}
		if v := os.Getenv(env); v != "" {
			*field = v
			return
		}
		*field = file[key]
	}
	pick(&c.server, "DRIVELIST_SERVER", "server")
	pick(&c.operatorToken, "DRIVELIST_OPERATOR_TOKEN", "operator_token")
	pick(&c.agentToken, "DRIVELIST_AGENT_TOKEN", "agent_token")
	pick(&c.actor, "DRIVELIST_ACTOR", "actor")
	// Tokens may also come from files, which is how the systemd units pass
	// them: DRIVELIST_*_TOKEN_FILE, or *_token_file in the config file.
	var operatorFile, agentFile string
	pick(&operatorFile, "DRIVELIST_OPERATOR_TOKEN_FILE", "operator_token_file")
	pick(&agentFile, "DRIVELIST_AGENT_TOKEN_FILE", "agent_token_file")
	if c.operatorToken == "" && operatorFile != "" {
		if b, err := os.ReadFile(operatorFile); err == nil {
			c.operatorToken = strings.TrimSpace(string(b))
		}
	}
	if c.agentToken == "" && agentFile != "" {
		if b, err := os.ReadFile(agentFile); err == nil {
			c.agentToken = strings.TrimSpace(string(b))
		}
	}
	if c.actor == "" {
		user := os.Getenv("USER")
		host, _ := os.Hostname()
		c.actor = user + "@" + host
	}
}

func (c *clientConfig) baseURL() (string, error) {
	c.resolve()
	if c.server == "" {
		return "", errors.New("no server: use --server, DRIVELIST_SERVER, or 'server = …' in " + configFile())
	}
	if !strings.Contains(c.server, "://") {
		return "http://" + c.server, nil
	}
	return c.server, nil
}

func withToken(token string) connect.ClientOption {
	return connect.WithInterceptors(connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			req.Header().Set("Authorization", "Bearer "+token)
			return next(ctx, req)
		}
	}))
}

func (c *clientConfig) queryClient() (drivelistv1connect.QueryClient, error) {
	url, err := c.baseURL()
	if err != nil {
		return nil, err
	}
	if c.operatorToken == "" {
		return nil, errors.New("no operator token: set DRIVELIST_OPERATOR_TOKEN or 'operator_token = …' in " + configFile())
	}
	return drivelistv1connect.NewQueryClient(httpClient(), url, withToken(c.operatorToken)), nil
}

func (c *clientConfig) collectorClient() (drivelistv1connect.CollectorClient, error) {
	url, err := c.baseURL()
	if err != nil {
		return nil, err
	}
	if c.agentToken == "" {
		return nil, errors.New("no agent token: set DRIVELIST_AGENT_TOKEN or 'agent_token = …' in " + configFile())
	}
	return drivelistv1connect.NewCollectorClient(httpClient(), url, withToken(c.agentToken)), nil
}

func httpClient() *http.Client { return &http.Client{Timeout: 60 * time.Second} }

// printJSON writes a response message as JSON, for --json.
func printJSON(w interface{ Write([]byte) (int, error) }, m proto.Message) error {
	b, err := protojson.MarshalOptions{Multiline: true, Indent: "  "}.Marshal(m)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(w, string(b))
	return err
}

// rpcErr turns a connect error into a plain message for the terminal.
func rpcErr(err error) error {
	var ce *connect.Error
	if errors.As(err, &ce) {
		switch ce.Code() {
		case connect.CodeUnauthenticated:
			return errors.New("server rejected the token")
		case connect.CodeUnavailable:
			return fmt.Errorf("server unreachable: %s", ce.Message())
		case connect.CodeUnimplemented:
			// A 404 from the HTTP layer: the server has no handler for this
			// call, which means it is older than this client.
			return fmt.Errorf("the server does not know this call (%s); it is older than this client, so upgrade the server (this is drivelist %s)", ce.Message(), report.Version)
		}
		return errors.New(ce.Message())
	}
	return err
}
