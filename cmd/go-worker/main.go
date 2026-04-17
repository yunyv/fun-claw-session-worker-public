package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/funclaw/go-worker/internal/gatewayclient"
	"github.com/funclaw/go-worker/internal/hubclient"
	"github.com/funclaw/go-worker/internal/protocol"
)

var (
	hubURL       = flag.String("hub-url", "ws://127.0.0.1:31880/ws", "Session Hub WebSocket URL")
	hubToken     = flag.String("hub-token", "", "Session Hub auth token")
	workerID     = flag.String("worker-id", "", "Worker ID (required)")
	gatewayURL   = flag.String("gateway-url", "http://127.0.0.1:18789", "OpenClaw Gateway HTTP URL")
	gatewayToken = flag.String("gateway-token", "", "OpenClaw Gateway auth token")
	gatewayWsURL = flag.String("gateway-ws-url", "ws://127.0.0.1:18789", "OpenClaw Gateway WebSocket URL")
	capabilities = flag.String("capabilities", "responses.create,session.history.get,node.invoke", "Worker capabilities")
	hostname     = flag.String("hostname", "", "Worker hostname (defaults to OS hostname)")
	version      = flag.String("version", "1.0.0", "Worker version")
)

func main() {
	flag.Parse()

	if *workerID == "" {
		fmt.Println("Error: --worker-id is required")
		flag.Usage()
		os.Exit(1)
	}

	if *hostname == "" {
		host, err := os.Hostname()
		if err != nil {
			host = "unknown"
		}
		*hostname = host
	}

	// Parse capabilities
	caps := parseCapabilities(*capabilities)

	fmt.Printf("[Worker] Starting with worker-id=%s, hub-url=%s, gateway-url=%s\n", *workerID, *hubURL, *gatewayURL)

	// Create Gateway client
	gwClient := gatewayclient.New(*gatewayURL, *gatewayToken, *gatewayWsURL)

	// Create Hub client - use pointer to avoid closure capture issue
	var hubClient *hubclient.HubClient
	hubClient = hubclient.New(hubclient.HubClientOptions{
		URL:      *hubURL,
		Token:    *hubToken,
		WorkerID: *workerID,
		Hostname: *hostname,
		Version:  *version,
		Capabilities: caps,
		OnTaskAssigned: func(task *protocol.TaskAssignedPayload) error {
			fmt.Printf("[Worker] Received task: request_id=%s, action=%s, session=%s\n",
				task.RequestID, task.Action, task.OpenclawSessionKey)
			return handleTask(hubClient, gwClient, task)
		},
	})

	ctx := context.Background()
	if err := hubClient.Start(ctx); err != nil {
		fmt.Printf("Failed to start Hub client: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("[Worker] Connected to Hub at %s as %s\n", *hubURL, *workerID)
	fmt.Printf("[Worker] Gateway endpoint: %s\n", *gatewayURL)

	// Wait for interrupt signal
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh

	fmt.Println("[Worker] Shutting down...")
	hubClient.Stop()
}

func handleTask(hub *hubclient.HubClient, gateway *gatewayclient.GatewayClient, task *protocol.TaskAssignedPayload) error {
	fmt.Printf("[Worker] Processing task: request_id=%s, action=%s\n", task.RequestID, task.Action)

	// Send task.accepted
	fmt.Printf("[Worker] Sending task.accepted for request_id=%s\n", task.RequestID)
	if err := hub.SendAccepted(task.RequestID); err != nil {
		fmt.Printf("[Worker] ERROR: failed to send accepted: %v\n", err)
		return fmt.Errorf("failed to send accepted: %w", err)
	}

	ctx := context.Background()
	var result interface{}
	artifacts := []protocol.ArtifactDescriptor{}

	switch task.Action {
	case protocol.TaskActionResponsesCreate:
		fmt.Printf("[Worker] Executing responses.create for request_id=%s\n", task.RequestID)
		// Debug: print input structure
		inputJSON, _ := json.MarshalIndent(task.Input, "", "  ")
		fmt.Printf("[Worker] DEBUG: input=%s\n", string(inputJSON))
		res, err := gateway.CreateResponse(ctx, task.Input, task.OpenclawSessionKey)
		if err != nil {
			fmt.Printf("[Worker] ERROR: responses.create failed: %v\n", err)
			return sendFailed(hub, task.RequestID, err)
		}
		result = res
		fmt.Printf("[Worker] responses.create completed for request_id=%s\n", task.RequestID)

	case protocol.TaskActionSessionHistoryGet:
		fmt.Printf("[Worker] Executing session.history.get for request_id=%s\n", task.RequestID)
		query := make(map[string]interface{})
		if inputMap, ok := task.Input.(map[string]interface{}); ok {
			for k, v := range inputMap {
				query[k] = v
			}
		}
		res, err := gateway.GetSessionHistory(ctx, task.OpenclawSessionKey, query)
		if err != nil {
			fmt.Printf("[Worker] ERROR: session.history.get failed: %v\n", err)
			return sendFailed(hub, task.RequestID, err)
		}
		result = res
		fmt.Printf("[Worker] session.history.get completed for request_id=%s\n", task.RequestID)

	case protocol.TaskActionNodeInvoke:
		fmt.Printf("[Worker] Executing node.invoke for request_id=%s\n", task.RequestID)
		res, err := gateway.InvokeNode(ctx, task.Input)
		if err != nil {
			fmt.Printf("[Worker] ERROR: node.invoke failed: %v\n", err)
			return sendFailed(hub, task.RequestID, err)
		}
		// Normalize artifacts
		normalizedResult, normalizedArtifacts := gateway.NormalizeNodeArtifacts(res)
		result = normalizedResult

		// Register artifacts
		for _, art := range normalizedArtifacts {
			fmt.Printf("[Worker] Registering artifact: filename=%s, kind=%s\n", art.Filename, art.Kind)
			desc, err := hub.RegisterArtifact(protocol.ArtifactRegisterParams{
				RequestID: task.RequestID,
				Artifact: protocol.ArtifactInput{
					Kind:          protocol.ArtifactKind(art.Kind),
					Filename:      art.Filename,
					MimeType:      art.MimeType,
					ContentBase64: art.ContentBase64,
					Meta:          art.Meta,
				},
			})
			if err != nil {
				fmt.Printf("[Worker] ERROR: artifact.register failed: %v\n", err)
				return sendFailed(hub, task.RequestID, err)
			}
			artifacts = append(artifacts, *desc)
		}
		fmt.Printf("[Worker] node.invoke completed for request_id=%s with %d artifacts\n", task.RequestID, len(artifacts))

	default:
		fmt.Printf("[Worker] ERROR: unsupported action: %s\n", task.Action)
		return sendFailed(hub, task.RequestID, fmt.Errorf("unsupported task action: %s", task.Action))
	}

	// Send task.completed
	fmt.Printf("[Worker] Sending task.completed for request_id=%s\n", task.RequestID)
	if err := hub.SendCompleted(task.RequestID, result, artifacts); err != nil {
		fmt.Printf("[Worker] ERROR: failed to send completed: %v\n", err)
		return fmt.Errorf("failed to send completed: %w", err)
	}

	fmt.Printf("[Worker] Task completed successfully: request_id=%s\n", task.RequestID)
	return nil
}

func sendFailed(hub *hubclient.HubClient, requestID string, err error) error {
	fmt.Printf("[Worker] Sending task.failed for request_id=%s: %v\n", requestID, err)
	errShape := protocol.ErrorShape{
		Code:    "WORKER_ERROR",
		Message: err.Error(),
	}
	if sendErr := hub.SendFailed(requestID, errShape); sendErr != nil {
		return fmt.Errorf("failed to send failed: %w (original error: %v)", sendErr, err)
	}
	return nil
}

func parseCapabilities(caps string) []string {
	if caps == "" {
		return []string{"responses.create", "session.history.get", "node.invoke"}
	}
	return strings.Split(caps, ",")
}
