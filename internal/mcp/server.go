// SPDX-License-Identifier: AGPL-3.0-or-later

// Package mcp is Araldo's Model Context Protocol server (ADR 0020): the
// tools an AI assistant uses to draft, check, schedule and follow up on
// posts, over stdio, as a client of the public API.
package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"sync"

	"github.com/spectrum-labs-tech/araldo/internal/buildinfo"
)

// protocolVersions are the MCP revisions this server speaks, newest first.
var protocolVersions = []string{"2025-06-18", "2025-03-26", "2024-11-05"}

// instructions tell the assistant how the tools fit together.
const instructions = `Araldo publishes posts to social platforms (Bluesky, Mastodon, Discord, Telegram…) for the brands of one org.
Work like this: list_brands and list_channels to see where posts can go; write the text yourself, or use a template
(list_templates, get_template); attach images with upload_media_from_url; call preview_post and fix every violation it
lists (each platform has its own length rules); then create_post. A test key (ald_test_) only ever reaches sandbox
channels, so it is safe to experiment. Afterwards, get_post shows each channel's outcome, and engagement_summary which
posts did best.`

// JSON-RPC 2.0 error codes.
const (
	codeParse          = -32700
	codeInvalidRequest = -32600
	codeMethodNotFound = -32601
	codeInvalidParams  = -32602
)

// Server answers MCP requests with Araldo's tools.
type Server struct {
	api   *Client
	tools []Tool
}

// NewServer returns a server whose tools call api.
func NewServer(api *Client) *Server {
	return &Server{api: api, tools: tools(api)}
}

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// Serve reads requests from in and writes responses to out, one JSON
// message per line, until in ends or ctx is done. Requests are answered
// in order.
func (s *Server) Serve(ctx context.Context, in io.Reader, out io.Writer) error {
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 64<<10), 16<<20)
	var mu sync.Mutex
	write := func(r response) error {
		r.JSONRPC = "2.0"
		b, err := json.Marshal(r)
		if err != nil {
			return err
		}
		mu.Lock()
		defer mu.Unlock()
		_, err = out.Write(append(b, '\n'))
		return err
	}
	for sc.Scan() {
		select {
		case <-ctx.Done():
			return nil // the assistant is shutting down
		default:
		}
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var req request
		if err := json.Unmarshal(line, &req); err != nil {
			if err := write(response{ID: json.RawMessage("null"), Error: &rpcError{Code: codeParse, Message: "not a JSON-RPC message"}}); err != nil {
				return err
			}
			continue
		}
		if len(req.ID) == 0 {
			continue // a notification: nothing to answer
		}
		result, rerr := s.handle(ctx, req)
		if err := write(response{ID: req.ID, Result: result, Error: rerr}); err != nil {
			return err
		}
	}
	return sc.Err()
}

func (s *Server) handle(ctx context.Context, req request) (any, *rpcError) {
	if req.JSONRPC != "2.0" {
		return nil, &rpcError{Code: codeInvalidRequest, Message: `jsonrpc must be "2.0"`}
	}
	switch req.Method {
	case "initialize":
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		_ = json.Unmarshal(req.Params, &p)
		version := protocolVersions[0]
		if slices.Contains(protocolVersions, p.ProtocolVersion) {
			version = p.ProtocolVersion
		}
		return map[string]any{
			"protocolVersion": version,
			"capabilities":    map[string]any{"tools": map[string]any{"listChanged": false}},
			"serverInfo":      map[string]any{"name": "araldo", "title": "Araldo", "version": buildinfo.Version},
			"instructions":    instructions,
		}, nil
	case "ping":
		return map[string]any{}, nil
	case "tools/list":
		list := make([]map[string]any, 0, len(s.tools))
		for _, t := range s.tools {
			list = append(list, t.describe())
		}
		return map[string]any{"tools": list}, nil
	case "tools/call":
		var p struct {
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return nil, &rpcError{Code: codeInvalidParams, Message: "tools/call needs a name and arguments"}
		}
		i := slices.IndexFunc(s.tools, func(t Tool) bool { return t.Name == p.Name })
		if i < 0 {
			return nil, &rpcError{Code: codeInvalidParams, Message: fmt.Sprintf("no tool named %q", p.Name)}
		}
		if p.Arguments == nil {
			p.Arguments = map[string]any{}
		}
		return s.call(ctx, s.tools[i], p.Arguments), nil
	}
	return nil, &rpcError{Code: codeMethodNotFound, Message: fmt.Sprintf("method %q is not supported", req.Method)}
}

// call runs a tool. Failures are tool results with isError, so the agent
// sees them and can correct itself; an API error carries its problem
// details (code, param, errors) as they are.
func (s *Server) call(ctx context.Context, t Tool, args map[string]any) map[string]any {
	out, err := t.Run(ctx, args)
	if err != nil {
		text := err.Error()
		var ae *APIError
		if errors.As(err, &ae) {
			text = string(ae.Problem)
		}
		return map[string]any{"content": []map[string]any{{"type": "text", "text": text}}, "isError": true}
	}
	return map[string]any{"content": []map[string]any{{"type": "text", "text": string(out)}}}
}
