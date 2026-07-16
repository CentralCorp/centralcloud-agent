package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/centralcorp/centralcloud-node-agent/internal/api"
	"github.com/centralcorp/centralcloud-node-agent/internal/auth"
	"github.com/centralcorp/centralcloud-node-agent/internal/backup"
	"github.com/centralcorp/centralcloud-node-agent/internal/config"
	"github.com/centralcorp/centralcloud-node-agent/internal/deployment"
	ccdocker "github.com/centralcorp/centralcloud-node-agent/internal/docker"
	"github.com/centralcorp/centralcloud-node-agent/internal/domain"
	"github.com/centralcorp/centralcloud-node-agent/internal/health"
	"github.com/centralcorp/centralcloud-node-agent/internal/logging"
	ccmetrics "github.com/centralcorp/centralcloud-node-agent/internal/metrics"
	"github.com/centralcorp/centralcloud-node-agent/internal/postgres"
	"github.com/centralcorp/centralcloud-node-agent/internal/resources"
	"github.com/centralcorp/centralcloud-node-agent/internal/storage"
	"github.com/prometheus/client_golang/prometheus"
)

var version = "dev"

func main() {
	configPath := flag.String("config", "/etc/centralcloud-agent/config.yaml", "configuration file")
	showVersion := flag.Bool("version", false, "print version")
	flag.Parse()
	if *showVersion {
		fmt.Println(version)
		return
	}
	log := logging.New(slog.LevelInfo)
	if e := run(*configPath, log); e != nil {
		log.Error("agent stopped", "error", e)
		os.Exit(1)
	}
}
func run(path string, log *slog.Logger) error {
	c, e := config.Load(path)
	if e != nil {
		return e
	}
	if c.Security.Mode == "token" && !isLoopback(c.Server.Address) {
		return fmt.Errorf("token mode may only listen on a loopback address")
	}
	clock := domain.RealClock{}
	repo, e := storage.Open(c.Storage.DatabaseFile, clock)
	if e != nil {
		return e
	}
	defer func() { _ = repo.Close() }()
	secrets, e := auth.NewSecretStore(c.Security.MasterKeyFile, c.Storage.RuntimeDirectory)
	if e != nil {
		return e
	}
	docker, e := ccdocker.New(c.Docker.Socket, c.Docker.RegistryUsernameFile, c.Docker.RegistryTokenFile, c.Docker.EgressNetwork)
	if e != nil {
		return e
	}
	defer func() { _ = docker.Close() }()
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()
	pg, e := postgres.New(ctx, c)
	if e != nil {
		return e
	}
	defer pg.Close()
	backups, e := backup.New(c, secrets)
	if e != nil {
		return e
	}
	defer func() { _ = backups.Close() }()
	checker := health.New(docker)
	registry := prometheus.DefaultRegisterer
	m := ccmetrics.New(registry)
	collector := resources.New(c.Storage.DatabaseFile, repo)
	svc := deployment.New(c, repo, docker, pg, checker, secrets, backups, collector, domain.UUIDGenerator{}, clock, log, m)
	api.Version = version
	handler, e := api.New(c, svc, repo, docker, pg, collector, m, log)
	if e != nil {
		return e
	}
	server := &http.Server{Addr: c.Server.Address, Handler: handler.Handler(), ReadHeaderTimeout: 10 * time.Second, ReadTimeout: c.Server.ReadTimeout, WriteTimeout: c.Server.WriteTimeout, IdleTimeout: c.Server.IdleTimeout, MaxHeaderBytes: 32 << 10}
	if c.Security.Mode == "mtls" {
		tlsConfig, e := serverTLS(c)
		if e != nil {
			return e
		}
		server.TLSConfig = tlsConfig
	}
	svc.Run(ctx)
	errCh := make(chan error, 1)
	go func() {
		if c.Security.Mode == "mtls" {
			errCh <- server.ListenAndServeTLS(c.Security.CertificateFile, c.Security.PrivateKeyFile)
		} else {
			errCh <- server.ListenAndServe()
		}
	}()
	log.Info("agent started", "address", c.Server.Address, "version", version, "security_mode", c.Security.Mode)
	select {
	case <-ctx.Done():
		shutdownCtx, done := context.WithTimeout(context.Background(), 15*time.Second)
		defer done()
		_ = server.Shutdown(shutdownCtx)
		svc.Wait()
		return nil
	case e := <-errCh:
		if errors.Is(e, http.ErrServerClosed) {
			return nil
		}
		return e
	}
}
func serverTLS(c config.Config) (*tls.Config, error) {
	b, e := os.ReadFile(c.Security.ClientCAFile)
	if e != nil {
		return nil, e
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(b) {
		return nil, fmt.Errorf("client CA contains no certificates")
	}
	return &tls.Config{MinVersion: tls.VersionTLS13, ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: pool}, nil
}
func isLoopback(address string) bool {
	host, _, e := net.SplitHostPort(address)
	if e != nil {
		return false
	}
	ip := net.ParseIP(host)
	return host == "localhost" || (ip != nil && ip.IsLoopback())
}
