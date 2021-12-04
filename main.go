/*
 * Copyright (c) 2021 Antoine Catton and contributors. All right reserved.
 * Licensed under the ISC license. See LICENSE file in project root for details.
 *
 */
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"strings"
	"sync"
)

const usageExit = 64

// usage prints the usage for the program.
func usage(progname string) {
	fmt.Printf("Usage: %s source destination\n\n", progname)
	fmt.Println("  source: the local address onto which to listen for new connections")
	fmt.Println("  destination: the remote address to which to connect")
	os.Exit(usageExit)
}

// logError formats and log a message with the "ERROR" prefix.
func logError(format string, a ...interface{}) {
	msg := fmt.Sprintf(format, a...)
	log.Printf("ERROR: %s\n", msg)
}

// isUnixSocket checks if an address is a unix socket.
//
// Unix sockets addresses must contain a slash. If the socket is in the current
// directory, one can use the ./filename trick.
func isUnixSocket(addr string) bool {
	return strings.Contains(addr, "/")
}

// getSourceListener creates a listener for the source address.
//
// Depending on whether the source address is a unix socket address or not, it
// creates a TCPListener or an UnixListener.
func getSourceListener(ctx context.Context, addr string) (net.Listener, error) {
	network := "tcp"
	if isUnixSocket(addr) {
		network = "unix"
	}

	return net.Listen(network, addr)
}

// proxy represent the meat of the program.
//
// It is used as context between all parallel functions running.
type proxy struct {
	wg        sync.WaitGroup
	connector func() (net.Conn, error)
	listener  net.Listener

	mu  sync.Mutex
	err error
}

// handleError handles a fatal error for the whole service.
//
// If there is already an error, and the program is gracefully shutting down,
// the error is just logged.
func (p *proxy) handleError(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.err == nil {
		log.Printf("ERROR: ignored error: %v", err)
		return
	}

	p.err = err
}

// run runs the main proxy server.
//
// This hangs for ever, or until there is a fatal error on the service.
func (p *proxy) run(ctx context.Context) error {
	p.wg.Add(1)
	go p.handleError(p.accept(ctx))

	p.wg.Wait()
	return p.err
}

// accept runs the main accept loop of the program.
//
// Unless Accept() fails, this will hang forever.
func (p *proxy) accept(ctx context.Context) error {
	defer p.wg.Done()

	for {
		conn, err := p.listener.Accept()
		if err != nil {
			return fmt.Errorf("could not listener.Accept(): %w", err)
		}
		p.wg.Add(1)
		go p.handleConn(ctx, conn)
	}
}

// handleConn handles a new comming connection.
//
// It is responsible for connecting to the destination, and copying data back
// and forth.
func (p *proxy) handleConn(parentCtx context.Context, in net.Conn) {
	defer p.wg.Done()

	ctx, cancel := context.WithCancel(parentCtx)
	defer cancel()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		<-ctx.Done()
		in.Close()
	}()

	out, err := p.connector()
	if err != nil {
		logError("could not connect to destination: %v", err)
		return
	}
	wg.Add(1)
	go func() {
		<-ctx.Done()
		out.Close()
	}()

	wg.Add(2)

	go func() {
		defer wg.Done()
		defer cancel()
		_, err := io.Copy(in, out)
		if err != nil {
			logError("while copying stream from destination back to the source: %v", err)
		}
	}()

	go func() {
		defer wg.Done()
		defer cancel()
		_, err := io.Copy(out, in)
		if err != nil {
			logError("while copying stream from source into the destination: %v", err)
		}
	}()

	wg.Wait()
}

func run(parentCtx context.Context, sourceAddr, destAddr string) error {
	ctx, cancel := context.WithCancel(parentCtx)

	var wg sync.WaitGroup

	listener, err := getSourceListener(ctx, sourceAddr)
	if err != nil {
		log.Fatalf("could not listen on the source: %v", err)
	}

	wg.Add(1)
	go func() { // Closes the listener once the context is canceed
		defer wg.Done()
		<-ctx.Done()
		listener.Close()
	}()

	err = nil
	wg.Add(1)
	go func() { // Run the proxy
		defer wg.Done()
		defer cancel()

		p := proxy{
			listener: listener,
			connector: func(address string) func() (net.Conn, error) {
				network := "tcp"
				if isUnixSocket(address) {
					network = "unix"
				}
				return func() (net.Conn, error) {
					return net.Dial(network, address)
				}
			}(destAddr),
		}

		err = p.run(ctx)
	}()

	wg.Wait()
	return err
}

func main() {
	ctx, cancel := context.WithCancel(context.Background())
	_ = cancel

	flag.Parse()
	progname := os.Args[0]

	args := flag.Args()
	if len(args) != 2 {
		logError("could not parse arguments. We only require 2 arguments, got %d", len(args))
		usage(progname)
	}

	err := run(ctx, args[0], args[1])
	if err != nil {
		log.Fatalf("proxying error: %v", err)
	}
}
