// Command mcpcall liga um servidor MCP de stdio a chamadas de linha de
// comando. O benchmark usa para dar a subagentes, que só têm Bash, as mesmas
// tools e respostas que um cliente MCP receberia. O servidor fica vivo entre
// chamadas (language servers demoram para subir) e cada chamada é registrada
// com bytes e latência.
//
//	mcpcall serve -socket /tmp/x.sock [-log calls.jsonl] -- <comando do servidor>
//	mcpcall call  -socket /tmp/x.sock <tool> '<argumentos JSON>'
//	mcpcall list  -socket /tmp/x.sock
//	mcpcall info  -socket /tmp/x.sock   (nome, versão e instruções do initialize)
//	mcpcall stop  -socket /tmp/x.sock
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// dialWait é quanto o cliente espera o socket aparecer: o servidor pode estar
// subindo um language server.
const dialWait = 90 * time.Second

type request struct {
	Op   string          `json:"op"`
	Tool string          `json:"tool,omitempty"`
	Args json.RawMessage `json:"args,omitempty"`
}

type response struct {
	Text    string `json:"text"`
	IsError bool   `json:"is_error"`
}

// logEntry é uma linha do registro de chamadas, a matéria-prima das métricas.
type logEntry struct {
	Time     string `json:"time"`
	Tool     string `json:"tool"`
	ArgBytes int    `json:"arg_bytes"`
	OutBytes int    `json:"out_bytes"`
	Millis   int64  `json:"ms"`
	IsError  bool   `json:"is_error"`
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: mcpcall serve|call|list|stop -socket PATH ...")
		os.Exit(2)
	}
	code, err := dispatch(os.Args[1], os.Args[2:])
	if err != nil {
		fmt.Fprintln(os.Stderr, "mcpcall:", err)
	}
	os.Exit(code)
}

func dispatch(op string, args []string) (int, error) {
	fs := flag.NewFlagSet(op, flag.ExitOnError)
	socket := fs.String("socket", "", "unix socket shared by serve and the clients")
	logPath := fs.String("log", "", "serve: JSONL file with one line per call")
	_ = fs.Parse(args)
	if *socket == "" {
		return 2, errors.New("-socket is required")
	}
	rest := fs.Args()
	switch op {
	case "serve":
		if len(rest) == 0 {
			return 2, errors.New("usage: mcpcall serve -socket PATH [-log FILE] -- command [args...]")
		}
		if err := serve(*socket, *logPath, rest); err != nil {
			return 1, err
		}
		return 0, nil
	case "call":
		if len(rest) == 0 {
			return 2, errors.New("usage: mcpcall call -socket PATH TOOL [JSON-ARGS]")
		}
		req := request{Op: "call", Tool: rest[0]}
		if len(rest) > 1 {
			if !json.Valid([]byte(rest[1])) {
				return 2, errors.New("arguments must be valid JSON")
			}
			req.Args = json.RawMessage(rest[1])
		}
		return print(*socket, req)
	case "list", "info", "stop":
		return print(*socket, request{Op: op})
	}
	return 2, fmt.Errorf("unknown command %q", op)
}

// print manda o pedido e escreve a resposta como o agente a leria; erro da
// tool vira código de saída 1 com o texto no stdout.
func print(socket string, req request) (int, error) {
	res, err := roundTrip(socket, req)
	if err != nil {
		return 1, err
	}
	fmt.Print(res.Text)
	if !strings.HasSuffix(res.Text, "\n") {
		fmt.Println()
	}
	if res.IsError {
		return 1, nil
	}
	return 0, nil
}

func roundTrip(socket string, req request) (response, error) {
	var conn net.Conn
	deadline := time.Now().Add(dialWait)
	for {
		c, err := net.DialTimeout("unix", socket, time.Second)
		if err == nil {
			conn = c
			break
		}
		if time.Now().After(deadline) {
			return response{}, fmt.Errorf("connecting to %s: %w", socket, err)
		}
		time.Sleep(200 * time.Millisecond)
	}
	defer func() { _ = conn.Close() }()
	if err := json.NewEncoder(conn).Encode(req); err != nil {
		return response{}, fmt.Errorf("sending request: %w", err)
	}
	var res response
	if err := json.NewDecoder(conn).Decode(&res); err != nil {
		return response{}, fmt.Errorf("reading response: %w", err)
	}
	return res, nil
}

type server struct {
	session *mcp.ClientSession
	logPath string
	stop    context.CancelFunc
}

func serve(socket, logPath string, command []string) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client := mcp.NewClient(&mcp.Implementation{Name: "mcpcall", Version: "0"}, nil)
	cmd := exec.Command(command[0], command[1:]...)
	cmd.Stderr = os.Stderr
	session, err := client.Connect(ctx, &mcp.CommandTransport{Command: cmd}, nil)
	if err != nil {
		return fmt.Errorf("starting %s: %w", command[0], err)
	}
	defer func() { _ = session.Close() }()
	_ = os.Remove(socket)
	ln, err := net.Listen("unix", socket)
	if err != nil {
		return fmt.Errorf("listening on %s: %w", socket, err)
	}
	defer func() { _ = ln.Close() }()
	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()
	s := &server{session: session, logPath: logPath, stop: cancel}
	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("accepting: %w", err)
		}
		s.handle(ctx, conn) // uma chamada por vez: a latência medida é só da tool
	}
}

func (s *server) handle(ctx context.Context, conn net.Conn) {
	defer func() { _ = conn.Close() }()
	var req request
	if err := json.NewDecoder(conn).Decode(&req); err != nil {
		_ = json.NewEncoder(conn).Encode(response{Text: "bad request: " + err.Error(), IsError: true})
		return
	}
	var res response
	switch req.Op {
	case "call":
		res = s.call(ctx, req)
	case "list":
		res = s.list(ctx)
	case "info":
		res = s.info()
	case "stop":
		res = response{Text: "stopped"}
		defer s.stop()
	default:
		res = response{Text: fmt.Sprintf("unknown op %q", req.Op), IsError: true}
	}
	_ = json.NewEncoder(conn).Encode(res)
}

func (s *server) call(ctx context.Context, req request) response {
	var args map[string]any
	if len(req.Args) > 0 {
		if err := json.Unmarshal(req.Args, &args); err != nil {
			return response{Text: "arguments must be a JSON object: " + err.Error(), IsError: true}
		}
	}
	start := time.Now()
	result, err := s.session.CallTool(ctx, &mcp.CallToolParams{Name: req.Tool, Arguments: args})
	elapsed := time.Since(start)
	res := response{}
	if err != nil {
		res = response{Text: err.Error(), IsError: true}
	} else {
		res = response{Text: contentText(result.Content), IsError: result.IsError}
	}
	s.record(logEntry{Time: start.UTC().Format(time.RFC3339Nano), Tool: req.Tool, ArgBytes: len(req.Args),
		OutBytes: len(res.Text), Millis: elapsed.Milliseconds(), IsError: res.IsError})
	return res
}

// list devolve as tools como o cliente MCP as recebe; o tamanho é o custo de
// schema que um agente paga a cada turno.
func (s *server) list(ctx context.Context) response {
	tools, err := s.session.ListTools(ctx, nil)
	if err != nil {
		return response{Text: err.Error(), IsError: true}
	}
	data, err := json.Marshal(tools)
	if err != nil {
		return response{Text: err.Error(), IsError: true}
	}
	return response{Text: string(data)}
}

// info devolve o que o servidor mandou no initialize: as instruções são o
// texto que um cliente MCP põe no prompt do agente.
func (s *server) info() response {
	init := s.session.InitializeResult()
	if init == nil {
		return response{Text: "no initialize result", IsError: true}
	}
	data, err := json.Marshal(map[string]any{"server": init.ServerInfo, "instructions": init.Instructions})
	if err != nil {
		return response{Text: err.Error(), IsError: true}
	}
	return response{Text: string(data)}
}

func contentText(content []mcp.Content) string {
	var b strings.Builder
	for _, c := range content {
		switch v := c.(type) {
		case *mcp.TextContent:
			b.WriteString(v.Text)
		default:
			fmt.Fprintf(&b, "[%T content omitted]", c)
		}
	}
	return b.String()
}

func (s *server) record(entry logEntry) {
	if s.logPath == "" {
		return
	}
	f, err := os.OpenFile(s.logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		fmt.Fprintln(os.Stderr, "mcpcall: log:", err)
		return
	}
	defer func() { _ = f.Close() }()
	_ = json.NewEncoder(f).Encode(entry)
}
