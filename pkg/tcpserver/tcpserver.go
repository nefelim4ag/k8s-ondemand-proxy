package tcpserver

import (
	"fmt"
	"net"
	"sync"
	"time"

	"log/slog"
)

type (
	ConnectionHandler func(conn *net.TCPConn, err error)

	TCPServer interface {
		ListenAndServe(address string, connectionQueue uint)
		AcceptConnections()
		Stop() error
	}

	Server struct {
		accepted sync.WaitGroup
		listener *net.TCPListener
		shutdown chan struct{}
		handler  ConnectionHandler
	}
)

func (s *Server) ListenAndServe(address string, handler ConnectionHandler) error {
	addr, err := net.ResolveTCPAddr("tcp", address)
	if err != nil {
		return fmt.Errorf("failed to resolve address %s: %w", address, err)
	}

	s.listener, err = net.ListenTCP("tcp", addr)
	if err != nil {
		return fmt.Errorf("failed to listen on address %s: %w", address, err)
	}
	slog.Info("Listening", "address", address)

	s.handler = handler
	s.shutdown = make(chan struct{})

	go s.AcceptConnections()

	return nil
}

func (s *Server) handlerWrap(conn *net.TCPConn, err error){
	s.accepted.Add(1)
	s.handler(conn, err)
	s.accepted.Done()
}

func (s *Server) AcceptConnections() {
	s.accepted.Add(1)
	defer s.accepted.Done()

	for {
		select {
		case <-s.shutdown:
			return
		default:
			connection, err := s.listener.AcceptTCP()
			go s.handlerWrap(connection, err)
		}
	}
}

func (s *Server) Stop() error {
	close(s.shutdown)
	s.listener.Close()

	done := make(chan struct{})
	go func() {
		s.accepted.Wait()
		close(done)
	}()

	select {
	case <-done:
		return nil
	case <-time.After(2 * time.Second):
		slog.Warn("Timed out waiting for connections to finish.")
		return nil
	}
}
