package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	publicv1 "github.com/dcm-project/osac-service-provider/internal/osacpb/osac/public/v1"
	"github.com/dcm-project/osac-service-provider/test/mockprovider"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

const shutdownTimeout = 5 * time.Second

func main() {
	if err := run(); err != nil {
		slog.Error("simulator failed", "error", err)
		os.Exit(1)
	}
}

func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	cfg, err := mockprovider.LoadConfig()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	grpcListener, err := net.Listen("tcp", cfg.GRPCAddress)
	if err != nil {
		return fmt.Errorf("listen gRPC: %w", err)
	}
	defer grpcListener.Close()
	oidcListener, err := net.Listen("tcp", cfg.OIDCAddress)
	if err != nil {
		return fmt.Errorf("listen OIDC: %w", err)
	}
	defer oidcListener.Close()

	tlsConfig, err := mockprovider.ServerTLSConfig()
	if err != nil {
		return fmt.Errorf("load simulator TLS: %w", err)
	}
	grpcServer := grpc.NewServer(grpc.Creds(credentials.NewTLS(tlsConfig)), grpc.UnaryInterceptor(newScenarioState().unary()))
	clusters := mockprovider.NewClustersServer()
	publicv1.RegisterCapabilitiesServer(grpcServer, mockprovider.NewCapabilitiesServer())
	publicv1.RegisterClustersServer(grpcServer, clusters)
	publicv1.RegisterSecretsServer(grpcServer, mockprovider.NewSecretsServer(clusters))
	publicv1.RegisterClusterTemplatesServer(grpcServer, mockprovider.NewClusterTemplatesServer())
	publicv1.RegisterClusterVersionsServer(grpcServer, newClusterVersionsServer())
	publicv1.RegisterComputeInstancesServer(grpcServer, mockprovider.NewComputeInstancesServer())
	publicv1.RegisterSubnetsServer(grpcServer, mockprovider.NewSubnetsServer())
	publicv1.RegisterVirtualNetworksServer(grpcServer, mockprovider.NewVirtualNetworksServer())

	oidcServer := &http.Server{Handler: mockprovider.NewOIDCHandler(slog.Default()), ReadHeaderTimeout: 10 * time.Second}
	errCh := make(chan error, 2)
	go func() { errCh <- grpcServer.Serve(grpcListener) }()
	go func() {
		if serveErr := oidcServer.Serve(oidcListener); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			errCh <- serveErr
			return
		}
		errCh <- nil
	}()

	select {
	case <-ctx.Done():
	case serveErr := <-errCh:
		if serveErr != nil {
			return serveErr
		}
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	grpcServer.GracefulStop()
	return oidcServer.Shutdown(shutdownCtx)
}
