package main

import (
	"flag"
	"net"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"
	"time"

	"log/slog"

	clientset "k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"nefelim4ag/k8s-ondemand-proxy/tcpserver"
)

type globalState struct {
	lastServe atomic.Int64
	upsreamSrv *net.TCPAddr
}

func (state *globalState) touch() {
	state.lastServe.Store(time.Now().UnixMicro())
}

func buildConfig(kubeconfig string) (*rest.Config, error) {
	if kubeconfig != "" {
		cfg, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
		if err != nil {
			return nil, err
		}
		return cfg, nil
	}

	cfg, err := rest.InClusterConfig()
	if err != nil {
		return nil, err
	}
	return cfg, nil
}

func (state *globalState) pipe(src *net.TCPConn, dst *net.TCPConn){
	defer src.Close()
	defer dst.Close()
	buf := make([]byte, 4 * 4096)

	for {
		state.touch()
		n, err := src.Read(buf)
		if err != nil {
			return
		}
		b := buf[:n]

		state.touch()
		n, err = dst.Write(b)
		if err != nil {
			return
		}
	}
}

func (state *globalState) connectionHandler(clientConn *net.TCPConn, err error) {
	if err != nil {
		slog.Error(err.Error())
		return
	}
	state.touch()

    serverConn, err := net.DialTCP("tcp", nil, state.upsreamSrv)
    if err != nil {
        slog.Error(err.Error())
        return
    }

	// Handle connection close internal in pipe, close both ends in same time
	go state.pipe(clientConn, serverConn)
	state.pipe(serverConn, clientConn)
}

func main() {
	var kubeconfig string
	var rawUpstreamServerAddr string

	flag.StringVar(&kubeconfig, "kubeconfig", "", "absolute path to the kubeconfig file")
	flag.StringVar(&rawUpstreamServerAddr, "upstream", "", "Remote server address like dind.ci.svc.cluster.local:2375")
	flag.Parse()

	programLevel := new(slog.LevelVar)
	logger := slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: programLevel})
	slog.SetDefault(slog.New(logger))

	config, err := buildConfig(kubeconfig)
	if err != nil {
		slog.Error(err.Error())
	}
	_ = clientset.NewForConfigOrDie(config)

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	state := globalState{}
	state.upsreamSrv, err = net.ResolveTCPAddr("tcp", rawUpstreamServerAddr)
	if err != nil {
		slog.Error("failed to resolve address", rawUpstreamServerAddr, err.Error())
		return
	}

	srvInstance := tcpserver.Server{}
	err = srvInstance.ListenAndServe(":11211", state.connectionHandler)
	if err != nil {
		slog.Error(err.Error())
	}

	<-sigChan
	slog.Info("Shutting down server...")
	srvInstance.Stop()
	slog.Info("Server stopped.")
}
