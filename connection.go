package main

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"
	"sync"

	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"go.mongodb.org/mongo-driver/v2/mongo/readpref"
)

type Connection struct {
	Name   string
	Config ConnectionConfig
	Client *mongo.Client

	pingMu sync.Mutex
	pinged bool
}

type ConnectionManager struct {
	mu          sync.RWMutex
	connections map[string]*Connection
	defaultName string
}

// parseHosts turns the host/port config into driver host strings.
// "h1:27017,h2:27018" → both entries as given; "h" → "h:<port>"; "h:1234" → as given.
func parseHosts(host string, port int) []string {
	if strings.Contains(host, ",") {
		hosts := []string{}
		for _, h := range strings.Split(host, ",") {
			if h = strings.TrimSpace(h); h != "" {
				hosts = append(hosts, h)
			}
		}
		return hosts
	}
	host = strings.TrimSpace(host)
	if strings.Contains(host, ":") {
		return []string{host}
	}
	return []string{net.JoinHostPort(host, strconv.Itoa(port))}
}

// buildClientOptions converts a connection config into driver options.
// The password is passed via SetAuth only — never embedded in a URI.
func buildClientOptions(cfg ConnectionConfig) (*options.ClientOptions, error) {
	timeout := cfg.Timeout()
	opts := options.Client().
		SetHosts(parseHosts(cfg.Host, cfg.Port)).
		SetDirect(cfg.DirectConnection).
		SetReadPreference(readpref.SecondaryPreferred()).
		SetServerSelectionTimeout(timeout).
		SetConnectTimeout(timeout).
		SetAppName("mcp-mongodb")
	if cfg.Username != "" {
		opts.SetAuth(options.Credential{
			Username:   cfg.Username,
			Password:   cfg.Password,
			AuthSource: cfg.AuthSource,
		})
	}
	if cfg.ReplicaSet != "" {
		opts.SetReplicaSet(cfg.ReplicaSet)
	}
	if cfg.TLS {
		opts.SetTLSConfig(&tls.Config{})
	}
	if err := opts.Validate(); err != nil {
		return nil, err
	}
	return opts, nil
}

// Init creates a client per connection. The v2 driver does not dial on
// Connect, so this never blocks on the network.
func (cm *ConnectionManager) Init(configs map[string]ConnectionConfig) error {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	if len(configs) == 0 {
		return fmt.Errorf("at least one connection is required in config")
	}

	names := make([]string, 0, len(configs))
	for name := range configs {
		names = append(names, name)
	}
	sort.Strings(names)

	cm.connections = make(map[string]*Connection, len(configs))
	for _, name := range names {
		cfg := configs[name]
		opts, err := buildClientOptions(cfg)
		if err != nil {
			return fmt.Errorf("connection %q: invalid options: %w", name, err)
		}
		client, err := mongo.Connect(opts)
		if err != nil {
			return fmt.Errorf("connection %q: failed to create client: %w", name, err)
		}
		cm.connections[name] = &Connection{Name: name, Config: cfg, Client: client}
	}

	if _, ok := configs["default"]; ok {
		cm.defaultName = "default"
	} else {
		cm.defaultName = names[0]
	}
	return nil
}

// Get returns the named connection (empty name → default), pinging it on
// first use. A failed ping is not remembered, so the next call retries.
func (cm *ConnectionManager) Get(ctx context.Context, name string) (*Connection, error) {
	conn, err := cm.lookup(name)
	if err != nil {
		return nil, err
	}
	if err := conn.ensurePinged(ctx); err != nil {
		return nil, err
	}
	return conn, nil
}

func (cm *ConnectionManager) lookup(name string) (*Connection, error) {
	cm.mu.RLock()
	defer cm.mu.RUnlock()

	if name == "" {
		name = cm.defaultName
	}
	conn, ok := cm.connections[name]
	if !ok {
		return nil, fmt.Errorf("connection %q not found. Available: %v", name, cm.connectionNamesLocked())
	}
	return conn, nil
}

func (cm *ConnectionManager) connectionNamesLocked() []string {
	names := make([]string, 0, len(cm.connections))
	for name := range cm.connections {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func (c *Connection) ensurePinged(ctx context.Context) error {
	c.pingMu.Lock()
	defer c.pingMu.Unlock()
	if c.pinged {
		return nil
	}
	pingCtx, cancel := context.WithTimeout(ctx, c.Config.Timeout())
	defer cancel()
	if err := c.Client.Ping(pingCtx, nil); err != nil {
		return fmt.Errorf("connection %q is unreachable: %v", c.Name, err)
	}
	c.pinged = true
	return nil
}
