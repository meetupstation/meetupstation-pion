package main

import (
	"fmt"
	"os"
	"os/signal"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/pion/webrtc/v4"
)

func checkClosedPeers(room *Room) {
	closedPeerConnectionIds := func() []uint64 {
		closedPeerConnectionIds := make([]uint64, 0)

		room.peersMutex.RLock()
		defer room.peersMutex.RUnlock()

		for peerConnectionId, peer := range room.peers {
			select {
			case <-peer.closedChannel:
				closedPeerConnectionIds = append(closedPeerConnectionIds, peerConnectionId)
			default:
			}
		}

		return closedPeerConnectionIds
	}()

	for _, peerConnectionId := range closedPeerConnectionIds {
		if err := room.closePeer(peerConnectionId); err != nil {
			fmt.Fprintf(os.Stderr,
				"Error: conn %d: %v\n",
				peerConnectionId,
				err)
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
			fmt.Fprintf(os.Stderr,
				"Error: conn %d: %v\n",
				peerConnectionId, err)
		}
	}
}

func main() {

	const portShift = 100

	var room Room
	var roomSignalling SignallingScope
	var mediaStream MediaStream

	if len(os.Args) != 4 ||
		(os.Args[1] != "host" && os.Args[1] != "guest") {
		fmt.Fprintf(os.Stdout,
			"example usage: ./meetupstation-pion [host,guest] https://meetupstation.com \"secret host room id\"\n")
		return
	}

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
	err = mediaStream.initializeLocalStreams(4000+portShift, 4002+portShift)
	if err != nil {
		fmt.Fprintf(os.Stderr,
			"Error: initialize local streams: %v\n",
			err)
		return
	}

	// waitGroup.Go(func() {
	// 	mediaStream.sendLocal(&room, webrtc.RTPCodecTypeAudio, &running)
	// })
	// waitGroup.Go(func() {
	// 	mediaStream.sendLocal(&room, webrtc.RTPCodecTypeVideo, &running)
	// })

	err = mediaStream.initializeRemoteStreams(4004+portShift, 4006+portShift)
	if err != nil {
		fmt.Fprintf(os.Stderr,
			"Error: initialize remote streams: %v\n",
			err)
		return
	}

	waitGroup.Go(func() {
		mediaStream.receiveRemote(&room, webrtc.RTPCodecTypeAudio, &running)
	})
	waitGroup.Go(func() {
		mediaStream.receiveRemote(&room, webrtc.RTPCodecTypeVideo, &running)
	})

	waitGroup.Go(func() {
		mediaStream.drainTransceiverSenders(&room, &running)
	})

	for running.Load() {
		roomSignalling.restart()

		peerConnectionId := room.getAndUpdatePeerConnectionId()

		if room.meetingType == MeetingTypeHost {
			checkClosedPeers(&room)
		}

		err = room.initializePeerConnection(peerConnectionId)
		if err != nil {
			fmt.Fprintf(os.Stderr,
				"Error: conn %d: %v\n",
				peerConnectionId,
				err)
			time.Sleep(time.Second)
			continue
		}

		peer := room.getPeer(peerConnectionId)

		fmt.Fprintf(os.Stdout,
			"conn %d: setting up tracks and data handlers\n",
			peerConnectionId)

		err = room.setupTracksAndDataHandlers(peerConnectionId)
		if err != nil {
			fmt.Fprintf(os.Stderr,
				"Error: conn %d: %v\n",
				peerConnectionId,
				err)
			room.closePeer(peerConnectionId)
			time.Sleep(time.Second)
			continue
		}

		err = room.prepareHostOffer(&roomSignalling, peerConnectionId)
		if err != nil {
			fmt.Fprintf(os.Stderr,
				"Error: conn %d: %v\n",
				peerConnectionId,
				err)
			room.closePeer(peerConnectionId)
			time.Sleep(time.Second)
			continue
		}

		fmt.Fprintf(os.Stdout,
			"conn %d: signalling/waiting for ice connection\n",
			peerConnectionId)

		err = room.waitForIceConnected(peer, &roomSignalling, &running)
		if err != nil {
			fmt.Fprintf(os.Stderr,
				"Error: conn %d: %v\n",
				peerConnectionId,
				err)
			if err = room.closePeer(peerConnectionId); err != nil {
				fmt.Fprintf(os.Stderr,
					"Error: conn %d: %v\n",
					peerConnectionId,
					err)
			}
			time.Sleep(time.Second)
			continue
		}

		if room.meetingType == MeetingTypeGuest {
			fmt.Fprintf(os.Stdout,
				"conn %d: signalling/waiting for ice closing\n",
				peerConnectionId)
			err = room.waitForIceClosed(peer, &roomSignalling, &running)
			if err != nil {
				fmt.Fprintf(os.Stderr,
					"Error: conn %d: %v\n",
					peerConnectionId,
					err)
			}
			if err = room.closePeer(peerConnectionId); err != nil {
				fmt.Fprintf(os.Stderr,
					"Error: conn %d: %v\n",
					peerConnectionId,
					err)
			}
		}
	}
	closeAllPeers(&room)

	fmt.Println("wait till goroutines are done")
	waitGroup.Wait()
	fmt.Println("goroutines are done")

	err = mediaStream.closeRemoteStreams()
	if err != nil {
		fmt.Fprintf(os.Stderr,
			"Error: close remote streams: %v\n",
			err)
	}
	err = mediaStream.closeLocalStreams()
	if err != nil {
		fmt.Fprintf(os.Stderr,
			"Error: close local streams: %v\n",
			err)
	}
}
