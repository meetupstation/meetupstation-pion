package main

import (
	"fmt"
	"os"
	"os/signal"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

type MediaType int

const (
	MediaTypeAudio MediaType = 0
	MediaTypeVideo MediaType = 1
)

func checkClosedPeers(room *Room) {
	closedPeerConnectionIds := func() []uint64 {
		closedPeerConnectionIds := make([]uint64, 0)

		room.peersMutex.RLock()
		defer room.peersMutex.RUnlock()

		for peerConnectionId, peer := range room.peers {
			select {
			case connected := <-peer.connectedChannel:
				if !connected {
					closedPeerConnectionIds = append(closedPeerConnectionIds, peerConnectionId)
				} else {
					fmt.Fprintf(os.Stderr, "Error: conn %d: ice connected\n", peerConnectionId)
				}
			default:
			}
		}

		return closedPeerConnectionIds
	}()

	for _, peerConnectionId := range closedPeerConnectionIds {
		if err := room.closePeer(peerConnectionId); err != nil {
			fmt.Fprintf(os.Stderr, "Error: conn %d: %s\n", peerConnectionId, err)
		}
	}
}

func closeAllPeers(room *Room) {
	room.peersMutex.RLock()
	ids := make([]uint64, 0, len(room.peers))
	for id := range room.peers {
		ids = append(ids, id)
	}
	room.peersMutex.RUnlock()

	for _, peerConnectionId := range ids {
		if err := room.closePeer(peerConnectionId); err != nil {
			fmt.Fprintf(os.Stderr, "Error: conn %d: %s\n", peerConnectionId, err)
		}
	}
}

func main() {

	if len(os.Args) != 4 ||
		(os.Args[1] != "host" && os.Args[1] != "guest") {
		fmt.Fprintf(os.Stdout,
			"example usage: ./meetupstation-pion [host,guest] https://meetupstation.com \"secret host room id\"\n")
		return
	}

	var room Room
	var roomSignalling SignallingScope
	var mediaStream MediaStream

	switch os.Args[1] {
	case "host":
		room.init(MeetingTypeHost)
	case "guest":
		room.init(MeetingTypeGuest)
	}

	roomSignalling.init(os.Args[2], os.Args[3])

	// roomSignalling.init("https://meetupstation.com", "secret room id")
	// room.init(MeetingTypeHost)

	var running atomic.Bool
	running.Store(true)

	interruptChannel := make(chan os.Signal, 1)
	signal.Notify(interruptChannel, os.Interrupt, syscall.SIGTERM)

	var waitGroup sync.WaitGroup
	waitGroup.Go(func() {
		<-interruptChannel
		running.Store(false)
	})

	var err error
	err = mediaStream.initializeLocalStreams(4000, 4002)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: initialize local streams: %s\n", err)
		return
	}

	waitGroup.Go(func() {
		mediaStream.sendLocal(&room, MediaTypeAudio, &running)
	})
	waitGroup.Go(func() {
		mediaStream.sendLocal(&room, MediaTypeVideo, &running)
	})

	err = mediaStream.initializeRemoteStreams(4004, 4006)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: initialize remote streams: %s\n", err)
		return
	}

	waitGroup.Go(func() {
		mediaStream.receiveRemote(MediaTypeAudio, &running)
	})
	waitGroup.Go(func() {
		mediaStream.receiveRemote(MediaTypeVideo, &running)
	})

	for running.Load() {
		roomSignalling.restart()

		mediaStream.remoteVideoTrack = nil
		mediaStream.remoteAudioTrack = nil

		peerConnectionId := room.getAndUpdatePeerConnectionId()

		err = room.initializePeerConnection(peerConnectionId)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: conn %d: %s\n", peerConnectionId, err)
			time.Sleep(time.Second)
			continue
		}

		peer := room.getPeer(peerConnectionId)

		fmt.Fprintf(os.Stdout, "conn %d: setting up tracks and data handlers\n", peerConnectionId)

		room.setupTracksAndDataHandlers(&mediaStream, peerConnectionId)

		err = room.prepareHostOffer(&roomSignalling, peerConnectionId)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: conn %d: %s\n", peerConnectionId, err)
			room.closePeer(peerConnectionId)
			time.Sleep(time.Second)
			continue
		}

		fmt.Fprintf(os.Stdout, "conn %d: signalling/waiting for ice connection\n", peerConnectionId)

		err = room.waitForIceConnected(peer, &roomSignalling, &running)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: conn %d: %s\n", peerConnectionId, err)
			if err = room.closePeer(peerConnectionId); err != nil {
				fmt.Fprintf(os.Stderr, "Error: conn %d: %s\n", peerConnectionId, err)
			}
			time.Sleep(time.Second)
			continue
		}

		switch room.meetingType {
		case MeetingTypeGuest:
			fmt.Fprintf(os.Stdout,
				"conn %d: signalling/waiting for ice closing\n",
				peerConnectionId)
			err = room.waitForIceClosed(peer, &roomSignalling, &running)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error: conn %d: %s\n", peerConnectionId, err)
			}
			if err = room.closePeer(peerConnectionId); err != nil {
				fmt.Fprintf(os.Stderr, "Error: conn %d: %s\n", peerConnectionId, err)
			}
		case MeetingTypeHost:
			checkClosedPeers(&room)
		}
	}
	closeAllPeers(&room)

	waitGroup.Wait()

	err = mediaStream.closeRemoteStreams()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: close remote streams: %s\n", err)
	}
	err = mediaStream.closeLocalStreams()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: close local streams: %s\n", err)
	}
}
