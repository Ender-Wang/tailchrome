package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"io"
	"net/http/httptest"
	"testing"
	"time"

	"tailscale.com/client/local"
	"tailscale.com/ipn/ipnstate"
	"tailscale.com/ipn/store/mem"
	"tailscale.com/net/netns"
	"tailscale.com/tsnet"
	"tailscale.com/tstest/integration/testcontrol"
	"tailscale.com/types/logger"
)

// Use the embedded nodes' real LocalAPI and PeerAPI handlers. Mocking PushFile
// cannot catch a Tailscale feature that was omitted from the helper binary.
func TestTaildropTransfersBetweenEmbeddedNodes(t *testing.T) {
	if testing.Short() {
		t.Skip("starts two embedded tailnet nodes")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	defer cancel()
	netns.SetEnabled(false)
	t.Cleanup(func() { netns.SetEnabled(true) })
	control := &testcontrol.Server{
		DERPMap:          splitDNSTestDERP(t),
		AllNodesSameUser: true,
		AllOnline:        true,
		Logf:             logger.Discard,
	}
	control.HTTPTestServer = httptest.NewServer(control)
	t.Cleanup(control.HTTPTestServer.Close)
	startNode := func(name string) (*local.Client, *ipnstate.Status) {
		t.Helper()
		node := &tsnet.Server{
			Dir: t.TempDir(), Hostname: name, ControlURL: control.HTTPTestServer.URL,
			Store: new(mem.Store), Ephemeral: true,
			Logf: logger.Discard, UserLogf: logger.Discard,
		}
		t.Cleanup(func() { node.Close() })
		status, err := node.Up(ctx)
		if err != nil {
			t.Fatal(err)
		}
		lc, err := node.LocalClient()
		if err != nil {
			t.Fatal(err)
		}
		return lc, status
	}
	sender, _ := startNode("taildrop-sender")
	receiver, receiverStatus := startNode("taildrop-receiver")
	targetID := receiverStatus.Self.ID
	files, err := receiver.WaitingFiles(ctx)
	if err != nil {
		t.Fatalf("receiver WaitingFiles: %v", err)
	}
	if len(files) != 0 {
		t.Fatalf("fresh receiver has %d waiting files", len(files))
	}

	// PeerAPI ports arrive in control-plane updates after a node is running.
	// Wait for the actual recipient to be advertised before attempting a send.
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for found := false; !found; {
		targets, err := sender.FileTargets(ctx)
		if err != nil {
			t.Fatalf("sender FileTargets: %v", err)
		}
		for _, target := range targets {
			if target.Node.StableID == targetID {
				found = true
			}
		}
		if found {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("receiver never became a Taildrop target:", ctx.Err())
		case <-ticker.C:
		}
	}

	const filename = "tailchrome-integration.bin"
	payload := bytes.Repeat([]byte{0, 1, 2, 127, 128, 254, 255}, 32768)
	var output bytes.Buffer
	h := newHost(nil, &output)
	h.runPushFile(ctx, sender, string(targetID), targetID, filename, payload, 0)
	replies := decodeAllReplies(t, &output)
	if len(replies) == 0 {
		t.Fatal("no transfer progress replies")
	}
	final := replies[len(replies)-1].FileSendProgress
	if final == nil || !final.Done || final.Error != "" || final.Percent != 100 {
		t.Fatalf("transfer did not complete successfully: %+v", final)
	}
	if final.Name != filename || final.TargetNodeID != string(targetID) {
		t.Fatalf("transfer completion identified the wrong file or recipient: %+v", final)
	}

	files, err = receiver.WaitingFiles(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files[0].Name != filename || files[0].Size != int64(len(payload)) {
		t.Fatalf("unexpected received files: %+v", files)
	}
	rc, size, err := receiver.GetWaitingFile(ctx, filename)
	if err != nil {
		t.Fatal(err)
	}
	got, readErr := io.ReadAll(rc)
	closeErr := rc.Close()
	if readErr != nil || closeErr != nil {
		t.Fatalf("read received file: %v; close: %v", readErr, closeErr)
	}
	if size != int64(len(payload)) || sha256.Sum256(got) != sha256.Sum256(payload) {
		t.Fatal("received file length or SHA-256 does not match the sent payload")
	}
	if err := receiver.DeleteWaitingFile(ctx, filename); err != nil {
		t.Fatal(err)
	}
	files, err = receiver.WaitingFiles(ctx)
	if err != nil || len(files) != 0 {
		t.Fatalf("receiver files after deletion: %+v, error: %v", files, err)
	}
}
