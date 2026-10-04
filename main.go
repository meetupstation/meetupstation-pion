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
	waitGroup.Add(1)
	go func() {
		<-interruptChannel
		running.Store(false)
		waitGroup.Done()
	}()

	var err error
	err = mediaStream.initializeLocalStreams(4000, 4002)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: initialize local streams: %s\n", err)
		return
	}

	waitGroup.Add(1)
	go mediaStream.sendLocal(&room, MediaTypeAudio, &waitGroup, &running)
	waitGroup.Add(1)
	go mediaStream.sendLocal(&room, MediaTypeVideo, &waitGroup, &running)

	err = mediaStream.initializeRemoteStreams(4004, 4006)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: initialize remote streams: %s\n", err)
		return
	}

	waitGroup.Add(1)
	go mediaStream.receiveRemote(MediaTypeAudio, &waitGroup, &running)
	waitGroup.Add(1)
	go mediaStream.receiveRemote(MediaTypeVideo, &waitGroup, &running)

	for running.Load() {
		roomSignalling.restart()

		mediaStream.remoteVideoTrack = nil
		mediaStream.remoteAudioTrack = nil

		peerConnectionId := room.getAndUpdatePeerConnectionId()

		err = room.initializePeerConnection(peerConnectionId)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: conn %d: %s\n", peerConnectionId, err)
			continue
		}

		peer := room.getPeer(peerConnectionId)

		fmt.Fprintf(os.Stdout, "conn %d: setting up tracks and data handlers\n", peerConnectionId)

		err = room.prepareGuestAnswerOrHostOffer(&roomSignalling, &mediaStream, peerConnectionId)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: conn %d: %s\n", peerConnectionId, err)
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

		waitForDisconnected := func(signalling *SignallingScope, waitGroup *sync.WaitGroup) {
			if waitGroup != nil {
				defer waitGroup.Done()
			}
			err = room.waitForIceDisconnected(peer, signalling, &running)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error: conn %d: %s\n", peerConnectionId, err)
			}
			if err = room.closePeer(peerConnectionId); err != nil {
				fmt.Fprintf(os.Stderr, "Error: conn %d: %s\n", peerConnectionId, err)
			}
		}

		switch room.meetingType {
		case MeetingTypeGuest:
			fmt.Fprintf(os.Stdout,
				"conn %d: signalling/waiting for ice disconnection\n",
				peerConnectionId)
			waitForDisconnected(&roomSignalling, nil)
		case MeetingTypeHost:
			fmt.Fprintf(os.Stdout,
				"conn %d: waiting for ice disconnection in a goroutine\n",
				peerConnectionId)

			waitGroup.Add(1)
			go waitForDisconnected(nil, &waitGroup)
		}
	}

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
