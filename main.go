package main

import (
	"fmt"
	"os"

	"github.com/mark3labs/mcp-go/server"
)

func main() {
	if err := LoadConfig(); err != nil {
		fmt.Fprintf(os.Stderr, "mcp-mongodb config error: %v\n", err)
		os.Exit(1)
	}

	s := server.NewMCPServer(
		"mcp-mongodb",
		"1.0.0",
		server.WithToolCapabilities(true),
	)

	registerTools(s)

	if err := server.ServeStdio(s); err != nil {
		fmt.Fprintf(os.Stderr, "mcp-mongodb server error: %v\n", err)
		os.Exit(1)
	}
}
