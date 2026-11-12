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
	for peerConnectionId, peer := range room.peers {
		select {
		case <-peer.closedChannel:
			if err := room.closePeer(peerConnectionId); err != nil {
				fmt.Fprintf(os.Stderr,
					"Error: conn %d: %v\n",
					peerConnectionId,
					err)
			}
		default:
		}
	}
}

func closeAllPeers(room *Room) {
	ids := make([]uint64, 0, len(room.peers))
	for id := range room.peers {
		ids = append(ids, id)
	}

	for peerConnectionId := range room.peers {
		if err := room.closePeer(peerConnectionId); err != nil {
			fmt.Fprintf(os.Stderr,
				"Error: conn %d: %v\n",
				peerConnectionId, err)
		}
	}
}

func main() {

	const mirrorStreams = 1
	const portShift = 100

	var room Room
	var roomSignalling SignallingScope
	var streamer Streamer

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

	err = streamer.init(mirrorStreams,
		4000+portShift,
		4002+portShift,
		4004+portShift,
		4006+portShift)
	if err != nil {
		fmt.Fprintf(os.Stderr,
			"Error: initialize streamer: %v\n",
			err)
		return
	}

	waitGroup.Go(func() {
		streamer.sendLocal(webrtc.RTPCodecTypeAudio, &running)
	})
	waitGroup.Go(func() {
		streamer.sendLocal(webrtc.RTPCodecTypeVideo, &running)
	})

	for index := range mirrorStreams {
		waitGroup.Go(func() {
			streamer.echoRemote(index, webrtc.RTPCodecTypeAudio, &running)
		})
		waitGroup.Go(func() {
			streamer.echoRemote(index, webrtc.RTPCodecTypeVideo, &running)
		})
	}

	waitGroup.Go(func() {
		streamer.receiveRemote(webrtc.RTPCodecTypeAudio, &running)
	})
	waitGroup.Go(func() {
		streamer.receiveRemote(webrtc.RTPCodecTypeVideo, &running)
	})

	for running.Load() {
		roomSignalling.restart()

		peerConnectionId := room.getAndUpdatePeerConnectionId()

		if room.meetingType == MeetingTypeHost {
			checkClosedPeers(&room)
		} else {
			roomSignalling.accessKey = ""
		}

		err = room.initializePeerConnection(peerConnectionId, &streamer)
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

		err = room.setupTracksAndDataHandlers(peerConnectionId, &streamer, &roomSignalling)
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

		err = room.ifHostPrepareOffer(&roomSignalling, peerConnectionId)
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

	err = streamer.receiveStream.closeRemoteConnections()
	if err != nil {
		fmt.Fprintf(os.Stderr,
			"Error: close remote connections: %v\n",
			err)
	}
	err = streamer.ownStream.closeListeners()
	if err != nil {
		fmt.Fprintf(os.Stderr,
			"Error: close local streams: %v\n",
			err)
	}
}
