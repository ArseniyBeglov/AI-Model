package main

import (
	"ai-model/internal/adapters/allure"
	openaiadapter "ai-model/internal/adapters/openai"
	"ai-model/internal/analyzer"
	"ai-model/internal/config"
	"ai-model/internal/storage"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	command, args, err := parseCommand(os.Args[1:])
	if err != nil {
		log.Fatalf("%v", err)
	}

	cfg, err := config.Load(ctx)
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	store, err := storage.New(ctx, cfg.Database)
	if err != nil {
		log.Fatalf("connect database: %v", err)
	}
	defer store.Close()

	switch command {
	case "migrate":
		if err := store.Migrate(ctx); err != nil {
			log.Fatalf("apply migrations: %v", err)
		}
		log.Println("database schema is up to date")
	case "sync":
		if err := runSync(ctx, cfg, store, args); err != nil {
			log.Fatalf("sync failed: %v", err)
		}
	case "triage":
		if err := runTriage(ctx, cfg, store, args); err != nil {
			log.Fatalf("triage failed: %v", err)
		}
	default:
		log.Fatalf("unsupported command: %s", command)
	}
}

func parseCommand(args []string) (string, []string, error) {
	if len(args) == 0 {
		return "", nil, errors.New("usage: ai-model <migrate|sync|triage> [flags]")
	}
	return args[0], args[1:], nil
}

func runSync(ctx context.Context, cfg config.Config, store *storage.Store, args []string) error {
	fs := flag.NewFlagSet("sync", flag.ContinueOnError)
	var launchID int64
	var recent int
	fs.Int64Var(&launchID, "launch-id", 0, "sync a specific allure launch")
	fs.IntVar(&recent, "recent", cfg.Allure.SyncLaunchLimit, "sync most recent launches")
	if err := fs.Parse(args); err != nil {
		return err
	}

	client := allure.NewClient(cfg.Allure)
	service := storage.NewSyncService(store, client)
	if err := store.Migrate(ctx); err != nil {
		return err
	}

	switch {
	case launchID > 0:
		return service.SyncLaunch(ctx, launchID)
	case recent > 0:
		return service.SyncRecentLaunches(ctx, recent)
	default:
		return errors.New("either --launch-id or --recent must be greater than zero")
	}
}

func runTriage(ctx context.Context, cfg config.Config, store *storage.Store, args []string) error {
	fs := flag.NewFlagSet("triage", flag.ContinueOnError)
	var launchID int64
	fs.Int64Var(&launchID, "launch-id", 0, "analyze failed tests in a specific launch")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if launchID == 0 {
		return errors.New("--launch-id is required")
	}

	if err := store.Migrate(ctx); err != nil {
		return err
	}

	client := allure.NewClient(cfg.Allure)
	syncService := storage.NewSyncService(store, client)
	if err := syncService.SyncLaunch(ctx, launchID); err != nil {
		return err
	}

	var embedder analyzer.Embedder
	if cfg.Embeddings.Enabled {
		embedder = openaiadapter.NewEmbedder(cfg.Embeddings)
	}

	var llm analyzer.ChatModel
	if cfg.LLM.Enabled {
		llm = openaiadapter.NewChatModel(cfg.LLM)
	}

	service := analyzer.NewService(store, embedder, llm, cfg)
	results, err := service.AnalyzeLaunch(ctx, launchID)
	if err != nil {
		return err
	}

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(results)
}
